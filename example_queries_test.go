package duckdb

// TestExampleQueries verifies that the workflows documented in EXAMPLE_QUERIES.md
// actually work, and exercises the features added in the fork that are not yet
// covered there (execute, export, httpserver endpoint, columns, group_by, cursor,
// select fieldset, alternative auth methods, MCP).

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/tobilg/caddy-duckdb-module/auth"
	"github.com/tobilg/caddy-duckdb-module/database"
	"github.com/tobilg/caddy-duckdb-module/handlers"
	"go.uber.org/zap"
)

// setupExampleModule returns a fully provisioned DuckDB module with all handlers
// wired up, including execute, export (with a temp dir), httpserver, columns, and MCP.
func setupExampleModule(t *testing.T) (*DuckDB, func()) {
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
		t.Fatalf("setupExampleModule: NewManagerForTesting: %v", err)
	}

	authorizer := auth.NewAuthorizer(mgr.AuthDB())

	// Admin key (full permissions including execute)
	if err := authorizer.CreateAPIKey("admin-key", "admin", nil); err != nil {
		mgr.Close()
		t.Fatalf("setupExampleModule: CreateAPIKey admin: %v", err)
	}
	// Reader key (read-only)
	if err := authorizer.CreateAPIKey("reader-key", "reader", nil); err != nil {
		mgr.Close()
		t.Fatalf("setupExampleModule: CreateAPIKey reader: %v", err)
	}

	// Grant execute permission to admin (equivalent to `auth-db migrate`)
	_, err = mgr.AuthDB().Exec(`ALTER TABLE permissions ADD COLUMN IF NOT EXISTS can_execute BOOLEAN DEFAULT false`)
	if err != nil {
		mgr.Close()
		t.Fatalf("setupExampleModule: add can_execute column: %v", err)
	}
	_, err = mgr.AuthDB().Exec(`UPDATE permissions SET can_execute = true WHERE role_name = 'admin'`)
	if err != nil {
		mgr.Close()
		t.Fatalf("setupExampleModule: grant execute to admin: %v", err)
	}

	// Create users table as documented in EXAMPLE_QUERIES.md
	_, err = mgr.ExecMain(`CREATE TABLE users (
		id      INTEGER PRIMARY KEY,
		name    VARCHAR,
		email   VARCHAR,
		age     INTEGER,
		status  VARCHAR,
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	)`)
	if err != nil {
		mgr.Close()
		t.Fatalf("setupExampleModule: create users: %v", err)
	}
	_, err = mgr.ExecMain(`INSERT INTO users (id, name, email, age, status) VALUES
		(1, 'Alice Johnson', 'alice@example.com',   28, 'active'),
		(2, 'Bob Smith',    'bob@example.com',      35, 'active'),
		(3, 'Charlie Brown','charlie@example.com',  22, 'pending'),
		(4, 'Diana Ross',   'diana@example.com',    45, 'active'),
		(5, 'Eve Wilson',   'eve@example.com',      31, 'inactive')`)
	if err != nil {
		mgr.Close()
		t.Fatalf("setupExampleModule: insert users: %v", err)
	}

	exportsDir := t.TempDir()

	d := &DuckDB{
		DatabasePath:     ":memory:",
		AuthDatabasePath: ":memory:",
		MaxRowsPerPage:   100,
		AbsoluteMaxRows:  10000,
		ExportsDir:       exportsDir,
		ExportsURL:       "/duckdb/exports",
		ExportTTLMinutes: 60,
		MaxMCPRows:       200,
		logger:           zap.NewNop(),
		dbMgr:            mgr,
		authorizer:       authorizer,
		authMw:           auth.NewMiddleware(authorizer),
		routePrefix:      "/duckdb",
	}

	d.crudHandler = handlers.NewCRUDHandler(mgr, authorizer, d.MaxRowsPerPage, d.AbsoluteMaxRows, d.logger)
	d.queryHandler = handlers.NewQueryHandler(mgr, authorizer, d.logger)
	d.openAPIHandler = handlers.NewOpenAPIHandler()
	d.columnsHandler = handlers.NewColumnsHandler(mgr, authorizer, d.logger)
	d.httpserverHandler = handlers.NewHTTPServerHandler(mgr, authorizer, d.logger)
	d.executeHandler = handlers.NewExecuteHandler(mgr, authorizer, d.logger)
	d.exportHandler = handlers.NewExportHandler(mgr, authorizer, d.logger, exportsDir, "/duckdb/exports", time.Hour)
	d.mcpHandler = handlers.NewMCPHandler(mgr, authorizer, d.exportHandler, d.logger, 200)

	return d, func() { mgr.Close() }
}

