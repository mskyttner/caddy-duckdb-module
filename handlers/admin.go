package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/tobilg/caddy-duckdb-module/auth"
	"github.com/tobilg/caddy-duckdb-module/database"
	"go.uber.org/zap"
)

// AdminHandler handles operational actions that change this server's live
// configuration without a restart. Currently: reloading init_file. Reuses
// the existing can_execute permission -- these actions are at least as
// sensitive as arbitrary write SQL, and this keeps the auth schema
// unchanged rather than adding a dedicated permission column for a single
// new action. See plans/reload-without-restart.md.
type AdminHandler struct {
	dbMgr      *database.Manager
	authorizer *auth.Authorizer
	logger     *zap.Logger
}

// NewAdminHandler creates a new admin handler.
func NewAdminHandler(dbMgr *database.Manager, authorizer *auth.Authorizer, logger *zap.Logger) *AdminHandler {
	return &AdminHandler{
		dbMgr:      dbMgr,
		authorizer: authorizer,
		logger:     logger,
	}
}

// ServeReloadInit handles POST /duckdb/admin/reload-init: re-runs the
// configured init_file against the live connection pool.
func (h *AdminHandler) ServeReloadInit(w http.ResponseWriter, r *http.Request) {
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

	statements, err := h.dbMgr.ReloadInitFile()
	if err != nil {
		h.logger.Error("Failed to reload init file", zap.Error(err), zap.String("request_id", requestID))
		h.sendError(w, "Reload failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	h.logger.Info("Init file reloaded",
		zap.Int("statements_executed", statements),
		zap.String("role", role),
		zap.String("request_id", requestID),
	)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"statements_executed": statements,
	})
}

func (h *AdminHandler) sendError(w http.ResponseWriter, message string, statusCode int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"error":   http.StatusText(statusCode),
		"message": message,
		"code":    statusCode,
	})
}
