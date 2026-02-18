package handlers

import (
	"encoding/json"
	"net/http"
)

// OpenAPIHandler serves the OpenAPI specification.
type OpenAPIHandler struct{}

// NewOpenAPIHandler creates a new OpenAPI handler.
func NewOpenAPIHandler() *OpenAPIHandler {
	return &OpenAPIHandler{}
}

// ServeHTTP handles HTTP requests for the OpenAPI specification.
func (h *OpenAPIHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusMethodNotAllowed)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error":   "Method Not Allowed",
			"message": "Only GET method is allowed for OpenAPI specification",
			"code":    405,
		})
		return
	}

	spec := h.generateOpenAPISpec()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(spec)
}

// generateOpenAPISpec generates the OpenAPI 3.0 specification.
func (h *OpenAPIHandler) generateOpenAPISpec() map[string]interface{} {
	return map[string]interface{}{
		"openapi": "3.0.3",
		"info": map[string]interface{}{
			"title":       "Caddy DuckDB REST API",
			"description": "A REST API for DuckDB database operations with built-in authentication and authorization.",
			"version":     "1.0.0",
			"contact": map[string]interface{}{
				"name": "GitHub Repository",
				"url":  "https://github.com/tobilg/caddy-duckdb-module",
			},
			"license": map[string]interface{}{
				"name": "MIT",
				"url":  "https://opensource.org/licenses/MIT",
			},
		},
		"servers": []map[string]interface{}{
			{
				"url":         "/duckdb",
				"description": "DuckDB API base path",
			},
		},
		"tags": []map[string]interface{}{
			{
				"name":        "CRUD",
				"description": "CRUD operations on database tables",
			},
			{
				"name":        "Query",
				"description": "Raw SQL query execution",
			},
			{
				"name":        "Macro",
				"description": "Execute table macros (api_* prefixed)",
			},
			{
				"name":        "View",
				"description": "Query views (api_* prefixed)",
			},
			{
				"name":        "Schema",
				"description": "Table and view column schema information",
			},
			{
				"name":        "OpenAPI",
				"description": "API documentation",
			},
		},
		"paths":      h.generatePaths(),
		"components": h.generateComponents(),
	}
}

// generatePaths generates the paths section of the OpenAPI spec.
func (h *OpenAPIHandler) generatePaths() map[string]interface{} {
	return map[string]interface{}{
		"/openapi.json": map[string]interface{}{
			"get": map[string]interface{}{
				"tags":        []string{"OpenAPI"},
				"summary":     "Get OpenAPI specification",
				"description": "Returns the OpenAPI 3.0 specification for this API",
				"operationId": "getOpenAPISpec",
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "OpenAPI specification",
						"content": map[string]interface{}{
							"application/json": map[string]interface{}{
								"schema": map[string]interface{}{
									"type": "object",
								},
							},
						},
					},
				},
			},
		},
		"/api": map[string]interface{}{
			"get": h.generateTableListOperation(),
		},
		"/api/{table}": map[string]interface{}{
			"get":    h.generateReadOperation(),
			"post":   h.generateCreateOperation(),
			"put":    h.generateUpdateOperation(),
			"delete": h.generateDeleteOperation(),
			"parameters": []map[string]interface{}{
				{
					"name":        "table",
					"in":          "path",
					"required":    true,
					"description": "Name of the database table",
					"schema": map[string]interface{}{
						"type": "string",
					},
				},
			},
		},
		"/query": map[string]interface{}{
			"post": h.generateQueryPostOperation(),
		},
		"/query/{sql}/result.{format}": map[string]interface{}{
			"get": h.generateQueryGetOperation(),
		},
		"/macro": map[string]interface{}{
			"get": h.generateMacroListOperation(),
		},
		"/macro/{name}": map[string]interface{}{
			"get": h.generateMacroExecuteOperation(),
		},
		"/view": map[string]interface{}{
			"get": h.generateViewListOperation(),
		},
		"/view/{name}": map[string]interface{}{
			"get": h.generateViewQueryOperation(),
		},
		"/api/{table}/columns": map[string]interface{}{
			"get": h.generateTableColumnsOperation(),
		},
		"/view/{name}/columns": map[string]interface{}{
			"get": h.generateViewColumnsOperation(),
		},
	}
}

// generateTableListOperation generates the GET /api operation spec.
func (h *OpenAPIHandler) generateTableListOperation() map[string]interface{} {
	return map[string]interface{}{
		"tags":        []string{"CRUD"},
		"summary":     "List available tables",
		"description": "Returns a list of all available database tables. Internal auth tables are excluded.",
		"operationId": "listTables",
		"security": []map[string]interface{}{
			{"ApiKeyAuth": []string{}},
		},
		"responses": map[string]interface{}{
			"200": map[string]interface{}{
				"description": "List of available tables",
				"content": map[string]interface{}{
					"application/json": map[string]interface{}{
						"schema": map[string]interface{}{
							"$ref": "#/components/schemas/TableListResponse",
						},
					},
				},
			},
			"401": map[string]interface{}{
				"description": "Unauthorized",
				"content": map[string]interface{}{
					"application/json": map[string]interface{}{
						"schema": map[string]interface{}{
							"$ref": "#/components/schemas/ErrorResponse",
						},
					},
				},
			},
		},
	}
}