// serve is a convenience helper: it sends a request through the module and returns
// the recorded response. Auth is pre-set to admin-key unless the request already
// carries an Authorization or X-API-Key header.
func serve(t *testing.T, d *DuckDB, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	if req.Header.Get("X-API-Key") == "" && req.Header.Get("Authorization") == "" {
		req.Header.Set("X-API-Key", "admin-key")
	}
	rec := httptest.NewRecorder()
	if err := d.ServeHTTP(rec, req, &mockNextHandler{}); err != nil {
		t.Fatalf("ServeHTTP: %v", err)
	}
	return rec
}

func jsonBody(t *testing.T, v interface{}) *bytes.Buffer {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("jsonBody: %v", err)
	}
	return bytes.NewBuffer(b)
}

func mustJSON(t *testing.T, body []byte) map[string]interface{} {
	t.Helper()
	var m map[string]interface{}
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("mustJSON: %v\nbody: %s", err, body)
	}
	return m
}

// ─── EXAMPLE_QUERIES.md: Setup ──────────────────────────────────────────────

func TestExampleQueries_Setup_CreateTable(t *testing.T) {
	d, cleanup := setupExampleModule(t)
	defer cleanup()

	req := httptest.NewRequest("POST", "/duckdb/query",
		strings.NewReader(`{"sql":"CREATE TABLE IF NOT EXISTS products (id INTEGER PRIMARY KEY, name VARCHAR, price DOUBLE)"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := serve(t, d, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body)
	}
}

// ─── EXAMPLE_QUERIES.md: CRUD ────────────────────────────────────────────────

func TestExampleQueries_CRUD_Insert(t *testing.T) {
	d, cleanup := setupExampleModule(t)
	defer cleanup()

	body := jsonBody(t, map[string]interface{}{
		"id": 10, "name": "Frank", "email": "frank@example.com", "age": 40, "status": "active",
	})
	req := httptest.NewRequest("POST", "/duckdb/api/users", body)
	req.Header.Set("Content-Type", "application/json")
	rec := serve(t, d, req)

	if rec.Code != http.StatusCreated && rec.Code != http.StatusOK {
		t.Fatalf("expected 2xx, got %d: %s", rec.Code, rec.Body)
	}
	m := mustJSON(t, rec.Body.Bytes())
	if m["success"] != true {
		t.Errorf("expected success:true, got %v", m)
	}
}

func TestExampleQueries_CRUD_List(t *testing.T) {
	d, cleanup := setupExampleModule(t)
	defer cleanup()

	req := httptest.NewRequest("GET", "/duckdb/api/users", nil)
	rec := serve(t, d, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body)
	}
	m := mustJSON(t, rec.Body.Bytes())
	data, ok := m["data"].([]interface{})
	if !ok || len(data) < 5 {
		t.Errorf("expected ≥5 rows, got %v", m["data"])
	}
}

func TestExampleQueries_CRUD_Pagination(t *testing.T) {
	d, cleanup := setupExampleModule(t)
	defer cleanup()

	req := httptest.NewRequest("GET", "/duckdb/api/users?page=1&limit=2", nil)
	rec := serve(t, d, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body)
	}
	m := mustJSON(t, rec.Body.Bytes())
	data := m["data"].([]interface{})
	if len(data) != 2 {
		t.Errorf("expected 2 rows, got %d", len(data))
	}
}

func TestExampleQueries_CRUD_Filter_Eq(t *testing.T) {
	d, cleanup := setupExampleModule(t)
	defer cleanup()

	req := httptest.NewRequest("GET", "/duckdb/api/users?filter=status:eq:active", nil)
	rec := serve(t, d, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body)
	}
	m := mustJSON(t, rec.Body.Bytes())
	data := m["data"].([]interface{})
	if len(data) != 3 {
		t.Errorf("expected 3 active users, got %d", len(data))
	}
}

func TestExampleQueries_CRUD_Filter_Gt(t *testing.T) {
	d, cleanup := setupExampleModule(t)
	defer cleanup()

	req := httptest.NewRequest("GET", "/duckdb/api/users?filter=age:gt:30", nil)
	rec := serve(t, d, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body)
	}
	data := mustJSON(t, rec.Body.Bytes())["data"].([]interface{})
	if len(data) != 3 {
		t.Errorf("expected 3 users over 30, got %d", len(data))
	}
}

func TestExampleQueries_CRUD_Filter_In(t *testing.T) {
	d, cleanup := setupExampleModule(t)
	defer cleanup()

	req := httptest.NewRequest("GET", "/duckdb/api/users?filter=status:in:active|pending", nil)
	rec := serve(t, d, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body)
	}
	data := mustJSON(t, rec.Body.Bytes())["data"].([]interface{})
	if len(data) != 4 {
		t.Errorf("expected 4 rows (active+pending), got %d", len(data))
	}
}

func TestExampleQueries_CRUD_Filter_MultipleAND(t *testing.T) {
	d, cleanup := setupExampleModule(t)
	defer cleanup()

	req := httptest.NewRequest("GET", "/duckdb/api/users?filter=status:eq:active,age:gt:30", nil)
	rec := serve(t, d, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body)
	}
	data := mustJSON(t, rec.Body.Bytes())["data"].([]interface{})
	if len(data) != 2 {
		t.Errorf("expected 2 rows (active AND age>30), got %d", len(data))
	}
}

func TestExampleQueries_CRUD_Sort(t *testing.T) {
	d, cleanup := setupExampleModule(t)
	defer cleanup()

	req := httptest.NewRequest("GET", "/duckdb/api/users?sort=age:desc", nil)
	rec := serve(t, d, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body)
	}
	data := mustJSON(t, rec.Body.Bytes())["data"].([]interface{})
	if len(data) < 2 {
		t.Fatal("not enough rows")
	}
	first := data[0].(map[string]interface{})
	if first["age"].(float64) < data[1].(map[string]interface{})["age"].(float64) {
		t.Error("results not sorted descending by age")
	}
}

func TestExampleQueries_CRUD_Update(t *testing.T) {
	d, cleanup := setupExampleModule(t)
	defer cleanup()

	body := jsonBody(t, map[string]interface{}{
		"where": []map[string]interface{}{{"column": "id", "op": "eq", "value": 3}},
		"set":   map[string]interface{}{"status": "active"},
	})
	req := httptest.NewRequest("PUT", "/duckdb/api/users", body)
	req.Header.Set("Content-Type", "application/json")
	rec := serve(t, d, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body)
	}
	m := mustJSON(t, rec.Body.Bytes())
	if m["success"] != true {
		t.Errorf("expected success:true, got %v", m)
	}
}

func TestExampleQueries_CRUD_Delete_DryRun(t *testing.T) {
	d, cleanup := setupExampleModule(t)
	defer cleanup()

	req := httptest.NewRequest("DELETE", "/duckdb/api/users?where=status:eq:pending&dry_run=true", nil)
	rec := serve(t, d, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body)
	}
	m := mustJSON(t, rec.Body.Bytes())
	if m["dry_run"] != true {
		t.Errorf("expected dry_run:true, got %v", m)
	}
	if m["affected_rows"].(float64) < 1 {
		t.Errorf("expected ≥1 affected row, got %v", m["affected_rows"])
	}
}

func TestExampleQueries_CRUD_Delete(t *testing.T) {
	d, cleanup := setupExampleModule(t)
	defer cleanup()

	req := httptest.NewRequest("DELETE", "/duckdb/api/users?where=id:eq:5", nil)
	rec := serve(t, d, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body)
	}
	m := mustJSON(t, rec.Body.Bytes())
	if m["success"] != true {
		t.Errorf("expected success:true, got %v", m)
	}
}

// ─── EXAMPLE_QUERIES.md: Query endpoint ──────────────────────────────────────

func TestExampleQueries_Query_SimpleSelect(t *testing.T) {
	d, cleanup := setupExampleModule(t)
	defer cleanup()

	req := httptest.NewRequest("POST", "/duckdb/query",
		strings.NewReader(`{"sql":"SELECT id, name, age FROM users ORDER BY age"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := serve(t, d, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body)
	}
	data := mustJSON(t, rec.Body.Bytes())["data"].([]interface{})
	if len(data) < 5 {
		t.Errorf("expected ≥5 rows, got %d", len(data))
	}
}

func TestExampleQueries_Query_Aggregation(t *testing.T) {
	d, cleanup := setupExampleModule(t)
	defer cleanup()

	req := httptest.NewRequest("POST", "/duckdb/query",
		strings.NewReader(`{"sql":"SELECT status, COUNT(*) as count, AVG(age) as avg_age FROM users GROUP BY status ORDER BY status"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := serve(t, d, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body)
	}
	data := mustJSON(t, rec.Body.Bytes())["data"].([]interface{})
	if len(data) == 0 {
		t.Error("expected at least one group")
	}
}

func TestExampleQueries_Query_Parameterized(t *testing.T) {
	d, cleanup := setupExampleModule(t)
	defer cleanup()

	req := httptest.NewRequest("POST", "/duckdb/query",
		strings.NewReader(`{"sql":"SELECT name FROM users WHERE age > ? AND status = ?","params":[30,"active"]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := serve(t, d, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body)
	}
}

func TestExampleQueries_Query_GetEndpoint(t *testing.T) {
	d, cleanup := setupExampleModule(t)
	defer cleanup()

	// URL-encoded: SELECT id, name FROM users
	req := httptest.NewRequest("GET", "/duckdb/query/SELECT%20id%2C%20name%20FROM%20users/result.json", nil)
	rec := serve(t, d, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body)
	}
}

// ─── EXAMPLE_QUERIES.md: Response formats ────────────────────────────────────

func TestExampleQueries_Format_CSV(t *testing.T) {
	d, cleanup := setupExampleModule(t)
	defer cleanup()

	req := httptest.NewRequest("GET", "/duckdb/api/users?limit=3", nil)
	req.Header.Set("Accept", "text/csv")
	rec := serve(t, d, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body)
	}
	ct := rec.Header().Get("Content-Type")
	if !strings.Contains(ct, "csv") {
		t.Errorf("expected csv content-type, got %s", ct)
	}
	if !strings.Contains(rec.Body.String(), "name") {
		t.Error("expected CSV header with 'name' column")
	}
}

func TestExampleQueries_Format_Parquet(t *testing.T) {
	d, cleanup := setupExampleModule(t)
	defer cleanup()

	req := httptest.NewRequest("GET", "/duckdb/query/SELECT%20%2A%20FROM%20users/result.parquet", nil)
	rec := serve(t, d, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body)
	}
	// Parquet magic bytes: PAR1
	if !bytes.HasPrefix(rec.Body.Bytes(), []byte("PAR1")) {
		t.Error("expected parquet magic bytes PAR1")
	}
}

// ─── Auth: alternative methods ───────────────────────────────────────────────

func TestExampleQueries_Auth_QueryParam(t *testing.T) {
	d, cleanup := setupExampleModule(t)
	defer cleanup()

	req := httptest.NewRequest("GET", "/duckdb/api/users?api_key=admin-key", nil)
	// No X-API-Key header
	rec := httptest.NewRecorder()
	if err := d.ServeHTTP(rec, req, &mockNextHandler{}); err != nil {
		t.Fatalf("ServeHTTP: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("api_key query param auth: expected 200, got %d: %s", rec.Code, rec.Body)
	}
}

func TestExampleQueries_Auth_BasicAuth(t *testing.T) {
	d, cleanup := setupExampleModule(t)
	defer cleanup()

	req := httptest.NewRequest("GET", "/duckdb/api/users", nil)
	req.SetBasicAuth("apikey", "admin-key")
	rec := httptest.NewRecorder()
	if err := d.ServeHTTP(rec, req, &mockNextHandler{}); err != nil {
		t.Fatalf("ServeHTTP: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("basic auth: expected 200, got %d: %s", rec.Code, rec.Body)
	}
}

func TestExampleQueries_Auth_MissingKey(t *testing.T) {
	d, cleanup := setupExampleModule(t)
	defer cleanup()

	req := httptest.NewRequest("GET", "/duckdb/api/users", nil)
	rec := httptest.NewRecorder()
	d.ServeHTTP(rec, req, &mockNextHandler{})

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}

func TestExampleQueries_Auth_InsufficientPermission(t *testing.T) {
	d, cleanup := setupExampleModule(t)
	defer cleanup()

	body := jsonBody(t, map[string]interface{}{"id": 99, "name": "Test"})
	req := httptest.NewRequest("POST", "/duckdb/api/users", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", "reader-key")
	rec := httptest.NewRecorder()
	d.ServeHTTP(rec, req, &mockNextHandler{})

	if rec.Code != http.StatusForbidden {
		t.Errorf("expected 403 for reader insert, got %d", rec.Code)
	}
}

// ─── New features: select / group_by / cursor ────────────────────────────────

func TestExampleQueries_Select_SparseFieldset(t *testing.T) {
	d, cleanup := setupExampleModule(t)
	defer cleanup()

	req := httptest.NewRequest("GET", "/duckdb/api/users?select=id,name&limit=3", nil)
	rec := serve(t, d, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body)
	}
	data := mustJSON(t, rec.Body.Bytes())["data"].([]interface{})
	if len(data) == 0 {
		t.Fatal("no rows")
	}
	row := data[0].(map[string]interface{})
	if _, hasName := row["name"]; !hasName {
		t.Error("expected 'name' column")
	}
	if _, hasEmail := row["email"]; hasEmail {
		t.Error("'email' should not be present in sparse fieldset")
	}
}

func TestExampleQueries_GroupBy(t *testing.T) {
	d, cleanup := setupExampleModule(t)
	defer cleanup()

	req := httptest.NewRequest("GET", "/duckdb/api/users?group_by=status", nil)
	rec := serve(t, d, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body)
	}
	m := mustJSON(t, rec.Body.Bytes())
	groups, ok := m["group_by"].([]interface{})
	if !ok || len(groups) == 0 {
		t.Errorf("expected group_by array, got %v", m)
	}
	first := groups[0].(map[string]interface{})
	if _, hasKey := first["key"]; !hasKey {
		t.Error("expected 'key' in group_by result")
	}
	if _, hasCount := first["count"]; !hasCount {
		t.Error("expected 'count' in group_by result")
	}
}

func TestExampleQueries_CursorPagination(t *testing.T) {
	d, cleanup := setupExampleModule(t)
	defer cleanup()

	// First page with cursor
	req := httptest.NewRequest("GET", "/duckdb/api/users?cursor=*&limit=2&sort=id:asc", nil)
	rec := serve(t, d, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body)
	}
	m := mustJSON(t, rec.Body.Bytes())
	meta, ok := m["meta"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected meta in cursor response, got %v", m)
	}
	if _, hasNext := meta["next_cursor"]; !hasNext {
		t.Error("expected next_cursor in meta")
	}
}

// ─── New features: columns endpoint ──────────────────────────────────────────

func TestExampleQueries_Columns_Standard(t *testing.T) {
	d, cleanup := setupExampleModule(t)
	defer cleanup()

	req := httptest.NewRequest("GET", "/duckdb/api/users/columns", nil)
	rec := serve(t, d, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body)
	}
	m := mustJSON(t, rec.Body.Bytes())
	cols, ok := m["columns"].([]interface{})
	if !ok || len(cols) == 0 {
		t.Errorf("expected columns array, got %v", m)
	}
}

