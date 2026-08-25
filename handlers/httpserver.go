package handlers

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/tobilg/caddy-duckdb-module/auth"
	"github.com/tobilg/caddy-duckdb-module/database"
	"github.com/tobilg/caddy-duckdb-module/formats"
	"go.uber.org/zap"
)

// HTTPServerHandler handles DuckDB-httpserver-compatible requests.
// Accepts raw SQL via POST with Content-Type application/x-www-form-urlencoded
// (body = raw SQL string) and defaults to JSONCompact output format.
// This enables duck-ui and other httpserver-compatible clients to connect directly.
type HTTPServerHandler struct {
	dbMgr      *database.Manager
	authorizer *auth.Authorizer
	logger     *zap.Logger
}

// NewHTTPServerHandler creates a new httpserver-compatible handler.
func NewHTTPServerHandler(dbMgr *database.Manager, authorizer *auth.Authorizer, logger *zap.Logger) *HTTPServerHandler {
	return &HTTPServerHandler{
		dbMgr:      dbMgr,
		authorizer: authorizer,
		logger:     logger,
	}
}

// ServeHTTP handles httpserver-compatible requests.
// Only POST and HEAD are accepted. The request body is the raw SQL string.
func (h *HTTPServerHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	requestID := auth.GetRequestIDFromContext(r.Context())

	// Check authorization
	role := auth.GetRoleFromContext(r.Context())
	allowed, err := h.authorizer.CheckPermission(role, "*", auth.OperationQuery)
	if err != nil {
		h.logger.Error("Failed to check permission", zap.Error(err), zap.String("request_id", requestID))
		h.sendError(w, "Failed to check permission", http.StatusInternalServerError)
		return
	}
	if !allowed {
		h.sendError(w, "Forbidden: insufficient permissions for raw SQL queries", http.StatusForbidden)
		return
	}

	switch r.Method {
	case http.MethodHead:
		format := GetAcceptFormatWithDefault(r, "JSONCompact")
		h.sendHeadResponse(w, format)
		return

	case http.MethodPost:
		// fall through to query handling

	default:
		h.sendError(w, "Method not allowed. Use POST to execute queries.", http.StatusMethodNotAllowed)
		return
	}

	// Read raw SQL from request body
	defer r.Body.Close()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		h.sendError(w, "Failed to read request body", http.StatusBadRequest)
		return
	}

	sqlQuery := strings.TrimSpace(string(body))
	if sqlQuery == "" {
		h.sendError(w, "SQL query is required", http.StatusBadRequest)
		return
	}

	// Prevent access to internal auth tables
	if containsInternalTables(sqlQuery) {
		h.sendError(w, "Access to internal auth tables is forbidden", http.StatusForbidden)
		return
	}

	// Only allow read-only SQL on this endpoint
	if !h.isSelectQuery(sqlQuery) {
		h.sendError(w, "Only read-only queries are allowed on this endpoint (SELECT, WITH, FROM, SHOW, DESCRIBE, EXPLAIN, PRAGMA)", http.StatusForbidden)
		return
	}

	format := GetAcceptFormatWithDefault(r, "JSONCompact")

	h.logger.Info("Executing httpserver query",
		zap.String("role", role),
		zap.String("sql", sqlQuery),
		zap.String("format", format),
		zap.String("request_id", requestID),
	)

	startTime := time.Now()
	rows, err := h.dbMgr.QueryMain(sqlQuery)
	executionTime := time.Since(startTime)
	if err != nil {
		h.logger.Error("Failed to execute query", zap.Error(err), zap.String("sql", sqlQuery), zap.String("request_id", requestID))
		h.sendError(w, "Query execution failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	switch format {
	case "csv":
		if err := formats.WriteCSV(w, rows); err != nil {
			h.logger.Error("Failed to format response", zap.Error(err), zap.String("request_id", requestID))
		}
	case "compact", "JSONCompact":
		if err := formats.WriteJSONCompact(w, rows, executionTime); err != nil {
			h.logger.Error("Failed to format response", zap.Error(err), zap.String("request_id", requestID))
		}
	case "meta":
		if err := formats.WriteJSONWithMeta(w, rows, executionTime); err != nil {
			h.logger.Error("Failed to format response", zap.Error(err), zap.String("request_id", requestID))
		}
	case "parquet":
		if err := formats.WriteParquet(w, rows); err != nil {
			h.logger.Error("Failed to format response", zap.Error(err), zap.String("request_id", requestID))
		}
	case "arrow":
		if err := formats.WriteArrowIPC(w, rows); err != nil {
			h.logger.Error("Failed to format response", zap.Error(err), zap.String("request_id", requestID))
		}
	default:
		// json, JSONEachRow, and anything else → standard JSON array of objects
		if err := formats.WriteJSON(w, rows, 1, 0, 0, false, 0, nil, ""); err != nil {
			h.logger.Error("Failed to format response", zap.Error(err), zap.String("request_id", requestID))
		}
	}
}

// isSelectQuery checks if the SQL query is read-only.
func (h *HTTPServerHandler) isSelectQuery(sql string) bool {
	trimmed := strings.TrimSpace(strings.ToUpper(sql))
	return strings.HasPrefix(trimmed, "SELECT") ||
		strings.HasPrefix(trimmed, "WITH") ||
		strings.HasPrefix(trimmed, "FROM") ||
		strings.HasPrefix(trimmed, "SHOW") ||
		strings.HasPrefix(trimmed, "DESCRIBE") ||
		strings.HasPrefix(trimmed, "EXPLAIN") ||
		strings.HasPrefix(trimmed, "PRAGMA")
}

// sendError writes a JSON error response.
func (h *HTTPServerHandler) sendError(w http.ResponseWriter, message string, statusCode int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"error":   http.StatusText(statusCode),
		"message": message,
		"code":    statusCode,
	})
}

// sendHeadResponse sends headers for HEAD requests without body.
func (h *HTTPServerHandler) sendHeadResponse(w http.ResponseWriter, format string) {
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
