package handlers

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/tobilg/caddy-duckdb-module/database"
)

// ParsePagination parses pagination parameters from the request.
// Returns limit, offset, page, and paginationRequested flag.
// If neither limit nor page is specified, pagination is optional (limit=0).
// The absoluteMaxRows safety limit is always enforced unless set to 0 (disabled).
func ParsePagination(r *http.Request, maxRowsPerPage int, absoluteMaxRows int) (limit, offset int, page int, paginationRequested bool) {
	limitStr := r.URL.Query().Get("limit")
	pageStr := r.URL.Query().Get("page")

	// Check if user explicitly requested pagination
	if limitStr == "" && pageStr == "" {
		// No pagination requested - will use absolute max as safety limit
		return 0, 0, 0, false
	}

	// User wants pagination
	paginationRequested = true

	// Parse page (default: 1)
	page = 1
	if pageStr != "" {
		if p, err := strconv.Atoi(pageStr); err == nil && p > 0 {
			page = p
		}
	}

	// Parse limit (default: maxRowsPerPage)
	limit = maxRowsPerPage
	if limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 {
			limit = l
		}
	}

	// Cap limit at maxRowsPerPage when pagination is used
	if limit > maxRowsPerPage {
		limit = maxRowsPerPage
	}

	// Calculate offset
	offset = (page - 1) * limit

	return limit, offset, page, paginationRequested
}

// parseSimplifiedOperator parses simplified operator syntax (e.g., ">30", "!deleted")
// and returns the standard operator and value.
// Mapping: >= → gte, <= → lte, > → gt, < → lt, ! → ne, none → eq
func parseSimplifiedOperator(operatorValue string) (operator, value string) {
	switch {
	case strings.HasPrefix(operatorValue, ">="):
		return "gte", operatorValue[2:]
	case strings.HasPrefix(operatorValue, "<="):
		return "lte", operatorValue[2:]
	case strings.HasPrefix(operatorValue, ">"):
		return "gt", operatorValue[1:]
	case strings.HasPrefix(operatorValue, "<"):
		return "lt", operatorValue[1:]
	case strings.HasPrefix(operatorValue, "!"):
		return "ne", operatorValue[1:]
	default:
		return "eq", operatorValue
	}
}

// ParseFilters parses filter parameters from the request.
// Supports two formats:
//   - Standard: filter=column:operator:value (e.g., age:gt:18)
//   - Simplified: filter=column:value (e.g., age:>18, status:!deleted)
//
// Simplified operators: > (gt), < (lt), >= (gte), <= (lte), ! (ne), none (eq)
// Example: filter=age:gt:18,status:eq:active OR filter=age:>18,status:active
func ParseFilters(r *http.Request) ([]database.Filter, error) {
	filterStr := r.URL.Query().Get("filter")
	if filterStr == "" {
		return nil, nil
	}

	filterParts := strings.Split(filterStr, ",")
	filters := make([]database.Filter, 0, len(filterParts))

	// Valid operators for standard format
	validOperators := map[string]bool{
		"eq": true, "ne": true, "gt": true, "gte": true,
		"lt": true, "lte": true, "like": true, "in": true,
	}

	for _, part := range filterParts {
		components := strings.SplitN(part, ":", 3)

		var column, operator, value string

		if len(components) == 3 {
			// Standard format: column:operator:value
			column = strings.TrimSpace(components[0])
			operator = strings.TrimSpace(components[1])
			value = components[2]

			if !validOperators[operator] {
				return nil, fmt.Errorf("invalid operator: %s", operator)
			}
		} else if len(components) == 2 {
			// Simplified format: column:value (with optional operator prefix)
			column = strings.TrimSpace(components[0])
			operator, value = parseSimplifiedOperator(components[1])
		} else {
			return nil, fmt.Errorf("invalid filter format: %s (expected column:operator:value or column:value)", part)
		}

		// Parse value based on operator
		var parsedValue interface{}
		if operator == "in" {
			// For IN operator, split by pipe
			parsedValue = strings.Split(value, "|")
		} else {
			parsedValue = value
		}

		filters = append(filters, database.Filter{
			Column:   column,
			Operator: operator,
			Value:    parsedValue,
		})
	}

	return filters, nil
}

