package handlers

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/tobilg/caddy-duckdb-module/auth"
	"github.com/tobilg/caddy-duckdb-module/database"
	"go.uber.org/zap"
)

// ExecuteHandler handles raw SQL write operations (INSERT, UPDATE, DELETE, CREATE TABLE AS SELECT, etc.).
// Requires OperationExecute permission. Read-only queries are rejected — use /duckdb/ or /duckdb/query instead.
// Access to internal auth tables is always blocked.
type ExecuteHandler struct {
	dbMgr      *database.Manager
	authorizer *auth.Authorizer
	logger     *zap.Logger
}

// NewExecuteHandler creates a new execute handler.
func NewExecuteHandler(dbMgr *database.Manager, authorizer *auth.Authorizer, logger *zap.Logger) *ExecuteHandler {
	return &ExecuteHandler{
		dbMgr:      dbMgr,
		authorizer: authorizer,
		logger:     logger,
	}
}

// ServeHTTP handles POST /duckdb/execute requests.
func (h *ExecuteHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	requestID := auth.GetRequestIDFromContext(r.Context())

	if r.Method != http.MethodPost {
		h.sendError(w, "Method not allowed. Use POST.", http.StatusMethodNotAllowed)
		return
	}

	role := auth.GetRoleFromContext(r.Context())
	allowed, err := h.authorizer.CheckPermission(role, "*", auth.OperationExecute)
	if err != nil {
		h.logger.Error("Failed to check execute permission", zap.Error(err), zap.String("request_id", requestID))
		h.sendError(w, "Failed to check permission", http.StatusInternalServerError)
		return
	}
	if !allowed {
		h.sendError(w, "Forbidden: role does not have execute permission (run: auth-db permission add -r <role> -t '*' -o e)", http.StatusForbidden)
		return
	}

	defer r.Body.Close()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		h.sendError(w, "Failed to read request body", http.StatusBadRequest)
		return
	}

	sqlQuery := strings.TrimSpace(string(body))
	if sqlQuery == "" {
		h.sendError(w, "SQL statement is required", http.StatusBadRequest)
		return
	}

	// Block access to internal auth tables.
	if containsInternalTables(sqlQuery) {
		h.sendError(w, "Access to internal auth tables is forbidden", http.StatusForbidden)
		return
	}

	// Reject read-only queries — those belong on /duckdb/ or /duckdb/query.
	if h.isReadOnlyQuery(sqlQuery) {
		h.sendError(w, "Read-only queries are not accepted on this endpoint. Use POST /duckdb/ or GET /duckdb/query instead.", http.StatusBadRequest)
		return
	}

	h.logger.Info("Executing write statement",
		zap.String("role", role),
		zap.String("sql", sqlQuery),
		zap.String("request_id", requestID),
	)

	result, err := h.dbMgr.ExecMain(sqlQuery)
	if err != nil {
		h.logger.Error("Failed to execute statement", zap.Error(err), zap.String("sql", sqlQuery), zap.String("request_id", requestID))
		h.sendError(w, "Execution failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	rowsAffected, _ := result.RowsAffected()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"rows_affected": rowsAffected,
	})
}

// isReadOnlyQuery returns true if the SQL looks like a read-only query.
func (h *ExecuteHandler) isReadOnlyQuery(sql string) bool {
	trimmed := strings.TrimSpace(strings.ToUpper(stripSQLComments(sql)))
	return strings.HasPrefix(trimmed, "SELECT") ||
		strings.HasPrefix(trimmed, "WITH") ||
		strings.HasPrefix(trimmed, "FROM") ||
		strings.HasPrefix(trimmed, "SHOW") ||
		strings.HasPrefix(trimmed, "DESCRIBE") ||
		strings.HasPrefix(trimmed, "EXPLAIN") ||
		strings.HasPrefix(trimmed, "PRAGMA")
}

func (h *ExecuteHandler) sendError(w http.ResponseWriter, message string, statusCode int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"error":   http.StatusText(statusCode),
		"message": message,
		"code":    statusCode,
	})
}
