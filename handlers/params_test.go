package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestParsePagination(t *testing.T) {
	tests := []struct {
		name              string
		query             string
		maxRowsPerPage    int
		absoluteMaxRows   int
		wantLimit         int
		wantOffset        int
		wantPage          int
		wantPaginationReq bool
	}{
		{
			name:              "no pagination params",
			query:             "",
			maxRowsPerPage:    100,
			absoluteMaxRows:   10000,
			wantLimit:         0,
			wantOffset:        0,
			wantPage:          0,
			wantPaginationReq: false,
		},
		{
			name:              "limit only",
			query:             "limit=50",
			maxRowsPerPage:    100,
			absoluteMaxRows:   10000,
			wantLimit:         50,
			wantOffset:        0,
			wantPage:          1,
			wantPaginationReq: true,
		},
		{
			name:              "page only",
			query:             "page=2",
			maxRowsPerPage:    100,
			absoluteMaxRows:   10000,
			wantLimit:         100,
			wantOffset:        100,
			wantPage:          2,
			wantPaginationReq: true,
		},
		{
			name:              "both limit and page",
			query:             "limit=25&page=3",
			maxRowsPerPage:    100,
			absoluteMaxRows:   10000,
			wantLimit:         25,
			wantOffset:        50,
			wantPage:          3,
			wantPaginationReq: true,
		},
		{
			name:              "limit exceeds max rows per page",
			query:             "limit=200",
			maxRowsPerPage:    100,
			absoluteMaxRows:   10000,
			wantLimit:         100,
			wantOffset:        0,
			wantPage:          1,
			wantPaginationReq: true,
		},
		{
			name:              "invalid limit (negative)",
			query:             "limit=-10",
			maxRowsPerPage:    100,
			absoluteMaxRows:   10000,
			wantLimit:         100,
			wantOffset:        0,
			wantPage:          1,
			wantPaginationReq: true,
		},
		{
			name:              "invalid page (zero)",
			query:             "page=0",
			maxRowsPerPage:    100,
			absoluteMaxRows:   10000,
			wantLimit:         100,
			wantOffset:        0,
			wantPage:          1,
			wantPaginationReq: true,
		},
		{
			name:              "invalid limit (non-numeric)",
			query:             "limit=abc",
			maxRowsPerPage:    100,
			absoluteMaxRows:   10000,
			wantLimit:         100,
			wantOffset:        0,
			wantPage:          1,
			wantPaginationReq: true,
		},
		{
			name:              "high page number",
			query:             "limit=10&page=100",
			maxRowsPerPage:    100,
			absoluteMaxRows:   10000,
			wantLimit:         10,
			wantOffset:        990,
			wantPage:          100,
			wantPaginationReq: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/?"+tt.query, nil)
			limit, offset, page, paginationRequested := ParsePagination(req, tt.maxRowsPerPage, tt.absoluteMaxRows)

			if limit != tt.wantLimit {
				t.Errorf("ParsePagination() limit = %v, want %v", limit, tt.wantLimit)
			}
			if offset != tt.wantOffset {
				t.Errorf("ParsePagination() offset = %v, want %v", offset, tt.wantOffset)
			}
			if page != tt.wantPage {
				t.Errorf("ParsePagination() page = %v, want %v", page, tt.wantPage)
			}
			if paginationRequested != tt.wantPaginationReq {
				t.Errorf("ParsePagination() paginationRequested = %v, want %v", paginationRequested, tt.wantPaginationReq)
			}
		})
	}
}

