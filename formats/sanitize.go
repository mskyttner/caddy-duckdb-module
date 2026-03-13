package formats

import (
	"fmt"

	duckdb "github.com/duckdb/duckdb-go/v2"
	"github.com/google/uuid"
)

// SanitizeValue converts DuckDB-specific Go types to JSON-serializable equivalents.
//
// The DuckDB Go driver returns several types that encoding/json cannot handle:
//   - duckdb.Map  (map[any]any)  — JSON requires string keys; we convert recursively.
//   - duckdb.UUID ([16]byte)     — JSON would base64-encode it; we emit the UUID string.
//   - duckdb.Decimal             — already handled at call sites, included here for completeness.
//   - []any slices               — elements may themselves contain Map/UUID; recurse.
//   - []byte                     — driver sometimes returns raw bytes for text; convert to string.
func SanitizeValue(v any) any {
	switch val := v.(type) {
	case nil:
		return nil
	case []byte:
		return string(val)
	case duckdb.UUID:
		return uuid.UUID(val).String()
	case duckdb.Decimal:
		return val.Float64()
	case duckdb.Map:
		return sanitizeMap(val)
	case map[any]any:
		// guard: same underlying type reached without the duckdb.Map alias
		return sanitizeMap(duckdb.Map(val))
	case []any:
		out := make([]any, len(val))
		for i, elem := range val {
			out[i] = SanitizeValue(elem)
		}
		return out
	default:
		return v
	}
}

// sanitizeMap converts a duckdb.Map (map[any]any) to map[string]any, recursing into values.
func sanitizeMap(m duckdb.Map) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[fmt.Sprintf("%v", k)] = SanitizeValue(v)
	}
	return out
}
