package formats

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// LinksConfig contains configuration for generating HATEOAS links.
type LinksConfig struct {
	Enabled  bool       // Whether to include _links in response
	BasePath string     // Base path for generating links (e.g., "/duckdb/api/users")
	Query    url.Values // Original query parameters to preserve
}

// JSONFormat specifies the JSON output format.
type JSONFormat string

const (
	// JSONFormatObjects outputs data as array of objects (default, backwards compatible)
	JSONFormatObjects JSONFormat = "objects"
	// JSONFormatCompact outputs data as array of arrays with meta (httpserver compatible)
	JSONFormatCompact JSONFormat = "compact"
)

// ColumnMeta represents metadata about a column.
type ColumnMeta struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// QueryStats represents query execution statistics.
type QueryStats struct {
	ElapsedSec float64 `json:"elapsed"`
	RowsRead   int64   `json:"rows_read"`
	BytesRead  int64   `json:"bytes_read"`
}

// JSONWriteOptions contains options for JSON output.
type JSONWriteOptions struct {
	Format        JSONFormat
	ExecutionTime time.Duration
	IncludeMeta   bool
}

// WriteJSON writes query results as JSON with pagination.
func WriteJSON(w http.ResponseWriter, rows *sql.Rows, page, limit int, totalRows int64, paginationRequested bool, safetyLimit int, linksConfig *LinksConfig) error {
	// Get column names
	columns, err := rows.Columns()
	if err != nil {
		return fmt.Errorf("failed to get columns: %w", err)
	}

	// Prepare data structure
	data := make([]map[string]interface{}, 0)
	rowCount := 0

	// Scan rows
	for rows.Next() {
		rowCount++

		// Create a slice of interface{} to hold each column
		values := make([]interface{}, len(columns))
		valuePtrs := make([]interface{}, len(columns))
		for i := range columns {
			valuePtrs[i] = &values[i]
		}

		if err := rows.Scan(valuePtrs...); err != nil {
			return fmt.Errorf("failed to scan row: %w", err)
		}

		// Create a map for this row
		rowMap := make(map[string]interface{})
		for i, col := range columns {
			val := values[i]

			// Handle NULL values and byte arrays
			switch v := val.(type) {
			case nil:
				rowMap[col] = nil
			case []byte:
				rowMap[col] = string(v)
			default:
				rowMap[col] = v
			}
		}

		data = append(data, rowMap)
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("error iterating rows: %w", err)
	}

	// Build response
	response := map[string]interface{}{
		"data": data,
	}

	// Add pagination metadata if requested
	if paginationRequested && limit > 0 {
		totalPages := 0
		if totalRows > 0 {
			totalPages = int((totalRows + int64(limit) - 1) / int64(limit))
		}

		response["pagination"] = map[string]interface{}{
			"page":        page,
			"limit":       limit,
			"total_rows":  totalRows,
			"total_pages": totalPages,
		}

		// Add HATEOAS links if enabled
		if linksConfig != nil && linksConfig.Enabled {
			links := generateHATEOASLinks(linksConfig.BasePath, linksConfig.Query, page, limit, totalPages)
			response["_links"] = links
		}
	} else if !paginationRequested {
		// No pagination requested - check if results were truncated by safety limit
		truncated := false
		if safetyLimit > 0 && int64(rowCount) >= int64(safetyLimit) && int64(rowCount) < totalRows {
			truncated = true
		}

		if truncated {
			response["truncated"] = true
			response["message"] = fmt.Sprintf("Results limited to %d rows by safety limit. Use pagination (?limit=X&page=Y) to access more data.", safetyLimit)
			response["total_available"] = totalRows
		}
	}

	// Write response
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	return json.NewEncoder(w).Encode(response)
}

