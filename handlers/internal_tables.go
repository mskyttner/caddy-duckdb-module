package handlers

import (
	"regexp"
	"strings"
)

// Pre-compiled regexes for internal table protection (compiled once at package init)
var (
	// SQL comment patterns
	blockCommentRegex = regexp.MustCompile(`/\*[\s\S]*?\*/`)
	lineCommentRegex  = regexp.MustCompile(`--[^\n]*`)
	whitespaceRegex   = regexp.MustCompile(`\s+`)

	// Internal table patterns with word boundaries
	internalTablePatterns = []*regexp.Regexp{
		regexp.MustCompile(`\bapi_keys\b`),
		regexp.MustCompile(`\broles\b`),
		regexp.MustCompile(`\bpermissions\b`),
		regexp.MustCompile(`\btrusted_users\b`),
	}
)

// containsInternalTables checks if the SQL query references internal auth tables.
// Uses comment stripping and word-boundary matching to prevent bypass attempts
// like SQL comments (api/**/keys) or whitespace variations.
func containsInternalTables(sql string) bool {
	// Strip SQL comments to prevent bypass via api/**/keys or similar
	cleaned := stripSQLComments(sql)

	// Normalize whitespace (collapse multiple spaces/tabs/newlines into single space)
	cleaned = whitespaceRegex.ReplaceAllString(cleaned, " ")

	// Convert to lowercase for case-insensitive matching
	lowerSQL := strings.ToLower(cleaned)

	// Check against pre-compiled patterns
	for _, pattern := range internalTablePatterns {
		if pattern.MatchString(lowerSQL) {
			return true
		}
	}

	return false
}

// stripSQLComments removes SQL comments from a query string.
// Handles both block comments (/* ... */) and line comments (-- ...).
func stripSQLComments(sql string) string {
	// Remove block comments /* ... */ (non-greedy to handle multiple comments)
	sql = blockCommentRegex.ReplaceAllString(sql, " ")

	// Remove line comments -- ... (everything from -- to end of line)
	sql = lineCommentRegex.ReplaceAllString(sql, " ")

	return sql
}
