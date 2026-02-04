package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/tobilg/caddy-duckdb-module/auth"
	"github.com/tobilg/caddy-duckdb-module/database"
	"go.uber.org/zap"
)

// setupColumnsTestHandler creates a ColumnsHandler with a test database
func setupColumnsTestHandler(t *testing.T) (*ColumnsHandler, *database.Manager, func()) {
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

	// Create test table
	_, err = mgr.ExecMain(`
		CREATE TABLE test_users (
			id INTEGER PRIMARY KEY,
			name VARCHAR NOT NULL,
			email VARCHAR,
			age INTEGER,
			score DOUBLE
		)
	`)
	if err != nil {
		t.Fatalf("Failed to create test table: %v", err)
	}

	// Insert test data
	_, err = mgr.ExecMain(`
		INSERT INTO test_users VALUES
			(1, 'Alice', 'alice@example.com', 30, 95.5),
			(2, 'Bob', 'bob@example.com', 25, 88.0),
			(3, 'Charlie', NULL, 35, 92.3)
	`)
	if err != nil {
		t.Fatalf("Failed to insert test data: %v", err)
	}

	// Create a test view
	_, err = mgr.ExecMain(`
		CREATE VIEW api_active_users AS
		SELECT id, name, age FROM test_users WHERE age >= 30
	`)
	if err != nil {
		t.Fatalf("Failed to create test view: %v", err)
	}

	authorizer := auth.NewAuthorizer(mgr.AuthDB())
	handler := NewColumnsHandler(mgr, authorizer, zap.NewNop())

	cleanup := func() {
		mgr.Close()
	}

	return handler, mgr, cleanup
}

// addColumnsAuthContext adds the role to the request context
func addColumnsAuthContext(r *http.Request, role string) *http.Request {
	ctx := context.WithValue(r.Context(), auth.ContextKeyRole, role)
	ctx = context.WithValue(ctx, auth.ContextKeyRequestID, "test-request-id")
	return r.WithContext(ctx)
}

func TestColumnsHandler_Table_StandardFormat(t *testing.T) {
	handler, _, cleanup := setupColumnsTestHandler(t)
	defer cleanup()

	req := httptest.NewRequest("GET", "/duckdb/api/test_users/columns", nil)
	req = addColumnsAuthContext(req, "admin")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var result StandardColumnsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	if result.Table != "test_users" {
		t.Errorf("Expected table 'test_users', got '%s'", result.Table)
	}

	if len(result.Columns) != 5 {
		t.Errorf("Expected 5 columns, got %d", len(result.Columns))
	}

	// Check first column
	if result.Columns[0].Name != "id" {
		t.Errorf("Expected first column 'id', got '%s'", result.Columns[0].Name)
	}
	if result.Columns[0].Type != "INTEGER" {
		t.Errorf("Expected type 'INTEGER', got '%s'", result.Columns[0].Type)
	}
}

func TestColumnsHandler_Table_TransformFormat(t *testing.T) {
	handler, _, cleanup := setupColumnsTestHandler(t)
	defer cleanup()

	req := httptest.NewRequest("GET", "/duckdb/api/test_users/columns?format=transform", nil)
	req = addColumnsAuthContext(req, "admin")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var result TransformColumnsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	if result.Table != "test_users" {
		t.Errorf("Expected table 'test_users', got '%s'", result.Table)
	}

	if len(result.Columns) != 5 {
		t.Errorf("Expected 5 columns, got %d", len(result.Columns))
	}

	// Check column type mappings
	if result.Columns["id"] != "INTEGER" {
		t.Errorf("Expected id type 'INTEGER', got '%s'", result.Columns["id"])
	}
	if result.Columns["name"] != "VARCHAR" {
		t.Errorf("Expected name type 'VARCHAR', got '%s'", result.Columns["name"])
	}
	if result.Columns["score"] != "DOUBLE" {
		t.Errorf("Expected score type 'DOUBLE', got '%s'", result.Columns["score"])
	}
}

