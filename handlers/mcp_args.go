package handlers

import (
	"encoding/json"

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
