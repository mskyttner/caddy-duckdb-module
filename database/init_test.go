package database

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/zap"
)

func TestParseSQL(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected []string
	}{
		{
			name:     "empty input",
			input:    "",
			expected: []string{},
		},
		{
			name:     "whitespace only",
			input:    "   \n\t\n   ",
			expected: []string{},
		},
		{
			name:     "single statement",
			input:    "SELECT 1;",
			expected: []string{"SELECT 1"},
		},
		{
			name:     "single statement without semicolon",
			input:    "SELECT 1",
			expected: []string{"SELECT 1"},
		},
		{
			name:     "multiple statements",
			input:    "SELECT 1; SELECT 2; SELECT 3;",
			expected: []string{"SELECT 1", "SELECT 2", "SELECT 3"},
		},
		{
			name:     "multiline statement",
			input:    "SELECT\n  id,\n  name\nFROM users;",
			expected: []string{"SELECT\n  id,\n  name\nFROM users"},
		},
		{
			name:     "single line comment",
			input:    "-- this is a comment\nSELECT 1;",
			expected: []string{"SELECT 1"},
		},
		{
			name:     "single line comment at end",
			input:    "SELECT 1; -- this is a comment",
			expected: []string{"SELECT 1"},
		},
		{
			name:     "comment only",
			input:    "-- just a comment",
			expected: []string{},
		},
		{
			name:     "block comment",
			input:    "/* this is a block comment */ SELECT 1;",
			expected: []string{"SELECT 1"},
		},
		{
			name:     "multiline block comment",
			input:    "/*\n  multiline\n  comment\n*/ SELECT 1;",
			expected: []string{"SELECT 1"},
		},
		{
			name:     "block comment in middle",
			input:    "SELECT /* comment */ 1;",
			expected: []string{"SELECT  1"},
		},
		{
			name:     "string literal with semicolon",
			input:    "SELECT 'hello; world';",
			expected: []string{"SELECT 'hello; world'"},
		},
		{
			name:     "string literal with multiple semicolons",
			input:    "INSERT INTO t VALUES ('a;b;c'); SELECT 1;",
			expected: []string{"INSERT INTO t VALUES ('a;b;c')", "SELECT 1"},
		},
		{
			name:     "escaped quote in string",
			input:    "SELECT 'it''s working';",
			expected: []string{"SELECT 'it''s working'"},
		},
		{
			name:     "complex escaped quotes",
			input:    "SELECT 'don''t stop; keep going';",
			expected: []string{"SELECT 'don''t stop; keep going'"},
		},
		{
			name:  "extension loading example",
			input: "SET autoinstall_known_extensions = 1;\nSET autoload_known_extensions = 1;\nLOAD httpfs;",
			expected: []string{
				"SET autoinstall_known_extensions = 1",
				"SET autoload_known_extensions = 1",
				"LOAD httpfs",
			},
		},
		{
			name: "create secret example",
			input: `CREATE SECRET my_secret (
    TYPE S3,
    KEY_ID 'my_key_id',
    SECRET 'my_secret_key',
    REGION 'us-east-1'
);`,
			expected: []string{
				"CREATE SECRET my_secret (\n    TYPE S3,\n    KEY_ID 'my_key_id',\n    SECRET 'my_secret_key',\n    REGION 'us-east-1'\n)"},
		},
		{
			name: "mixed comments and statements",
			input: `-- Configure extensions
SET autoinstall_known_extensions = 1;
/* Load the httpfs extension
   for remote file access */
LOAD httpfs;
-- Done`,
			expected: []string{
				"SET autoinstall_known_extensions = 1",
				"LOAD httpfs",
			},
		},
		{
			name:     "comment-like content in string",
			input:    "SELECT '-- not a comment'; SELECT '/* also not */';",
			expected: []string{"SELECT '-- not a comment'", "SELECT '/* also not */'"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := parseSQL(tt.input)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if len(result) != len(tt.expected) {
				t.Fatalf("expected %d statements, got %d\nexpected: %v\ngot: %v",
					len(tt.expected), len(result), tt.expected, result)
			}

			for i, stmt := range result {
				if stmt != tt.expected[i] {
					t.Errorf("statement %d mismatch\nexpected: %q\ngot: %q",
						i, tt.expected[i], stmt)
				}
			}
		})
	}
}