// generateReadOperation generates the GET operation spec.
func (h *OpenAPIHandler) generateReadOperation() map[string]interface{} {
	return map[string]interface{}{
		"tags":        []string{"CRUD"},
		"summary":     "Read records from a table",
		"description": "Retrieves records from the specified table with optional filtering, sorting, and pagination",
		"operationId": "readRecords",
		"security": []map[string]interface{}{
			{"ApiKeyAuth": []string{}},
		},
		"parameters": []map[string]interface{}{
			{
				"name":        "page",
				"in":          "query",
				"description": "Page number for pagination (starts at 1)",
				"schema": map[string]interface{}{
					"type":    "integer",
					"minimum": 1,
					"default": 1,
				},
			},
			{
				"name":        "limit",
				"in":          "query",
				"description": "Number of records per page",
				"schema": map[string]interface{}{
					"type":    "integer",
					"minimum": 1,
					"maximum": 10000,
					"default": 100,
				},
			},
			{
				"name":        "filter",
				"in":          "query",
				"description": "Filter conditions. Standard format: column:operator:value. Simplified format: column:>value, column:<value, column:!value. Operators: eq, ne, gt (>), gte (>=), lt (<), lte (<=), like, in. Comma-separated for multiple.",
				"schema": map[string]interface{}{
					"type": "string",
				},
				"example": "age:>18,status:active",
			},
			{
				"name":        "sort",
				"in":          "query",
				"description": "Sort order in format: column:direction (comma-separated for multiple). Direction: asc or desc",
				"schema": map[string]interface{}{
					"type": "string",
				},
				"example": "created_at:desc,name:asc",
			},
			{
				"name":        "links",
				"in":          "query",
				"description": "Include HATEOAS navigation links in response",
				"schema": map[string]interface{}{
					"type":    "boolean",
					"default": false,
				},
			},
			{
				"name":        "select",
				"in":          "query",
				"description": "Comma-separated list of columns to return (sparse fieldset). If omitted, all columns are returned.",
				"schema": map[string]interface{}{
					"type": "string",
				},
				"example": "id,name,email",
			},
			{
				"name":        "group_by",
				"in":          "query",
				"description": "Column to group results by. Returns aggregated counts per unique value instead of individual records.",
				"schema": map[string]interface{}{
					"type": "string",
				},
				"example": "status",
			},
			{
				"name":        "cursor",
				"in":          "query",
				"description": "Cursor for keyset pagination. Use '*' for initial request, then use next_cursor from response for subsequent pages. More efficient than offset pagination for large datasets.",
				"schema": map[string]interface{}{
					"type": "string",
				},
				"example": "*",
			},
		},
		"responses": map[string]interface{}{
			"200": map[string]interface{}{
				"description": "Successful response with records",
				"content": map[string]interface{}{
					"application/json": map[string]interface{}{
						"schema": map[string]interface{}{
							"$ref": "#/components/schemas/ReadResponse",
						},
					},
					"text/csv": map[string]interface{}{
						"schema": map[string]interface{}{
							"type": "string",
						},
					},
					"application/parquet": map[string]interface{}{
						"schema": map[string]interface{}{
							"type":   "string",
							"format": "binary",
						},
					},
					"application/vnd.apache.arrow.stream": map[string]interface{}{
						"schema": map[string]interface{}{
							"type":   "string",
							"format": "binary",
						},
					},
				},
			},
			"400": map[string]interface{}{
				"description": "Bad request",
				"content": map[string]interface{}{
					"application/json": map[string]interface{}{
						"schema": map[string]interface{}{
							"$ref": "#/components/schemas/ErrorResponse",
						},
					},
				},
			},
			"401": map[string]interface{}{
				"description": "Unauthorized",
				"content": map[string]interface{}{
					"application/json": map[string]interface{}{
						"schema": map[string]interface{}{
							"$ref": "#/components/schemas/ErrorResponse",
						},
					},
				},
			},
			"403": map[string]interface{}{
				"description": "Forbidden",
				"content": map[string]interface{}{
					"application/json": map[string]interface{}{
						"schema": map[string]interface{}{
							"$ref": "#/components/schemas/ErrorResponse",
						},
					},
				},
			},
			"404": map[string]interface{}{
				"description": "Table not found",
				"content": map[string]interface{}{
					"application/json": map[string]interface{}{
						"schema": map[string]interface{}{
							"$ref": "#/components/schemas/ErrorResponse",
						},
					},
				},
			},
		},
	}
}

// generateCreateOperation generates the POST operation spec.
func (h *OpenAPIHandler) generateCreateOperation() map[string]interface{} {
	return map[string]interface{}{
		"tags":        []string{"CRUD"},
		"summary":     "Create a new record",
		"description": "Inserts a new record into the specified table",
		"operationId": "createRecord",
		"security": []map[string]interface{}{
			{"ApiKeyAuth": []string{}},
		},
		"requestBody": map[string]interface{}{
			"required":    true,
			"description": "Record data as key-value pairs",
			"content": map[string]interface{}{
				"application/json": map[string]interface{}{
					"schema": map[string]interface{}{
						"type": "object",
						"additionalProperties": map[string]interface{}{
							"oneOf": []map[string]interface{}{
								{"type": "string"},
								{"type": "number"},
								{"type": "boolean"},
								{"type": "null"},
							},
						},
					},
					"example": map[string]interface{}{
						"name":  "John Doe",
						"email": "john@example.com",
						"age":   30,
					},
				},
			},
		},
		"responses": map[string]interface{}{
			"201": map[string]interface{}{
				"description": "Record created successfully",
				"content": map[string]interface{}{
					"application/json": map[string]interface{}{
						"schema": map[string]interface{}{
							"$ref": "#/components/schemas/SuccessResponse",
						},
					},
				},
			},
			"400": map[string]interface{}{
				"description": "Bad request",
				"content": map[string]interface{}{
					"application/json": map[string]interface{}{
						"schema": map[string]interface{}{
							"$ref": "#/components/schemas/ErrorResponse",
						},
					},
				},
			},
			"401": map[string]interface{}{
				"description": "Unauthorized",
				"content": map[string]interface{}{
					"application/json": map[string]interface{}{
						"schema": map[string]interface{}{
							"$ref": "#/components/schemas/ErrorResponse",
						},
					},
				},
			},
			"403": map[string]interface{}{
				"description": "Forbidden",
				"content": map[string]interface{}{
					"application/json": map[string]interface{}{
						"schema": map[string]interface{}{
							"$ref": "#/components/schemas/ErrorResponse",
						},
					},
				},
			},
		},
	}
}

// generateUpdateOperation generates the PUT operation spec.
func (h *OpenAPIHandler) generateUpdateOperation() map[string]interface{} {
	return map[string]interface{}{
		"tags":        []string{"CRUD"},
		"summary":     "Update records",
		"description": "Updates records matching the WHERE clause",
		"operationId": "updateRecords",
		"security": []map[string]interface{}{
			{"ApiKeyAuth": []string{}},
		},
		"requestBody": map[string]interface{}{
			"required":    true,
			"description": "Update specification with WHERE conditions and SET values",
			"content": map[string]interface{}{
				"application/json": map[string]interface{}{
					"schema": map[string]interface{}{
						"$ref": "#/components/schemas/UpdateRequest",
					},
				},
			},
		},
		"responses": map[string]interface{}{
			"200": map[string]interface{}{
				"description": "Records updated successfully",
				"content": map[string]interface{}{
					"application/json": map[string]interface{}{
						"schema": map[string]interface{}{
							"$ref": "#/components/schemas/SuccessResponse",
						},
					},
				},
			},
			"400": map[string]interface{}{
				"description": "Bad request",
				"content": map[string]interface{}{
					"application/json": map[string]interface{}{
						"schema": map[string]interface{}{
							"$ref": "#/components/schemas/ErrorResponse",
						},
					},
				},
			},
			"401": map[string]interface{}{
				"description": "Unauthorized",
				"content": map[string]interface{}{
					"application/json": map[string]interface{}{
						"schema": map[string]interface{}{
							"$ref": "#/components/schemas/ErrorResponse",
						},
					},
				},
			},
			"403": map[string]interface{}{
				"description": "Forbidden",
				"content": map[string]interface{}{
					"application/json": map[string]interface{}{
						"schema": map[string]interface{}{
							"$ref": "#/components/schemas/ErrorResponse",
						},
					},
				},
			},
		},
	}
}

