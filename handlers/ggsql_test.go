package handlers

import (
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

// setupGgsqlHandler creates a GgsqlHandler with a test database, pointed at the
// given mock ggvisual server URL (use "" and check per-test if a case doesn't
// need to reach the sidecar at all, e.g. validation-error tests).
func setupGgsqlHandler(t *testing.T, ggvisualURL string) (*GgsqlHandler, func()) {
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
	_, err = mgr.ExecMain(`CREATE TABLE works AS SELECT * FROM (VALUES
		(2020, 12), (2021, 18), (2022, 9)) AS v(year, revenue)`)
	if err != nil {
		mgr.Close()
		t.Fatalf("Failed to create test table: %v", err)
	}

	authorizer := auth.NewAuthorizer(mgr.AuthDB())
	handler := NewGgsqlHandler(mgr, authorizer, ggvisualURL, 0, zap.NewNop())

	cleanup := func() { mgr.Close() }
	return handler, cleanup
}

func addGgsqlAuthContext(r *http.Request, role string) *http.Request {
	ctx := r.Context()
	return r.WithContext(auth.SetContextValues(ctx, &auth.APIKey{RoleName: role}, role))
}

func TestGgsqlHandler_MethodNotAllowed(t *testing.T) {
	handler, cleanup := setupGgsqlHandler(t, "")
	defer cleanup()

	req := httptest.NewRequest("GET", "/duckdb/ggsql", nil)
	req = addGgsqlAuthContext(req, "admin")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rec.Code)
	}
}

func TestGgsqlHandler_Forbidden_NoQueryPermission(t *testing.T) {
	handler, cleanup := setupGgsqlHandler(t, "")
	defer cleanup()

	body := `{"sql":"SELECT * FROM works","visualise":"VISUALISE year AS x, revenue AS y DRAW bar"}`
	req := httptest.NewRequest("POST", "/duckdb/ggsql", strings.NewReader(body))
	req = addGgsqlAuthContext(req, "reader")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestGgsqlHandler_MissingFields(t *testing.T) {
	handler, cleanup := setupGgsqlHandler(t, "")
	defer cleanup()

	cases := []string{
		`{"visualise":"VISUALISE year AS x, revenue AS y DRAW bar"}`, // missing sql
		`{"sql":"SELECT * FROM works"}`,                              // missing visualise
		`not json`,
	}
	for _, body := range cases {
		req := httptest.NewRequest("POST", "/duckdb/ggsql", strings.NewReader(body))
		req = addGgsqlAuthContext(req, "admin")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("body %q: expected 400, got %d: %s", body, rec.Code, rec.Body.String())
		}
	}
}

func TestGgsqlHandler_InternalTableAccess_Forbidden(t *testing.T) {
	handler, cleanup := setupGgsqlHandler(t, "")
	defer cleanup()

	body := `{"sql":"SELECT * FROM api_keys","visualise":"VISUALISE key AS x DRAW bar"}`
	req := httptest.NewRequest("POST", "/duckdb/ggsql", strings.NewReader(body))
	req = addGgsqlAuthContext(req, "admin")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestGgsqlHandler_Success_RelaysContentTypeAndBody(t *testing.T) {
	var gotVisual, gotFormat, gotCSV string
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/render" {
			t.Errorf("expected /render, got %s", r.URL.Path)
		}
		gotVisual = r.URL.Query().Get("visual")
		gotFormat = r.URL.Query().Get("format")
		body := make([]byte, r.ContentLength)
		r.Body.Read(body)
		gotCSV = string(body)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"$schema":"vega-lite"}`))
	}))
	defer mock.Close()

	handler, cleanup := setupGgsqlHandler(t, mock.URL)
	defer cleanup()

	body := `{"sql":"SELECT * FROM works ORDER BY year","visualise":"VISUALISE year AS x, revenue AS y DRAW bar","format":"vegalite"}`
	req := httptest.NewRequest("POST", "/duckdb/ggsql", strings.NewReader(body))
	req = addGgsqlAuthContext(req, "admin")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("expected relayed Content-Type application/json, got %q", ct)
	}
	if rec.Body.String() != `{"$schema":"vega-lite"}` {
		t.Errorf("expected relayed body, got %q", rec.Body.String())
	}
	if gotVisual != "VISUALISE year AS x, revenue AS y DRAW bar" {
		t.Errorf("expected visual query param forwarded, got %q", gotVisual)
	}
	if gotFormat != "vegalite" {
		t.Errorf("expected format=vegalite forwarded, got %q", gotFormat)
	}
	if !strings.Contains(gotCSV, "year") || !strings.Contains(gotCSV, "2020") {
		t.Errorf("expected query result as CSV body, got %q", gotCSV)
	}
}

func TestGgsqlHandler_Success_PreservesANSIBytes(t *testing.T) {
	// Confirms the raw-bytes relay doesn't corrupt control characters the way
	// JSON-wrapping would (the finding this handler's own code comments cite).
	ansi := "\x1b[38;2;255;0;0mRED\x1b[0m"
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(ansi))
	}))
	defer mock.Close()

	handler, cleanup := setupGgsqlHandler(t, mock.URL)
	defer cleanup()

	body := `{"sql":"SELECT * FROM works","visualise":"VISUALISE year AS x, revenue AS y DRAW bar","format":"ansi"}`
	req := httptest.NewRequest("POST", "/duckdb/ggsql", strings.NewReader(body))
	req = addGgsqlAuthContext(req, "admin")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if rec.Body.String() != ansi {
		t.Errorf("expected exact ANSI bytes preserved, got %q", rec.Body.String())
	}
}

func TestGgsqlHandler_ErrorEnvelope_CategoryMapping(t *testing.T) {
	cases := []struct {
		category   string
		wantStatus int
	}{
		{"bad_ggsql", http.StatusBadRequest},
		{"bad_sql", http.StatusBadRequest},
		{"timeout", http.StatusGatewayTimeout},
		{"internal", http.StatusInternalServerError},
		{"something_unrecognized", http.StatusInternalServerError},
	}

	for _, tc := range cases {
		mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest) // ggvisual's own status is irrelevant; category drives ours
			env := ggvisualErrorEnvelope{}
			env.Error.Category = tc.category
			env.Error.Message = "boom"
			b, _ := json.Marshal(env)
			w.Write(b)
		}))

		handler, cleanup := setupGgsqlHandler(t, mock.URL)
		body := `{"sql":"SELECT * FROM works","visualise":"VISUALISE year AS x, revenue AS y DRAW bar"}`
		req := httptest.NewRequest("POST", "/duckdb/ggsql", strings.NewReader(body))
		req = addGgsqlAuthContext(req, "admin")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != tc.wantStatus {
			t.Errorf("category %q: expected status %d, got %d: %s", tc.category, tc.wantStatus, rec.Code, rec.Body.String())
		}

		mock.Close()
		cleanup()
	}
}

func TestGgsqlHandler_SidecarUnreachable(t *testing.T) {
	// Point at a URL nothing is listening on.
	handler, cleanup := setupGgsqlHandler(t, "http://127.0.0.1:1")
	defer cleanup()

	body := `{"sql":"SELECT * FROM works","visualise":"VISUALISE year AS x, revenue AS y DRAW bar"}`
	req := httptest.NewRequest("POST", "/duckdb/ggsql", strings.NewReader(body))
	req = addGgsqlAuthContext(req, "admin")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d: %s", rec.Code, rec.Body.String())
	}
}
