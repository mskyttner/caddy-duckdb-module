package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/tobilg/caddy-duckdb-module/auth"
	"github.com/tobilg/caddy-duckdb-module/database"
	"go.uber.org/zap"
)

// ColumnsHandler handles requests for column schema and statistics.
type ColumnsHandler struct {
	dbMgr      *database.Manager
	authorizer *auth.Authorizer
	logger     *zap.Logger
}

// NewColumnsHandler creates a new ColumnsHandler.
func NewColumnsHandler(dbMgr *database.Manager, authorizer *auth.Authorizer, logger *zap.Logger) *ColumnsHandler {
	return &ColumnsHandler{
		dbMgr:      dbMgr,
		authorizer: authorizer,
		logger:     logger,
	}
}

// ColumnInfo represents column information in the standard response format.
type ColumnInfo struct {
	Name     string                 `json:"name"`
	Type     string                 `json:"type"`
	Nullable bool                   `json:"nullable"`
	Stats    map[string]interface{} `json:"stats,omitempty"`
}

// StandardColumnsResponse is the standard format response.
type StandardColumnsResponse struct {
	Table   string       `json:"table"`
	Columns []ColumnInfo `json:"columns"`
}

// TransformColumnsResponse is the transform format response for json_transform().
type TransformColumnsResponse struct {
	Table   string            `json:"table"`
	Columns map[string]string `json:"columns"`
}

// SummarizeColumnsResponse is the summarize format response with full statistics.
type SummarizeColumnsResponse struct {
	Table      string       `json:"table"`
	TotalRows  int64        `json:"total_rows"`
	SampleSize int          `json:"sample_size"`
	Columns    []ColumnInfo `json:"columns"`
}

// ServeHTTP handles HTTP requests for column schema operations.
func (h *ColumnsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Only GET and HEAD methods allowed
	// HEAD is required for DuckDB's read_json() which checks content-type first
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		h.sendError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Handle HEAD request - return headers only
	if r.Method == http.MethodHead {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		return
	}

	requestID := auth.GetRequestIDFromContext(r.Context())

	// Determine if this is a table or view request
	isView := strings.Contains(r.URL.Path, "/view/")
	var name string
	var err error

	if isView {
		name, err = h.extractViewName(r.URL.Path)
	} else {
		name, err = h.extractTableName(r.URL.Path)
	}

	if err != nil || name == "" {
		h.sendError(w, "Invalid path: name required", http.StatusBadRequest)
		return
	}

	// Sanitize name
	if err := SanitizeTableName(name); err != nil {
		h.sendError(w, fmt.Sprintf("Invalid name: %s", err.Error()), http.StatusBadRequest)
		return
	}

	// For views, check api_ prefix
	if isView && !strings.HasPrefix(name, "api_") {
		h.sendError(w, "Only api_ prefixed views are accessible", http.StatusForbidden)
		return
	}

	// Block access to internal auth tables
	if !isView && auth.IsInternalTable(name) {
		h.sendError(w, "Access to internal tables is forbidden", http.StatusForbidden)
		return
	}

	// Check existence
	var exists bool
	if isView {
		exists, err = h.dbMgr.ViewExists(name)
	} else {
		exists, err = h.dbMgr.TableExists(name)
	}

	if err != nil {
		h.logger.Error("Failed to check existence", zap.Error(err), zap.String("request_id", requestID))
		h.sendError(w, "Failed to check existence", http.StatusInternalServerError)
		return
	}
	if !exists {
		entityType := "Table"
		if isView {
			entityType = "View"
		}
		h.sendError(w, fmt.Sprintf("%s '%s' not found", entityType, name), http.StatusNotFound)
		return
	}

	// Check authorization for read operation
	role := auth.GetRoleFromContext(r.Context())
	allowed, err := h.authorizer.CheckPermission(role, name, auth.OperationRead)
	if err != nil {
		h.logger.Error("Failed to check permission", zap.Error(err), zap.String("request_id", requestID))
		h.sendError(w, "Failed to check permission", http.StatusInternalServerError)
		return
	}
	if !allowed {
		h.sendError(w, "Forbidden: insufficient permissions for READ operation", http.StatusForbidden)
		return
	}

	// Parse query parameters
	format := r.URL.Query().Get("format")
	if format == "" {
		format = "standard"
	}

	includeStats := r.URL.Query().Get("stats") == "true"
	sampleSize := 10000
	if sampleStr := r.URL.Query().Get("sample"); sampleStr != "" {
		if s, err := strconv.Atoi(sampleStr); err == nil && s > 0 {
			sampleSize = s
		}
	}

	// For summarize format, always include stats
	if format == "summarize" {
		includeStats = true
	}

	// Get schema or summary based on parameters
	if includeStats {
		h.handleWithStats(w, r, name, isView, format, sampleSize)
	} else {
		h.handleSchemaOnly(w, r, name, isView, format)
	}
}