// ParseSorts parses sort parameters from the request.
// Format: sort=column:direction,column2:direction2
// Example: sort=created_at:desc,name:asc
func ParseSorts(r *http.Request) ([]database.Sort, error) {
	sortStr := r.URL.Query().Get("sort")
	if sortStr == "" {
		return nil, nil
	}

	sortParts := strings.Split(sortStr, ",")
	sorts := make([]database.Sort, 0, len(sortParts))

	for _, part := range sortParts {
		components := strings.SplitN(part, ":", 2)
		column := strings.TrimSpace(components[0])
		direction := "asc"

		if len(components) == 2 {
			dir := strings.ToLower(strings.TrimSpace(components[1]))
			if dir == "desc" || dir == "asc" {
				direction = dir
			} else {
				return nil, fmt.Errorf("invalid sort direction: %s (must be 'asc' or 'desc')", components[1])
			}
		}

		sorts = append(sorts, database.Sort{
			Column:    column,
			Direction: direction,
		})
	}

	return sorts, nil
}

// ParseWhereClause parses WHERE clause from query parameters.
// Supports two formats:
//   - Standard: where=column:operator:value (e.g., id:eq:123)
//   - Simplified: where=column:value (e.g., id:>100, status:!deleted)
//
// Example: where=id:eq:123,status:ne:deleted OR where=id:>100,status:!deleted
// Supports all the same operators as filter: eq, ne, gt, gte, lt, lte, like, in
func ParseWhereClause(r *http.Request) ([]database.Filter, error) {
	whereStr := r.URL.Query().Get("where")
	if whereStr == "" {
		return nil, nil
	}

	whereParts := strings.Split(whereStr, ",")
	filters := make([]database.Filter, 0, len(whereParts))

	// Valid operators for standard format
	validOperators := map[string]bool{
		"eq": true, "ne": true, "gt": true, "gte": true,
		"lt": true, "lte": true, "like": true, "in": true,
	}

	for _, part := range whereParts {
		components := strings.SplitN(part, ":", 3)

		var column, operator, value string

		if len(components) == 3 {
			// Standard format: column:operator:value
			column = strings.TrimSpace(components[0])
			operator = strings.TrimSpace(components[1])
			value = components[2]

			if !validOperators[operator] {
				return nil, fmt.Errorf("invalid operator in where clause: %s (supported: eq, ne, gt, gte, lt, lte, like, in)", operator)
			}
		} else if len(components) == 2 {
			// Simplified format: column:value (with optional operator prefix)
			column = strings.TrimSpace(components[0])
			operator, value = parseSimplifiedOperator(components[1])
		} else {
			return nil, fmt.Errorf("invalid where format: %s (expected column:operator:value or column:value)", part)
		}

		// Parse value based on operator
		var parsedValue interface{}
		if operator == "in" {
			// For IN operator, split by pipe
			parsedValue = strings.Split(value, "|")
		} else {
			parsedValue = value
		}

		filters = append(filters, database.Filter{
			Column:   column,
			Operator: operator,
			Value:    parsedValue,
		})
	}

	return filters, nil
}

// ParseDryRun checks if dry_run parameter is set to true.
// When true, DELETE operations return affected row count without actually deleting.
func ParseDryRun(r *http.Request) bool {
	dryRun := r.URL.Query().Get("dry_run")
	return dryRun == "true" || dryRun == "1"
}

// ParseGroupBy parses the group_by parameter from the request.
// Returns the column name to group by, or empty string if not specified.
func ParseGroupBy(r *http.Request) (string, error) {
	groupBy := r.URL.Query().Get("group_by")
	if groupBy == "" {
		return "", nil
	}

	// Trim whitespace
	groupBy = strings.TrimSpace(groupBy)

	// Validate column name
	if err := SanitizeColumnName(groupBy); err != nil {
		return "", fmt.Errorf("invalid group_by column: %s", err.Error())
	}

	return groupBy, nil
}

// ParseSelectColumns parses the select parameter to get requested columns.
// Format: select=col1,col2,col3
// Returns nil if no select parameter is provided (meaning SELECT *).
func ParseSelectColumns(r *http.Request) ([]string, error) {
	selectStr := r.URL.Query().Get("select")
	if selectStr == "" {
		return nil, nil
	}

	parts := strings.Split(selectStr, ",")
	columns := make([]string, 0, len(parts))

	for _, part := range parts {
		col := strings.TrimSpace(part)
		if col == "" {
			continue
		}

		// Validate column name
		if err := SanitizeColumnName(col); err != nil {
			return nil, fmt.Errorf("invalid column in select: %s", err.Error())
		}

		columns = append(columns, col)
	}

	if len(columns) == 0 {
		return nil, fmt.Errorf("select parameter cannot be empty")
	}

	return columns, nil
}