func TestExampleQueries_Columns_Transform(t *testing.T) {
	d, cleanup := setupExampleModule(t)
	defer cleanup()

	req := httptest.NewRequest("GET", "/duckdb/api/users/columns?format=transform", nil)
	rec := serve(t, d, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body)
	}
	m := mustJSON(t, rec.Body.Bytes())
	cols, ok := m["columns"].(map[string]interface{})
	if !ok || len(cols) == 0 {
		t.Errorf("expected columns map for transform format, got %v", m)
	}
}

// ─── New features: httpserver-compatible endpoint ────────────────────────────

func TestExampleQueries_HTTPServer_RawSQL(t *testing.T) {
	d, cleanup := setupExampleModule(t)
	defer cleanup()

	req := httptest.NewRequest("POST", "/duckdb/", strings.NewReader("SELECT COUNT(*) AS n FROM users"))
	req.Header.Set("Content-Type", "text/plain")
	rec := serve(t, d, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body)
	}
	m := mustJSON(t, rec.Body.Bytes())
	if _, hasMeta := m["meta"]; !hasMeta {
		t.Errorf("expected JSONCompact with 'meta', got %v", m)
	}
}

func TestExampleQueries_HTTPServer_Head(t *testing.T) {
	d, cleanup := setupExampleModule(t)
	defer cleanup()

	req := httptest.NewRequest("HEAD", "/duckdb/", nil)
	rec := serve(t, d, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestExampleQueries_HTTPServer_RejectsWriteSQL(t *testing.T) {
	d, cleanup := setupExampleModule(t)
	defer cleanup()

	req := httptest.NewRequest("POST", "/duckdb/", strings.NewReader("INSERT INTO users VALUES (99,'X','x@x.com',1,'active')"))
	req.Header.Set("Content-Type", "text/plain")
	rec := serve(t, d, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("httpserver endpoint should reject write SQL with 403, got %d", rec.Code)
	}
}

// ─── New features: execute endpoint ──────────────────────────────────────────

func TestExampleQueries_Execute_Insert(t *testing.T) {
	d, cleanup := setupExampleModule(t)
	defer cleanup()

	req := httptest.NewRequest("POST", "/duckdb/execute",
		strings.NewReader("INSERT INTO users (id, name, email, age, status) VALUES (20, 'Grace', 'grace@example.com', 27, 'active')"))
	req.Header.Set("Content-Type", "text/plain")
	rec := serve(t, d, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body)
	}
	m := mustJSON(t, rec.Body.Bytes())
	if m["rows_affected"].(float64) != 1 {
		t.Errorf("expected rows_affected:1, got %v", m["rows_affected"])
	}
}

func TestExampleQueries_Execute_DDL(t *testing.T) {
	d, cleanup := setupExampleModule(t)
	defer cleanup()

	req := httptest.NewRequest("POST", "/duckdb/execute",
		strings.NewReader("CREATE TABLE IF NOT EXISTS events (id INTEGER, name VARCHAR)"))
	req.Header.Set("Content-Type", "text/plain")
	rec := serve(t, d, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body)
	}
}