// generateDeleteOperation generates the DELETE operation spec.
func (h *OpenAPIHandler) generateDeleteOperation() map[string]interface{} {
	return map[string]interface{}{
		"tags":        []string{"CRUD"},
		"summary":     "Delete records",
		"description": "Deletes records matching the WHERE clause. Use dry_run=true to preview affected rows without deleting.",
		"operationId": "deleteRecords",
		"security": []map[string]interface{}{
			{"ApiKeyAuth": []string{}},
		},
		"parameters": []map[string]interface{}{
			{
				"name":        "where",
				"in":          "query",
				"required":    true,
				"description": "WHERE conditions in format: column:operator:value (comma-separated for multiple). Operators: eq, ne, gt, gte, lt, lte, like, in",
				"schema": map[string]interface{}{
					"type": "string",
				},
				"example": "status:eq:inactive",
			},
			{
				"name":        "dry_run",
				"in":          "query",
				"description": "If true, returns affected row count without actually deleting",
				"schema": map[string]interface{}{
					"type":    "boolean",
					"default": false,
				},
			},
		},
		"responses": map[string]interface{}{
			"200": map[string]interface{}{
				"description": "Records deleted successfully (or dry run result)",
				"content": map[string]interface{}{
					"application/json": map[string]interface{}{
						"schema": map[string]interface{}{
							"oneOf": []map[string]interface{}{
								{"$ref": "#/components/schemas/SuccessResponse"},
								{"$ref": "#/components/schemas/DryRunResponse"},
							},
						},
					},
				},
			},
			"400": map[string]interface{}{
				"description": "Bad request",
				"content": map[string]interface{}{
					"application/json": map[string]interface{}{
						"schema": map[string]interface{}{
							"$ref": "#/components/schemas/ErrorResponse",
						},
					},
				},
			},
			"401": map[string]interface{}{
				"description": "Unauthorized",
				"content": map[string]interface{}{
					"application/json": map[string]interface{}{
						"schema": map[string]interface{}{
							"$ref": "#/components/schemas/ErrorResponse",
						},
					},
				},
			},
			"403": map[string]interface{}{
				"description": "Forbidden",
				"content": map[string]interface{}{
					"application/json": map[string]interface{}{
						"schema": map[string]interface{}{
							"$ref": "#/components/schemas/ErrorResponse",
						},
					},
				},
			},
		},
	}
}

// generateQueryPostOperation generates the POST /query operation spec.
func (h *OpenAPIHandler) generateQueryPostOperation() map[string]interface{} {
	return map[string]interface{}{
		"tags":        []string{"Query"},
		"summary":     "Execute SQL query",
		"description": "Executes a raw SQL query with optional parameters. Requires can_query permission.",
		"operationId": "executeQuery",
		"security": []map[string]interface{}{
			{"ApiKeyAuth": []string{}},
		},
		"requestBody": map[string]interface{}{
			"required":    true,
			"description": "SQL query and optional parameters",
			"content": map[string]interface{}{
				"application/json": map[string]interface{}{
					"schema": map[string]interface{}{
						"$ref": "#/components/schemas/QueryRequest",
					},
				},
			},
		},
		"responses": map[string]interface{}{
			"200": map[string]interface{}{
				"description": "Query executed successfully",
				"content": map[string]interface{}{
					"application/json": map[string]interface{}{
						"schema": map[string]interface{}{
							"$ref": "#/components/schemas/QueryResponse",
						},
					},
					"text/csv": map[string]interface{}{
						"schema": map[string]interface{}{
							"type": "string",
						},
					},
					"application/parquet": map[string]interface{}{
						"schema": map[string]interface{}{
							"type":   "string",
							"format": "binary",
						},
					},
					"application/vnd.apache.arrow.stream": map[string]interface{}{
						"schema": map[string]interface{}{
							"type":   "string",
							"format": "binary",
						},
					},
				},
			},
			"400": map[string]interface{}{
				"description": "Bad request",
				"content": map[string]interface{}{
					"application/json": map[string]interface{}{
						"schema": map[string]interface{}{
							"$ref": "#/components/schemas/ErrorResponse",
						},
					},
				},
			},
			"401": map[string]interface{}{
				"description": "Unauthorized",
				"content": map[string]interface{}{
					"application/json": map[string]interface{}{
						"schema": map[string]interface{}{
							"$ref": "#/components/schemas/ErrorResponse",
						},
					},
				},
			},
			"403": map[string]interface{}{
				"description": "Forbidden",
				"content": map[string]interface{}{
					"application/json": map[string]interface{}{
						"schema": map[string]interface{}{
							"$ref": "#/components/schemas/ErrorResponse",
						},
					},
				},
			},
		},
	}
}