// handleSchemaOnly handles requests for schema only (no statistics).
func (h *ColumnsHandler) handleSchemaOnly(w http.ResponseWriter, r *http.Request, name string, isView bool, format string) {
	requestID := auth.GetRequestIDFromContext(r.Context())

	var schema []database.ColumnSchema
	var err error

	if isView {
		schema, err = h.dbMgr.GetViewSchema(name)
	} else {
		schema, err = h.dbMgr.GetTableSchema(name)
	}

	if err != nil {
		h.logger.Error("Failed to get schema", zap.Error(err), zap.String("request_id", requestID))
		h.sendError(w, fmt.Sprintf("Failed to get schema: %s", err.Error()), http.StatusInternalServerError)
		return
	}

	// Convert to response format
	columns := make([]ColumnInfo, len(schema))
	for i, col := range schema {
		columns[i] = ColumnInfo{
			Name:     col.Name,
			Type:     col.Type,
			Nullable: col.Nullable,
		}
	}

	h.sendResponse(w, name, columns, format, 0, 0)
}

// handleWithStats handles requests that include statistics.
func (h *ColumnsHandler) handleWithStats(w http.ResponseWriter, r *http.Request, name string, isView bool, format string, sampleSize int) {
	requestID := auth.GetRequestIDFromContext(r.Context())

	var summary *database.TableSummary
	var err error

	if isView {
		summary, err = h.dbMgr.GetViewSummary(name, sampleSize)
	} else {
		summary, err = h.dbMgr.GetTableSummary(name, sampleSize)
	}

	if err != nil {
		h.logger.Error("Failed to get summary", zap.Error(err), zap.String("request_id", requestID))
		h.sendError(w, fmt.Sprintf("Failed to get summary: %s", err.Error()), http.StatusInternalServerError)
		return
	}

	// Convert to response format
	columns := make([]ColumnInfo, len(summary.Columns))
	for i, col := range summary.Columns {
		stats := map[string]interface{}{
			"min":             col.Min,
			"max":             col.Max,
			"approx_unique":   col.ApproxUnique,
			"count_sample":    col.Count,
			"null_percentage": col.NullPercentage,
		}

		// Only include numeric stats if they're available
		if col.Avg != nil {
			stats["avg"] = *col.Avg
		}
		if col.Std != nil {
			stats["std"] = *col.Std
		}
		if col.Q25 != nil {
			stats["q25"] = col.Q25
		}
		if col.Q50 != nil {
			stats["q50"] = col.Q50
		}
		if col.Q75 != nil {
			stats["q75"] = col.Q75
		}

		// Determine nullable based on null_percentage
		nullable := col.NullPercentage > 0

		columns[i] = ColumnInfo{
			Name:     col.ColumnName,
			Type:     col.ColumnType,
			Nullable: nullable,
			Stats:    stats,
		}
	}

	h.sendResponse(w, name, columns, format, summary.TotalRows, summary.SampleSize)
}

// sendResponse sends the response in the requested format.
func (h *ColumnsHandler) sendResponse(w http.ResponseWriter, name string, columns []ColumnInfo, format string, totalRows int64, sampleSize int) {
	w.Header().Set("Content-Type", "application/json")

	var response interface{}

	switch format {
	case "transform":
		// Transform format: {"columns": {"col": "TYPE", ...}}
		colMap := make(map[string]string)
		for _, col := range columns {
			colMap[col.Name] = col.Type
		}
		response = TransformColumnsResponse{
			Table:   name,
			Columns: colMap,
		}

	case "summarize":
		// Summarize format: includes all stats and row counts
		response = SummarizeColumnsResponse{
			Table:      name,
			TotalRows:  totalRows,
			SampleSize: sampleSize,
			Columns:    columns,
		}

	default:
		// Standard format (default)
		// Keep stats if they were requested (columns already have them populated)
		response = StandardColumnsResponse{
			Table:   name,
			Columns: columns,
		}
	}

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(response)
}

// extractTableName extracts the table name from path like /duckdb/api/{table}/columns
func (h *ColumnsHandler) extractTableName(path string) (string, error) {
	// Path format: /duckdb/api/{table}/columns
	prefix := "/duckdb/api/"
	suffix := "/columns"

	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, suffix) {
		return "", fmt.Errorf("invalid path format")
	}

	// Extract table name
	name := strings.TrimPrefix(path, prefix)
	name = strings.TrimSuffix(name, suffix)

	if name == "" {
		return "", fmt.Errorf("table name required")
	}

	return name, nil
}

// extractViewName extracts the view name from path like /duckdb/view/{name}/columns
func (h *ColumnsHandler) extractViewName(path string) (string, error) {
	// Path format: /duckdb/view/{name}/columns
	prefix := "/duckdb/view/"
	suffix := "/columns"

	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, suffix) {
		return "", fmt.Errorf("invalid path format")
	}

	// Extract view name
	name := strings.TrimPrefix(path, prefix)
	name = strings.TrimSuffix(name, suffix)

	if name == "" {
		return "", fmt.Errorf("view name required")
	}

	return name, nil
}

// sendError sends an error response.
func (h *ColumnsHandler) sendError(w http.ResponseWriter, message string, statusCode int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"error":   http.StatusText(statusCode),
		"message": message,
		"code":    statusCode,
	})
}
