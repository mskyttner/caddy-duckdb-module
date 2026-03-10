package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/tobilg/caddy-duckdb-module/auth"
	"github.com/tobilg/caddy-duckdb-module/database"
	"go.uber.org/zap"
)

// MCPHandler exposes the DuckDB module as an MCP server over streamable-HTTP.
// Auth is enforced via the existing Caddy auth middleware — LLM clients pass
// X-API-Key (or use trusted-user-header) exactly as with the REST API.
//
// Exposed tools:
//   - query(sql, max_rows?)      read-only SQL, result capped at maxRows
//   - execute(sql)               write SQL, requires OperationExecute
//   - export(sql, format?, ttl?) write result to file, returns URL
//   - list_tables(schema?, include_views?)
//   - describe(table)
//   - database_info()
type MCPHandler struct {
	httpHandler http.Handler
}

// NewMCPHandler creates the MCP handler and registers all tools.
func NewMCPHandler(
	dbMgr *database.Manager,
	authorizer *auth.Authorizer,
	exportHandler *ExportHandler,
	logger *zap.Logger,
	maxRows int,
) *MCPHandler {
	if maxRows <= 0 {
		maxRows = 500
	}

	mcpSrv := mcpserver.NewMCPServer(
		"caddy-duckdb",
		"1.0.0",
		mcpserver.WithToolCapabilities(false),
	)

	// --- query ---
	mcpSrv.AddTool(
		mcp.NewTool("query",
			mcp.WithDescription(fmt.Sprintf(
				"Execute a read-only SQL query (SELECT, WITH, FROM, SHOW, DESCRIBE, EXPLAIN). "+
					"Results are capped at max_rows (default %d) to protect context window size. "+
					"Use the export tool for large result sets.", maxRows)),
			mcp.WithString("sql", mcp.Required(), mcp.Description("Read-only SQL statement")),
			mcp.WithNumber("max_rows", mcp.Description("Max rows to return")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			role := auth.GetRoleFromContext(ctx)
			if ok, _ := authorizer.CheckPermission(role, "*", auth.OperationQuery); !ok {
				return mcp.NewToolResultText("Error: insufficient query permission"), nil
			}
			sql := req.GetString("sql", "")
			if strings.TrimSpace(sql) == "" {
				return mcp.NewToolResultText("Error: sql argument is required"), nil
			}
			if containsInternalTables(sql) {
				return mcp.NewToolResultText("Error: access to internal auth tables is forbidden"), nil
			}
			limit := maxRows
			if n := req.GetInt("max_rows", 0); n > 0 {
				limit = n
			}
			return runQueryTool(dbMgr, sql, limit)
		},
	)

	// --- execute ---
	mcpSrv.AddTool(
		mcp.NewTool("execute",
			mcp.WithDescription("Execute a write SQL statement (INSERT, UPDATE, DELETE, CREATE TABLE AS SELECT, COPY, etc.). Requires execute permission on your role."),
			mcp.WithString("sql", mcp.Required(), mcp.Description("Write SQL statement")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			role := auth.GetRoleFromContext(ctx)
			if ok, _ := authorizer.CheckPermission(role, "*", auth.OperationExecute); !ok {
				return mcp.NewToolResultText("Error: execute permission required (run: auth-db permission add -r <role> -t '*' -o e)"), nil
			}
			sql := req.GetString("sql", "")
			if strings.TrimSpace(sql) == "" {
				return mcp.NewToolResultText("Error: sql argument is required"), nil
			}
			if containsInternalTables(sql) {
				return mcp.NewToolResultText("Error: access to internal auth tables is forbidden"), nil
			}
			result, err := dbMgr.ExecMain(sql)
			if err != nil {
				return mcp.NewToolResultText("Error: " + err.Error()), nil
			}
			rowsAffected, _ := result.RowsAffected()
			return mcp.NewToolResultText(fmt.Sprintf("OK: %d rows affected", rowsAffected)), nil
		},
	)

	// --- export ---
	mcpSrv.AddTool(
		mcp.NewTool("export",
			mcp.WithDescription("Execute a SQL query and write results to a file. Returns a download URL instead of row data — use this for large result sets to avoid filling the context window. Supported formats: parquet (default), csv, json."),
			mcp.WithString("sql", mcp.Required(), mcp.Description("SQL SELECT query to export")),
			mcp.WithString("format", mcp.Description("Output format: parquet (default), csv, json"), mcp.Enum("parquet", "csv", "json")),
			mcp.WithNumber("ttl_minutes", mcp.Description("File lifetime in minutes (0 = server default)")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			role := auth.GetRoleFromContext(ctx)
			if ok, _ := authorizer.CheckPermission(role, "*", auth.OperationQuery); !ok {
				return mcp.NewToolResultText("Error: insufficient query permission"), nil
			}
			if exportHandler == nil || exportHandler.exportsDir == "" {
				return mcp.NewToolResultText("Error: export directory not configured on this server (set exports_dir in Caddyfile)"), nil
			}
			sql := req.GetString("sql", "")
			if strings.TrimSpace(sql) == "" {
				return mcp.NewToolResultText("Error: sql argument is required"), nil
			}
			if containsInternalTables(sql) {
				return mcp.NewToolResultText("Error: access to internal auth tables is forbidden"), nil
			}
			format := req.GetString("format", "parquet")
			ttlMinutes := req.GetInt("ttl_minutes", 0)

			resp, err := exportHandler.runExport(sql, format, ttlMinutes)
			if err != nil {
				return mcp.NewToolResultText("Error: " + err.Error()), nil
			}
			b, _ := json.Marshal(resp)
			return mcp.NewToolResultText(string(b)), nil
		},
	)

	// --- list_tables ---
	mcpSrv.AddTool(
		mcp.NewTool("list_tables",
			mcp.WithDescription("List tables (and optionally views) in the database with column counts."),
			mcp.WithString("schema", mcp.Description("Schema name to filter by (default: main)")),
			mcp.WithString("include_views", mcp.Description("Include views: 'true' or 'false' (default: false)")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			role := auth.GetRoleFromContext(ctx)
			if ok, _ := authorizer.CheckPermission(role, "*", auth.OperationQuery); !ok {
				return mcp.NewToolResultText("Error: insufficient query permission"), nil
			}
			schema := req.GetString("schema", "main")
			tableTypes := "'BASE TABLE'"
			if req.GetString("include_views", "false") == "true" {
				tableTypes = "'BASE TABLE','VIEW'"
			}
			sql := fmt.Sprintf(`
				SELECT t.table_name, t.table_type,
				       (SELECT COUNT(*) FROM information_schema.columns c
				        WHERE c.table_name = t.table_name AND c.table_schema = t.table_schema) AS column_count
				FROM information_schema.tables t
				WHERE t.table_schema = '%s' AND t.table_type IN (%s)
				ORDER BY t.table_name
			`, schema, tableTypes)
			return runQueryTool(dbMgr, sql, 2000)
		},
	)

	// --- describe ---
	mcpSrv.AddTool(
		mcp.NewTool("describe",
			mcp.WithDescription("Get the column schema for a table or view."),
			mcp.WithString("table", mcp.Required(), mcp.Description("Table or view name")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			role := auth.GetRoleFromContext(ctx)
			if ok, _ := authorizer.CheckPermission(role, "*", auth.OperationQuery); !ok {
				return mcp.NewToolResultText("Error: insufficient query permission"), nil
			}
			table := req.GetString("table", "")
			if table == "" || !isSimpleIdentifier(table) {
				return mcp.NewToolResultText("Error: valid table name is required"), nil
			}
			return runQueryTool(dbMgr, "DESCRIBE "+table, 500)
		},
	)

	// --- database_info ---
	mcpSrv.AddTool(
		mcp.NewTool("database_info",
			mcp.WithDescription("Get an overview of the database: tables, schemas, and loaded extensions."),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			role := auth.GetRoleFromContext(ctx)
			if ok, _ := authorizer.CheckPermission(role, "*", auth.OperationQuery); !ok {
				return mcp.NewToolResultText("Error: insufficient query permission"), nil
			}
			return runQueryTool(dbMgr, `
				SELECT t.table_catalog AS database, t.table_schema AS schema,
				       t.table_name, t.table_type,
				       (SELECT COUNT(*) FROM information_schema.columns c
				        WHERE c.table_name = t.table_name AND c.table_schema = t.table_schema) AS columns
				FROM information_schema.tables t
				WHERE t.table_schema NOT IN ('information_schema','pg_catalog')
				ORDER BY t.table_catalog, t.table_schema, t.table_name
			`, 5000)
		},
	)

	// Propagate Caddy auth context into MCP tool handler contexts.
	contextFunc := func(ctx context.Context, r *http.Request) context.Context {
		if apiKey := auth.GetAPIKeyFromContext(r.Context()); apiKey != nil {
			ctx = auth.SetContextValues(ctx, apiKey, apiKey.RoleName)
		}
		if reqID := auth.GetRequestIDFromContext(r.Context()); reqID != "" {
			ctx = auth.SetRequestID(ctx, reqID)
		}
		return ctx
	}

	httpSrv := mcpserver.NewStreamableHTTPServer(mcpSrv,
		mcpserver.WithStateLess(true),
		mcpserver.WithHTTPContextFunc(contextFunc),
	)

	return &MCPHandler{httpHandler: httpSrv}
}

// ServeHTTP delegates to the underlying StreamableHTTPServer.
func (h *MCPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.httpHandler.ServeHTTP(w, r)
}

// runQueryTool executes sql, reads up to limit rows, and returns them as JSON text.
func runQueryTool(dbMgr *database.Manager, sql string, limit int) (*mcp.CallToolResult, error) {
	rows, err := dbMgr.QueryMain(sql)
	if err != nil {
		return mcp.NewToolResultText("Error: " + err.Error()), nil
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return mcp.NewToolResultText("Error: " + err.Error()), nil
	}

	values := make([]interface{}, len(cols))
	ptrs := make([]interface{}, len(cols))
	for i := range cols {
		ptrs[i] = &values[i]
	}

	var data []map[string]interface{}
	truncated := false
	for rows.Next() {
		if len(data) >= limit {
			truncated = true
			break
		}
		if err := rows.Scan(ptrs...); err != nil {
			return mcp.NewToolResultText("Error: " + err.Error()), nil
		}
		row := make(map[string]interface{}, len(cols))
		for i, col := range cols {
			switch v := values[i].(type) {
			case nil:
				row[col] = nil
			case []byte:
				row[col] = string(v)
			default:
				row[col] = v
			}
		}
		data = append(data, row)
	}

	out := map[string]interface{}{
		"columns": cols,
		"rows":    data,
		"count":   len(data),
	}
	if truncated {
		out["truncated"] = true
		out["note"] = fmt.Sprintf("Result truncated to %d rows. Use the export tool or add LIMIT/OFFSET to your query.", limit)
	}

	b, err := json.Marshal(out)
	if err != nil {
		return mcp.NewToolResultText("Error: " + err.Error()), nil
	}
	return mcp.NewToolResultText(string(b)), nil
}

// isSimpleIdentifier allows only word characters and dots (for schema.table).
func isSimpleIdentifier(s string) bool {
	for _, c := range s {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '.') {
			return false
		}
	}
	return len(s) > 0
}
