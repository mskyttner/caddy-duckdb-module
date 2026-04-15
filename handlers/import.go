package handlers

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/tobilg/caddy-duckdb-module/database"
	"go.uber.org/zap"
)

// ImportHandler manages remote data imports: downloading parquet/csv/json files
// from HTTPS URLs, converting them to local parquet, and registering them as
// DuckDB table macros visible to all pool connections via the :memory: catalog.
type ImportHandler struct {
	dbMgr      *database.Manager
	logger     *zap.Logger
	importsDir string
	defaultTTL time.Duration
	mu         sync.Mutex
	imports    map[string]*importEntry // alias → entry
}

// importEntry tracks a single active remote import.
type importEntry struct {
	Alias     string    `json:"alias"`
	SourceURL string    `json:"source_url"`
	LocalPath string    `json:"local_path"`
	RowCount  int64     `json:"row_count"`
	SizeBytes int64     `json:"size_bytes"`
	Columns   []string  `json:"columns"`
	ExpiresAt time.Time `json:"expires_at"`
}

// NewImportHandler creates an import handler.
// importsDir is the filesystem directory where downloaded files are stored.
// Pass an empty string to disable the feature (all tool calls will return an error).
func NewImportHandler(dbMgr *database.Manager, logger *zap.Logger, importsDir string, defaultTTL time.Duration) *ImportHandler {
	if defaultTTL == 0 {
		defaultTTL = time.Hour
	}
	return &ImportHandler{
		dbMgr:      dbMgr,
		logger:     logger,
		importsDir: importsDir,
		defaultTTL: defaultTTL,
		imports:    make(map[string]*importEntry),
	}
}

// StartCleanup launches a background goroutine that drops expired imports.
// Stops when ctx is cancelled (e.g. on Caddy shutdown).
func (h *ImportHandler) StartCleanup(ctx context.Context, interval time.Duration) {
	if interval == 0 {
		interval = 5 * time.Minute
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				h.sweepExpired()
			}
		}
	}()
}

func (h *ImportHandler) sweepExpired() {
	now := time.Now()
	h.mu.Lock()
	var expired []*importEntry
	for _, e := range h.imports {
		if now.After(e.ExpiresAt) {
			expired = append(expired, e)
		}
	}
	for _, e := range expired {
		delete(h.imports, e.Alias)
	}
	h.mu.Unlock()

	for _, e := range expired {
		if err := h.dropMacro(e.Alias); err != nil {
			h.logger.Warn("Failed to drop expired import macro",
				zap.String("alias", e.Alias), zap.Error(err))
		}
		if err := os.Remove(e.LocalPath); err != nil && !os.IsNotExist(err) {
			h.logger.Warn("Failed to remove expired import file",
				zap.String("path", e.LocalPath), zap.Error(err))
		} else {
			h.logger.Info("Removed expired import", zap.String("alias", e.Alias))
		}
	}
}