// generateQueryGetOperation generates the GET /query/{sql}/result.{format} operation spec.
func (h *OpenAPIHandler) generateQueryGetOperation() map[string]interface{} {
	return map[string]interface{}{
		"tags":        []string{"Query"},
		"summary":     "Execute read-only SQL query via URL",
		"description": "Executes a URL-encoded read-only SQL query (SELECT, SHOW, DESCRIBE, EXPLAIN only). Useful for bookmarkable queries and data exports.",
		"operationId": "executeQueryGet",
		"security": []map[string]interface{}{
			{"ApiKeyAuth": []string{}},
		},
		"parameters": []map[string]interface{}{
			{
				"name":        "sql",
				"in":          "path",
				"required":    true,
				"description": "URL-encoded SQL query",
				"schema": map[string]interface{}{
					"type": "string",
				},
			},
			{
				"name":        "format",
				"in":          "path",
				"required":    true,
				"description": "Response format",
				"schema": map[string]interface{}{
					"type": "string",
					"enum": []string{"json", "csv", "parquet", "arrow"},
				},
			},
		},
		"responses": map[string]interface{}{
			"200": map[string]interface{}{
				"description": "Query executed successfully",
				"content": map[string]interface{}{
					"application/json": map[string]interface{}{
						"schema": map[string]interface{}{
							"$ref": "#/components/schemas/QueryResponse",
						},
					},
					"text/csv": map[string]interface{}{
						"schema": map[string]interface{}{
							"type": "string",
						},
					},
					"application/parquet": map[string]interface{}{
						"schema": map[string]interface{}{
							"type":   "string",
							"format": "binary",
						},
					},
					"application/vnd.apache.arrow.stream": map[string]interface{}{
						"schema": map[string]interface{}{
							"type":   "string",
							"format": "binary",
						},
					},
				},
			},
			"400": map[string]interface{}{
				"description": "Bad request",
				"content": map[string]interface{}{
					"application/json": map[string]interface{}{
						"schema": map[string]interface{}{
							"$ref": "#/components/schemas/ErrorResponse",
						},
					},
				},
			},
			"401": map[string]interface{}{
				"description": "Unauthorized",
				"content": map[string]interface{}{
					"application/json": map[string]interface{}{
						"schema": map[string]interface{}{
							"$ref": "#/components/schemas/ErrorResponse",
						},
					},
				},
			},
			"403": map[string]interface{}{
				"description": "Forbidden",
				"content": map[string]interface{}{
					"application/json": map[string]interface{}{
						"schema": map[string]interface{}{
							"$ref": "#/components/schemas/ErrorResponse",
						},
					},
				},
			},
			"405": map[string]interface{}{
				"description": "Method not allowed (write queries not permitted via GET)",
				"content": map[string]interface{}{
					"application/json": map[string]interface{}{
						"schema": map[string]interface{}{
							"$ref": "#/components/schemas/ErrorResponse",
						},
					},
				},
			},
		},
	}
}

// generateMacroListOperation generates the GET /macro operation spec.
func (h *OpenAPIHandler) generateMacroListOperation() map[string]interface{} {
	return map[string]interface{}{
		"tags":        []string{"Macro"},
		"summary":     "List available table macros",
		"description": "Returns a list of available table macros (api_* prefixed only). These are DuckDB table-generating functions that accept parameters.",
		"operationId": "listMacros",
		"security": []map[string]interface{}{
			{"ApiKeyAuth": []string{}},
		},
		"responses": map[string]interface{}{
			"200": map[string]interface{}{
				"description": "List of available macros",
				"content": map[string]interface{}{
					"application/json": map[string]interface{}{
						"schema": map[string]interface{}{
							"$ref": "#/components/schemas/MacroListResponse",
						},
					},
				},
			},
			"401": map[string]interface{}{
				"description": "Unauthorized",
				"content": map[string]interface{}{
					"application/json": map[string]interface{}{
						"schema": map[string]interface{}{
							"$ref": "#/components/schemas/ErrorResponse",
						},
					},
				},
			},
		},
	}
}

// generateMacroExecuteOperation generates the GET /macro/{name} operation spec.
func (h *OpenAPIHandler) generateMacroExecuteOperation() map[string]interface{} {
	return map[string]interface{}{
		"tags":    []string{"Macro"},
		"summary": "Execute a table macro",
		"description": "Executes a table macro with the provided parameters. Only api_* prefixed macros are accessible.\n\n" +
			"**Macro parameters** are passed as additional query string arguments alongside the standard pagination params.\n" +
			"Use `GET /macro` first to discover available macros and their parameter names.\n\n" +
			"Example: a macro defined as `api_find(search_query, result_limit := 10)` is called as:\n" +
			"`GET /macro/api_find?search_query=machine+learning&result_limit=5`",
		"operationId": "executeMacro",
		"security": []map[string]interface{}{
			{"ApiKeyAuth": []string{}},
		},
		"parameters": []map[string]interface{}{
			{
				"name":        "name",
				"in":          "path",
				"required":    true,
				"description": "Name of the macro to execute (must start with api_). Use GET /macro to list available macros and their parameters.",
				"schema": map[string]interface{}{
					"type": "string",
				},
				"example": "api_find",
			},
			{
				"name":        "limit",
				"in":          "query",
				"required":    false,
				"description": "Maximum number of rows to return.",
				"schema": map[string]interface{}{
					"type":    "integer",
					"default": 100,
				},
			},
			{
				"name":        "page",
				"in":          "query",
				"required":    false,
				"description": "Page number for offset pagination.",
				"schema": map[string]interface{}{
					"type":    "integer",
					"default": 1,
				},
			},
			{
				"name":        "select",
				"in":          "query",
				"required":    false,
				"description": "Comma-separated list of columns to return (sparse fieldset).",
				"schema": map[string]interface{}{
					"type": "string",
				},
				"example": "id,title,score",
			},
			{
				"name":        "links",
				"in":          "query",
				"required":    false,
				"description": "Set to true to include HATEOAS navigation links in the response.",
				"schema": map[string]interface{}{
					"type":    "boolean",
					"default": false,
				},
			},
			{
				"name": "<param>",
				"in":   "query",
				"description": "Macro-specific parameter. Any query string argument not listed above is forwarded to the macro as a named argument. " +
					"Call GET /macro first to discover parameter names for a given macro. " +
					"Example: `?search_query=climate+change&result_limit=20`",
				"required": false,
				"schema": map[string]interface{}{
					"type": "string",
				},
			},
		},
		"responses": map[string]interface{}{
			"200": map[string]interface{}{
				"description": "Macro executed successfully",
				"content": map[string]interface{}{
					"application/json": map[string]interface{}{
						"schema": map[string]interface{}{
							"$ref": "#/components/schemas/ReadResponse",
						},
					},
				},
			},
			"400": map[string]interface{}{
				"description": "Bad request (missing required parameters)",
				"content": map[string]interface{}{
					"application/json": map[string]interface{}{
						"schema": map[string]interface{}{
							"$ref": "#/components/schemas/ErrorResponse",
						},
					},
				},
			},
			"401": map[string]interface{}{
				"description": "Unauthorized",
				"content": map[string]interface{}{
					"application/json": map[string]interface{}{
						"schema": map[string]interface{}{
							"$ref": "#/components/schemas/ErrorResponse",
						},
					},
				},
			},
			"404": map[string]interface{}{
				"description": "Macro not found",
				"content": map[string]interface{}{
					"application/json": map[string]interface{}{
						"schema": map[string]interface{}{
							"$ref": "#/components/schemas/ErrorResponse",
						},
					},
				},
			},
		},
	}
}