func TestParseFilters(t *testing.T) {
	tests := []struct {
		name       string
		query      string
		wantCount  int
		wantErr    bool
		checkFirst func(t *testing.T, column, operator string, value interface{})
	}{
		{
			name:      "no filters",
			query:     "",
			wantCount: 0,
			wantErr:   false,
		},
		{
			name:      "single eq filter",
			query:     "filter=name:eq:John",
			wantCount: 1,
			wantErr:   false,
			checkFirst: func(t *testing.T, column, operator string, value interface{}) {
				if column != "name" {
					t.Errorf("expected column 'name', got '%s'", column)
				}
				if operator != "eq" {
					t.Errorf("expected operator 'eq', got '%s'", operator)
				}
				if value != "John" {
					t.Errorf("expected value 'John', got '%v'", value)
				}
			},
		},
		{
			name:      "multiple filters",
			query:     "filter=age:gt:18,status:eq:active",
			wantCount: 2,
			wantErr:   false,
		},
		{
			name:      "all operators",
			query:     "filter=a:eq:1",
			wantCount: 1,
			wantErr:   false,
		},
		{
			name:      "ne operator",
			query:     "filter=status:ne:deleted",
			wantCount: 1,
			wantErr:   false,
			checkFirst: func(t *testing.T, column, operator string, value interface{}) {
				if operator != "ne" {
					t.Errorf("expected operator 'ne', got '%s'", operator)
				}
			},
		},
		{
			name:      "gt operator",
			query:     "filter=age:gt:21",
			wantCount: 1,
			wantErr:   false,
		},
		{
			name:      "gte operator",
			query:     "filter=age:gte:18",
			wantCount: 1,
			wantErr:   false,
		},
		{
			name:      "lt operator",
			query:     "filter=price:lt:100",
			wantCount: 1,
			wantErr:   false,
		},
		{
			name:      "lte operator",
			query:     "filter=price:lte:99",
			wantCount: 1,
			wantErr:   false,
		},
		{
			name:      "like operator",
			query:     "filter=name:like:John%25", // %25 is URL-encoded %
			wantCount: 1,
			wantErr:   false,
			checkFirst: func(t *testing.T, column, operator string, value interface{}) {
				if value != "John%" {
					t.Errorf("expected value 'John%%', got '%v'", value)
				}
			},
		},
		{
			name:      "in operator",
			query:     "filter=status:in:active|pending|review",
			wantCount: 1,
			wantErr:   false,
			checkFirst: func(t *testing.T, column, operator string, value interface{}) {
				if operator != "in" {
					t.Errorf("expected operator 'in', got '%s'", operator)
				}
				values, ok := value.([]string)
				if !ok {
					t.Errorf("expected []string for in operator, got %T", value)
				}
				if len(values) != 3 {
					t.Errorf("expected 3 values for in operator, got %d", len(values))
				}
			},
		},
		{
			name:      "two parts now valid as simplified format",
			query:     "filter=name:eq",
			wantCount: 1,
			wantErr:   false,
			checkFirst: func(t *testing.T, column, operator string, value interface{}) {
				// "name:eq" is now parsed as simplified: column=name, value=eq (with implicit eq operator)
				if column != "name" || operator != "eq" || value != "eq" {
					t.Errorf("expected name eq eq, got %s %s %v", column, operator, value)
				}
			},
		},
		{
			name:      "invalid format - single part",
			query:     "filter=name",
			wantCount: 0,
			wantErr:   true,
		},
		{
			name:      "invalid operator",
			query:     "filter=name:contains:test",
			wantCount: 0,
			wantErr:   true,
		},
		{
			name:      "value with colons",
			query:     "filter=time:eq:12:30:00",
			wantCount: 1,
			wantErr:   false,
			checkFirst: func(t *testing.T, column, operator string, value interface{}) {
				if value != "12:30:00" {
					t.Errorf("expected value '12:30:00', got '%v'", value)
				}
			},
		},
		// Simplified operator tests
		{
			name:      "simplified gt operator",
			query:     "filter=age:>18",
			wantCount: 1,
			wantErr:   false,
			checkFirst: func(t *testing.T, column, operator string, value interface{}) {
				if column != "age" || operator != "gt" || value != "18" {
					t.Errorf("expected age gt 18, got %s %s %v", column, operator, value)
				}
			},
		},
		{
			name:      "simplified lt operator",
			query:     "filter=price:<100",
			wantCount: 1,
			wantErr:   false,
			checkFirst: func(t *testing.T, column, operator string, value interface{}) {
				if operator != "lt" || value != "100" {
					t.Errorf("expected lt 100, got %s %v", operator, value)
				}
			},
		},
		{
			name:      "simplified gte operator",
			query:     "filter=score:>=90",
			wantCount: 1,
			wantErr:   false,
			checkFirst: func(t *testing.T, column, operator string, value interface{}) {
				if operator != "gte" || value != "90" {
					t.Errorf("expected gte 90, got %s %v", operator, value)
				}
			},
		},
		{
			name:      "simplified lte operator",
			query:     "filter=count:<=10",
			wantCount: 1,
			wantErr:   false,
			checkFirst: func(t *testing.T, column, operator string, value interface{}) {
				if operator != "lte" || value != "10" {
					t.Errorf("expected lte 10, got %s %v", operator, value)
				}
			},
		},
		{
			name:      "simplified ne operator",
			query:     "filter=status:!deleted",
			wantCount: 1,
			wantErr:   false,
			checkFirst: func(t *testing.T, column, operator string, value interface{}) {
				if operator != "ne" || value != "deleted" {
					t.Errorf("expected ne deleted, got %s %v", operator, value)
				}
			},
		},
		{
			name:      "simplified eq operator (no prefix)",
			query:     "filter=name:John",
			wantCount: 1,
			wantErr:   false,
			checkFirst: func(t *testing.T, column, operator string, value interface{}) {
				if operator != "eq" || value != "John" {
					t.Errorf("expected eq John, got %s %v", operator, value)
				}
			},
		},
		{
			name:      "mixed simplified and standard formats",
			query:     "filter=age:>18,status:eq:active,score:>=90",
			wantCount: 3,
			wantErr:   false,
		},
		{
			name:      "backwards compatible - standard format still works",
			query:     "filter=age:gt:30",
			wantCount: 1,
			wantErr:   false,
			checkFirst: func(t *testing.T, column, operator string, value interface{}) {
				if column != "age" || operator != "gt" || value != "30" {
					t.Errorf("expected age gt 30, got %s %s %v", column, operator, value)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/?"+tt.query, nil)
			filters, err := ParseFilters(req)

			if (err != nil) != tt.wantErr {
				t.Errorf("ParseFilters() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if len(filters) != tt.wantCount {
				t.Errorf("ParseFilters() count = %v, want %v", len(filters), tt.wantCount)
			}

			if tt.checkFirst != nil && len(filters) > 0 {
				tt.checkFirst(t, filters[0].Column, filters[0].Operator, filters[0].Value)
			}
		})
	}
}

func TestParseSorts(t *testing.T) {
	tests := []struct {
		name       string
		query      string
		wantCount  int
		wantErr    bool
		checkFirst func(t *testing.T, column, direction string)
	}{
		{
			name:      "no sorts",
			query:     "",
			wantCount: 0,
			wantErr:   false,
		},
		{
			name:      "single sort ascending",
			query:     "sort=name:asc",
			wantCount: 1,
			wantErr:   false,
			checkFirst: func(t *testing.T, column, direction string) {
				if column != "name" {
					t.Errorf("expected column 'name', got '%s'", column)
				}
				if direction != "asc" {
					t.Errorf("expected direction 'asc', got '%s'", direction)
				}
			},
		},
		{
			name:      "single sort descending",
			query:     "sort=created_at:desc",
			wantCount: 1,
			wantErr:   false,
			checkFirst: func(t *testing.T, column, direction string) {
				if direction != "desc" {
					t.Errorf("expected direction 'desc', got '%s'", direction)
				}
			},
		},
		{
			name:      "sort without direction defaults to asc",
			query:     "sort=name",
			wantCount: 1,
			wantErr:   false,
			checkFirst: func(t *testing.T, column, direction string) {
				if direction != "asc" {
					t.Errorf("expected default direction 'asc', got '%s'", direction)
				}
			},
		},
		{
			name:      "multiple sorts",
			query:     "sort=created_at:desc,name:asc",
			wantCount: 2,
			wantErr:   false,
		},
		{
			name:      "invalid direction",
			query:     "sort=name:invalid",
			wantCount: 0,
			wantErr:   true,
		},
		{
			name:      "uppercase direction normalized",
			query:     "sort=name:DESC",
			wantCount: 1,
			wantErr:   false,
			checkFirst: func(t *testing.T, column, direction string) {
				if direction != "desc" {
					t.Errorf("expected normalized direction 'desc', got '%s'", direction)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/?"+tt.query, nil)
			sorts, err := ParseSorts(req)

			if (err != nil) != tt.wantErr {
				t.Errorf("ParseSorts() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if len(sorts) != tt.wantCount {
				t.Errorf("ParseSorts() count = %v, want %v", len(sorts), tt.wantCount)
			}

			if tt.checkFirst != nil && len(sorts) > 0 {
				tt.checkFirst(t, sorts[0].Column, sorts[0].Direction)
			}
		})
	}
}

func TestParseWhereClause(t *testing.T) {
	tests := []struct {
		name       string
		query      string
		wantCount  int
		wantErr    bool
		checkFirst func(t *testing.T, column, operator string, value interface{})
	}{
		{
			name:      "no where clause",
			query:     "",
			wantCount: 0,
			wantErr:   false,
		},
		{
			name:      "single condition",
			query:     "where=id:eq:123",
			wantCount: 1,
			wantErr:   false,
			checkFirst: func(t *testing.T, column, operator string, value interface{}) {
				if column != "id" || operator != "eq" || value != "123" {
					t.Errorf("unexpected where clause values: column=%s, op=%s, val=%v", column, operator, value)
				}
			},
		},
		{
			name:      "multiple conditions",
			query:     "where=status:eq:active,type:ne:deleted",
			wantCount: 2,
			wantErr:   false,
		},
		{
			name:      "in operator with pipe-delimited values",
			query:     "where=id:in:1|2|3",
			wantCount: 1,
			wantErr:   false,
			checkFirst: func(t *testing.T, column, operator string, value interface{}) {
				values, ok := value.([]string)
				if !ok {
					t.Errorf("expected []string for in operator")
				}
				if len(values) != 3 {
					t.Errorf("expected 3 values, got %d", len(values))
				}
			},
		},
		{
			name:      "invalid format - single part",
			query:     "where=invalid",
			wantCount: 0,
			wantErr:   true,
		},
		{
			name:      "invalid operator in where",
			query:     "where=name:contains:test",
			wantCount: 0,
			wantErr:   true,
		},
		// Simplified operator tests for where clause
		{
			name:      "simplified gt operator in where",
			query:     "where=id:>100",
			wantCount: 1,
			wantErr:   false,
			checkFirst: func(t *testing.T, column, operator string, value interface{}) {
				if column != "id" || operator != "gt" || value != "100" {
					t.Errorf("expected id gt 100, got %s %s %v", column, operator, value)
				}
			},
		},
		{
			name:      "simplified ne operator in where",
			query:     "where=status:!deleted",
			wantCount: 1,
			wantErr:   false,
			checkFirst: func(t *testing.T, column, operator string, value interface{}) {
				if operator != "ne" || value != "deleted" {
					t.Errorf("expected ne deleted, got %s %v", operator, value)
				}
			},
		},
		{
			name:      "mixed simplified and standard in where",
			query:     "where=id:>100,status:eq:active",
			wantCount: 2,
			wantErr:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/?"+tt.query, nil)
			filters, err := ParseWhereClause(req)

			if (err != nil) != tt.wantErr {
				t.Errorf("ParseWhereClause() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if len(filters) != tt.wantCount {
				t.Errorf("ParseWhereClause() count = %v, want %v", len(filters), tt.wantCount)
			}

			if tt.checkFirst != nil && len(filters) > 0 {
				tt.checkFirst(t, filters[0].Column, filters[0].Operator, filters[0].Value)
			}
		})
	}
}

func TestParseDryRun(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  bool
	}{
		{"no dry_run param", "", false},
		{"dry_run=true", "dry_run=true", true},
		{"dry_run=1", "dry_run=1", true},
		{"dry_run=false", "dry_run=false", false},
		{"dry_run=0", "dry_run=0", false},
		{"dry_run=yes", "dry_run=yes", false}, // only "true" and "1" are accepted
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/?"+tt.query, nil)
			if got := ParseDryRun(req); got != tt.want {
				t.Errorf("ParseDryRun() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParseLinks(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  bool
	}{
		{"no links param", "", false},
		{"links=true", "links=true", true},
		{"links=1", "links=1", true},
		{"links=false", "links=false", false},
		{"links=0", "links=0", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/?"+tt.query, nil)
			if got := ParseLinks(req); got != tt.want {
				t.Errorf("ParseLinks() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParseSelectColumns(t *testing.T) {
	tests := []struct {
		name        string
		query       string
		wantColumns []string
		wantErr     bool
	}{
		{
			name:        "no select param",
			query:       "",
			wantColumns: nil,
			wantErr:     false,
		},
		{
			name:        "single column",
			query:       "select=id",
			wantColumns: []string{"id"},
			wantErr:     false,
		},
		{
			name:        "multiple columns",
			query:       "select=id,name,email",
			wantColumns: []string{"id", "name", "email"},
			wantErr:     false,
		},
		{
			name:        "columns with spaces",
			query:       "select=id,%20name%20,%20email",
			wantColumns: []string{"id", "name", "email"},
			wantErr:     false,
		},
		{
			name:        "columns with underscores",
			query:       "select=first_name,last_name,created_at",
			wantColumns: []string{"first_name", "last_name", "created_at"},
			wantErr:     false,
		},
		{
			name:        "invalid column name with dash",
			query:       "select=first-name",
			wantColumns: nil,
			wantErr:     true,
		},
		{
			name:        "invalid column name with special chars",
			query:       "select=name%3BDROP",
			wantColumns: nil,
			wantErr:     true,
		},
		{
			name:        "empty select value returns nil",
			query:       "select=",
			wantColumns: nil,
			wantErr:     false, // empty string returns nil, nil (treated as no select param)
		},
		{
			name:        "empty columns after split",
			query:       "select=,,,",
			wantColumns: nil,
			wantErr:     true,
		},
		{
			name:        "mixed valid and empty",
			query:       "select=id,,name",
			wantColumns: []string{"id", "name"},
			wantErr:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/?"+tt.query, nil)
			columns, err := ParseSelectColumns(req)

			if (err != nil) != tt.wantErr {
				t.Errorf("ParseSelectColumns() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr {
				if len(columns) != len(tt.wantColumns) {
					t.Errorf("ParseSelectColumns() columns count = %v, want %v", len(columns), len(tt.wantColumns))
					return
				}
				for i, col := range columns {
					if col != tt.wantColumns[i] {
						t.Errorf("ParseSelectColumns() column[%d] = %v, want %v", i, col, tt.wantColumns[i])
					}
				}
			}
		})
	}
}

func TestParseGroupBy(t *testing.T) {
	tests := []struct {
		name    string
		query   string
		want    string
		wantErr bool
	}{
		{
			name:    "no group_by param",
			query:   "",
			want:    "",
			wantErr: false,
		},
		{
			name:    "valid column",
			query:   "group_by=status",
			want:    "status",
			wantErr: false,
		},
		{
			name:    "valid column with underscore",
			query:   "group_by=created_year",
			want:    "created_year",
			wantErr: false,
		},
		{
			name:    "column with spaces trimmed",
			query:   "group_by=%20status%20",
			want:    "status",
			wantErr: false,
		},
		{
			name:    "invalid column with dash",
			query:   "group_by=status-code",
			want:    "",
			wantErr: true,
		},
		{
			name:    "invalid column with special chars",
			query:   "group_by=status%3BDROP",
			want:    "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/?"+tt.query, nil)
			got, err := ParseGroupBy(req)

			if (err != nil) != tt.wantErr {
				t.Errorf("ParseGroupBy() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if got != tt.want {
				t.Errorf("ParseGroupBy() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGetAcceptFormat(t *testing.T) {
	tests := []struct {
		name   string
		accept string
		want   string
	}{
		{"no accept header", "", "json"},
		{"application/json", "application/json", "json"},
		{"text/csv", "text/csv", "csv"},
		{"application/parquet", "application/parquet", "parquet"},
		{"application/vnd.apache.arrow", "application/vnd.apache.arrow.stream", "arrow"},
		{"text/html defaults to json", "text/html", "json"},
		{"*/* defaults to json", "*/*", "json"},
		{"csv with charset", "text/csv; charset=utf-8", "csv"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/", nil)
			if tt.accept != "" {
				req.Header.Set("Accept", tt.accept)
			}
			if got := GetAcceptFormat(req); got != tt.want {
				t.Errorf("GetAcceptFormat() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSanitizeTableName(t *testing.T) {
	tests := []struct {
		name      string
		tableName string
		wantErr   bool
	}{
		{"valid lowercase", "users", false},
		{"valid uppercase", "USERS", false},
		{"valid mixed case", "UserData", false},
		{"valid with underscore", "user_data", false},
		{"valid with numbers", "users123", false},
		{"valid complex", "User_Data_2024", false},
		{"empty string", "", true},
		{"contains space", "user data", true},
		{"contains dash", "user-data", true},
		{"contains dot", "user.data", true},
		{"contains semicolon", "users;DROP", true},
		{"contains quotes", "users'test", true},
		{"contains parentheses", "users()", true},
		{"SQL injection attempt", "users; DROP TABLE users;--", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := SanitizeTableName(tt.tableName)
			if (err != nil) != tt.wantErr {
				t.Errorf("SanitizeTableName(%q) error = %v, wantErr %v", tt.tableName, err, tt.wantErr)
			}
		})
	}
}

func TestSanitizeColumnName(t *testing.T) {
	tests := []struct {
		name       string
		columnName string
		wantErr    bool
	}{
		{"valid lowercase", "name", false},
		{"valid uppercase", "NAME", false},
		{"valid with underscore", "first_name", false},
		{"valid with numbers", "col1", false},
		{"empty string", "", true},
		{"contains space", "first name", true},
		{"contains dash", "first-name", true},
		{"contains asterisk", "col*", true},
		{"SQL injection", "name;--", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := SanitizeColumnName(tt.columnName)
			if (err != nil) != tt.wantErr {
				t.Errorf("SanitizeColumnName(%q) error = %v, wantErr %v", tt.columnName, err, tt.wantErr)
			}
		})
	}
}

func TestParseGETQueryPath(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		wantSQL    string
		wantFormat string
		wantErr    bool
	}{
		{
			name:       "valid JSON path",
			path:       "/duckdb/query/SELECT%20*%20FROM%20users/result.json",
			wantSQL:    "SELECT * FROM users",
			wantFormat: "json",
			wantErr:    false,
		},
		{
			name:       "valid CSV path",
			path:       "/duckdb/query/SELECT%20id%2C%20name%20FROM%20users/result.csv",
			wantSQL:    "SELECT id, name FROM users",
			wantFormat: "csv",
			wantErr:    false,
		},
		{
			name:       "valid Parquet path",
			path:       "/duckdb/query/SELECT%20*%20FROM%20data/result.parquet",
			wantSQL:    "SELECT * FROM data",
			wantFormat: "parquet",
			wantErr:    false,
		},
		{
			name:       "valid Arrow path",
			path:       "/duckdb/query/SELECT%20*%20FROM%20data/result.arrow",
			wantSQL:    "SELECT * FROM data",
			wantFormat: "arrow",
			wantErr:    false,
		},
		{
			name:    "invalid prefix",
			path:    "/api/query/SELECT%20*%20FROM%20users/result.json",
			wantErr: true,
		},
		{
			name:    "missing result pattern",
			path:    "/duckdb/query/SELECT%20*%20FROM%20users",
			wantErr: true,
		},
		{
			name:    "empty SQL",
			path:    "/duckdb/query//result.json",
			wantErr: true,
		},
		{
			name:    "invalid format",
			path:    "/duckdb/query/SELECT%20*%20FROM%20users/result.xml",
			wantErr: true,
		},
		{
			name:    "missing format extension",
			path:    "/duckdb/query/SELECT%20*%20FROM%20users/result.",
			wantErr: true,
		},
		{
			name:       "complex SQL query",
			path:       "/duckdb/query/SELECT%20u.id%2C%20u.name%20FROM%20users%20u%20WHERE%20u.active%20%3D%20true/result.json",
			wantSQL:    "SELECT u.id, u.name FROM users u WHERE u.active = true",
			wantFormat: "json",
			wantErr:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sql, format, err := ParseGETQueryPath(tt.path)

			if (err != nil) != tt.wantErr {
				t.Errorf("ParseGETQueryPath() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr {
				if sql != tt.wantSQL {
					t.Errorf("ParseGETQueryPath() sql = %v, want %v", sql, tt.wantSQL)
				}
				if format != tt.wantFormat {
					t.Errorf("ParseGETQueryPath() format = %v, want %v", format, tt.wantFormat)
				}
			}
		})
	}
}

// Benchmark tests for performance-critical functions
func BenchmarkParseFilters(b *testing.B) {
	req := httptest.NewRequest("GET", "/?filter=age:gt:18,status:eq:active,name:like:John%", nil)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ParseFilters(req)
	}
}

func BenchmarkParsePagination(b *testing.B) {
	req := httptest.NewRequest("GET", "/?limit=50&page=5", nil)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ParsePagination(req, 100, 10000)
	}
}

func BenchmarkSanitizeTableName(b *testing.B) {
	for i := 0; i < b.N; i++ {
		SanitizeTableName("user_data_2024")
	}
}

// Helper to create a request with specific method
func newRequest(method, url string) *http.Request {
	return httptest.NewRequest(method, url, nil)
}
