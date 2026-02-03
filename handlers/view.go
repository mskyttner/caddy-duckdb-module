package handlers

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/tobilg/caddy-duckdb-module/auth"
	"github.com/tobilg/caddy-duckdb-module/database"
	"github.com/tobilg/caddy-duckdb-module/formats"
	"go.uber.org/zap"
)

// ViewHandler handles requests to query views.
type ViewHandler struct {
	dbMgr           *database.Manager
	authorizer      *auth.Authorizer
	maxRowsPerPage  int
	absoluteMaxRows int
	logger          *zap.Logger
}

// NewViewHandler creates a new ViewHandler.
func NewViewHandler(dbMgr *database.Manager, authorizer *auth.Authorizer, maxRowsPerPage, absoluteMaxRows int, logger *zap.Logger) *ViewHandler {
	return &ViewHandler{
		dbMgr:           dbMgr,
		authorizer:      authorizer,
		maxRowsPerPage:  maxRowsPerPage,
		absoluteMaxRows: absoluteMaxRows,
		logger:          logger,
	}
}

// ServeHTTP handles HTTP requests for view operations.
func (h *ViewHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Only GET and HEAD methods allowed (read-only)
	// HEAD is supported for HTTP clients that check content-type/size before downloading
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		h.sendError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Extract view name from path: /duckdb/view or /duckdb/view/{name}
	viewName := h.extractViewName(r.URL.Path)

	// Handle HEAD request - return headers only
	if r.Method == http.MethodHead {
		format := GetAcceptFormat(r)
		h.sendHeadResponse(w, format)
		return
	}

	if viewName == "" {
		// List all available views
		h.listViews(w, r)
	} else {
		// Query the specified view
		h.queryView(w, r, viewName)
	}
}

// extractViewName extracts the view name from the request path.
// Path format: /duckdb/view/{name} or /duckdb/view
func (h *ViewHandler) extractViewName(path string) string {
	// Remove the /duckdb/view prefix
	prefix := "/duckdb/view"
	if !strings.HasPrefix(path, prefix) {
		return ""
	}

	remaining := strings.TrimPrefix(path, prefix)
	remaining = strings.Trim(remaining, "/")

	if remaining == "" {
		return ""
	}

	// Return the view name (first path segment)
	parts := strings.SplitN(remaining, "/", 2)
	return parts[0]
}