// generateViewListOperation generates the GET /view operation spec.
func (h *OpenAPIHandler) generateViewListOperation() map[string]interface{} {
	return map[string]interface{}{
		"tags":        []string{"View"},
		"summary":     "List available views",
		"description": "Returns a list of available views (api_* prefixed only). Views are pre-defined queries that can be accessed like tables.",
		"operationId": "listViews",
		"security": []map[string]interface{}{
			{"ApiKeyAuth": []string{}},
		},
		"responses": map[string]interface{}{
			"200": map[string]interface{}{
				"description": "List of available views",
				"content": map[string]interface{}{
					"application/json": map[string]interface{}{
						"schema": map[string]interface{}{
							"$ref": "#/components/schemas/ViewListResponse",
						},
					},
				},
			},
			"401": map[string]interface{}{
				"description": "Unauthorized",
				"content": map[string]interface{}{
					"application/json": map[string]interface{}{
						"schema": map[string]interface{}{
							"$ref": "#/components/schemas/ErrorResponse",
						},
					},
				},
			},
		},
	}
}

// generateViewQueryOperation generates the GET /view/{name} operation spec.
func (h *OpenAPIHandler) generateViewQueryOperation() map[string]interface{} {
	return map[string]interface{}{
		"tags":        []string{"View"},
		"summary":     "Query a view",
		"description": "Queries a view with optional filtering, sorting, and pagination. Only api_* prefixed views are accessible.",
		"operationId": "queryView",
		"security": []map[string]interface{}{
			{"ApiKeyAuth": []string{}},
		},
		"parameters": []map[string]interface{}{
			{
				"name":        "name",
				"in":          "path",
				"required":    true,
				"description": "Name of the view to query (must start with api_)",
				"schema": map[string]interface{}{
					"type": "string",
				},
				"example": "api_active_users",
			},
			{
				"name":        "page",
				"in":          "query",
				"description": "Page number for pagination (starts at 1)",
				"schema": map[string]interface{}{
					"type":    "integer",
					"minimum": 1,
					"default": 1,
				},
			},
			{
				"name":        "limit",
				"in":          "query",
				"description": "Number of records per page",
				"schema": map[string]interface{}{
					"type":    "integer",
					"minimum": 1,
					"maximum": 10000,
					"default": 100,
				},
			},
			{
				"name":        "filter",
				"in":          "query",
				"description": "Filter conditions. Standard format: column:operator:value. Simplified format: column:>value, column:<value, column:!value.",
				"schema": map[string]interface{}{
					"type": "string",
				},
				"example": "email:like:%@gmail.com",
			},
			{
				"name":        "sort",
				"in":          "query",
				"description": "Sort order in format: column:direction",
				"schema": map[string]interface{}{
					"type": "string",
				},
				"example": "created_at:desc",
			},
		},
		"responses": map[string]interface{}{
			"200": map[string]interface{}{
				"description": "View queried successfully",
				"content": map[string]interface{}{
					"application/json": map[string]interface{}{
						"schema": map[string]interface{}{
							"$ref": "#/components/schemas/ReadResponse",
						},
					},
				},
			},
			"400": map[string]interface{}{
				"description": "Bad request",
				"content": map[string]interface{}{
					"application/json": map[string]interface{}{
						"schema": map[string]interface{}{
							"$ref": "#/components/schemas/ErrorResponse",
						},
					},
				},
			},
			"401": map[string]interface{}{
				"description": "Unauthorized",
				"content": map[string]interface{}{
					"application/json": map[string]interface{}{
						"schema": map[string]interface{}{
							"$ref": "#/components/schemas/ErrorResponse",
						},
					},
				},
			},
			"404": map[string]interface{}{
				"description": "View not found",
				"content": map[string]interface{}{
					"application/json": map[string]interface{}{
						"schema": map[string]interface{}{
							"$ref": "#/components/schemas/ErrorResponse",
						},
					},
				},
			},
		},
	}
}

// generateTableColumnsOperation generates the GET /api/{table}/columns operation spec.
func (h *OpenAPIHandler) generateTableColumnsOperation() map[string]interface{} {
	return map[string]interface{}{
		"tags":        []string{"Schema"},
		"summary":     "Get table column schema",
		"description": "Returns column names, types, and optional statistics for a table. Use format=transform for json_transform() compatibility in DuckDB queries.",
		"operationId": "getTableColumns",
		"security": []map[string]interface{}{
			{"ApiKeyAuth": []string{}},
		},
		"parameters": []map[string]interface{}{
			{
				"name":        "table",
				"in":          "path",
				"required":    true,
				"description": "Name of the database table",
				"schema": map[string]interface{}{
					"type": "string",
				},
			},
			{
				"name":        "format",
				"in":          "query",
				"description": "Response format: 'standard' (default) for LLMs/analysts, 'transform' for DuckDB json_transform(), 'summarize' for full statistics",
				"schema": map[string]interface{}{
					"type":    "string",
					"enum":    []string{"standard", "transform", "summarize"},
					"default": "standard",
				},
			},
			{
				"name":        "stats",
				"in":          "query",
				"description": "Include column statistics (min, max, approx_unique, null_percentage). Automatically true for format=summarize.",
				"schema": map[string]interface{}{
					"type":    "boolean",
					"default": false,
				},
			},
			{
				"name":        "sample",
				"in":          "query",
				"description": "Maximum rows to sample for statistics calculation",
				"schema": map[string]interface{}{
					"type":    "integer",
					"default": 10000,
				},
			},
		},
		"responses": map[string]interface{}{
			"200": map[string]interface{}{
				"description": "Column schema retrieved successfully",
				"content": map[string]interface{}{
					"application/json": map[string]interface{}{
						"schema": map[string]interface{}{
							"oneOf": []map[string]interface{}{
								{"$ref": "#/components/schemas/StandardColumnsResponse"},
								{"$ref": "#/components/schemas/TransformColumnsResponse"},
								{"$ref": "#/components/schemas/SummarizeColumnsResponse"},
							},
						},
					},
				},
			},
			"400": map[string]interface{}{
				"description": "Bad request",
				"content": map[string]interface{}{
					"application/json": map[string]interface{}{
						"schema": map[string]interface{}{
							"$ref": "#/components/schemas/ErrorResponse",
						},
					},
				},
			},
			"401": map[string]interface{}{
				"description": "Unauthorized",
				"content": map[string]interface{}{
					"application/json": map[string]interface{}{
						"schema": map[string]interface{}{
							"$ref": "#/components/schemas/ErrorResponse",
						},
					},
				},
			},
			"403": map[string]interface{}{
				"description": "Forbidden",
				"content": map[string]interface{}{
					"application/json": map[string]interface{}{
						"schema": map[string]interface{}{
							"$ref": "#/components/schemas/ErrorResponse",
						},
					},
				},
			},
			"404": map[string]interface{}{
				"description": "Table not found",
				"content": map[string]interface{}{
					"application/json": map[string]interface{}{
						"schema": map[string]interface{}{
							"$ref": "#/components/schemas/ErrorResponse",
						},
					},
				},
			},
		},
	}
}

