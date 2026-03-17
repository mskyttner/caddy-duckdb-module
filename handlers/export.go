package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/tobilg/caddy-duckdb-module/auth"
	"github.com/tobilg/caddy-duckdb-module/database"
	"github.com/tobilg/caddy-duckdb-module/formats"
	"go.uber.org/zap"
)

// ExportHandler handles POST /duckdb/export requests.
// Executes a SQL query and writes results to a file in the exports directory,
// returning a URL to download the file. This avoids dumping large result sets
// into the HTTP response (and into LLM context windows).
type ExportHandler struct {
	dbMgr      *database.Manager
	authorizer *auth.Authorizer
	logger     *zap.Logger
	exportsDir string
	exportsURL string        // URL prefix for serving exported files (e.g. /duckdb/exports)
	defaultTTL time.Duration // how long exported files are kept
	mu         sync.Mutex
	expiry     map[string]time.Time // filename → expiry time
}

// NewExportHandler creates a new export handler.
func NewExportHandler(dbMgr *database.Manager, authorizer *auth.Authorizer, logger *zap.Logger, exportsDir, exportsURL string, defaultTTL time.Duration) *ExportHandler {
	if defaultTTL == 0 {
		defaultTTL = time.Hour
	}
	return &ExportHandler{
		dbMgr:      dbMgr,
		authorizer: authorizer,
		logger:     logger,
		exportsDir: exportsDir,
		exportsURL: strings.TrimSuffix(exportsURL, "/"),
		defaultTTL: defaultTTL,
		expiry:     make(map[string]time.Time),
	}
}

