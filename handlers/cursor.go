package handlers

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
)

// Cursor represents the state for cursor-based pagination.
// It encodes the position in the result set for efficient keyset pagination.
type Cursor struct {
	// SortColumns contains the column names used for sorting
	SortColumns []string `json:"s"`
	// SortValues contains the values of the sort columns from the last row
	SortValues []interface{} `json:"v"`
	// SortDirections contains the sort direction for each column ("asc" or "desc")
	SortDirections []string `json:"d"`
	// Offset is used as a tie-breaker when multiple rows have the same sort values
	Offset int `json:"o"`
}

// EncodeCursor encodes a cursor to a URL-safe base64 string.
func EncodeCursor(cursor *Cursor) (string, error) {
	if cursor == nil {
		return "", nil
	}

	data, err := json.Marshal(cursor)
	if err != nil {
		return "", fmt.Errorf("failed to marshal cursor: %w", err)
	}

	return base64.URLEncoding.EncodeToString(data), nil
}

// DecodeCursor decodes a base64 string to a Cursor.
// Returns nil if the input is empty or "*" (initial cursor).
func DecodeCursor(encoded string) (*Cursor, error) {
	if encoded == "" || encoded == "*" {
		return nil, nil
	}

	data, err := base64.URLEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("invalid cursor encoding: %w", err)
	}

	var cursor Cursor
	if err := json.Unmarshal(data, &cursor); err != nil {
		return nil, fmt.Errorf("invalid cursor format: %w", err)
	}

	return &cursor, nil
}

// ParseCursorPagination parses the cursor parameter from the request.
// Returns:
//   - cursor: the encoded cursor string
//   - isCursor: true if cursor pagination is being used
//   - isInitial: true if this is the initial cursor request (cursor=*)
func ParseCursorPagination(r *http.Request) (cursor string, isCursor bool, isInitial bool) {
	cursor = r.URL.Query().Get("cursor")
	if cursor == "" {
		return "", false, false
	}

	if cursor == "*" {
		return "*", true, true
	}

	return cursor, true, false
}

// BuildNextCursor creates a cursor from the last row of results.
// sortColumns: the columns used for sorting
// sortDirections: the direction for each sort column
// lastRowValues: the values from the last row for the sort columns
// offset: tie-breaker offset (incremented when same values repeat)
func BuildNextCursor(sortColumns []string, sortDirections []string, lastRowValues []interface{}, offset int) (*Cursor, error) {
	if len(sortColumns) == 0 {
		return nil, fmt.Errorf("sort columns required for cursor pagination")
	}

	if len(sortColumns) != len(lastRowValues) {
		return nil, fmt.Errorf("mismatch between sort columns and values")
	}

	// Ensure directions array matches columns
	dirs := make([]string, len(sortColumns))
	for i := range sortColumns {
		if i < len(sortDirections) {
			dirs[i] = sortDirections[i]
		} else {
			dirs[i] = "asc"
		}
	}

	return &Cursor{
		SortColumns:    sortColumns,
		SortValues:     lastRowValues,
		SortDirections: dirs,
		Offset:         offset,
	}, nil
}
