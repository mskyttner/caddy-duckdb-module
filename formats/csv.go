package formats

import (
	"database/sql"
	"encoding/csv"
	"fmt"
	"net/http"
)

const (
	// csvFlushInterval is the number of rows after which to flush the CSV writer
	// and HTTP response for streaming. This enables true streaming for large datasets.
	csvFlushInterval = 1000
)

// WriteCSV writes query results as CSV with streaming support.
// It periodically flushes data to enable true HTTP streaming for large datasets,
// which is important for clients like DuckDB that can consume CSV streams.
func WriteCSV(w http.ResponseWriter, rows *sql.Rows) error {
	return WriteCSVWithOptions(w, rows, false)
}

// WriteCSVDownload writes query results as CSV with Content-Disposition header
// for browser downloads.
func WriteCSVDownload(w http.ResponseWriter, rows *sql.Rows, filename string) error {
	if filename == "" {
		filename = "export.csv"
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	return WriteCSVWithOptions(w, rows, true)
}

// WriteCSVWithOptions writes query results as CSV with configurable options.
// If headersAlreadySet is true, it skips setting Content-Disposition.
func WriteCSVWithOptions(w http.ResponseWriter, rows *sql.Rows, headersAlreadySet bool) error {
	// Get column names
	columns, err := rows.Columns()
	if err != nil {
		return fmt.Errorf("failed to get columns: %w", err)
	}

	// Set CSV headers
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	// Note: No Content-Length header to enable chunked transfer encoding
	w.WriteHeader(http.StatusOK)

	// Get HTTP flusher for streaming support
	flusher, canFlush := w.(http.Flusher)

	// Create CSV writer
	csvWriter := csv.NewWriter(w)

	// Write header row
	if err := csvWriter.Write(columns); err != nil {
		return fmt.Errorf("failed to write CSV header: %w", err)
	}

	// Prepare value holders (reuse across rows for efficiency)
	values := make([]interface{}, len(columns))
	valuePtrs := make([]interface{}, len(columns))
	for i := range columns {
		valuePtrs[i] = &values[i]
	}
	record := make([]string, len(columns))

	// Scan and write rows with periodic flushing for streaming
	rowCount := 0
	for rows.Next() {
		if err := rows.Scan(valuePtrs...); err != nil {
			return fmt.Errorf("failed to scan row: %w", err)
		}

		// Convert to strings for CSV
		for i, val := range values {
			record[i] = formatCSVValue(val)
		}

		if err := csvWriter.Write(record); err != nil {
			return fmt.Errorf("failed to write CSV row: %w", err)
		}

		rowCount++

		// Periodically flush for streaming
		if rowCount%csvFlushInterval == 0 {
			csvWriter.Flush()
			if err := csvWriter.Error(); err != nil {
				return fmt.Errorf("failed to flush CSV: %w", err)
			}
			if canFlush {
				flusher.Flush()
			}
		}
	}

	// Final flush
	csvWriter.Flush()
	if err := csvWriter.Error(); err != nil {
		return fmt.Errorf("failed to flush CSV: %w", err)
	}
	if canFlush {
		flusher.Flush()
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("error iterating rows: %w", err)
	}

	return nil
}

// formatCSVValue converts a database value to a string for CSV output.
func formatCSVValue(val interface{}) string {
	if val == nil {
		return ""
	}

	switch v := val.(type) {
	case []byte:
		return string(v)
	case string:
		return v
	case int, int8, int16, int32, int64:
		return fmt.Sprintf("%d", v)
	case uint, uint8, uint16, uint32, uint64:
		return fmt.Sprintf("%d", v)
	case float32, float64:
		return fmt.Sprintf("%f", v)
	case bool:
		if v {
			return "true"
		}
		return "false"
	default:
		return fmt.Sprintf("%v", v)
	}
}
