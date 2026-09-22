package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/tobilg/caddy-duckdb-module/auth"
	"github.com/tobilg/caddy-duckdb-module/database"
	"github.com/tobilg/caddy-duckdb-module/formats"
	"go.uber.org/zap"
)

// GgsqlHandler handles POST /duckdb/ggsql: it runs the caller's SQL against this
// module's own DuckDB engine, sends the result as CSV to the ggvisual sidecar
// service alongside a ggsql VISUALISE clause, and relays the rendered chart back
// with its Content-Type preserved. See ggsql-endpoint.md for the full design.
type GgsqlHandler struct {
	dbMgr           *database.Manager
	authorizer      *auth.Authorizer
	serviceURL      string
	client          *http.Client
	absoluteMaxRows int
	logger          *zap.Logger
}

// NewGgsqlHandler creates a new ggsql chart-rendering handler proxying to ggvisual.
// absoluteMaxRows caps the SQL result before it's ever handed to the sidecar --
// ggvisual's own timeout/prlimit backstops bound how expensive a bad request is,
// but not how large a successful-but-enormous render can get (a 2M-row scatter
// plot was measured producing a 525MB spec / 7.4GB peak RSS in ggvisual's own
// scoping doc). Pass 0 to disable the cap.
func NewGgsqlHandler(dbMgr *database.Manager, authorizer *auth.Authorizer, serviceURL string, absoluteMaxRows int, logger *zap.Logger) *GgsqlHandler {
	return &GgsqlHandler{
		dbMgr:           dbMgr,
		authorizer:      authorizer,
		serviceURL:      strings.TrimSuffix(serviceURL, "/"),
		client:          &http.Client{Timeout: 35 * time.Second}, // above ggvisual's own 30s default request-timeout
		absoluteMaxRows: absoluteMaxRows,
		logger:          logger,
	}
}

// ggsqlRequest is the POST /duckdb/ggsql request body.
type ggsqlRequest struct {
	SQL       string `json:"sql"`
	Visualise string `json:"visualise"`
	Format    string `json:"format"`
}

// ggvisualErrorEnvelope is ggvisual's own structured error response shape --
// {"error":{"category":"bad_ggsql"|"bad_sql"|"timeout"|"internal","message":"..."}}
// per persistent-service.md's "Structured error envelope" section. ggvisual
// always returns this as JSON regardless of the requested render format.
type ggvisualErrorEnvelope struct {
	Error struct {
		Category string `json:"category"`
		Message  string `json:"message"`
	} `json:"error"`
}

// categoryToStatus maps ggvisual's error category to this module's own HTTP
// status, per ggsql-endpoint.md's explicit mapping. "unavailable" is not a
// category ggvisual itself emits -- render() uses it for transport-level
// failures (sidecar unreachable/interrupted), which map to 503 like any other
// upstream-down condition.
func categoryToStatus(category string) int {
	switch category {
	case "bad_ggsql", "bad_sql":
		return http.StatusBadRequest
	case "timeout":
		return http.StatusGatewayTimeout
	case "unavailable":
		return http.StatusServiceUnavailable
	default: // "internal" and anything unrecognized
		return http.StatusInternalServerError
	}
}

// ggsqlRenderError is a categorized render failure, from either running the
// local SQL/CSV encoding step or from ggvisual's own structured error
// envelope. Callers (the REST handler and the ggsql_chart MCP tool) each
// render it their own way -- an HTTP status for one, a chat-friendly message
// for the other -- without duplicating the category->status mapping.
type ggsqlRenderError struct {
	category string
	message  string
}

func (e *ggsqlRenderError) Error() string { return e.message }

// capSQL wraps sqlQuery in a LIMIT clause per absoluteMaxRows, if configured.
// Shared by the REST handler and the ggsql_chart MCP tool.
func (h *GgsqlHandler) capSQL(sqlQuery string) string {
	if h.absoluteMaxRows > 0 {
		return fmt.Sprintf("SELECT * FROM (%s) AS ggsql_capped LIMIT %d", sqlQuery, h.absoluteMaxRows)
	}
	return sqlQuery
}