func TestColumnsHandler_Table_SummarizeFormat(t *testing.T) {
	handler, _, cleanup := setupColumnsTestHandler(t)
	defer cleanup()

	req := httptest.NewRequest("GET", "/duckdb/api/test_users/columns?format=summarize", nil)
	req = addColumnsAuthContext(req, "admin")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var result SummarizeColumnsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	if result.Table != "test_users" {
		t.Errorf("Expected table 'test_users', got '%s'", result.Table)
	}

	if result.TotalRows != 3 {
		t.Errorf("Expected 3 total rows, got %d", result.TotalRows)
	}

	if result.SampleSize != 10000 {
		t.Errorf("Expected sample size 10000, got %d", result.SampleSize)
	}

	// Check that stats are included
	for _, col := range result.Columns {
		if col.Stats == nil {
			t.Errorf("Expected stats for column '%s'", col.Name)
		}
	}
}

func TestColumnsHandler_Table_WithStats(t *testing.T) {
	handler, _, cleanup := setupColumnsTestHandler(t)
	defer cleanup()

	req := httptest.NewRequest("GET", "/duckdb/api/test_users/columns?stats=true", nil)
	req = addColumnsAuthContext(req, "admin")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var result map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	columns := result["columns"].([]interface{})
	for _, col := range columns {
		colMap := col.(map[string]interface{})
		if colMap["stats"] == nil {
			t.Errorf("Expected stats for column '%s'", colMap["name"])
		}
	}
}

func TestColumnsHandler_Table_CustomSampleSize(t *testing.T) {
	handler, _, cleanup := setupColumnsTestHandler(t)
	defer cleanup()

	req := httptest.NewRequest("GET", "/duckdb/api/test_users/columns?format=summarize&sample=2", nil)
	req = addColumnsAuthContext(req, "admin")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var result SummarizeColumnsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	if result.SampleSize != 2 {
		t.Errorf("Expected sample size 2, got %d", result.SampleSize)
	}
}

func TestColumnsHandler_View_StandardFormat(t *testing.T) {
	handler, _, cleanup := setupColumnsTestHandler(t)
	defer cleanup()

	req := httptest.NewRequest("GET", "/duckdb/view/api_active_users/columns", nil)
	req = addColumnsAuthContext(req, "admin")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var result StandardColumnsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	if result.Table != "api_active_users" {
		t.Errorf("Expected view 'api_active_users', got '%s'", result.Table)
	}

	if len(result.Columns) != 3 {
		t.Errorf("Expected 3 columns, got %d", len(result.Columns))
	}
}

func TestColumnsHandler_View_TransformFormat(t *testing.T) {
	handler, _, cleanup := setupColumnsTestHandler(t)
	defer cleanup()

	req := httptest.NewRequest("GET", "/duckdb/view/api_active_users/columns?format=transform", nil)
	req = addColumnsAuthContext(req, "admin")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var result TransformColumnsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	if result.Table != "api_active_users" {
		t.Errorf("Expected view 'api_active_users', got '%s'", result.Table)
	}

	if len(result.Columns) != 3 {
		t.Errorf("Expected 3 columns, got %d", len(result.Columns))
	}

	// Check column types
	if _, ok := result.Columns["id"]; !ok {
		t.Error("Expected 'id' column in transform format")
	}
	if _, ok := result.Columns["name"]; !ok {
		t.Error("Expected 'name' column in transform format")
	}
	if _, ok := result.Columns["age"]; !ok {
		t.Error("Expected 'age' column in transform format")
	}
}

func TestColumnsHandler_View_WithStats(t *testing.T) {
	handler, _, cleanup := setupColumnsTestHandler(t)
	defer cleanup()

	req := httptest.NewRequest("GET", "/duckdb/view/api_active_users/columns?format=summarize", nil)
	req = addColumnsAuthContext(req, "admin")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var result SummarizeColumnsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	// View only has users with age >= 30 (Alice and Charlie)
	if result.TotalRows != 2 {
		t.Errorf("Expected 2 total rows in view, got %d", result.TotalRows)
	}
}