// ParseLinks checks if links parameter is set to true.
// When true, HATEOAS navigation links are included in paginated responses.
func ParseLinks(r *http.Request) bool {
	links := r.URL.Query().Get("links")
	return links == "true" || links == "1"
}

// GetAcceptFormat returns the preferred response format based on query parameter,
// X-ClickHouse-Format header, or Accept header (in that order of precedence).
// Supported formats:
//   - json, JSONEachRow: array of objects (default)
//   - compact, JSONCompact: array of arrays with meta (httpserver compatible)
//   - meta: array of objects with meta and statistics
//   - csv, parquet, arrow: binary formats
func GetAcceptFormat(r *http.Request) string {
	// Check query parameter first (highest precedence, httpserver compatible)
	if format := r.URL.Query().Get("default_format"); format != "" {
		return format
	}

	// Check X-ClickHouse-Format header (httpserver compatible)
	if format := r.Header.Get("X-ClickHouse-Format"); format != "" {
		return format
	}

	// Check Accept header
	accept := r.Header.Get("Accept")

	if strings.Contains(accept, "text/csv") {
		return "csv"
	}
	if strings.Contains(accept, "application/parquet") {
		return "parquet"
	}
	if strings.Contains(accept, "application/vnd.apache.arrow") {
		return "arrow"
	}

	// Default to JSON objects format
	return "json"
}

// SanitizeTableName validates and sanitizes table names to prevent SQL injection.
func SanitizeTableName(tableName string) error {
	if tableName == "" {
		return fmt.Errorf("table name cannot be empty")
	}

	// Check for valid characters (alphanumeric and underscore only)
	for _, c := range tableName {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_') {
			return fmt.Errorf("invalid table name: must contain only alphanumeric characters and underscores")
		}
	}

	return nil
}

// SanitizeColumnName validates and sanitizes column names to prevent SQL injection.
func SanitizeColumnName(columnName string) error {
	if columnName == "" {
		return fmt.Errorf("column name cannot be empty")
	}

	// Check for valid characters (alphanumeric and underscore only)
	for _, c := range columnName {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_') {
			return fmt.Errorf("invalid column name: must contain only alphanumeric characters and underscores")
		}
	}

	return nil
}

// ParseGETQueryPath parses GET request path to extract SQL query and format.
// Expected pattern: /duckdb/query/{urlEncodedSQL}/result.{format}
// Returns: sql string, format string, error
func ParseGETQueryPath(path string) (string, string, error) {
	// Check if path matches the GET query pattern
	// Pattern: /duckdb/query/.../result.{ext}
	if !strings.HasPrefix(path, "/duckdb/query/") {
		return "", "", fmt.Errorf("invalid path: must start with /duckdb/query/")
	}

	// Find "/result." in the path
	resultIndex := strings.LastIndex(path, "/result.")
	if resultIndex == -1 {
		return "", "", fmt.Errorf("invalid path: must contain /result.{format}")
	}

	// Extract the URL-encoded SQL (between "/duckdb/query/" and "/result.")
	sqlStart := len("/duckdb/query/")
	if resultIndex <= sqlStart {
		return "", "", fmt.Errorf("invalid path: missing SQL query")
	}

	encodedSQL := path[sqlStart:resultIndex]
	if encodedSQL == "" {
		return "", "", fmt.Errorf("invalid path: SQL query cannot be empty")
	}

	// Decode the URL-encoded SQL
	decodedSQL, err := url.QueryUnescape(encodedSQL)
	if err != nil {
		return "", "", fmt.Errorf("failed to decode SQL query: %w", err)
	}

	// Extract the format extension
	formatStart := resultIndex + len("/result.")
	if formatStart >= len(path) {
		return "", "", fmt.Errorf("invalid path: missing format extension")
	}

	format := path[formatStart:]
	if format == "" {
		return "", "", fmt.Errorf("invalid path: format extension cannot be empty")
	}

	// Validate format
	validFormats := map[string]bool{
		"json":        true,
		"compact":     true,
		"meta":        true,
		"csv":         true,
		"arrow":       true,
		"parquet":     true,
		"JSONCompact": true,
		"JSONEachRow": true,
	}

	if !validFormats[format] {
		return "", "", fmt.Errorf("invalid format: %s (must be json, compact, meta, csv, arrow, or parquet)", format)
	}

	return decodedSQL, format, nil
}
