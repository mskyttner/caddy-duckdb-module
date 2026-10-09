package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/tobilg/caddy-duckdb-module/auth"
	"github.com/tobilg/caddy-duckdb-module/database"
	"go.uber.org/zap"
)

const htmlURLPrefix = "/duckdb/html"

func setupHTMLTestHandler(t *testing.T) (*HTMLHandler, *database.Manager, func()) {
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

	_, err = mgr.ExecMain(`
		CREATE TABLE pub (
			id INTEGER PRIMARY KEY,
			html VARCHAR
		)
	`)
	if err != nil {
		t.Fatalf("Failed to create test table: %v", err)
	}

	_, err = mgr.ExecMain(`INSERT INTO pub VALUES (1, '<p>static record 1</p>')`)
	if err != nil {
		t.Fatalf("Failed to insert test data: %v", err)
	}

	authorizer := auth.NewAuthorizer(mgr.AuthDB())
	handler := NewHTMLHandler(mgr, authorizer, zap.NewNop())

	cleanup := func() { mgr.Close() }
	return handler, mgr, cleanup
}

func TestHTMLHandler_StaticColumn_OK(t *testing.T) {
	handler, _, cleanup := setupHTMLTestHandler(t)
	defer cleanup()

	req := httptest.NewRequest("GET", htmlURLPrefix+"/pub/1", nil)
	req = addAuthContext(req, "admin")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req, htmlURLPrefix)

	if rec.Code != http.StatusOK {
		t.Fatalf("Expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "<p>static record 1</p>" {
		t.Errorf("Unexpected body: %s", rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("Expected text/html content type, got %q", ct)
	}
	if rec.Header().Get("ETag") == "" {
		t.Error("Expected ETag header to be set")
	}
}

// TestHTMLHandler_HEAD_OK verifies HEAD returns the same headers as GET
// (Content-Type, ETag) but with no body -- matching crud.go/macro.go's
// existing HEAD support convention in this codebase.
func TestHTMLHandler_HEAD_OK(t *testing.T) {
	handler, _, cleanup := setupHTMLTestHandler(t)
	defer cleanup()

	req := httptest.NewRequest("HEAD", htmlURLPrefix+"/pub/1", nil)
	req = addAuthContext(req, "admin")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req, htmlURLPrefix)

	if rec.Code != http.StatusOK {
		t.Fatalf("Expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("Expected text/html content type, got %q", ct)
	}
	if rec.Header().Get("ETag") == "" {
		t.Error("Expected ETag header to be set")
	}
	if rec.Body.Len() != 0 {
		t.Errorf("Expected empty body for HEAD, got %q", rec.Body.String())
	}
}

func TestHTMLHandler_HEAD_UnknownID_NotFound(t *testing.T) {
	handler, _, cleanup := setupHTMLTestHandler(t)
	defer cleanup()

	req := httptest.NewRequest("HEAD", htmlURLPrefix+"/pub/999", nil)
	req = addAuthContext(req, "admin")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req, htmlURLPrefix)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("Expected status 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestHTMLHandler_HEAD_ConditionalGET_NotModified verifies HEAD also honors
// If-None-Match, since computing it requires rendering the content anyway
// (unlike crud.go's cheap, query-free HEAD).
func TestHTMLHandler_HEAD_ConditionalGET_NotModified(t *testing.T) {
	handler, _, cleanup := setupHTMLTestHandler(t)
	defer cleanup()

	req := httptest.NewRequest("GET", htmlURLPrefix+"/pub/1", nil)
	req = addAuthContext(req, "admin")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req, htmlURLPrefix)
	etag := rec.Header().Get("ETag")
	if etag == "" {
		t.Fatal("expected ETag on first response")
	}

	req2 := httptest.NewRequest("HEAD", htmlURLPrefix+"/pub/1", nil)
	req2 = addAuthContext(req2, "admin")
	req2.Header.Set("If-None-Match", etag)
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2, htmlURLPrefix)

	if rec2.Code != http.StatusNotModified {
		t.Fatalf("Expected status 304, got %d: %s", rec2.Code, rec2.Body.String())
	}
	if rec2.Body.Len() != 0 {
		t.Errorf("Expected empty body on 304, got %q", rec2.Body.String())
	}
}