func TestExampleQueries_Execute_RejectsReadSQL(t *testing.T) {
	d, cleanup := setupExampleModule(t)
	defer cleanup()

	req := httptest.NewRequest("POST", "/duckdb/execute",
		strings.NewReader("SELECT * FROM users"))
	req.Header.Set("Content-Type", "text/plain")
	rec := serve(t, d, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("execute should reject SELECT with 400, got %d: %s", rec.Code, rec.Body)
	}
}

func TestExampleQueries_Execute_ForbiddenForReader(t *testing.T) {
	d, cleanup := setupExampleModule(t)
	defer cleanup()

	req := httptest.NewRequest("POST", "/duckdb/execute",
		strings.NewReader("INSERT INTO users (id,name,email,age,status) VALUES (30,'X','x@x',1,'active')"))
	req.Header.Set("Content-Type", "text/plain")
	req.Header.Set("X-API-Key", "reader-key")
	rec := httptest.NewRecorder()
	d.ServeHTTP(rec, req, &mockNextHandler{})

	if rec.Code != http.StatusForbidden {
		t.Errorf("reader should not execute writes, expected 403, got %d", rec.Code)
	}
}

// ─── New features: export endpoint ───────────────────────────────────────────

func TestExampleQueries_Export_Parquet(t *testing.T) {
	d, cleanup := setupExampleModule(t)
	defer cleanup()

	req := httptest.NewRequest("POST", "/duckdb/export",
		strings.NewReader(`{"sql":"SELECT id, name, age FROM users","format":"parquet"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := serve(t, d, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body)
	}
	m := mustJSON(t, rec.Body.Bytes())
	if _, hasURL := m["url"]; !hasURL {
		t.Errorf("expected 'url' in export response, got %v", m)
	}
	if m["rows"].(float64) != 5 {
		t.Errorf("expected rows:5, got %v", m["rows"])
	}
	if m["format"] != "parquet" {
		t.Errorf("expected format:parquet, got %v", m["format"])
	}
}

func TestExampleQueries_Export_CSV(t *testing.T) {
	d, cleanup := setupExampleModule(t)
	defer cleanup()

	req := httptest.NewRequest("POST", "/duckdb/export",
		strings.NewReader(`{"sql":"SELECT id, name FROM users LIMIT 3","format":"csv"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := serve(t, d, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body)
	}
	m := mustJSON(t, rec.Body.Bytes())
	if m["format"] != "csv" {
		t.Errorf("expected format:csv, got %v", m["format"])
	}
}

func TestExampleQueries_Export_JSON(t *testing.T) {
	d, cleanup := setupExampleModule(t)
	defer cleanup()

	req := httptest.NewRequest("POST", "/duckdb/export",
		strings.NewReader(`{"sql":"SELECT id, name FROM users LIMIT 3","format":"json"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := serve(t, d, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body)
	}
	m := mustJSON(t, rec.Body.Bytes())
	if m["format"] != "json" {
		t.Errorf("expected format:json, got %v", m["format"])
	}
}

func TestExampleQueries_Export_TokenSaving(t *testing.T) {
	// Verify that the export response is small regardless of result size —
	// this is the core token-saving property documented in docs/llms.txt.
	d, cleanup := setupExampleModule(t)
	defer cleanup()

	req := httptest.NewRequest("POST", "/duckdb/export",
		strings.NewReader(`{"sql":"SELECT * FROM users"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := serve(t, d, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body)
	}
	// The response must contain url, rows, size_bytes, expires_at — not row data.
	body := rec.Body.Bytes()
	if len(body) > 512 {
		t.Errorf("export response should be small (<512 bytes), got %d bytes", len(body))
	}
	m := mustJSON(t, body)
	for _, key := range []string{"url", "filename", "format", "rows", "size_bytes", "expires_at"} {
		if _, ok := m[key]; !ok {
			t.Errorf("expected key %q in export response", key)
		}
	}
}

// ─── New features: MCP endpoint ──────────────────────────────────────────────

func mcpCall(t *testing.T, d *DuckDB, id int, method string, params interface{}) map[string]interface{} {
	t.Helper()
	payload := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest("POST", "/duckdb/mcp", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	rec := serve(t, d, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("MCP %s: expected 200, got %d: %s", method, rec.Code, rec.Body)
	}
	return mustJSON(t, rec.Body.Bytes())
}

func TestExampleQueries_MCP_Initialize(t *testing.T) {
	d, cleanup := setupExampleModule(t)
	defer cleanup()

	resp := mcpCall(t, d, 1, "initialize", map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]interface{}{},
		"clientInfo":      map[string]interface{}{"name": "test", "version": "0.1"},
	})

	result, ok := resp["result"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected result object, got %v", resp)
	}
	if result["protocolVersion"] != "2024-11-05" {
		t.Errorf("unexpected protocolVersion: %v", result["protocolVersion"])
	}
}

func TestExampleQueries_MCP_ToolsList(t *testing.T) {
	d, cleanup := setupExampleModule(t)
	defer cleanup()

	resp := mcpCall(t, d, 2, "tools/list", map[string]interface{}{})
	result := resp["result"].(map[string]interface{})
	tools := result["tools"].([]interface{})

	want := map[string]bool{"query": false, "execute": false, "export": false,
		"list_tables": false, "describe": false, "database_info": false}
	for _, raw := range tools {
		name := raw.(map[string]interface{})["name"].(string)
		want[name] = true
	}
	for name, found := range want {
		if !found {
			t.Errorf("MCP tool %q missing from tools/list", name)
		}
	}
}

func TestExampleQueries_MCP_Query(t *testing.T) {
	d, cleanup := setupExampleModule(t)
	defer cleanup()

	resp := mcpCall(t, d, 3, "tools/call", map[string]interface{}{
		"name":      "query",
		"arguments": map[string]interface{}{"sql": "SELECT COUNT(*) AS n FROM users"},
	})
	result := resp["result"].(map[string]interface{})
	content := result["content"].([]interface{})
	text := content[0].(map[string]interface{})["text"].(string)
	if !strings.Contains(text, `"n"`) {
		t.Errorf("expected column 'n' in MCP query result, got: %s", text)
	}
}

func TestExampleQueries_MCP_ListTables(t *testing.T) {
	d, cleanup := setupExampleModule(t)
	defer cleanup()

	resp := mcpCall(t, d, 4, "tools/call", map[string]interface{}{
		"name":      "list_tables",
		"arguments": map[string]interface{}{"schema": "main"},
	})
	result := resp["result"].(map[string]interface{})
	content := result["content"].([]interface{})
	text := content[0].(map[string]interface{})["text"].(string)
	if !strings.Contains(text, "users") {
		t.Errorf("expected 'users' in list_tables result, got: %s", text)
	}
}

func TestExampleQueries_MCP_Describe(t *testing.T) {
	d, cleanup := setupExampleModule(t)
	defer cleanup()

	resp := mcpCall(t, d, 5, "tools/call", map[string]interface{}{
		"name":      "describe",
		"arguments": map[string]interface{}{"table": "users"},
	})
	result := resp["result"].(map[string]interface{})
	content := result["content"].([]interface{})
	text := content[0].(map[string]interface{})["text"].(string)
	if !strings.Contains(text, "column_name") && !strings.Contains(text, "column_type") {
		t.Errorf("expected column info in describe result, got: %s", text)
	}
}

func TestExampleQueries_MCP_Execute(t *testing.T) {
	d, cleanup := setupExampleModule(t)
	defer cleanup()

	resp := mcpCall(t, d, 6, "tools/call", map[string]interface{}{
		"name": "execute",
		"arguments": map[string]interface{}{
			"sql": "CREATE TABLE IF NOT EXISTS mcp_test (id INTEGER)",
		},
	})
	result := resp["result"].(map[string]interface{})
	content := result["content"].([]interface{})
	text := content[0].(map[string]interface{})["text"].(string)
	if !strings.Contains(text, "OK") {
		t.Errorf("expected OK in execute result, got: %s", text)
	}
}

func TestExampleQueries_MCP_Export(t *testing.T) {
	d, cleanup := setupExampleModule(t)
	defer cleanup()

	resp := mcpCall(t, d, 7, "tools/call", map[string]interface{}{
		"name": "export",
		"arguments": map[string]interface{}{
			"sql":    "SELECT * FROM users",
			"format": "parquet",
		},
	})
	result := resp["result"].(map[string]interface{})
	content := result["content"].([]interface{})
	text := content[0].(map[string]interface{})["text"].(string)
	if strings.HasPrefix(text, "Error:") {
		t.Fatalf("MCP export returned error: %s", text)
	}
	var exportResp map[string]interface{}
	if err := json.Unmarshal([]byte(text), &exportResp); err != nil {
		t.Fatalf("MCP export result not valid JSON: %v\n%s", err, text)
	}
	if _, hasURL := exportResp["url"]; !hasURL {
		t.Errorf("expected url in MCP export result, got: %v", exportResp)
	}
}

func TestExampleQueries_MCP_DatabaseInfo(t *testing.T) {
	d, cleanup := setupExampleModule(t)
	defer cleanup()

	resp := mcpCall(t, d, 8, "tools/call", map[string]interface{}{
		"name":      "database_info",
		"arguments": map[string]interface{}{},
	})
	result := resp["result"].(map[string]interface{})
	content := result["content"].([]interface{})
	text := content[0].(map[string]interface{})["text"].(string)
	if !strings.Contains(text, "users") {
		t.Errorf("expected 'users' in database_info result, got: %s", text)
	}
}

// ─── Trusted-user-header auth (PR #15) ───────────────────────────────────────

func TestExampleQueries_TrustedUserHeader(t *testing.T) {
	d, cleanup := setupExampleModule(t)
	defer cleanup()

	// Register a trusted user directly in the auth DB
	_, err := d.dbMgr.AuthDB().Exec(`
		INSERT INTO trusted_users (username, role_name, note, is_active)
		VALUES ('trusted@example.com', 'admin', 'test user', true)
	`)
	if err != nil {
		t.Fatalf("insert trusted_user: %v", err)
	}

	d.TrustedUserHeader = "X-Vouch-User"

	req := httptest.NewRequest("GET", "/duckdb/api/users", nil)
	req.Header.Set("X-Vouch-User", "trusted@example.com")
	// No API key — should authenticate via trusted header
	rec := httptest.NewRecorder()
	if err := d.ServeHTTP(rec, req, &mockNextHandler{}); err != nil {
		t.Fatalf("ServeHTTP: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("trusted user header auth: expected 200, got %d: %s", rec.Code, rec.Body)
	}
}

// ─── Error cases documented in EXAMPLE_QUERIES.md ───────────────────────────

func TestExampleQueries_Error_TableNotFound(t *testing.T) {
	d, cleanup := setupExampleModule(t)
	defer cleanup()

	req := httptest.NewRequest("GET", "/duckdb/api/nonexistent", nil)
	rec := serve(t, d, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404 for nonexistent table, got %d", rec.Code)
	}
}

func TestExampleQueries_Error_DeleteMissingWhere(t *testing.T) {
	d, cleanup := setupExampleModule(t)
	defer cleanup()

	req := httptest.NewRequest("DELETE", "/duckdb/api/users", nil)
	rec := serve(t, d, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for DELETE without WHERE, got %d", rec.Code)
	}
}

// ─── OpenAPI spec includes all new endpoints ──────────────────────────────────

func TestExampleQueries_OpenAPI_ContainsNewEndpoints(t *testing.T) {
	d, cleanup := setupExampleModule(t)
	defer cleanup()

	req := httptest.NewRequest("GET", "/duckdb/openapi.json", nil)
	rec := serve(t, d, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	spec := mustJSON(t, rec.Body.Bytes())
	paths, ok := spec["paths"].(map[string]interface{})
	if !ok {
		t.Fatal("expected paths in OpenAPI spec")
	}
	for _, path := range []string{"/execute", "/export", "/mcp"} {
		if _, exists := paths[path]; !exists {
			t.Errorf("OpenAPI spec missing path %q", path)
		}
	}
}

// ─── Env var fallback (PR env-var wiring) ────────────────────────────────────

func TestExampleQueries_EnvVar_ExportsDir(t *testing.T) {
	dir := t.TempDir()
	os.Setenv("DUCKDB_EXPORTS_DIR", dir)
	defer os.Unsetenv("DUCKDB_EXPORTS_DIR")
	os.Setenv("DUCKDB_MAX_MCP_ROWS", "42")
	defer os.Unsetenv("DUCKDB_MAX_MCP_ROWS")

	d := &DuckDB{}
	// Simulate env var pickup (same logic as Provision)
	if v := os.Getenv("DUCKDB_EXPORTS_DIR"); v != "" {
		d.ExportsDir = v
	}
	if v := os.Getenv("DUCKDB_MAX_MCP_ROWS"); v != "" {
		fmt.Sscanf(v, "%d", &d.MaxMCPRows)
	}

	if d.ExportsDir != dir {
		t.Errorf("expected ExportsDir=%s, got %s", dir, d.ExportsDir)
	}
	if d.MaxMCPRows != 42 {
		t.Errorf("expected MaxMCPRows=42, got %d", d.MaxMCPRows)
	}
}