// WriteJSONCompact writes query results in JSONCompact format (httpserver compatible).
// This format includes column metadata, data as array of arrays, row count, and statistics.
func WriteJSONCompact(w http.ResponseWriter, rows *sql.Rows, executionTime time.Duration) error {
	// Get column names and types
	columns, err := rows.Columns()
	if err != nil {
		return fmt.Errorf("failed to get columns: %w", err)
	}

	columnTypes, err := rows.ColumnTypes()
	if err != nil {
		return fmt.Errorf("failed to get column types: %w", err)
	}

	// Build meta array with column names and types
	meta := make([]ColumnMeta, len(columns))
	for i, ct := range columnTypes {
		meta[i] = ColumnMeta{
			Name: ct.Name(),
			Type: ct.DatabaseTypeName(),
		}
	}

	// Prepare data structure as array of arrays
	data := make([][]interface{}, 0)
	rowCount := 0

	// Scan rows
	for rows.Next() {
		rowCount++

		// Create a slice of interface{} to hold each column
		values := make([]interface{}, len(columns))
		valuePtrs := make([]interface{}, len(columns))
		for i := range columns {
			valuePtrs[i] = &values[i]
		}

		if err := rows.Scan(valuePtrs...); err != nil {
			return fmt.Errorf("failed to scan row: %w", err)
		}

		// Create array for this row
		rowArray := make([]interface{}, len(columns))
		for i := range columns {
			val := values[i]

			// Handle NULL values and byte arrays
			switch v := val.(type) {
			case nil:
				rowArray[i] = nil
			case []byte:
				rowArray[i] = string(v)
			default:
				rowArray[i] = v
			}
		}

		data = append(data, rowArray)
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("error iterating rows: %w", err)
	}

	// Build response in httpserver JSONCompact format
	response := map[string]interface{}{
		"meta": meta,
		"data": data,
		"rows": rowCount,
		"statistics": QueryStats{
			ElapsedSec: executionTime.Seconds(),
			RowsRead:   int64(rowCount),
			BytesRead:  0, // Not tracked currently
		},
	}

	// Write response
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	return json.NewEncoder(w).Encode(response)
}

// WriteJSONWithMeta writes query results as JSON objects with column metadata.
// This is a hybrid format that keeps the object-based data but adds meta information.
func WriteJSONWithMeta(w http.ResponseWriter, rows *sql.Rows, executionTime time.Duration) error {
	// Get column names and types
	columns, err := rows.Columns()
	if err != nil {
		return fmt.Errorf("failed to get columns: %w", err)
	}

	columnTypes, err := rows.ColumnTypes()
	if err != nil {
		return fmt.Errorf("failed to get column types: %w", err)
	}

	// Build meta array with column names and types
	meta := make([]ColumnMeta, len(columns))
	for i, ct := range columnTypes {
		meta[i] = ColumnMeta{
			Name: ct.Name(),
			Type: ct.DatabaseTypeName(),
		}
	}

	// Prepare data structure as array of objects
	data := make([]map[string]interface{}, 0)
	rowCount := 0

	// Scan rows
	for rows.Next() {
		rowCount++

		// Create a slice of interface{} to hold each column
		values := make([]interface{}, len(columns))
		valuePtrs := make([]interface{}, len(columns))
		for i := range columns {
			valuePtrs[i] = &values[i]
		}

		if err := rows.Scan(valuePtrs...); err != nil {
			return fmt.Errorf("failed to scan row: %w", err)
		}

		// Create a map for this row
		rowMap := make(map[string]interface{})
		for i, col := range columns {
			val := values[i]

			// Handle NULL values and byte arrays
			switch v := val.(type) {
			case nil:
				rowMap[col] = nil
			case []byte:
				rowMap[col] = string(v)
			default:
				rowMap[col] = v
			}
		}

		data = append(data, rowMap)
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("error iterating rows: %w", err)
	}

	// Build response with meta and statistics
	response := map[string]interface{}{
		"meta": meta,
		"data": data,
		"rows": rowCount,
		"statistics": QueryStats{
			ElapsedSec: executionTime.Seconds(),
			RowsRead:   int64(rowCount),
			BytesRead:  0, // Not tracked currently
		},
	}

	// Write response
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	return json.NewEncoder(w).Encode(response)
}

// generateHATEOASLinks generates navigation links for paginated responses.
func generateHATEOASLinks(basePath string, query url.Values, page, limit, totalPages int) map[string]string {
	links := make(map[string]string)

	// Helper to build URL with page parameter
	buildURL := func(targetPage int) string {
		q := make(url.Values)
		// Copy existing query params except page
		for key, values := range query {
			if key != "page" && key != "links" {
				for _, v := range values {
					q.Add(key, v)
				}
			}
		}
		q.Set("page", fmt.Sprintf("%d", targetPage))
		q.Set("links", "true")
		return fmt.Sprintf("%s?%s", basePath, q.Encode())
	}

	// Self link (current page)
	links["self"] = buildURL(page)

	// First page link
	links["first"] = buildURL(1)

	// Last page link
	if totalPages > 0 {
		links["last"] = buildURL(totalPages)
	}

	// Previous page link (if not on first page)
	if page > 1 {
		links["prev"] = buildURL(page - 1)
	}

	// Next page link (if not on last page)
	if page < totalPages {
		links["next"] = buildURL(page + 1)
	}

	return links
}