func setupManagerWithInitFile(t *testing.T, initFilePath string) *Manager {
	t.Helper()
	cfg := Config{
		MainDBPath:   ":memory:",
		AuthDBPath:   ":memory:",
		Threads:      1,
		AccessMode:   "read_write",
		QueryTimeout: 10 * time.Second,
		InitFilePath: initFilePath,
		Logger:       zap.NewNop(),
	}
	mgr, err := NewManagerForTesting(cfg)
	if err != nil {
		t.Fatalf("NewManagerForTesting: %v", err)
	}
	t.Cleanup(func() { mgr.Close() })
	return mgr
}

func writeInitFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "init.sql")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write init file: %v", err)
	}
	return path
}

func TestManager_ReloadInitFile_NoInitFileConfigured(t *testing.T) {
	mgr := setupManagerWithInitFile(t, "")
	if _, err := mgr.ReloadInitFile(); err == nil {
		t.Error("expected error when no init_file is configured, got nil")
	}
}

func TestManager_ReloadInitFile_RunsAgain(t *testing.T) {
	path := writeInitFile(t, `CREATE OR REPLACE MACRO greeting() AS 'hello';`)
	mgr := setupManagerWithInitFile(t, path)

	n, err := mgr.ReloadInitFile()
	if err != nil {
		t.Fatalf("first reload failed: %v", err)
	}
	if n != 1 {
		t.Errorf("expected 1 statement executed, got %d", n)
	}

	var greeting string
	if err := mgr.mainDB.QueryRow("SELECT greeting()").Scan(&greeting); err != nil {
		t.Fatalf("macro not callable after reload: %v", err)
	}
	if greeting != "hello" {
		t.Errorf("expected 'hello', got %q", greeting)
	}

	// Simulate an ops edit to the init file, then reload again -- the macro
	// should reflect the new definition without restarting the process.
	if err := os.WriteFile(path, []byte(`CREATE OR REPLACE MACRO greeting() AS 'goodbye';`), 0644); err != nil {
		t.Fatalf("failed to rewrite init file: %v", err)
	}
	if _, err := mgr.ReloadInitFile(); err != nil {
		t.Fatalf("second reload failed: %v", err)
	}
	if err := mgr.mainDB.QueryRow("SELECT greeting()").Scan(&greeting); err != nil {
		t.Fatalf("macro not callable after second reload: %v", err)
	}
	if greeting != "goodbye" {
		t.Errorf("expected 'goodbye' after reload, got %q", greeting)
	}
}

func TestManager_ReloadInitFile_NonIdempotentStatementFailsCleanly(t *testing.T) {
	// A bare CREATE TABLE (no IF NOT EXISTS/OR REPLACE) is exactly the kind of
	// init.sql statement that works once at startup but must fail -- loudly,
	// not silently -- on a second reload. This documents that failure mode
	// rather than papering over it (see plans/reload-without-restart.md).
	path := writeInitFile(t, `CREATE TABLE reload_test (x INTEGER);`)
	mgr := setupManagerWithInitFile(t, path)

	if _, err := mgr.ReloadInitFile(); err != nil {
		t.Fatalf("first reload failed: %v", err)
	}
	if _, err := mgr.ReloadInitFile(); err == nil {
		t.Error("expected second reload of a non-idempotent CREATE TABLE to fail, got nil error")
	}
}

func TestManager_ReloadInitFile_MissingFile(t *testing.T) {
	mgr := setupManagerWithInitFile(t, "/nonexistent/path/init.sql")
	if _, err := mgr.ReloadInitFile(); err == nil {
		t.Error("expected error for a missing init file, got nil")
	}
}

func TestTruncateStatement(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		maxLen   int
		expected string
	}{
		{
			name:     "short statement",
			input:    "SELECT 1",
			maxLen:   50,
			expected: "SELECT 1",
		},
		{
			name:     "exact length",
			input:    "SELECT 1",
			maxLen:   8,
			expected: "SELECT 1",
		},
		{
			name:     "truncated",
			input:    "SELECT * FROM very_long_table_name WHERE id = 1",
			maxLen:   20,
			expected: "SELECT * FROM ver...",
		},
		{
			name:     "multiline normalized",
			input:    "SELECT\n  id,\n  name\nFROM users",
			maxLen:   50,
			expected: "SELECT id, name FROM users",
		},
		{
			name:     "multiline truncated",
			input:    "SELECT\n  id,\n  name\nFROM users",
			maxLen:   15,
			expected: "SELECT id, n...",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := truncateStatement(tt.input, tt.maxLen)
			if result != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, result)
			}
		})
	}
}