// generateViewColumnsOperation generates the GET /view/{name}/columns operation spec.
func (h *OpenAPIHandler) generateViewColumnsOperation() map[string]interface{} {
	return map[string]interface{}{
		"tags":        []string{"Schema"},
		"summary":     "Get view column schema",
		"description": "Returns column names, types, and optional statistics for a view. Only api_* prefixed views are accessible. Use format=transform for json_transform() compatibility.",
		"operationId": "getViewColumns",
		"security": []map[string]interface{}{
			{"ApiKeyAuth": []string{}},
		},
		"parameters": []map[string]interface{}{
			{
				"name":        "name",
				"in":          "path",
				"required":    true,
				"description": "Name of the view (must start with api_)",
				"schema": map[string]interface{}{
					"type": "string",
				},
				"example": "api_active_users",
			},
			{
				"name":        "format",
				"in":          "query",
				"description": "Response format: 'standard' (default) for LLMs/analysts, 'transform' for DuckDB json_transform(), 'summarize' for full statistics",
				"schema": map[string]interface{}{
					"type":    "string",
					"enum":    []string{"standard", "transform", "summarize"},
					"default": "standard",
				},
			},
			{
				"name":        "stats",
				"in":          "query",
				"description": "Include column statistics (min, max, approx_unique, null_percentage). Automatically true for format=summarize.",
				"schema": map[string]interface{}{
					"type":    "boolean",
					"default": false,
				},
			},
			{
				"name":        "sample",
				"in":          "query",
				"description": "Maximum rows to sample for statistics calculation",
				"schema": map[string]interface{}{
					"type":    "integer",
					"default": 10000,
				},
			},
		},
		"responses": map[string]interface{}{
			"200": map[string]interface{}{
				"description": "Column schema retrieved successfully",
				"content": map[string]interface{}{
					"application/json": map[string]interface{}{
						"schema": map[string]interface{}{
							"oneOf": []map[string]interface{}{
								{"$ref": "#/components/schemas/StandardColumnsResponse"},
								{"$ref": "#/components/schemas/TransformColumnsResponse"},
								{"$ref": "#/components/schemas/SummarizeColumnsResponse"},
							},
						},
					},
				},
			},
			"400": map[string]interface{}{
				"description": "Bad request",
				"content": map[string]interface{}{
					"application/json": map[string]interface{}{
						"schema": map[string]interface{}{
							"$ref": "#/components/schemas/ErrorResponse",
						},
					},
				},
			},
			"401": map[string]interface{}{
				"description": "Unauthorized",
				"content": map[string]interface{}{
					"application/json": map[string]interface{}{
						"schema": map[string]interface{}{
							"$ref": "#/components/schemas/ErrorResponse",
						},
					},
				},
			},
			"403": map[string]interface{}{
				"description": "Forbidden",
				"content": map[string]interface{}{
					"application/json": map[string]interface{}{
						"schema": map[string]interface{}{
							"$ref": "#/components/schemas/ErrorResponse",
						},
					},
				},
			},
			"404": map[string]interface{}{
				"description": "View not found",
				"content": map[string]interface{}{
					"application/json": map[string]interface{}{
						"schema": map[string]interface{}{
							"$ref": "#/components/schemas/ErrorResponse",
						},
					},
				},
			},
		},
	}
}