// render runs sqlQuery against this module's own DuckDB engine, sends the
// result as CSV to the ggvisual sidecar alongside the visualise/format
// parameters, and returns the rendered chart bytes and the Content-Type
// ggvisual reported. Shared by ServeHTTP (REST) and the ggsql_chart MCP tool.
func (h *GgsqlHandler) render(ctx context.Context, sqlQuery, visualise, format string) ([]byte, string, error) {
	if format == "" {
		format = "vegalite"
	}

	rows, err := h.dbMgr.QueryMain(sqlQuery)
	if err != nil {
		return nil, "", &ggsqlRenderError{category: "bad_sql", message: fmt.Sprintf("Query failed: %s", err.Error())}
	}
	defer rows.Close()

	var csvBuf bytes.Buffer
	if _, err := formats.WriteCSVToWriter(&csvBuf, rows); err != nil {
		return nil, "", &ggsqlRenderError{category: "internal", message: "Failed to prepare query result for rendering: " + err.Error()}
	}

	proxyURL, err := url.Parse(h.serviceURL + "/render")
	if err != nil {
		return nil, "", &ggsqlRenderError{category: "internal", message: "Internal server error: " + err.Error()}
	}
	q := proxyURL.Query()
	q.Set("visual", visualise)
	q.Set("format", format)
	proxyURL.RawQuery = q.Encode()

	proxyReq, err := http.NewRequestWithContext(ctx, http.MethodPost, proxyURL.String(), &csvBuf)
	if err != nil {
		return nil, "", &ggsqlRenderError{category: "internal", message: "Internal server error: " + err.Error()}
	}
	proxyReq.Header.Set("Content-Type", "text/csv")

	resp, err := h.client.Do(proxyReq)
	if err != nil {
		// Covers connection refused/reset and context deadline exceeded alike --
		// per persistent-service.md's "Graceful shutdown" section, a ggvisual
		// redeploy mid-request produces exactly this (a transport error, no
		// envelope), and the caller should treat it as retriable rather than as
		// evidence of a bad request.
		return nil, "", &ggsqlRenderError{category: "unavailable", message: "ggvisual sidecar unavailable or interrupted -- safe to retry"}
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", &ggsqlRenderError{category: "internal", message: "Failed to read ggvisual response: " + err.Error()}
	}

	if resp.StatusCode >= 400 {
		var envelope ggvisualErrorEnvelope
		if err := json.Unmarshal(body, &envelope); err != nil || envelope.Error.Category == "" {
			// Unrecognized error shape -- fail safe rather than guessing.
			return nil, "", &ggsqlRenderError{category: "internal", message: "ggvisual returned an unrecognized error"}
		}
		return nil, "", &ggsqlRenderError{category: envelope.Error.Category, message: envelope.Error.Message}
	}

	return body, resp.Header.Get("Content-Type"), nil
}

// contentTypeExtensions maps ggvisual's known Content-Type responses to a file
// extension, verified directly against a running ggvisual instance across all
// 19 of its /formats entries (2026-09-22).
var contentTypeExtensions = map[string]string{
	"application/json": "json",
	"text/html":        "html",
	"image/png":        "png",
	"image/svg+xml":    "svg",
	"text/plain":       "txt",
}

// extensionForContentType picks a file extension for a materialized chart
// artifact from ggvisual's response Content-Type, falling back to a sanitized
// form of the requested ggsql format name for any Content-Type not in
// contentTypeExtensions (e.g. ggvisual's "url" format, which returns a plain
// text URL as application/octet-stream) -- defensive against ggvisual's
// format list evolving independently of this repo.
func extensionForContentType(contentType, format string) string {
	ct := contentType
	if idx := strings.Index(ct, ";"); idx >= 0 {
		ct = strings.TrimSpace(ct[:idx])
	}
	if ext, ok := contentTypeExtensions[ct]; ok {
		return ext
	}
	var b strings.Builder
	for _, r := range strings.ToLower(format) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	if b.Len() == 0 {
		return "bin"
	}
	return b.String()
}

// ServeHTTP handles POST /duckdb/ggsql.
func (h *GgsqlHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	requestID := auth.GetRequestIDFromContext(r.Context())

	if r.Method != http.MethodPost {
		h.sendError(w, "Method not allowed. Use POST.", http.StatusMethodNotAllowed)
		return
	}

	// Reuse the existing can_query permission -- running arbitrary SQL is the
	// same privilege boundary regardless of what happens to the result
	// afterward. Do not add a separate permission for this endpoint.
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

	var req ggsqlRequest
	defer r.Body.Close()
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.sendError(w, "Invalid JSON in request body", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.SQL) == "" {
		h.sendError(w, "'sql' field is required", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Visualise) == "" {
		h.sendError(w, "'visualise' field is required", http.StatusBadRequest)
		return
	}
	if containsInternalTables(req.SQL) {
		h.sendError(w, "Access to internal auth tables is forbidden", http.StatusForbidden)
		return
	}

	startTime := time.Now()
	body, contentType, err := h.render(r.Context(), h.capSQL(req.SQL), req.Visualise, req.Format)
	if err != nil {
		var rerr *ggsqlRenderError
		if errors.As(err, &rerr) {
			h.sendError(w, rerr.message, categoryToStatus(rerr.category))
		} else {
			h.logger.Error("ggsql render failed", zap.Error(err), zap.String("request_id", requestID))
			h.sendError(w, "Internal server error", http.StatusInternalServerError)
		}
		return
	}

	h.logger.Debug("ggvisual request completed",
		zap.Duration("duration", time.Since(startTime)),
		zap.String("request_id", requestID),
	)

	// Relay the response Content-Type and body byte-for-byte. Never re-wrap
	// this in JSON -- ansi/braille formats contain raw escape codes that JSON
	// string-escaping would corrupt (confirmed independently twice: via Go's
	// json.Marshal, and via ggsql-endpoint.md's own curl/xxd test).
	if contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	w.WriteHeader(http.StatusOK)
	w.Write(body)
}

// CheckHealth checks if the ggvisual sidecar service is healthy.
func (h *GgsqlHandler) CheckHealth() (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.serviceURL+"/health", nil)
	if err != nil {
		return false, err
	}
	resp, err := h.client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK, nil
}

func (h *GgsqlHandler) sendError(w http.ResponseWriter, message string, statusCode int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"error":   http.StatusText(statusCode),
		"message": message,
		"code":    statusCode,
	})
}
