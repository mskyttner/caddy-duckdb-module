package database

import (
	"context"
	"fmt"
	"os"
	"strings"

	"go.uber.org/zap"
)

// ReloadInitFile re-runs the configured init file against the live
// connection pool, without restarting the module. Returns the number of
// statements executed. Returns an error if no init_file was configured at
// startup.
//
// This re-executes every statement in the file, so the init file's own
// statements must be idempotent to reload cleanly -- CREATE OR REPLACE
// MACRO/TABLE, ATTACH IF NOT EXISTS, SET, and INSTALL/LOAD are all safe to
// re-run; a bare CREATE MACRO/TABLE or ATTACH is not and will fail with an
// "already exists" error on reload. Not transactional across statements: a
// failure partway through leaves the already-applied statements in effect.
func (m *Manager) ReloadInitFile() (int, error) {
	if m.initFilePath == "" {
		return 0, fmt.Errorf("no init_file configured for this server")
	}
	return m.loadInitFile(m.initFilePath)
}

// loadInitFile reads and executes SQL statements from an init file, returning
// the number of statements executed. This is called during database
// initialization to set up extensions, configuration, and other startup SQL
// commands -- and again, later, by ReloadInitFile for a live reload.
func (m *Manager) loadInitFile(path string) (int, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("failed to read init file '%s': %w", path, err)
	}

	statements, err := parseSQL(string(content))
	if err != nil {
		return 0, fmt.Errorf("failed to parse init file '%s': %w", path, err)
	}

	if len(statements) == 0 {
		m.logger.Info("Init file is empty, skipping",
			zap.String("path", path),
		)
		return 0, nil
	}

	m.logger.Info("Executing init SQL file",
		zap.String("path", path),
		zap.Int("statements", len(statements)),
	)

	ctx, cancel := context.WithTimeout(context.Background(), m.queryTimeout)
	defer cancel()

	for i, stmt := range statements {
		m.logger.Debug("Executing init statement",
			zap.Int("index", i+1),
			zap.String("statement", truncateStatement(stmt, 100)),
		)

		_, err := m.mainDB.ExecContext(ctx, stmt)
		if err != nil {
			return 0, fmt.Errorf("failed to execute init statement %d (%s): %w",
				i+1, truncateStatement(stmt, 50), err)
		}
	}

	m.logger.Info("Init SQL file executed successfully",
		zap.String("path", path),
		zap.Int("statements_executed", len(statements)),
	)

	return len(statements), nil
}

// parseSQL parses SQL content into individual statements.
// It handles:
// - Multiline statements
// - Single-line comments (-- ...)
// - Block comments (/* ... */)
// - String literals with preserved semicolons
func parseSQL(content string) ([]string, error) {
	var statements []string
	var currentStmt strings.Builder

	inString := false
	inBlockComment := false
	inLineComment := false

	runes := []rune(content)
	n := len(runes)

	for i := 0; i < n; i++ {
		c := runes[i]

		// Handle line comments
		if inLineComment {
			if c == '\n' {
				inLineComment = false
				currentStmt.WriteRune('\n') // Preserve newline for readability
			}
			continue
		}

		// Handle block comments
		if inBlockComment {
			if c == '*' && i+1 < n && runes[i+1] == '/' {
				inBlockComment = false
				i++ // Skip the '/'
			}
			continue
		}

		// Handle string literals
		if inString {
			currentStmt.WriteRune(c)
			if c == '\'' {
				// Check for escaped quote ('')
				if i+1 < n && runes[i+1] == '\'' {
					currentStmt.WriteRune(runes[i+1])
					i++ // Skip the second quote
				} else {
					inString = false
				}
			}
			continue
		}

		// Not in string, comment - check for special sequences
		if c == '\'' {
			inString = true
			currentStmt.WriteRune(c)
			continue
		}

		if c == '-' && i+1 < n && runes[i+1] == '-' {
			inLineComment = true
			i++ // Skip the second '-'
			continue
		}

		if c == '/' && i+1 < n && runes[i+1] == '*' {
			inBlockComment = true
			i++ // Skip the '*'
			continue
		}

		// Statement terminator
		if c == ';' {
			stmt := strings.TrimSpace(currentStmt.String())
			if stmt != "" {
				statements = append(statements, stmt)
			}
			currentStmt.Reset()
			continue
		}

		currentStmt.WriteRune(c)
	}

	// Handle any remaining content (statement without trailing semicolon)
	remaining := strings.TrimSpace(currentStmt.String())
	if remaining != "" {
		statements = append(statements, remaining)
	}

	return statements, nil
}

// truncateStatement truncates a statement for logging purposes.
func truncateStatement(stmt string, maxLen int) string {
	// Normalize whitespace for display
	stmt = strings.Join(strings.Fields(stmt), " ")
	if len(stmt) <= maxLen {
		return stmt
	}
	return stmt[:maxLen-3] + "..."
}