// StartCleanup launches a background goroutine that removes expired export files.
// It stops when ctx is cancelled (e.g. on Caddy shutdown).
func (h *ExportHandler) StartCleanup(ctx context.Context, interval time.Duration) {
	if interval == 0 {
		interval = 10 * time.Minute
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

func (h *ExportHandler) sweepExpired() {
	now := time.Now()
	h.mu.Lock()
	var expired []string
	for name, exp := range h.expiry {
		if now.After(exp) {
			expired = append(expired, name)
		}
	}
	for _, name := range expired {
		delete(h.expiry, name)
	}
	h.mu.Unlock()

	for _, name := range expired {
		path := filepath.Join(h.exportsDir, name)
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			h.logger.Warn("Failed to remove expired export file", zap.String("path", path), zap.Error(err))
		} else {
			h.logger.Info("Removed expired export file", zap.String("file", name))
		}
	}
}

// ExportRequest is the JSON body for POST /duckdb/export.
type ExportRequest struct {
	SQL        string `json:"sql"`
	Format     string `json:"format"`      // parquet | csv | json (default: parquet)
	TTLMinutes int    `json:"ttl_minutes"` // 0 = use server default
}

// ExportResponse is the JSON response from POST /duckdb/export.
type ExportResponse struct {
	URL       string    `json:"url"`
	Filename  string    `json:"filename"`
	Format    string    `json:"format"`
	Rows      int64     `json:"rows"`
	SizeBytes int64     `json:"size_bytes"`
	ExpiresAt time.Time `json:"expires_at"`
}

// runExport executes the core export logic and returns the response struct.
// Called by both ServeHTTP and the MCP export tool.
func (h *ExportHandler) runExport(sqlQuery, format string, ttlMinutes int) (*ExportResponse, error) {
	if h.exportsDir == "" {
		return nil, fmt.Errorf("export directory not configured")
	}
	switch format {
	case "parquet", "csv", "json":
	case "":
		format = "parquet"
	default:
		return nil, fmt.Errorf("unsupported format %q (valid: parquet, csv, json)", format)
	}
	ttl := h.defaultTTL
	if ttlMinutes > 0 {
		ttl = time.Duration(ttlMinutes) * time.Minute
	}
	if err := os.MkdirAll(h.exportsDir, 0750); err != nil {
		return nil, fmt.Errorf("failed to create exports directory: %w", err)
	}
	filename := uuid.New().String() + "." + format
	filePath := filepath.Join(h.exportsDir, filename)

	rows, err := h.dbMgr.QueryMain(sqlQuery)
	if err != nil {
		return nil, fmt.Errorf("query failed: %w", err)
	}
	defer rows.Close()

	f, err := os.Create(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to create export file: %w", err)
	}

	var rowCount int64
	switch format {
	case "parquet":
		rowCount, err = formats.WriteParquetToWriter(f, rows)
	case "csv":
		rowCount, err = formats.WriteCSVToWriter(f, rows)
	case "json":
		rowCount, err = formats.WriteJSONToWriter(f, rows)
	}
	f.Close()

	if err != nil {
		os.Remove(filePath)
		return nil, fmt.Errorf("failed to write export: %w", err)
	}

	fi, err := os.Stat(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to stat export file: %w", err)
	}

	expiresAt := time.Now().Add(ttl)
	h.mu.Lock()
	h.expiry[filename] = expiresAt
	h.mu.Unlock()

	return &ExportResponse{
		URL:       h.exportsURL + "/" + filename,
		Filename:  filename,
		Format:    format,
		Rows:      rowCount,
		SizeBytes: fi.Size(),
		ExpiresAt: expiresAt,
	}, nil
}

// ServeHTTP handles POST /duckdb/export.
func (h *ExportHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	requestID := auth.GetRequestIDFromContext(r.Context())

	if r.Method != http.MethodPost {
		h.sendError(w, "Method not allowed. Use POST.", http.StatusMethodNotAllowed)
		return
	}

	if h.exportsDir == "" {
		h.sendError(w, "Export directory not configured (set exports_dir in Caddyfile)", http.StatusServiceUnavailable)
		return
	}

	role := auth.GetRoleFromContext(r.Context())
	allowed, err := h.authorizer.CheckPermission(role, "*", auth.OperationQuery)
	if err != nil {
		h.logger.Error("Failed to check permission", zap.Error(err), zap.String("request_id", requestID))
		h.sendError(w, "Failed to check permission", http.StatusInternalServerError)
		return
	}
	if !allowed {
		h.sendError(w, "Forbidden: insufficient permissions", http.StatusForbidden)
		return
	}

	var req ExportRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.sendError(w, "Invalid JSON body: "+err.Error(), http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	if strings.TrimSpace(req.SQL) == "" {
		h.sendError(w, "sql field is required", http.StatusBadRequest)
		return
	}
	if containsInternalTables(req.SQL) {
		h.sendError(w, "Access to internal auth tables is forbidden", http.StatusForbidden)
		return
	}

	format := strings.ToLower(req.Format)
	if format == "" {
		format = "parquet"
	}
	switch format {
	case "parquet", "csv", "json":
	default:
		h.sendError(w, fmt.Sprintf("Unsupported format %q (valid: parquet, csv, json)", format), http.StatusBadRequest)
		return
	}

	ttl := h.defaultTTL
	if req.TTLMinutes > 0 {
		ttl = time.Duration(req.TTLMinutes) * time.Minute
	}

	// Ensure exports directory exists.
	if err := os.MkdirAll(h.exportsDir, 0750); err != nil {
		h.logger.Error("Failed to create exports directory", zap.Error(err), zap.String("request_id", requestID))
		h.sendError(w, "Failed to create exports directory", http.StatusInternalServerError)
		return
	}

	filename := uuid.New().String() + "." + format
	filePath := filepath.Join(h.exportsDir, filename)

	h.logger.Info("Exporting query results",
		zap.String("role", role),
		zap.String("format", format),
		zap.String("file", filename),
		zap.String("request_id", requestID),
	)

	rows, err := h.dbMgr.QueryMain(req.SQL)
	if err != nil {
		h.logger.Error("Query failed", zap.Error(err), zap.String("request_id", requestID))
		h.sendError(w, "Query execution failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	f, err := os.Create(filePath)
	if err != nil {
		h.logger.Error("Failed to create export file", zap.Error(err), zap.String("request_id", requestID))
		h.sendError(w, "Failed to create export file", http.StatusInternalServerError)
		return
	}

	var rowCount int64
	switch format {
	case "parquet":
		rowCount, err = formats.WriteParquetToWriter(f, rows)
	case "csv":
		rowCount, err = formats.WriteCSVToWriter(f, rows)
	case "json":
		rowCount, err = formats.WriteJSONToWriter(f, rows)
	}
	f.Close()

	if err != nil {
		os.Remove(filePath)
		h.logger.Error("Failed to write export file", zap.Error(err), zap.String("request_id", requestID))
		h.sendError(w, "Failed to write export: "+err.Error(), http.StatusInternalServerError)
		return
	}

	fi, err := os.Stat(filePath)
	if err != nil {
		h.sendError(w, "Failed to stat export file", http.StatusInternalServerError)
		return
	}

	expiresAt := time.Now().Add(ttl)
	h.mu.Lock()
	h.expiry[filename] = expiresAt
	h.mu.Unlock()

	h.logger.Info("Export complete",
		zap.String("file", filename),
		zap.Int64("rows", rowCount),
		zap.Int64("size_bytes", fi.Size()),
		zap.String("request_id", requestID),
	)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(ExportResponse{
		URL:       h.exportsURL + "/" + filename,
		Filename:  filename,
		Format:    format,
		Rows:      rowCount,
		SizeBytes: fi.Size(),
		ExpiresAt: expiresAt,
	})
}

// ServeDownload handles GET /duckdb/exports/<filename>.
// It only serves files that were created by this handler (tracked in h.expiry)
// to prevent directory traversal or serving unrelated files.
func (h *ExportHandler) ServeDownload(w http.ResponseWriter, r *http.Request, urlPrefix string) {
	if r.Method != http.MethodGet {
		h.sendError(w, "Method not allowed. Use GET.", http.StatusMethodNotAllowed)
		return
	}

	// Strip the URL prefix to get the bare filename, then reject anything with
	// a path separator so we never escape the exports directory.
	filename := strings.TrimPrefix(r.URL.Path, urlPrefix+"/")
	if filename == "" || strings.ContainsAny(filename, "/\\") {
		h.sendError(w, "Not found", http.StatusNotFound)
		return
	}

	// Only serve files we created (prevents serving arbitrary host files).
	h.mu.Lock()
	_, known := h.expiry[filename]
	h.mu.Unlock()
	if !known {
		h.sendError(w, "Not found", http.StatusNotFound)
		return
	}

	filePath := filepath.Join(h.exportsDir, filename)
	f, err := os.Open(filePath)
	if err != nil {
		h.sendError(w, "Not found", http.StatusNotFound)
		return
	}
	defer f.Close()

	switch {
	case strings.HasSuffix(filename, ".csv"):
		w.Header().Set("Content-Type", "text/csv")
	case strings.HasSuffix(filename, ".json"):
		w.Header().Set("Content-Type", "application/json")
	default:
		w.Header().Set("Content-Type", "application/octet-stream")
	}
	w.Header().Set("Content-Disposition", "attachment; filename="+filename)
	http.ServeContent(w, r, filename, time.Time{}, f)
}

func (h *ExportHandler) sendError(w http.ResponseWriter, message string, statusCode int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"error":   http.StatusText(statusCode),
		"message": message,
		"code":    statusCode,
	})
}