// listViews returns a list of all available api_* views.
func (h *ViewHandler) listViews(w http.ResponseWriter, r *http.Request) {
	requestID := auth.GetRequestIDFromContext(r.Context())

	views, err := h.dbMgr.ListAPIViews()
	if err != nil {
		h.logger.Error("Failed to list views", zap.Error(err), zap.String("request_id", requestID))
		h.sendError(w, "Failed to list views", http.StatusInternalServerError)
		return
	}

	response := map[string]interface{}{
		"views": views,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(response)
}

// queryView queries a view with optional filters, sorting, and pagination.
func (h *ViewHandler) queryView(w http.ResponseWriter, r *http.Request, viewName string) {
	requestID := auth.GetRequestIDFromContext(r.Context())

	// Validate view name
	if err := SanitizeTableName(viewName); err != nil {
		h.sendError(w, fmt.Sprintf("Invalid view name: %s", err.Error()), http.StatusBadRequest)
		return
	}

	// Check if view has api_ prefix
	if !strings.HasPrefix(viewName, "api_") {
		h.sendError(w, "Only api_ prefixed views are accessible", http.StatusForbidden)
		return
	}

	// Check if view exists
	exists, err := h.dbMgr.ViewExists(viewName)
	if err != nil {
		h.logger.Error("Failed to check view existence", zap.Error(err), zap.String("request_id", requestID))
		h.sendError(w, "Failed to check view existence", http.StatusInternalServerError)
		return
	}
	if !exists {
		h.sendError(w, fmt.Sprintf("View '%s' not found", viewName), http.StatusNotFound)
		return
	}

	// Parse pagination
	limit, offset, page, paginationRequested := ParsePagination(r, h.maxRowsPerPage, h.absoluteMaxRows)

	// Apply safety limit if pagination not requested
	safetyLimit := limit
	if !paginationRequested && h.absoluteMaxRows > 0 {
		safetyLimit = h.absoluteMaxRows
	}

	// Parse select columns
	selectColumns, err := ParseSelectColumns(r)
	if err != nil {
		h.sendError(w, fmt.Sprintf("Invalid select: %s", err.Error()), http.StatusBadRequest)
		return
	}

	// Parse filters
	filters, err := ParseFilters(r)
	if err != nil {
		h.sendError(w, fmt.Sprintf("Invalid filters: %s", err.Error()), http.StatusBadRequest)
		return
	}

	// Validate filter column names
	for _, f := range filters {
		if err := SanitizeColumnName(f.Column); err != nil {
			h.sendError(w, fmt.Sprintf("Invalid filter column '%s': %s", f.Column, err.Error()), http.StatusBadRequest)
			return
		}
	}

	// Parse sorts
	sorts, err := ParseSorts(r)
	if err != nil {
		h.sendError(w, fmt.Sprintf("Invalid sort: %s", err.Error()), http.StatusBadRequest)
		return
	}

	// Validate sort column names
	for _, s := range sorts {
		if err := SanitizeColumnName(s.Column); err != nil {
			h.sendError(w, fmt.Sprintf("Invalid sort column '%s': %s", s.Column, err.Error()), http.StatusBadRequest)
			return
		}
	}

	// Execute query
	rows, err := h.dbMgr.QueryView(viewName, selectColumns, filters, sorts, safetyLimit, offset)
	if err != nil {
		h.logger.Error("Failed to query view", zap.Error(err), zap.String("view", viewName), zap.String("request_id", requestID))
		h.sendError(w, fmt.Sprintf("Failed to query view: %s", err.Error()), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	// Determine response format
	format := GetAcceptFormat(r)

	// Build links config if requested
	var linksConfig *formats.LinksConfig
	if ParseLinks(r) {
		linksConfig = &formats.LinksConfig{
			Enabled:  true,
			BasePath: r.URL.Path,
			Query:    r.URL.Query(),
		}
	}

	// Format response (no total count for views to keep it simple)
	if err := h.formatResponse(w, rows, format, page, limit, 0, paginationRequested, safetyLimit, linksConfig); err != nil {
		h.logger.Error("Failed to format response", zap.Error(err), zap.String("request_id", requestID))
		h.sendError(w, "Failed to format response", http.StatusInternalServerError)
	}
}

// formatResponse formats the query result based on the requested format.
func (h *ViewHandler) formatResponse(w http.ResponseWriter, rows *sql.Rows, format string, page, limit int, totalRows int64, paginationRequested bool, safetyLimit int, linksConfig *formats.LinksConfig) error {
	switch format {
	case "csv":
		return formats.WriteCSV(w, rows)
	case "parquet":
		return formats.WriteParquet(w, rows)
	case "arrow":
		return formats.WriteArrowIPC(w, rows)
	case "json":
		return formats.WriteJSON(w, rows, page, limit, totalRows, paginationRequested, safetyLimit, linksConfig)
	default:
		return formats.WriteJSON(w, rows, page, limit, totalRows, paginationRequested, safetyLimit, linksConfig)
	}
}

// sendError sends an error response.
func (h *ViewHandler) sendError(w http.ResponseWriter, message string, statusCode int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"error":   http.StatusText(statusCode),
		"message": message,
		"code":    statusCode,
	})
}

// sendHeadResponse sends headers for HEAD requests without body.
func (h *ViewHandler) sendHeadResponse(w http.ResponseWriter, format string) {
	switch format {
	case "csv":
		w.Header().Set("Content-Type", "text/csv")
	case "parquet":
		w.Header().Set("Content-Type", "application/parquet")
	case "arrow":
		w.Header().Set("Content-Type", "application/vnd.apache.arrow.stream")
	default:
		w.Header().Set("Content-Type", "application/json")
	}
	w.WriteHeader(http.StatusOK)
}
