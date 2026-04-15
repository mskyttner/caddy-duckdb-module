package handlers

import (
	"encoding/json"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// argString extracts a named string argument from a CallToolRequest.
// Returns def if the key is absent or not a string.
func argString(req *mcp.CallToolRequest, name, def string) string {
	m := argMap(req)
	if v, ok := m[name]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return def
}

// argInt extracts a named integer argument from a CallToolRequest.
// JSON numbers unmarshal as float64; this rounds to int.
// Returns def if the key is absent or not a number.
func argInt(req *mcp.CallToolRequest, name string, def int) int {
	m := argMap(req)
	if v, ok := m[name]; ok {
		if f, ok := v.(float64); ok {
			return int(f)
		}
	}
	return def
}

// argBool extracts a named boolean argument from a CallToolRequest.
// Returns def if the key is absent or not a bool.
func argBool(req *mcp.CallToolRequest, name string, def bool) bool {
	m := argMap(req)
	if v, ok := m[name]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return def
}

// quoteLiteral escapes a string for use as a SQL string literal (single-quoted).
// Embedded single quotes are doubled per SQL standard.
func quoteLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// isAlias reports whether s is a valid SQL identifier usable as a macro alias:
// starts with a letter or underscore, followed by letters, digits, or underscores.
// Dots and spaces are not allowed (unlike isSafeIdentifier which permits dots).
func isAlias(s string) bool {
	if len(s) == 0 || len(s) > 64 {
		return false
	}
	for i, c := range s {
		if i == 0 {
			if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_') {
				return false
			}
		} else {
			if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_') {
				return false
			}
		}
	}
	return true
}

// argMap unmarshals all tool arguments into a map[string]any.
// Returns an empty map on missing or invalid arguments.
func argMap(req *mcp.CallToolRequest) map[string]any {
	if req.Params == nil || len(req.Params.Arguments) == 0 {
		return map[string]any{}
	}
	var m map[string]any
	if err := json.Unmarshal(req.Params.Arguments, &m); err != nil {
		return map[string]any{}
	}
	return m
}

// textResult wraps a plain string in a CallToolResult.
func textResult(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: s}},
	}
}

// toInt64 converts a value returned by a DuckDB scan (int64, float64, or
// a formats.SanitizeValue-produced int64) to int64. Returns 0 for nil or
// unrecognised types.
func toInt64(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case float64:
		return int64(n)
	case int32:
		return int64(n)
	case int:
		return int64(n)
	}
	return 0
}

// apiKeyFromRequest extracts the X-API-Key header from an MCP request.
// Returns an empty string if Extra is nil or the header is absent.
func apiKeyFromRequest(req *mcp.CallToolRequest) string {
	if req.Extra == nil {
		return ""
	}
	return req.Extra.Header.Get("X-API-Key")
}
