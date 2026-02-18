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

// MacroHandler handles requests to execute table macros.
type MacroHandler struct {
	dbMgr           *database.Manager
	authorizer      *auth.Authorizer
	maxRowsPerPage  int
	absoluteMaxRows int
	logger          *zap.Logger
}

// NewMacroHandler creates a new MacroHandler.
func NewMacroHandler(dbMgr *database.Manager, authorizer *auth.Authorizer, maxRowsPerPage, absoluteMaxRows int, logger *zap.Logger) *MacroHandler {
	return &MacroHandler{
		dbMgr:           dbMgr,
		authorizer:      authorizer,
		maxRowsPerPage:  maxRowsPerPage,
		absoluteMaxRows: absoluteMaxRows,
		logger:          logger,
	}
}

// ServeHTTP handles HTTP requests for macro operations.
func (h *MacroHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Only GET and HEAD methods allowed (read-only)
	// HEAD is supported for HTTP clients that check content-type/size before downloading
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		h.sendError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Extract macro name from path: /duckdb/macro or /duckdb/macro/{name}
	macroName := h.extractMacroName(r.URL.Path)

	// Handle HEAD request - return headers only
	if r.Method == http.MethodHead {
		format := GetAcceptFormat(r)
		h.sendHeadResponse(w, format)
		return
	}

	if macroName == "" {
		// List all available macros
		h.listMacros(w, r)
	} else {
		// Execute the specified macro
		h.executeMacro(w, r, macroName)
	}
}

// extractMacroName extracts the macro name from the request path.
// Path format: /duckdb/macro/{name} or /duckdb/macro
func (h *MacroHandler) extractMacroName(path string) string {
	// Remove the /duckdb/macro prefix
	prefix := "/duckdb/macro"
	if !strings.HasPrefix(path, prefix) {
		return ""
	}

	remaining := strings.TrimPrefix(path, prefix)
	remaining = strings.Trim(remaining, "/")

	if remaining == "" {
		return ""
	}

	// Return the macro name (first path segment)
	parts := strings.SplitN(remaining, "/", 2)
	return parts[0]
}

// listMacros returns a list of all available api_* macros.
func (h *MacroHandler) listMacros(w http.ResponseWriter, r *http.Request) {
	requestID := auth.GetRequestIDFromContext(r.Context())

	macros, err := h.dbMgr.ListAPIMacros()
	if err != nil {
		h.logger.Error("Failed to list macros", zap.Error(err), zap.String("request_id", requestID))
		h.sendError(w, "Failed to list macros", http.StatusInternalServerError)
		return
	}

	response := map[string]interface{}{
		"macros": macros,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(response)
}

// executeMacro executes a table macro with parameters from query string.
func (h *MacroHandler) executeMacro(w http.ResponseWriter, r *http.Request, macroName string) {
	requestID := auth.GetRequestIDFromContext(r.Context())

	// Validate macro name
	if err := SanitizeTableName(macroName); err != nil {
		h.sendError(w, fmt.Sprintf("Invalid macro name: %s", err.Error()), http.StatusBadRequest)
		return
	}

	// Check if macro has api_ prefix
	if !strings.HasPrefix(macroName, "api_") {
		h.sendError(w, "Only api_ prefixed macros are accessible", http.StatusForbidden)
		return
	}

	// Check if macro exists
	exists, err := h.dbMgr.MacroExists(macroName)
	if err != nil {
		h.logger.Error("Failed to check macro existence", zap.Error(err), zap.String("request_id", requestID))
		h.sendError(w, "Failed to check macro existence", http.StatusInternalServerError)
		return
	}
	if !exists {
		h.sendError(w, fmt.Sprintf("Macro '%s' not found", macroName), http.StatusNotFound)
		return
	}

	// Parse pagination
	limit, offset, page, paginationRequested := ParsePagination(r, h.maxRowsPerPage, h.absoluteMaxRows)

	// Apply safety limit if pagination not requested
	safetyLimit := limit
	if !paginationRequested && h.absoluteMaxRows > 0 {
		safetyLimit = h.absoluteMaxRows
	}

	// Collect macro parameters from query string
	// Skip known reserved parameters so they are not forwarded to the macro
	skipParams := map[string]bool{
		"limit": true, "page": true, "links": true,
		"default_format": true, "select": true,
		"api_key": true, "cursor": true,
	}

	params := make(map[string]string)
	for key, values := range r.URL.Query() {
		if !skipParams[key] && len(values) > 0 {
			params[key] = values[0]
		}
	}

	// Execute macro
	rows, err := h.dbMgr.ExecuteMacro(macroName, params, safetyLimit, offset)
	if err != nil {
		h.logger.Error("Failed to execute macro", zap.Error(err), zap.String("macro", macroName), zap.String("request_id", requestID))
		h.sendError(w, fmt.Sprintf("Failed to execute macro: %s", err.Error()), http.StatusInternalServerError)
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

	// Format response (no total count for macros - would require re-executing)
	if err := h.formatResponse(w, rows, format, page, limit, 0, paginationRequested, safetyLimit, linksConfig); err != nil {
		h.logger.Error("Failed to format response", zap.Error(err), zap.String("request_id", requestID))
		h.sendError(w, "Failed to format response", http.StatusInternalServerError)
	}
}

// formatResponse formats the query result based on the requested format.
func (h *MacroHandler) formatResponse(w http.ResponseWriter, rows *sql.Rows, format string, page, limit int, totalRows int64, paginationRequested bool, safetyLimit int, linksConfig *formats.LinksConfig) error {
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
func (h *MacroHandler) sendError(w http.ResponseWriter, message string, statusCode int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"error":   http.StatusText(statusCode),
		"message": message,
		"code":    statusCode,
	})
}

// sendHeadResponse sends headers for HEAD requests without body.
func (h *MacroHandler) sendHeadResponse(w http.ResponseWriter, format string) {
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
