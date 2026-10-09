package handlers

import (
	"crypto/md5"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/tobilg/caddy-duckdb-module/auth"
	"github.com/tobilg/caddy-duckdb-module/database"
	"go.uber.org/zap"
)

// HTMLHandler serves a single table row's rendered HTML column as a browser-
// facing document (GET /duckdb/html/{table}/{id}), or an index/search
// listing page (GET /duckdb/html/{table}, optionally with ?q=), with
// ETag-based conditional GET support.
//
// If a table macro named "{table}_html" exists, it's called instead of
// querying the table's static column directly -- this is the dynamic
// rendering path (e.g. a Tera template via the DuckDB "tera" community
// extension, as in ../kth-works/katharsis/srv/.duckdbrc's render_html macro).
// The listing path works the same way via "{table}_html_search" and
// "{table}_html_index" -- there's no static-column fallback for a listing,
// since there's no single row to read a column from.
// All paths share the same permission check, ETag computation, and
// Content-Type handling.
type HTMLHandler struct {
	dbMgr      *database.Manager
	authorizer *auth.Authorizer
	logger     *zap.Logger
}

// NewHTMLHandler creates a new HTML record handler.
func NewHTMLHandler(dbMgr *database.Manager, authorizer *auth.Authorizer, logger *zap.Logger) *HTMLHandler {
	return &HTMLHandler{
		dbMgr:      dbMgr,
		authorizer: authorizer,
		logger:     logger,
	}
}

// ServeHTTP handles GET and HEAD /duckdb/html/{table}/{id}. HEAD runs the
// same permission/existence/render steps as GET (ETag depends on rendered
// content, so it can't be computed without running the query -- unlike
// crud.go's cheap, query-free HEAD) but writes no body.
// urlPrefix is the route prefix this handler is mounted under (e.g.
// "/duckdb/html"), matching the convention used by ExportHandler.ServeDownload.
func (h *HTMLHandler) ServeHTTP(w http.ResponseWriter, r *http.Request, urlPrefix string) {
	requestID := auth.GetRequestIDFromContext(r.Context())

	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		h.sendError(w, "Method not allowed. Use GET or HEAD.", http.StatusMethodNotAllowed)
		return
	}

	tableName, id, ok := extractTableAndID(r.URL.Path, urlPrefix)
	if !ok {
		h.sendError(w, "Invalid path: expected /html/{table} or /html/{table}/{id}", http.StatusBadRequest)
		return
	}

	if err := SanitizeTableName(tableName); err != nil {
		h.sendError(w, err.Error(), http.StatusBadRequest)
		return
	}

	if auth.IsInternalTable(tableName) {
		h.sendError(w, "Access to internal tables is forbidden", http.StatusForbidden)
		return
	}

	htmlColumn := r.URL.Query().Get("html_column")
	if htmlColumn == "" {
		htmlColumn = "html"
	}
	if err := SanitizeColumnName(htmlColumn); err != nil {
		h.sendError(w, err.Error(), http.StatusBadRequest)
		return
	}

	idColumn := r.URL.Query().Get("id_column")
	if idColumn == "" {
		idColumn = "id"
	}
	if err := SanitizeColumnName(idColumn); err != nil {
		h.sendError(w, err.Error(), http.StatusBadRequest)
		return
	}

	role := auth.GetRoleFromContext(r.Context())
	allowed, err := h.authorizer.CheckPermission(role, tableName, auth.OperationRead)
	if err != nil {
		h.logger.Error("Failed to check permission", zap.Error(err), zap.String("request_id", requestID))
		h.sendError(w, "Failed to check permission", http.StatusInternalServerError)
		return
	}
	if !allowed {
		// The reserved "public" role stands in for a missing API key (see
		// module.go's auth fallback): a denial there means we don't know
		// whether real credentials would succeed, so ask for them (401)
		// rather than asserting the resource is forbidden outright (403).
		if role == "public" {
			h.sendError(w, "Unauthorized: this table is not publicly readable", http.StatusUnauthorized)
		} else {
			h.sendError(w, "Forbidden: insufficient permissions", http.StatusForbidden)
		}
		return
	}

	exists, err := h.dbMgr.TableExists(tableName)
	if err != nil {
		h.logger.Error("Failed to check table existence", zap.Error(err), zap.String("request_id", requestID))
		h.sendError(w, "Failed to check table existence", http.StatusInternalServerError)
		return
	}
	if !exists {
		h.sendError(w, fmt.Sprintf("Table '%s' does not exist", tableName), http.StatusNotFound)
		return
	}

	var htmlContent string
	if id != "" {
		htmlContent, err = h.renderRecord(tableName, id, htmlColumn, idColumn)
	} else {
		htmlContent, err = h.renderListing(tableName, htmlColumn, r.URL.Query())
	}
	if err != nil {
		if err == sql.ErrNoRows {
			h.sendError(w, "Not found", http.StatusNotFound)
			return
		}
		if err == errListingNotAvailable {
			h.sendError(w, fmt.Sprintf("Index/search not available for table '%s'", tableName), http.StatusNotFound)
			return
		}
		h.logger.Error("Failed to render record", zap.Error(err), zap.String("table", tableName), zap.String("request_id", requestID))
		h.sendError(w, "Failed to render record", http.StatusInternalServerError)
		return
	}

	hash := md5.Sum([]byte(htmlContent))
	etag := `"` + hex.EncodeToString(hash[:]) + `"`

	if match := r.Header.Get("If-None-Match"); match != "" && (match == etag || match == "*") {
		w.Header().Set("ETag", etag)
		w.WriteHeader(http.StatusNotModified)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("ETag", etag)
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		w.Write([]byte(htmlContent))
	}
}

