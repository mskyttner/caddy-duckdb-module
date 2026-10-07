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
	dbMgr            *database.Manager
	authorizer       *auth.Authorizer
	logger           *zap.Logger
	exportsDir       string
	exportsURL       string        // URL prefix for serving exported files (e.g. /duckdb/exports)
	publicExportsDir string        // directory for auth-free public exports (empty = feature disabled)
	publicExportsURL string        // URL prefix for public export files
	defaultTTL       time.Duration // how long exported files are kept
	mu               sync.Mutex
	expiry           map[string]time.Time // filename → expiry time (auth-gated exports)
	publicExpiry     map[string]time.Time // filename → expiry time (public exports)
}

// NewExportHandler creates a new export handler.
// publicExportsDir and publicExportsURL enable auth-free public exports (Feature A).
// Pass empty strings to disable the feature.
func NewExportHandler(dbMgr *database.Manager, authorizer *auth.Authorizer, logger *zap.Logger, exportsDir, exportsURL, publicExportsDir, publicExportsURL string, defaultTTL time.Duration) *ExportHandler {
	if defaultTTL == 0 {
		defaultTTL = time.Hour
	}
	return &ExportHandler{
		dbMgr:            dbMgr,
		authorizer:       authorizer,
		logger:           logger,
		exportsDir:       exportsDir,
		exportsURL:       strings.TrimSuffix(exportsURL, "/"),
		publicExportsDir: publicExportsDir,
		publicExportsURL: strings.TrimSuffix(publicExportsURL, "/"),
		defaultTTL:       defaultTTL,
		expiry:           make(map[string]time.Time),
		publicExpiry:     make(map[string]time.Time),
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
	var expired, expiredPublic []string
	for name, exp := range h.expiry {
		if now.After(exp) {
			expired = append(expired, name)
		}
	}
	for _, name := range expired {
		delete(h.expiry, name)
	}
	for name, exp := range h.publicExpiry {
		if now.After(exp) {
			expiredPublic = append(expiredPublic, name)
		}
	}
	for _, name := range expiredPublic {
		delete(h.publicExpiry, name)
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
	for _, name := range expiredPublic {
		path := filepath.Join(h.publicExportsDir, name)
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			h.logger.Warn("Failed to remove expired public export file", zap.String("path", path), zap.Error(err))
		} else {
			h.logger.Info("Removed expired public export file", zap.String("file", name))
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
// When public is true, the file is written to publicExportsDir and the URL
// uses publicExportsURL, making the download accessible without authentication.
func (h *ExportHandler) runExport(sqlQuery, format string, ttlMinutes int, public bool) (*ExportResponse, error) {
	targetDir := h.exportsDir
	targetURL := h.exportsURL
	if public {
		if h.publicExportsDir == "" {
			return nil, fmt.Errorf("public export directory not configured (set DUCKDB_PUBLIC_EXPORTS_DIR)")
		}
		targetDir = h.publicExportsDir
		targetURL = h.publicExportsURL
	} else if h.exportsDir == "" {
		return nil, fmt.Errorf("export directory not configured")
	}
	switch format {
	case "parquet", "csv", "json", "html":
	case "":
		format = "parquet"
	default:
		return nil, fmt.Errorf("unsupported format %q (valid: parquet, csv, json, html)", format)
	}
	ttl := h.defaultTTL
	if ttlMinutes > 0 {
		ttl = time.Duration(ttlMinutes) * time.Minute
	}
	if err := os.MkdirAll(targetDir, 0750); err != nil {
		return nil, fmt.Errorf("failed to create exports directory: %w", err)
	}
	filename := uuid.New().String() + "." + format
	filePath := filepath.Join(targetDir, filename)

	if format == "html" {
		html, err := h.dbMgr.RenderChart(context.Background(), sqlQuery)
		if err != nil {
			return nil, fmt.Errorf("chart render failed: %w", err)
		}
		if err := os.WriteFile(filePath, html, 0640); err != nil {
			return nil, fmt.Errorf("failed to write chart file: %w", err)
		}
		return h.finalizeExport(filePath, filename, format, targetURL, 0, public, ttl)
	}

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

	return h.finalizeExport(filePath, filename, format, targetURL, rowCount, public, ttl)
}

// writeChartArtifact materializes pre-rendered chart bytes (from the ggsql_chart MCP
// tool) as a new export file, reusing the same exports_dir/public_exports_dir, UUID
// filename, and expiry bookkeeping as runExport -- so chart URLs expire and are served
// identically to every other export format.
func (h *ExportHandler) writeChartArtifact(data []byte, ext, format string, ttlMinutes int, public bool) (*ExportResponse, error) {
	targetDir := h.exportsDir
	targetURL := h.exportsURL
	if public {
		if h.publicExportsDir == "" {
			return nil, fmt.Errorf("public export directory not configured (set DUCKDB_PUBLIC_EXPORTS_DIR)")
		}
		targetDir = h.publicExportsDir
		targetURL = h.publicExportsURL
	} else if h.exportsDir == "" {
		return nil, fmt.Errorf("export directory not configured")
	}
	ttl := h.defaultTTL
	if ttlMinutes > 0 {
		ttl = time.Duration(ttlMinutes) * time.Minute
	}
	if err := os.MkdirAll(targetDir, 0750); err != nil {
		return nil, fmt.Errorf("failed to create exports directory: %w", err)
	}
	filename := uuid.New().String() + "." + ext
	filePath := filepath.Join(targetDir, filename)
	if err := os.WriteFile(filePath, data, 0640); err != nil {
		return nil, fmt.Errorf("failed to write chart file: %w", err)
	}
	return h.finalizeExport(filePath, filename, format, targetURL, 0, public, ttl)
}

// finalizeExport stats the written file, records its expiry, and builds the response.
// Shared by every export format's write path in runExport.
func (h *ExportHandler) finalizeExport(filePath, filename, format, targetURL string, rowCount int64, public bool, ttl time.Duration) (*ExportResponse, error) {
	fi, err := os.Stat(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to stat export file: %w", err)
	}

	expiresAt := time.Now().Add(ttl)
	h.mu.Lock()
	if public {
		h.publicExpiry[filename] = expiresAt
	} else {
		h.expiry[filename] = expiresAt
	}
	h.mu.Unlock()

	return &ExportResponse{
		URL:       targetURL + "/" + filename,
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
	case "parquet", "csv", "json", "html":
	default:
		h.sendError(w, fmt.Sprintf("Unsupported format %q (valid: parquet, csv, json, html)", format), http.StatusBadRequest)
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

	var rowCount int64
	if format == "html" {
		html, err := h.dbMgr.RenderChart(r.Context(), req.SQL)
		if err != nil {
			h.logger.Error("Chart render failed", zap.Error(err), zap.String("request_id", requestID))
			h.sendError(w, "Chart render failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if err := os.WriteFile(filePath, html, 0640); err != nil {
			h.logger.Error("Failed to write chart file", zap.Error(err), zap.String("request_id", requestID))
			h.sendError(w, "Failed to write chart file", http.StatusInternalServerError)
			return
		}
	} else {
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

	h.serveFile(w, r, filename, f)
}

// serveFile sets the Content-Type/Content-Disposition for filename, then
// serves f via http.ServeContent with real conditional-GET support: an ETag
// (the filename itself -- already a UUID, unique per export, and the file is
// immutable for its lifetime, so this costs nothing to compute and is always
// correct) and a Last-Modified from the file's own mtime (export files are
// written once and never touched again, so mtime is exact, not approximate).
// Shared by ServeDownload and ServePublicDownload -- and, transitively, by
// ggsql_chart's materialized output, which goes through the same two
// download endpoints via ExportHandler.writeChartArtifact.
func (h *ExportHandler) serveFile(w http.ResponseWriter, r *http.Request, filename string, f *os.File) {
	switch {
	case strings.HasSuffix(filename, ".csv"):
		w.Header().Set("Content-Type", "text/csv")
	case strings.HasSuffix(filename, ".json"):
		w.Header().Set("Content-Type", "application/json")
	case strings.HasSuffix(filename, ".html"):
		// Chart exports render inline (e.g. in an iframe) rather than downloading.
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
	default:
		w.Header().Set("Content-Type", "application/octet-stream")
	}
	if !strings.HasSuffix(filename, ".html") {
		w.Header().Set("Content-Disposition", "attachment; filename="+filename)
	}

	w.Header().Set("ETag", `"`+filename+`"`)
	var modTime time.Time
	if fi, statErr := f.Stat(); statErr == nil {
		modTime = fi.ModTime()
	}
	http.ServeContent(w, r, filename, modTime, f)
}

// ServePublicDownload handles GET /duckdb/public-exports/<filename>.
// No authentication is required — the UUID filename is the capability token.
// Only files created by a public export (tracked in h.publicExpiry) are served.
func (h *ExportHandler) ServePublicDownload(w http.ResponseWriter, r *http.Request, urlPrefix string) {
	if r.Method != http.MethodGet {
		h.sendError(w, "Method not allowed. Use GET.", http.StatusMethodNotAllowed)
		return
	}

	filename := strings.TrimPrefix(r.URL.Path, urlPrefix+"/")
	if filename == "" || strings.ContainsAny(filename, "/\\") {
		h.sendError(w, "Not found", http.StatusNotFound)
		return
	}

	h.mu.Lock()
	_, known := h.publicExpiry[filename]
	h.mu.Unlock()
	if !known {
		h.sendError(w, "Not found", http.StatusNotFound)
		return
	}

	filePath := filepath.Join(h.publicExportsDir, filename)
	f, err := os.Open(filePath)
	if err != nil {
		h.sendError(w, "Not found", http.StatusNotFound)
		return
	}
	defer f.Close()

	h.serveFile(w, r, filename, f)
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