// generateComponents generates the components section of the OpenAPI spec.
func (h *OpenAPIHandler) generateComponents() map[string]interface{} {
	return map[string]interface{}{
		"securitySchemes": map[string]interface{}{
			"ApiKeyAuth": map[string]interface{}{
				"type":        "apiKey",
				"in":          "header",
				"name":        "X-API-Key",
				"description": "API key for authentication. Alternative methods: 'api_key' query parameter, or HTTP Basic auth with username 'apikey' and API key as password (e.g., https://apikey:YOUR_KEY@host/path).",
			},
		},
		"schemas": map[string]interface{}{
			"ErrorResponse": map[string]interface{}{
				"type":     "object",
				"required": []string{"error", "message", "code", "request_id"},
				"properties": map[string]interface{}{
					"error": map[string]interface{}{
						"type":        "string",
						"description": "HTTP status text",
						"example":     "Bad Request",
					},
					"message": map[string]interface{}{
						"type":        "string",
						"description": "Detailed error message",
						"example":     "Invalid JSON in request body",
					},
					"code": map[string]interface{}{
						"type":        "integer",
						"description": "HTTP status code",
						"example":     400,
					},
					"request_id": map[string]interface{}{
						"type":        "string",
						"description": "Unique request identifier for tracing",
						"example":     "550e8400-e29b-41d4-a716-446655440000",
					},
				},
			},
			"SuccessResponse": map[string]interface{}{
				"type":     "object",
				"required": []string{"success", "rows_affected", "request_id"},
				"properties": map[string]interface{}{
					"success": map[string]interface{}{
						"type":    "boolean",
						"example": true,
					},
					"rows_affected": map[string]interface{}{
						"type":        "integer",
						"description": "Number of rows affected by the operation",
						"example":     1,
					},
					"request_id": map[string]interface{}{
						"type":        "string",
						"description": "Unique request identifier for tracing",
						"example":     "550e8400-e29b-41d4-a716-446655440000",
					},
				},
			},
			"DryRunResponse": map[string]interface{}{
				"type":     "object",
				"required": []string{"dry_run", "affected_rows", "request_id"},
				"properties": map[string]interface{}{
					"dry_run": map[string]interface{}{
						"type":    "boolean",
						"example": true,
					},
					"affected_rows": map[string]interface{}{
						"type":        "integer",
						"description": "Number of rows that would be affected",
						"example":     42,
					},
					"request_id": map[string]interface{}{
						"type":        "string",
						"description": "Unique request identifier for tracing",
						"example":     "550e8400-e29b-41d4-a716-446655440000",
					},
				},
			},
			"ReadResponse": map[string]interface{}{
				"type":     "object",
				"required": []string{"data"},
				"properties": map[string]interface{}{
					"data": map[string]interface{}{
						"type":        "array",
						"description": "Array of records",
						"items": map[string]interface{}{
							"type":                 "object",
							"additionalProperties": true,
						},
					},
					"pagination": map[string]interface{}{
						"$ref": "#/components/schemas/Pagination",
					},
					"_links": map[string]interface{}{
						"$ref": "#/components/schemas/HATEOASLinks",
					},
					"truncated": map[string]interface{}{
						"type":        "boolean",
						"description": "Indicates if results were truncated by safety limit",
					},
					"message": map[string]interface{}{
						"type":        "string",
						"description": "Message about truncation if applicable",
					},
					"total_available": map[string]interface{}{
						"type":        "integer",
						"description": "Total rows available when results are truncated",
					},
					"request_id": map[string]interface{}{
						"type":        "string",
						"description": "Unique request identifier for tracing",
						"example":     "550e8400-e29b-41d4-a716-446655440000",
					},
				},
			},
			"Pagination": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"page": map[string]interface{}{
						"type":        "integer",
						"description": "Current page number",
						"example":     1,
					},
					"limit": map[string]interface{}{
						"type":        "integer",
						"description": "Records per page",
						"example":     100,
					},
					"total_rows": map[string]interface{}{
						"type":        "integer",
						"description": "Total number of records",
						"example":     250,
					},
					"total_pages": map[string]interface{}{
						"type":        "integer",
						"description": "Total number of pages",
						"example":     3,
					},
				},
			},
			"HATEOASLinks": map[string]interface{}{
				"type":        "object",
				"description": "Navigation links for pagination (included when links=true)",
				"properties": map[string]interface{}{
					"self": map[string]interface{}{
						"type":        "string",
						"description": "Current page URL",
					},
					"first": map[string]interface{}{
						"type":        "string",
						"description": "First page URL",
					},
					"prev": map[string]interface{}{
						"type":        "string",
						"description": "Previous page URL (if not on first page)",
					},
					"next": map[string]interface{}{
						"type":        "string",
						"description": "Next page URL (if not on last page)",
					},
					"last": map[string]interface{}{
						"type":        "string",
						"description": "Last page URL",
					},
				},
			},
			"UpdateRequest": map[string]interface{}{
				"type":     "object",
				"required": []string{"where", "set"},
				"properties": map[string]interface{}{
					"where": map[string]interface{}{
						"type":        "array",
						"description": "WHERE conditions for filtering records to update",
						"items": map[string]interface{}{
							"$ref": "#/components/schemas/FilterCondition",
						},
					},
					"set": map[string]interface{}{
						"type":        "object",
						"description": "Column-value pairs to update",
						"additionalProperties": map[string]interface{}{
							"oneOf": []map[string]interface{}{
								{"type": "string"},
								{"type": "number"},
								{"type": "boolean"},
								{"type": "null"},
							},
						},
					},
				},
				"example": map[string]interface{}{
					"where": []map[string]interface{}{
						{"column": "age", "op": "gt", "value": 18},
						{"column": "status", "op": "eq", "value": "pending"},
					},
					"set": map[string]interface{}{
						"status": "adult",
					},
				},
			},
			"FilterCondition": map[string]interface{}{
				"type":     "object",
				"required": []string{"column", "op", "value"},
				"properties": map[string]interface{}{
					"column": map[string]interface{}{
						"type":        "string",
						"description": "Column name",
					},
					"op": map[string]interface{}{
						"type":        "string",
						"description": "Comparison operator",
						"enum":        []string{"eq", "ne", "gt", "gte", "lt", "lte", "like", "in"},
					},
					"value": map[string]interface{}{
						"description": "Value to compare against",
						"oneOf": []map[string]interface{}{
							{"type": "string"},
							{"type": "number"},
							{"type": "boolean"},
							{"type": "array", "items": map[string]interface{}{"type": "string"}},
						},
					},
				},
			},
			"QueryRequest": map[string]interface{}{
				"type":     "object",
				"required": []string{"sql"},
				"properties": map[string]interface{}{
					"sql": map[string]interface{}{
						"type":        "string",
						"description": "SQL query to execute",
						"example":     "SELECT * FROM users WHERE age > ? ORDER BY name",
					},
					"params": map[string]interface{}{
						"type":        "array",
						"description": "Query parameters for parameterized queries",
						"items": map[string]interface{}{
							"oneOf": []map[string]interface{}{
								{"type": "string"},
								{"type": "number"},
								{"type": "boolean"},
								{"type": "null"},
							},
						},
						"example": []interface{}{18},
					},
				},
			},
			"QueryResponse": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"columns": map[string]interface{}{
						"type":        "array",
						"description": "Column names",
						"items": map[string]interface{}{
							"type": "string",
						},
						"example": []string{"id", "name", "email", "age"},
					},
					"data": map[string]interface{}{
						"type":        "array",
						"description": "Query results as array of arrays",
						"items": map[string]interface{}{
							"type": "array",
							"items": map[string]interface{}{
								"oneOf": []map[string]interface{}{
									{"type": "string"},
									{"type": "number"},
									{"type": "boolean"},
									{"type": "null"},
								},
							},
						},
					},
					"rows_affected": map[string]interface{}{
						"type":        "integer",
						"description": "Number of rows returned or affected",
						"example":     1,
					},
					"execution_time_ms": map[string]interface{}{
						"type":        "integer",
						"description": "Query execution time in milliseconds",
						"example":     45,
					},
					"request_id": map[string]interface{}{
						"type":        "string",
						"description": "Unique request identifier for tracing",
						"example":     "550e8400-e29b-41d4-a716-446655440000",
					},
				},
			},
			"MacroListResponse": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"macros": map[string]interface{}{
						"type":        "array",
						"description": "List of available table macros",
						"items": map[string]interface{}{
							"$ref": "#/components/schemas/MacroInfo",
						},
					},
				},
			},
			"MacroInfo": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"name": map[string]interface{}{
						"type":        "string",
						"description": "Macro name",
						"example":     "api_search_products",
					},
					"parameters": map[string]interface{}{
						"type":        "array",
						"description": "List of parameter names",
						"items": map[string]interface{}{
							"type": "string",
						},
						"example": []string{"query", "min_price"},
					},
				},
			},
			"ViewListResponse": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"views": map[string]interface{}{
						"type":        "array",
						"description": "List of available views",
						"items": map[string]interface{}{
							"$ref": "#/components/schemas/ViewInfo",
						},
					},
				},
			},
			"ViewInfo": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"name": map[string]interface{}{
						"type":        "string",
						"description": "View name",
						"example":     "api_active_users",
					},
				},
			},
			"TableListResponse": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"tables": map[string]interface{}{
						"type":        "array",
						"description": "List of available tables",
						"items": map[string]interface{}{
							"$ref": "#/components/schemas/TableInfo",
						},
					},
				},
			},
			"TableInfo": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"name": map[string]interface{}{
						"type":        "string",
						"description": "Table name",
						"example":     "users",
					},
				},
			},
			"GroupByResponse": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"group_by": map[string]interface{}{
						"type":        "array",
						"description": "Aggregated results grouped by the specified column",
						"items": map[string]interface{}{
							"$ref": "#/components/schemas/GroupByResult",
						},
					},
				},
			},
			"GroupByResult": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"key": map[string]interface{}{
						"description": "The unique value of the grouped column",
						"oneOf": []map[string]interface{}{
							{"type": "string"},
							{"type": "number"},
							{"type": "boolean"},
							{"type": "null"},
						},
					},
					"key_display_name": map[string]interface{}{
						"type":        "string",
						"description": "Human-readable display name for the key",
					},
					"count": map[string]interface{}{
						"type":        "integer",
						"description": "Number of records with this value",
						"example":     150,
					},
				},
			},
			"CursorResponse": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"data": map[string]interface{}{
						"type":        "array",
						"description": "Array of records",
						"items": map[string]interface{}{
							"type":                 "object",
							"additionalProperties": true,
						},
					},
					"meta": map[string]interface{}{
						"$ref": "#/components/schemas/CursorMeta",
					},
				},
			},
			"CursorMeta": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"count": map[string]interface{}{
						"type":        "integer",
						"description": "Number of records in this response",
						"example":     100,
					},
					"next_cursor": map[string]interface{}{
						"type":        "string",
						"description": "Cursor for fetching the next page. Empty if no more results.",
						"example":     "eyJzIjpbImlkIl0sInYiOlsxMDBdLCJkIjpbImFzYyJdLCJvIjowfQ==",
					},
					"per_page": map[string]interface{}{
						"type":        "integer",
						"description": "Number of records per page",
						"example":     100,
					},
				},
			},
			"StandardColumnsResponse": map[string]interface{}{
				"type":        "object",
				"description": "Standard format for LLMs and analysts discovering table structure",
				"required":    []string{"table", "columns"},
				"properties": map[string]interface{}{
					"table": map[string]interface{}{
						"type":        "string",
						"description": "Table or view name",
						"example":     "works",
					},
					"columns": map[string]interface{}{
						"type":        "array",
						"description": "List of column information",
						"items": map[string]interface{}{
							"$ref": "#/components/schemas/ColumnInfo",
						},
					},
				},
			},
			"TransformColumnsResponse": map[string]interface{}{
				"type":        "object",
				"description": "Transform format for DuckDB json_transform() structure parameter",
				"required":    []string{"table", "columns"},
				"properties": map[string]interface{}{
					"table": map[string]interface{}{
						"type":        "string",
						"description": "Table or view name",
						"example":     "works",
					},
					"columns": map[string]interface{}{
						"type":        "object",
						"description": "Column names mapped to DuckDB type strings",
						"additionalProperties": map[string]interface{}{
							"type": "string",
						},
						"example": map[string]interface{}{
							"work_id":        "BIGINT",
							"title":          "VARCHAR",
							"cited_by_count": "INTEGER",
						},
					},
				},
			},
			"SummarizeColumnsResponse": map[string]interface{}{
				"type":        "object",
				"description": "Summarize format with full statistics from SUMMARIZE",
				"required":    []string{"table", "total_rows", "sample_size", "columns"},
				"properties": map[string]interface{}{
					"table": map[string]interface{}{
						"type":        "string",
						"description": "Table or view name",
						"example":     "works",
					},
					"total_rows": map[string]interface{}{
						"type":        "integer",
						"description": "Total number of rows in the table",
						"example":     476951866,
					},
					"sample_size": map[string]interface{}{
						"type":        "integer",
						"description": "Number of rows sampled for statistics",
						"example":     10000,
					},
					"columns": map[string]interface{}{
						"type":        "array",
						"description": "List of column information with statistics",
						"items": map[string]interface{}{
							"$ref": "#/components/schemas/ColumnInfoWithStats",
						},
					},
				},
			},
			"ColumnInfo": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"name": map[string]interface{}{
						"type":        "string",
						"description": "Column name",
						"example":     "work_id",
					},
					"type": map[string]interface{}{
						"type":        "string",
						"description": "DuckDB data type",
						"example":     "BIGINT",
					},
					"nullable": map[string]interface{}{
						"type":        "boolean",
						"description": "Whether the column allows NULL values",
						"example":     false,
					},
				},
			},
			"ColumnInfoWithStats": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"name": map[string]interface{}{
						"type":        "string",
						"description": "Column name",
						"example":     "work_id",
					},
					"type": map[string]interface{}{
						"type":        "string",
						"description": "DuckDB data type",
						"example":     "BIGINT",
					},
					"nullable": map[string]interface{}{
						"type":        "boolean",
						"description": "Whether the column allows NULL values",
						"example":     false,
					},
					"stats": map[string]interface{}{
						"$ref": "#/components/schemas/ColumnStats",
					},
				},
			},
			"ColumnStats": map[string]interface{}{
				"type":        "object",
				"description": "Column statistics from SUMMARIZE",
				"properties": map[string]interface{}{
					"min": map[string]interface{}{
						"description": "Minimum value in the column",
					},
					"max": map[string]interface{}{
						"description": "Maximum value in the column",
					},
					"approx_unique": map[string]interface{}{
						"type":        "integer",
						"description": "Approximate number of unique values",
						"example":     10000,
					},
					"count_sample": map[string]interface{}{
						"type":        "integer",
						"description": "Number of non-null values in the sample",
						"example":     10000,
					},
					"avg": map[string]interface{}{
						"type":        "number",
						"description": "Average value (numeric columns only)",
						"example":     500000.5,
					},
					"std": map[string]interface{}{
						"type":        "number",
						"description": "Standard deviation (numeric columns only)",
					},
					"q25": map[string]interface{}{
						"description": "25th percentile value",
					},
					"q50": map[string]interface{}{
						"description": "50th percentile (median) value",
					},
					"q75": map[string]interface{}{
						"description": "75th percentile value",
					},
					"null_percentage": map[string]interface{}{
						"type":        "number",
						"description": "Percentage of NULL values (0.0 to 100.0)",
						"example":     0.0,
					},
				},
			},
		},
		"headers": map[string]interface{}{
			"X-Request-ID": map[string]interface{}{
				"description": "Unique request identifier for tracing. If provided in request, will be echoed back. Otherwise, a UUID is generated.",
				"schema": map[string]interface{}{
					"type": "string",
				},
			},
		},
	}
}
