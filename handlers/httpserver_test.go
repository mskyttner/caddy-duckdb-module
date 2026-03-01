package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tobilg/caddy-duckdb-module/auth"
	"github.com/tobilg/caddy-duckdb-module/database"
	"go.uber.org/zap"
)

func setupHTTPServerHandler(t *testing.T) (*HTTPServerHandler, *database.Manager, func()) {
	t.Helper()
	cfg := database.Config{
		MainDBPath:   ":memory:",
		AuthDBPath:   ":memory:",
		Threads:      1,
		AccessMode:   "read_write",
		QueryTimeout: 30 * time.Second,
		Logger:       zap.NewNop(),
	}

	mgr, err := database.NewManagerForTesting(cfg)
	if err != nil {
		t.Fatalf("Failed to create manager: %v", err)
	}

	_, err = mgr.ExecMain(`CREATE TABLE test_hs (id INTEGER PRIMARY KEY, name VARCHAR, value DOUBLE)`)
	if err != nil {
		mgr.Close()
		t.Fatalf("Failed to create test table: %v", err)
	}
	_, err = mgr.ExecMain(`INSERT INTO test_hs VALUES (1, 'Alice', 1.0), (2, 'Bob', 2.0)`)
	if err != nil {
		mgr.Close()
		t.Fatalf("Failed to insert test data: %v", err)
	}

	authorizer := auth.NewAuthorizer(mgr.AuthDB())
	handler := NewHTTPServerHandler(mgr, authorizer, zap.NewNop())
	return handler, mgr, func() { mgr.Close() }
}

func addHTTPServerAuthContext(r *http.Request, role string) *http.Request {
	ctx := context.WithValue(r.Context(), auth.ContextKeyRole, role)
	ctx = context.WithValue(ctx, auth.ContextKeyRequestID, "test-req-id")
	return r.WithContext(ctx)
}

func TestHTTPServerHandler_POST_DefaultsToJSONCompact(t *testing.T) {
	handler, _, cleanup := setupHTTPServerHandler(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodPost, "/duckdb/", strings.NewReader("SELECT 42 AS answer"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = addHTTPServerAuthContext(req, "admin")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("Expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// JSONCompact format has "meta", "data", "rows" keys
	var result map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}
	if _, ok := result["meta"]; !ok {
		t.Error("Expected JSONCompact format with 'meta' key")
	}
	if _, ok := result["data"]; !ok {
		t.Error("Expected JSONCompact format with 'data' key")
	}
}

func TestHTTPServerHandler_POST_FormatHeader(t *testing.T) {
	handler, _, cleanup := setupHTTPServerHandler(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodPost, "/duckdb/", strings.NewReader("SELECT 42 AS answer"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("format", "JSONCompact")
	req = addHTTPServerAuthContext(req, "admin")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("Expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var result map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}
	if _, ok := result["meta"]; !ok {
		t.Error("Expected JSONCompact format with 'meta' key")
	}
}

func TestHTTPServerHandler_POST_SelectRows(t *testing.T) {
	handler, _, cleanup := setupHTTPServerHandler(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodPost, "/duckdb/", strings.NewReader("SELECT * FROM test_hs ORDER BY id"))
	req = addHTTPServerAuthContext(req, "admin")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("Expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var result map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}
	rows, ok := result["rows"].(float64)
	if !ok || rows != 2 {
		t.Errorf("Expected 2 rows, got %v", result["rows"])
	}
}

func TestHTTPServerHandler_POST_WriteQuery_Forbidden(t *testing.T) {
	handler, _, cleanup := setupHTTPServerHandler(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodPost, "/duckdb/", strings.NewReader("INSERT INTO test_hs VALUES (99, 'X', 0)"))
	req = addHTTPServerAuthContext(req, "admin")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("Expected 403 for write query, got %d", rec.Code)
	}
}

func TestHTTPServerHandler_POST_InternalTable_Forbidden(t *testing.T) {
	handler, _, cleanup := setupHTTPServerHandler(t)
	defer cleanup()

	for _, sql := range []string{
		"SELECT * FROM api_keys",
		"SELECT * FROM roles",
		"SELECT * FROM permissions",
		"SELECT * FROM trusted_users",
	} {
		req := httptest.NewRequest(http.MethodPost, "/duckdb/", strings.NewReader(sql))
		req = addHTTPServerAuthContext(req, "admin")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("Expected 403 for %q, got %d", sql, rec.Code)
		}
	}
}

func TestHTTPServerHandler_POST_EmptyBody(t *testing.T) {
	handler, _, cleanup := setupHTTPServerHandler(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodPost, "/duckdb/", strings.NewReader(""))
	req = addHTTPServerAuthContext(req, "admin")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("Expected 400, got %d", rec.Code)
	}
}

func TestHTTPServerHandler_MethodNotAllowed(t *testing.T) {
	handler, _, cleanup := setupHTTPServerHandler(t)
	defer cleanup()

	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		req := httptest.NewRequest(method, "/duckdb/", nil)
		req = addHTTPServerAuthContext(req, "admin")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("Expected 405 for %s, got %d", method, rec.Code)
		}
	}
}

func TestHTTPServerHandler_HEAD(t *testing.T) {
	handler, _, cleanup := setupHTTPServerHandler(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodHead, "/duckdb/", nil)
	req = addHTTPServerAuthContext(req, "admin")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Expected 200 for HEAD, got %d", rec.Code)
	}
	ct := rec.Header().Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Errorf("Expected application/json for HEAD, got %s", ct)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("Expected empty body for HEAD, got %d bytes", rec.Body.Len())
	}
}

func TestHTTPServerHandler_Forbidden_NoQueryPermission(t *testing.T) {
	handler, _, cleanup := setupHTTPServerHandler(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodPost, "/duckdb/", strings.NewReader("SELECT 1"))
	req = addHTTPServerAuthContext(req, "reader") // reader role has no query permission

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("Expected 403, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHTTPServerHandler_POST_ShowDatabases(t *testing.T) {
	handler, _, cleanup := setupHTTPServerHandler(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodPost, "/duckdb/", strings.NewReader("SHOW DATABASES"))
	req = addHTTPServerAuthContext(req, "admin")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHTTPServerHandler_POST_WithCTE(t *testing.T) {
	handler, _, cleanup := setupHTTPServerHandler(t)
	defer cleanup()

	sql := `WITH cte AS (SELECT * FROM test_hs WHERE id > 1) SELECT * FROM cte`
	req := httptest.NewRequest(http.MethodPost, "/duckdb/", strings.NewReader(sql))
	req = addHTTPServerAuthContext(req, "admin")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}