// ImportRemote downloads a remote file, converts it to local parquet, and
// registers a table macro so the data is queryable as FROM alias().
// If the alias already exists it is replaced (idempotent re-import).
func (h *ImportHandler) ImportRemote(url, alias string, ttlMinutes int) (*importEntry, error) {
	if h.importsDir == "" {
		return nil, fmt.Errorf("imports directory not configured (set DUCKDB_IMPORTS_DIR)")
	}
	if !isAlias(alias) {
		return nil, fmt.Errorf("invalid alias %q: must be a valid SQL identifier ([a-zA-Z_][a-zA-Z0-9_]*)", alias)
	}
	if !strings.HasPrefix(url, "https://") {
		return nil, fmt.Errorf("only https:// URLs are allowed")
	}
	ext := strings.ToLower(filepath.Ext(strings.SplitN(url, "?", 2)[0]))
	var readFn string
	switch ext {
	case ".parquet":
		readFn = "read_parquet"
	case ".csv":
		readFn = "read_csv"
	case ".json", ".ndjson":
		readFn = "read_json"
	default:
		return nil, fmt.Errorf("unsupported file extension %q (allowed: .parquet, .csv, .json)", ext)
	}

	ttl := h.defaultTTL
	if ttlMinutes > 0 {
		ttl = time.Duration(ttlMinutes) * time.Minute
	}

	// Drop any existing import with this alias before re-importing.
	h.mu.Lock()
	old, hadOld := h.imports[alias]
	h.mu.Unlock()
	if hadOld {
		_ = h.dropMacro(alias)
		_ = os.Remove(old.LocalPath)
		h.mu.Lock()
		delete(h.imports, alias)
		h.mu.Unlock()
	}

	if err := os.MkdirAll(h.importsDir, 0750); err != nil {
		return nil, fmt.Errorf("failed to create imports directory: %w", err)
	}
	localPath := filepath.Join(h.importsDir, uuid.New().String()+".parquet")

	// Step 1: load httpfs (safe no-op if already loaded), then download.
	// Use 3× the default query timeout — network fetches can be slow.
	downloadTimeout := h.dbMgr.QueryTimeout() * 3
	if downloadTimeout < 5*time.Minute {
		downloadTimeout = 5 * time.Minute
	}

	safeURL := strings.ReplaceAll(url, "'", "''")
	safePath := strings.ReplaceAll(localPath, "'", "''")
	copySQL := fmt.Sprintf(
		"LOAD httpfs; COPY (FROM %s('%s')) TO '%s' (FORMAT PARQUET)",
		readFn, safeURL, safePath,
	)
	if _, err := h.dbMgr.ExecMainTimeout(downloadTimeout, copySQL); err != nil {
		_ = os.Remove(localPath)
		return nil, fmt.Errorf("failed to download %s: %w", url, err)
	}

	// Step 2: register as a table macro in the :memory: catalog explicitly.
	// We use memory."alias" to avoid the default catalog (which may be a
	// read-only attached database set via SET search_path in init.sql).
	// The memory catalog is searched by all connections, so FROM alias()
	// works without qualification regardless of the active search_path.
	macroSQL := fmt.Sprintf(
		`CREATE OR REPLACE MACRO memory."%s"() AS TABLE (FROM read_parquet('%s'))`,
		strings.ReplaceAll(alias, `"`, `""`),
		strings.ReplaceAll(localPath, "'", "''"),
	)
	if _, err := h.dbMgr.ExecMain(macroSQL); err != nil {
		_ = os.Remove(localPath)
		return nil, fmt.Errorf("failed to create macro %q: %w", alias, err)
	}

	// Step 3: collect metadata — row count and column names.
	rowCount, err := h.countRows(alias)
	if err != nil {
		h.logger.Warn("Could not count rows for import", zap.String("alias", alias), zap.Error(err))
	}
	columns, err := h.describeColumns(localPath)
	if err != nil {
		h.logger.Warn("Could not describe columns for import", zap.String("alias", alias), zap.Error(err))
	}

	fi, err := os.Stat(localPath)
	var sizeBytes int64
	if err == nil {
		sizeBytes = fi.Size()
	}

	entry := &importEntry{
		Alias:     alias,
		SourceURL: url,
		LocalPath: localPath,
		RowCount:  rowCount,
		SizeBytes: sizeBytes,
		Columns:   columns,
		ExpiresAt: time.Now().Add(ttl),
	}
	h.mu.Lock()
	h.imports[alias] = entry
	h.mu.Unlock()

	h.logger.Info("Imported remote file",
		zap.String("alias", alias),
		zap.String("url", url),
		zap.Int64("rows", rowCount),
		zap.Int64("size_bytes", sizeBytes),
	)
	return entry, nil
}

// ListImports returns a snapshot of all active imports.
func (h *ImportHandler) ListImports() []*importEntry {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]*importEntry, 0, len(h.imports))
	for _, e := range h.imports {
		out = append(out, e)
	}
	return out
}

// DropImport removes a named import: drops the macro and deletes the local file.
func (h *ImportHandler) DropImport(alias string) error {
	h.mu.Lock()
	entry, ok := h.imports[alias]
	if ok {
		delete(h.imports, alias)
	}
	h.mu.Unlock()

	if !ok {
		return fmt.Errorf("no active import with alias %q", alias)
	}
	if err := h.dropMacro(alias); err != nil {
		h.logger.Warn("Failed to drop macro", zap.String("alias", alias), zap.Error(err))
	}
	if err := os.Remove(entry.LocalPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to delete local file: %w", err)
	}
	h.logger.Info("Dropped import", zap.String("alias", alias))
	return nil
}

func (h *ImportHandler) dropMacro(alias string) error {
	sql := fmt.Sprintf(`DROP MACRO IF EXISTS memory."%s"`, strings.ReplaceAll(alias, `"`, `""`))
	_, err := h.dbMgr.ExecMain(sql)
	return err
}

func (h *ImportHandler) countRows(alias string) (int64, error) {
	sql := fmt.Sprintf(`SELECT count(*) FROM memory."%s"()`, strings.ReplaceAll(alias, `"`, `""`))
	rows, err := h.dbMgr.QueryMain(sql)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	if rows.Next() {
		var n int64
		if err := rows.Scan(&n); err != nil {
			return 0, err
		}
		return n, nil
	}
	return 0, nil
}

func (h *ImportHandler) describeColumns(localPath string) ([]string, error) {
	safePath := strings.ReplaceAll(localPath, "'", "''")
	sql := fmt.Sprintf(`DESCRIBE FROM read_parquet('%s')`, safePath)
	rows, err := h.dbMgr.QueryMain(sql)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	// DESCRIBE returns column_name, column_type, null, key, default, extra.
	// We want the first column (column_name).
	nameIdx := -1
	for i, c := range cols {
		if strings.EqualFold(c, "column_name") {
			nameIdx = i
			break
		}
	}
	if nameIdx < 0 && len(cols) > 0 {
		nameIdx = 0
	}

	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}

	var names []string
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			continue
		}
		if nameIdx >= 0 {
			if s, ok := vals[nameIdx].(string); ok {
				names = append(names, s)
			}
		}
	}
	return names, rows.Err()
}
