package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/tobilg/caddy-duckdb-module/auth"
	"go.uber.org/zap"
)

// FTSHandler handles full-text search requests by proxying to the FTS sidecar service.
type FTSHandler struct {
	serviceURL string
	client     *http.Client
	authorizer *auth.Authorizer
	logger     *zap.Logger
}

// FTSSearchResponse represents the response from an FTS search
type FTSSearchResponse struct {
	Query           string                   `json:"query"`
	Table           string                   `json:"table"`
	Hits            []map[string]interface{} `json:"hits"`
	TotalHits       int                      `json:"total_hits"`
	ExecutionTimeMs int64                    `json:"execution_time_ms"`
}

// NewFTSHandler creates a new FTS handler that proxies to the sidecar service.
func NewFTSHandler(serviceURL string, authorizer *auth.Authorizer, logger *zap.Logger) *FTSHandler {
	// Ensure URL doesn't have trailing slash
	serviceURL = strings.TrimSuffix(serviceURL, "/")

	return &FTSHandler{
		serviceURL: serviceURL,
		client: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:        100,
				MaxIdleConnsPerHost: 100,
				IdleConnTimeout:     90 * time.Second,
			},
		},
		authorizer: authorizer,
		logger:     logger,
	}
}

// ServeHTTP handles HTTP requests for full-text search.
// Endpoint: GET /duckdb/find?q={query}&table={table}&limit={n}&columns={cols}
func (h *FTSHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	requestID := auth.GetRequestIDFromContext(r.Context())

	// Only allow GET and HEAD methods
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		h.sendError(w, "Method not allowed. Use GET for search.", http.StatusMethodNotAllowed)
		return
	}

	// Handle HEAD request
	if r.Method == http.MethodHead {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		return
	}

	// Parse query parameters
	query := r.URL.Query().Get("q")
	table := r.URL.Query().Get("table")
	limit := r.URL.Query().Get("limit")
	columns := r.URL.Query().Get("columns")
	filter := r.URL.Query().Get("filter")
	highlight := r.URL.Query().Get("highlight")
	autocomplete := r.URL.Query().Get("autocomplete")

	// Validate required parameters
	if query == "" {
		h.sendError(w, "Query parameter 'q' is required", http.StatusBadRequest)
		return
	}
	if table == "" {
		h.sendError(w, "Query parameter 'table' is required", http.StatusBadRequest)
		return
	}

	// Check authorization - user must have read permission on the table
	role := auth.GetRoleFromContext(r.Context())
	allowed, err := h.authorizer.CheckPermission(role, table, auth.OperationRead)
	if err != nil {
		h.logger.Error("Failed to check permission",
			zap.Error(err),
			zap.String("request_id", requestID),
		)
		h.sendError(w, "Failed to check permission", http.StatusInternalServerError)
		return
	}
	if !allowed {
		h.sendError(w, fmt.Sprintf("Forbidden: insufficient permissions to read table '%s'", table), http.StatusForbidden)
		return
	}

	// Build proxy URL
	proxyURL, err := url.Parse(h.serviceURL + "/search")
	if err != nil {
		h.logger.Error("Failed to parse service URL",
			zap.Error(err),
			zap.String("request_id", requestID),
		)
		h.sendError(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	// Copy query parameters
	proxyQuery := proxyURL.Query()
	proxyQuery.Set("q", query)
	proxyQuery.Set("table", table)
	if limit != "" {
		proxyQuery.Set("limit", limit)
	}
	if columns != "" {
		proxyQuery.Set("columns", columns)
	}
	if filter != "" {
		proxyQuery.Set("filter", filter)
	}
	if highlight != "" {
		proxyQuery.Set("highlight", highlight)
	}
	if autocomplete != "" {
		proxyQuery.Set("autocomplete", autocomplete)
	}
	proxyURL.RawQuery = proxyQuery.Encode()

	h.logger.Debug("Proxying FTS request",
		zap.String("url", proxyURL.String()),
		zap.String("table", table),
		zap.String("query", query),
		zap.String("request_id", requestID),
	)

	// Create proxy request
	proxyReq, err := http.NewRequestWithContext(r.Context(), http.MethodGet, proxyURL.String(), nil)
	if err != nil {
		h.logger.Error("Failed to create proxy request",
			zap.Error(err),
			zap.String("request_id", requestID),
		)
		h.sendError(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	// Forward request ID for tracing
	proxyReq.Header.Set("X-Request-ID", requestID)

	// Execute request
	startTime := time.Now()
	resp, err := h.client.Do(proxyReq)
	if err != nil {
		h.logger.Error("FTS service request failed",
			zap.Error(err),
			zap.String("request_id", requestID),
		)
		h.sendError(w, "FTS service unavailable", http.StatusServiceUnavailable)
		return
	}
	defer resp.Body.Close()

	// Read response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		h.logger.Error("Failed to read FTS response",
			zap.Error(err),
			zap.String("request_id", requestID),
		)
		h.sendError(w, "Failed to read FTS response", http.StatusInternalServerError)
		return
	}

	h.logger.Debug("FTS request completed",
		zap.Int("status", resp.StatusCode),
		zap.Duration("duration", time.Since(startTime)),
		zap.String("request_id", requestID),
	)

	// Forward response headers
	for key, values := range resp.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}

	// Ensure content type is set
	w.Header().Set("Content-Type", "application/json")

	// Forward status code and body
	w.WriteHeader(resp.StatusCode)
	w.Write(body)
}

// CheckHealth checks if the FTS service is healthy
func (h *FTSHandler) CheckHealth() (bool, error) {
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

func (h *FTSHandler) sendError(w http.ResponseWriter, message string, statusCode int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"error":   http.StatusText(statusCode),
		"message": message,
		"code":    statusCode,
	})
}
