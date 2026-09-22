package handlers

import (
	"bytes"
	"context"
	"encoding/json"
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
// status, per ggsql-endpoint.md's explicit mapping.
func categoryToStatus(category string) int {
	switch category {
	case "bad_ggsql", "bad_sql":
		return http.StatusBadRequest
	case "timeout":
		return http.StatusGatewayTimeout
	default: // "internal" and anything unrecognized
		return http.StatusInternalServerError
	}
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
	format := req.Format
	if format == "" {
		format = "vegalite"
	}

	sqlQuery := req.SQL
	if h.absoluteMaxRows > 0 {
		sqlQuery = fmt.Sprintf("SELECT * FROM (%s) AS ggsql_capped LIMIT %d", sqlQuery, h.absoluteMaxRows)
	}

	rows, err := h.dbMgr.QueryMain(sqlQuery)
	if err != nil {
		h.sendError(w, fmt.Sprintf("Query failed: %s", err.Error()), http.StatusBadRequest)
		return
	}
	defer rows.Close()

	var csvBuf bytes.Buffer
	if _, err := formats.WriteCSVToWriter(&csvBuf, rows); err != nil {
		h.logger.Error("Failed to write CSV for ggvisual", zap.Error(err), zap.String("request_id", requestID))
		h.sendError(w, "Failed to prepare query result for rendering", http.StatusInternalServerError)
		return
	}

	proxyURL, err := url.Parse(h.serviceURL + "/render")
	if err != nil {
		h.logger.Error("Failed to parse ggvisual service URL", zap.Error(err), zap.String("request_id", requestID))
		h.sendError(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	q := proxyURL.Query()
	q.Set("visual", req.Visualise)
	q.Set("format", format)
	proxyURL.RawQuery = q.Encode()

	proxyReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, proxyURL.String(), &csvBuf)
	if err != nil {
		h.logger.Error("Failed to create ggvisual request", zap.Error(err), zap.String("request_id", requestID))
		h.sendError(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	proxyReq.Header.Set("Content-Type", "text/csv")
	proxyReq.Header.Set("X-Request-ID", requestID)

	startTime := time.Now()
	resp, err := h.client.Do(proxyReq)
	if err != nil {
		// Covers connection refused/reset and context deadline exceeded alike --
		// per persistent-service.md's "Graceful shutdown" section, a ggvisual
		// redeploy mid-request produces exactly this (a transport error, no
		// envelope), and the caller should treat it as retriable rather than as
		// evidence of a bad request.
		h.logger.Error("ggvisual request failed", zap.Error(err), zap.String("request_id", requestID))
		h.sendError(w, "ggvisual sidecar unavailable or interrupted -- safe to retry", http.StatusServiceUnavailable)
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		h.logger.Error("Failed to read ggvisual response", zap.Error(err), zap.String("request_id", requestID))
		h.sendError(w, "Failed to read ggvisual response", http.StatusInternalServerError)
		return
	}

	h.logger.Debug("ggvisual request completed",
		zap.Int("status", resp.StatusCode),
		zap.Duration("duration", time.Since(startTime)),
		zap.String("request_id", requestID),
	)

	if resp.StatusCode >= 400 {
		var envelope ggvisualErrorEnvelope
		if err := json.Unmarshal(body, &envelope); err != nil || envelope.Error.Category == "" {
			// Unrecognized error shape -- fail safe rather than guessing.
			h.sendError(w, "ggvisual returned an unrecognized error", http.StatusInternalServerError)
			return
		}
		h.sendError(w, envelope.Error.Message, categoryToStatus(envelope.Error.Category))
		return
	}

	// Success: relay the response Content-Type and body byte-for-byte. Never
	// re-wrap this in JSON -- ansi/braille formats contain raw escape codes
	// that JSON string-escaping would corrupt (confirmed independently twice:
	// via Go's json.Marshal, and via ggsql-endpoint.md's own curl/xxd test).
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.WriteHeader(resp.StatusCode)
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