func TestColumnsHandler_View_NonApiPrefix_Forbidden(t *testing.T) {
	handler, mgr, cleanup := setupColumnsTestHandler(t)
	defer cleanup()

	// Create a view without api_ prefix
	_, err := mgr.ExecMain(`CREATE VIEW regular_view AS SELECT * FROM test_users`)
	if err != nil {
		t.Fatalf("Failed to create view: %v", err)
	}

	req := httptest.NewRequest("GET", "/duckdb/view/regular_view/columns", nil)
	req = addColumnsAuthContext(req, "admin")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("Expected status 403, got %d", rec.Code)
	}
}

func TestColumnsHandler_Table_NotFound(t *testing.T) {
	handler, _, cleanup := setupColumnsTestHandler(t)
	defer cleanup()

	req := httptest.NewRequest("GET", "/duckdb/api/nonexistent_table/columns", nil)
	req = addColumnsAuthContext(req, "admin")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("Expected status 404, got %d", rec.Code)
	}
}

func TestColumnsHandler_View_NotFound(t *testing.T) {
	handler, _, cleanup := setupColumnsTestHandler(t)
	defer cleanup()

	req := httptest.NewRequest("GET", "/duckdb/view/api_nonexistent/columns", nil)
	req = addColumnsAuthContext(req, "admin")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("Expected status 404, got %d", rec.Code)
	}
}

func TestColumnsHandler_InvalidTableName(t *testing.T) {
	handler, _, cleanup := setupColumnsTestHandler(t)
	defer cleanup()

	req := httptest.NewRequest("GET", "/duckdb/api/invalid;table/columns", nil)
	req = addColumnsAuthContext(req, "admin")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("Expected status 400, got %d", rec.Code)
	}
}

func TestColumnsHandler_MethodNotAllowed(t *testing.T) {
	handler, _, cleanup := setupColumnsTestHandler(t)
	defer cleanup()

	req := httptest.NewRequest("POST", "/duckdb/api/test_users/columns", nil)
	req = addColumnsAuthContext(req, "admin")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("Expected status 405, got %d", rec.Code)
	}
}

func TestColumnsHandler_HeadRequest(t *testing.T) {
	handler, _, cleanup := setupColumnsTestHandler(t)
	defer cleanup()

	// HEAD request should return 200 with Content-Type header but no body
	// This is required for DuckDB's read_json() which sends HEAD first
	req := httptest.NewRequest("HEAD", "/duckdb/api/test_users/columns", nil)
	req = addColumnsAuthContext(req, "admin")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Expected status 200 for HEAD, got %d", rec.Code)
	}

	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Expected Content-Type 'application/json', got '%s'", ct)
	}

	if rec.Body.Len() != 0 {
		t.Errorf("Expected empty body for HEAD request, got %d bytes", rec.Body.Len())
	}
}

func TestColumnsHandler_Unauthorized(t *testing.T) {
	handler, _, cleanup := setupColumnsTestHandler(t)
	defer cleanup()

	// Use a role with no read permission
	req := httptest.NewRequest("GET", "/duckdb/api/test_users/columns", nil)
	// Add context without role (simulating unauthorized access)
	ctx := context.WithValue(req.Context(), auth.ContextKeyRequestID, "test-request-id")
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	// Should fail on permission check (role empty -> permission denied)
	if rec.Code != http.StatusForbidden {
		t.Errorf("Expected status 403, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestColumnsHandler_InternalTable_Forbidden(t *testing.T) {
	handler, _, cleanup := setupColumnsTestHandler(t)
	defer cleanup()

	// Try to access internal auth table
	req := httptest.NewRequest("GET", "/duckdb/api/api_keys/columns", nil)
	req = addColumnsAuthContext(req, "admin")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("Expected status 403, got %d", rec.Code)
	}
}