func TestHTMLHandler_UnknownID_NotFound(t *testing.T) {
	handler, _, cleanup := setupHTMLTestHandler(t)
	defer cleanup()

	req := httptest.NewRequest("GET", htmlURLPrefix+"/pub/999", nil)
	req = addAuthContext(req, "admin")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req, htmlURLPrefix)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("Expected status 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHTMLHandler_UnknownTable_NotFound(t *testing.T) {
	handler, _, cleanup := setupHTMLTestHandler(t)
	defer cleanup()

	req := httptest.NewRequest("GET", htmlURLPrefix+"/nope/1", nil)
	req = addAuthContext(req, "admin")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req, htmlURLPrefix)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("Expected status 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHTMLHandler_InternalTable_Forbidden(t *testing.T) {
	handler, _, cleanup := setupHTMLTestHandler(t)
	defer cleanup()

	req := httptest.NewRequest("GET", htmlURLPrefix+"/api_keys/1", nil)
	req = addAuthContext(req, "admin")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req, htmlURLPrefix)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("Expected status 403, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHTMLHandler_InvalidTableName_BadRequest(t *testing.T) {
	handler, _, cleanup := setupHTMLTestHandler(t)
	defer cleanup()

	req := httptest.NewRequest("GET", htmlURLPrefix+"/bad-name/1", nil)
	req = addAuthContext(req, "admin")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req, htmlURLPrefix)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("Expected status 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestHTMLHandler_TableOnly_NoListingMacros_NotFound verifies that
// /html/{table} (no id) -- once a 400 in v1 -- is now valid path shape that
// dispatches to index/search, and 404s (not 400) when neither
// "{table}_html_index" nor "{table}_html_search" exists for that table.
func TestHTMLHandler_TableOnly_NoListingMacros_NotFound(t *testing.T) {
	handler, _, cleanup := setupHTMLTestHandler(t)
	defer cleanup()

	req := httptest.NewRequest("GET", htmlURLPrefix+"/pub", nil)
	req = addAuthContext(req, "admin")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req, htmlURLPrefix)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("Expected status 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestHTMLHandler_TooManyPathSegments_BadRequest verifies a path with more
// than two segments after the table (neither the record nor the listing
// shape) is still rejected.
func TestHTMLHandler_TooManyPathSegments_BadRequest(t *testing.T) {
	handler, _, cleanup := setupHTMLTestHandler(t)
	defer cleanup()

	req := httptest.NewRequest("GET", htmlURLPrefix+"/pub/1/extra", nil)
	req = addAuthContext(req, "admin")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req, htmlURLPrefix)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("Expected status 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHTMLHandler_Index_OK(t *testing.T) {
	handler, mgr, cleanup := setupHTMLTestHandler(t)
	defer cleanup()

	_, err := mgr.ExecMain(`CREATE OR REPLACE MACRO pub_html_index(page) AS TABLE (SELECT 'index-page:' || page::VARCHAR AS html)`)
	if err != nil {
		t.Fatalf("Failed to create index macro: %v", err)
	}

	req := httptest.NewRequest("GET", htmlURLPrefix+"/pub", nil)
	req = addAuthContext(req, "admin")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req, htmlURLPrefix)

	if rec.Code != http.StatusOK {
		t.Fatalf("Expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "index-page:1" {
		t.Errorf("Expected default page 1, got %q", rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("Expected text/html content type, got %q", ct)
	}
}

func TestHTMLHandler_Index_WithPageParam(t *testing.T) {
	handler, mgr, cleanup := setupHTMLTestHandler(t)
	defer cleanup()

	_, err := mgr.ExecMain(`CREATE OR REPLACE MACRO pub_html_index(page) AS TABLE (SELECT 'index-page:' || page::VARCHAR AS html)`)
	if err != nil {
		t.Fatalf("Failed to create index macro: %v", err)
	}

	req := httptest.NewRequest("GET", htmlURLPrefix+"/pub?page=3", nil)
	req = addAuthContext(req, "admin")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req, htmlURLPrefix)

	if rec.Code != http.StatusOK {
		t.Fatalf("Expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "index-page:3" {
		t.Errorf("Expected page 3, got %q", rec.Body.String())
	}
}

func TestHTMLHandler_Search_OK(t *testing.T) {
	handler, mgr, cleanup := setupHTMLTestHandler(t)
	defer cleanup()

	_, err := mgr.ExecMain(`CREATE OR REPLACE MACRO pub_html_search(q) AS TABLE (SELECT 'results-for:' || q AS html)`)
	if err != nil {
		t.Fatalf("Failed to create search macro: %v", err)
	}

	req := httptest.NewRequest("GET", htmlURLPrefix+"/pub?q=duckdb", nil)
	req = addAuthContext(req, "admin")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req, htmlURLPrefix)

	if rec.Code != http.StatusOK {
		t.Fatalf("Expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "results-for:duckdb" {
		t.Errorf("Expected search results body, got %q", rec.Body.String())
	}
}

// TestHTMLHandler_Search_TakesPrecedenceOverIndex verifies that when both
// macros exist and ?q= is present, search wins -- matching
// caddy-html-duckdb's own dispatch precedence.
func TestHTMLHandler_Search_TakesPrecedenceOverIndex(t *testing.T) {
	handler, mgr, cleanup := setupHTMLTestHandler(t)
	defer cleanup()

	_, err := mgr.ExecMain(`CREATE OR REPLACE MACRO pub_html_index(page) AS TABLE (SELECT 'index-page:' || page::VARCHAR AS html)`)
	if err != nil {
		t.Fatalf("Failed to create index macro: %v", err)
	}
	_, err = mgr.ExecMain(`CREATE OR REPLACE MACRO pub_html_search(q) AS TABLE (SELECT 'results-for:' || q AS html)`)
	if err != nil {
		t.Fatalf("Failed to create search macro: %v", err)
	}

	req := httptest.NewRequest("GET", htmlURLPrefix+"/pub?q=duckdb", nil)
	req = addAuthContext(req, "admin")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req, htmlURLPrefix)

	if rec.Code != http.StatusOK {
		t.Fatalf("Expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "results-for:duckdb" {
		t.Errorf("Expected search to take precedence, got %q", rec.Body.String())
	}
}

// TestHTMLHandler_Search_QParamButNoSearchMacro_FallsBackToIndex verifies
// that ?q= with no "{table}_html_search" macro falls back to the index
// macro rather than 404ing outright, since step 1 of the dispatch only
// applies "if ... exists".
func TestHTMLHandler_Search_QParamButNoSearchMacro_FallsBackToIndex(t *testing.T) {
	handler, mgr, cleanup := setupHTMLTestHandler(t)
	defer cleanup()

	_, err := mgr.ExecMain(`CREATE OR REPLACE MACRO pub_html_index(page) AS TABLE (SELECT 'index-page:' || page::VARCHAR AS html)`)
	if err != nil {
		t.Fatalf("Failed to create index macro: %v", err)
	}

	req := httptest.NewRequest("GET", htmlURLPrefix+"/pub?q=duckdb", nil)
	req = addAuthContext(req, "admin")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req, htmlURLPrefix)

	if rec.Code != http.StatusOK {
		t.Fatalf("Expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "index-page:1" {
		t.Errorf("Expected fallback to index, got %q", rec.Body.String())
	}
}

func TestHTMLHandler_Index_PermissionDenied_Forbidden(t *testing.T) {
	handler, mgr, cleanup := setupHTMLTestHandler(t)
	defer cleanup()

	_, err := mgr.ExecMain(`CREATE OR REPLACE MACRO pub_html_index(page) AS TABLE (SELECT 'index-page:' || page::VARCHAR AS html)`)
	if err != nil {
		t.Fatalf("Failed to create index macro: %v", err)
	}
	authorizer := auth.NewAuthorizer(mgr.AuthDB())
	if err := authorizer.CreateRole("norights", "no permissions at all"); err != nil {
		t.Fatalf("Failed to create role: %v", err)
	}

	req := httptest.NewRequest("GET", htmlURLPrefix+"/pub", nil)
	req = addAuthContext(req, "norights")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req, htmlURLPrefix)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("Expected status 403, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHTMLHandler_Index_HEAD_OK(t *testing.T) {
	handler, mgr, cleanup := setupHTMLTestHandler(t)
	defer cleanup()

	_, err := mgr.ExecMain(`CREATE OR REPLACE MACRO pub_html_index(page) AS TABLE (SELECT 'index-page:' || page::VARCHAR AS html)`)
	if err != nil {
		t.Fatalf("Failed to create index macro: %v", err)
	}

	req := httptest.NewRequest("HEAD", htmlURLPrefix+"/pub", nil)
	req = addAuthContext(req, "admin")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req, htmlURLPrefix)

	if rec.Code != http.StatusOK {
		t.Fatalf("Expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("ETag") == "" {
		t.Error("Expected ETag header to be set")
	}
	if rec.Body.Len() != 0 {
		t.Errorf("Expected empty body for HEAD, got %q", rec.Body.String())
	}
}

func TestHTMLHandler_ConditionalGET_NotModified(t *testing.T) {
	handler, _, cleanup := setupHTMLTestHandler(t)
	defer cleanup()

	req := httptest.NewRequest("GET", htmlURLPrefix+"/pub/1", nil)
	req = addAuthContext(req, "admin")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req, htmlURLPrefix)
	etag := rec.Header().Get("ETag")
	if etag == "" {
		t.Fatal("expected ETag on first response")
	}

	req2 := httptest.NewRequest("GET", htmlURLPrefix+"/pub/1", nil)
	req2 = addAuthContext(req2, "admin")
	req2.Header.Set("If-None-Match", etag)
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2, htmlURLPrefix)

	if rec2.Code != http.StatusNotModified {
		t.Fatalf("Expected status 304, got %d: %s", rec2.Code, rec2.Body.String())
	}
	if rec2.Body.Len() != 0 {
		t.Errorf("Expected empty body on 304, got %q", rec2.Body.String())
	}
}

// TestHTMLHandler_TableMacro_TakesPrecedence verifies that when a table macro
// named "{table}_html" exists, it's used instead of querying the table's
// static html column directly -- this is the dynamic Tera-style rendering
// path (see plans/integrate-caddy-html-duckdb.md).
func TestHTMLHandler_TableMacro_TakesPrecedence(t *testing.T) {
	handler, mgr, cleanup := setupHTMLTestHandler(t)
	defer cleanup()

	_, err := mgr.ExecMain(`CREATE OR REPLACE MACRO pub_html(id) AS TABLE (SELECT 'dynamic:' || id::VARCHAR AS html)`)
	if err != nil {
		t.Fatalf("Failed to create table macro: %v", err)
	}

	req := httptest.NewRequest("GET", htmlURLPrefix+"/pub/1", nil)
	req = addAuthContext(req, "admin")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req, htmlURLPrefix)

	if rec.Code != http.StatusOK {
		t.Fatalf("Expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "dynamic:1" {
		t.Errorf("Expected macro-rendered body, got %q", rec.Body.String())
	}
}

// TestHTMLHandler_PublicRole_GrantedTable_OK verifies the reserved "public"
// role can read a table it's been explicitly granted can_read on, without
// any other permissions.
func TestHTMLHandler_PublicRole_GrantedTable_OK(t *testing.T) {
	handler, mgr, cleanup := setupHTMLTestHandler(t)
	defer cleanup()

	authorizer := auth.NewAuthorizer(mgr.AuthDB())
	if err := authorizer.CreateRole("public", "unauthenticated read-only access"); err != nil {
		t.Fatalf("Failed to create public role: %v", err)
	}
	// Not authorizer.CreatePermission: it always writes can_execute, which the
	// InitAuthSchemaForTesting legacy schema doesn't have (see its own doc
	// comment -- that column is added by `auth-db migrate` in real
	// deployments, out of scope here). Insert directly against the columns
	// the test schema actually has.
	_, err := mgr.AuthDB().Exec(`
		INSERT INTO permissions (id, role_name, table_name, can_read)
		VALUES (nextval('permissions_id_seq'), 'public', 'pub', true)
	`)
	if err != nil {
		t.Fatalf("Failed to grant public permission: %v", err)
	}

	req := httptest.NewRequest("GET", htmlURLPrefix+"/pub/1", nil)
	req = addAuthContext(req, "public")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req, htmlURLPrefix)

	if rec.Code != http.StatusOK {
		t.Fatalf("Expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestHTMLHandler_PublicRole_UngrantedTable_Unauthorized verifies that the
// reserved "public" role, when not explicitly granted access to a table,
// is rejected with 401 (not 403) -- inviting the caller to retry with real
// credentials, since we can't tell whether those would succeed.
func TestHTMLHandler_PublicRole_UngrantedTable_Unauthorized(t *testing.T) {
	handler, _, cleanup := setupHTMLTestHandler(t)
	defer cleanup()

	req := httptest.NewRequest("GET", htmlURLPrefix+"/pub/1", nil)
	req = addAuthContext(req, "public")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req, htmlURLPrefix)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("Expected status 401, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestHTMLHandler_ReaderRole_NoExplicitGrant_Forbidden verifies an
// authenticated-but-insufficiently-privileged role gets 403, not 401 --
// unlike the "public" pseudo-role, we already know who they are.
func TestHTMLHandler_AuthenticatedRole_NoReadPermission_Forbidden(t *testing.T) {
	handler, mgr, cleanup := setupHTMLTestHandler(t)
	defer cleanup()

	authorizer := auth.NewAuthorizer(mgr.AuthDB())
	if err := authorizer.CreateRole("norights", "no permissions at all"); err != nil {
		t.Fatalf("Failed to create role: %v", err)
	}

	req := httptest.NewRequest("GET", htmlURLPrefix+"/pub/1", nil)
	req = addAuthContext(req, "norights")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req, htmlURLPrefix)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("Expected status 403, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHTMLHandler_CustomColumns(t *testing.T) {
	handler, mgr, cleanup := setupHTMLTestHandler(t)
	defer cleanup()

	_, err := mgr.ExecMain(`
		CREATE TABLE docs (
			doc_id INTEGER PRIMARY KEY,
			rendered VARCHAR
		)
	`)
	if err != nil {
		t.Fatalf("Failed to create docs table: %v", err)
	}
	_, err = mgr.ExecMain(`INSERT INTO docs VALUES (42, '<p>custom columns</p>')`)
	if err != nil {
		t.Fatalf("Failed to insert docs row: %v", err)
	}

	req := httptest.NewRequest("GET", htmlURLPrefix+"/docs/42?html_column=rendered&id_column=doc_id", nil)
	req = addAuthContext(req, "admin")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req, htmlURLPrefix)

	if rec.Code != http.StatusOK {
		t.Fatalf("Expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "<p>custom columns</p>" {
		t.Errorf("Unexpected body: %s", rec.Body.String())
	}
}