// renderRecord fetches a single row's rendered HTML. If a table macro named
// "{table}_html" exists, it's used (dynamic rendering); otherwise the
// static html/id columns are queried directly.
func (h *HTMLHandler) renderRecord(tableName, id, htmlColumn, idColumn string) (string, error) {
	macroName := tableName + "_html"
	hasMacro, err := h.dbMgr.MacroExists(macroName)
	if err != nil {
		return "", err
	}

	var query string
	if hasMacro {
		query = fmt.Sprintf(`SELECT "%s" FROM "%s"($1)`, htmlColumn, macroName)
	} else {
		query = fmt.Sprintf(`SELECT "%s" FROM "%s" WHERE "%s" = $1`, htmlColumn, tableName, idColumn)
	}

	var htmlContent string
	if err := h.dbMgr.QueryRowScanMain(query, []interface{}{&htmlContent}, id); err != nil {
		return "", err
	}
	return htmlContent, nil
}

// errListingNotAvailable is returned by renderListing when neither a
// "{table}_html_search" nor a "{table}_html_index" macro exists for the
// requested table.
var errListingNotAvailable = fmt.Errorf("index/search not available for this table")

// renderListing fetches an index or search-results page for a table with no
// id in the path. If query carries a non-empty "q" and a "{table}_html_search"
// macro exists, that macro is called with q bound as $1. Otherwise, if a
// "{table}_html_index" macro exists, it's called with "page" (query param,
// default "1") bound as $1. If neither applies, errListingNotAvailable is
// returned -- there's no static-column fallback for a listing, unlike the
// single-record case.
func (h *HTMLHandler) renderListing(tableName, htmlColumn string, query url.Values) (string, error) {
	q := query.Get("q")
	if q != "" {
		searchMacro := tableName + "_html_search"
		hasSearch, err := h.dbMgr.MacroExists(searchMacro)
		if err != nil {
			return "", err
		}
		if hasSearch {
			return h.callListingMacro(searchMacro, htmlColumn, q)
		}
	}

	indexMacro := tableName + "_html_index"
	hasIndex, err := h.dbMgr.MacroExists(indexMacro)
	if err != nil {
		return "", err
	}
	if !hasIndex {
		return "", errListingNotAvailable
	}

	page := query.Get("page")
	if page == "" {
		page = "1"
	}
	return h.callListingMacro(indexMacro, htmlColumn, page)
}

func (h *HTMLHandler) callListingMacro(macroName, htmlColumn, arg string) (string, error) {
	query := fmt.Sprintf(`SELECT "%s" FROM "%s"($1)`, htmlColumn, macroName)
	var htmlContent string
	if err := h.dbMgr.QueryRowScanMain(query, []interface{}{&htmlContent}, arg); err != nil {
		return "", err
	}
	return htmlContent, nil
}

// extractTableAndID parses the request path after urlPrefix into a table
// name and an optional id, e.g. "/duckdb/html/pub/1" with urlPrefix
// "/duckdb/html" -> ("pub", "1", true); "/duckdb/html/pub" -> ("pub", "",
// true) -- the listing shape, no id. ok is false if the path has zero or
// more than two segments, or any segment is empty.
func extractTableAndID(path, urlPrefix string) (table, id string, ok bool) {
	remaining := strings.TrimPrefix(path, urlPrefix)
	remaining = strings.Trim(remaining, "/")
	if remaining == "" {
		return "", "", false
	}
	parts := strings.Split(remaining, "/")
	switch len(parts) {
	case 1:
		if parts[0] == "" {
			return "", "", false
		}
		return parts[0], "", true
	case 2:
		if parts[0] == "" || parts[1] == "" {
			return "", "", false
		}
		return parts[0], parts[1], true
	default:
		return "", "", false
	}
}

func (h *HTMLHandler) sendError(w http.ResponseWriter, message string, statusCode int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"error":   http.StatusText(statusCode),
		"message": message,
		"code":    statusCode,
	})
}
