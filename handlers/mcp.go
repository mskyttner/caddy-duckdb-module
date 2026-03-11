package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	duckdb "github.com/duckdb/duckdb-go/v2"
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
//   - query(sql, max_rows?)                read-only SQL, result capped at maxRows
//   - execute(sql)                         write SQL, requires OperationExecute
//   - export(sql, format?, ttl?)           write result to file, returns URL
//   - list_tables(schema?, include_views?)
//   - describe(table)
//   - database_info()
//   - summarize(table? | sql?)             per-column statistics via DuckDB SUMMARIZE
//   - schema(table_pattern?, compact?)     all tables+columns; filter + compact dict output
//   - value_counts(table, column, limit?)  GROUP BY frequency
//   - sample(table, n?)                    reservoir-sampled rows
//   - column_search(column_name)           find tables that contain a column by name
//   - row_counts(table?)                   row count for one or all tables
//   - sample_by_id_range(table, id_column?, start?, end?, n?)  range-filtered reservoir sample
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

	// --- summarize ---
	mcpSrv.AddTool(
		mcp.NewTool("summarize",
			mcp.WithDescription("Run DuckDB SUMMARIZE on a table or SQL query. "+
				"Returns per-column statistics: min, max, avg, std, q25, median, q75, "+
				"count, null_count, approx_unique. Provide either 'table' (table name) "+
				"or 'sql' (a SELECT query), not both. Note: performs a full table scan."),
			mcp.WithString("table", mcp.Description("Table or view name to summarize")),
			mcp.WithString("sql", mcp.Description("SQL SELECT query to summarize")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			role := auth.GetRoleFromContext(ctx)
			if ok, _ := authorizer.CheckPermission(role, "*", auth.OperationQuery); !ok {
				return mcp.NewToolResultText("Error: insufficient query permission"), nil
			}
			table := req.GetString("table", "")
			userSQL := req.GetString("sql", "")
			if table == "" && userSQL == "" {
				return mcp.NewToolResultText("Error: one of 'table' or 'sql' is required"), nil
			}
			if table != "" && userSQL != "" {
				return mcp.NewToolResultText("Error: provide either 'table' or 'sql', not both"), nil
			}
			var summarizeSQL string
			if table != "" {
				if !isSimpleIdentifier(table) {
					return mcp.NewToolResultText("Error: invalid table name"), nil
				}
				// Strip schema prefix before internal table check (e.g. main.api_keys)
				tablePart := table
				if idx := strings.LastIndex(table, "."); idx >= 0 {
					tablePart = table[idx+1:]
				}
				if auth.IsInternalTable(tablePart) {
					return mcp.NewToolResultText("Error: access to internal auth tables is forbidden"), nil
				}
				summarizeSQL = "SUMMARIZE " + table
			} else {
				if strings.TrimSpace(userSQL) == "" {
					return mcp.NewToolResultText("Error: sql argument is empty"), nil
				}
				if containsInternalTables(userSQL) {
					return mcp.NewToolResultText("Error: access to internal auth tables is forbidden"), nil
				}
				summarizeSQL = "SUMMARIZE (" + userSQL + ")"
			}
			return runQueryTool(dbMgr, summarizeSQL, 500)
		},
	)

	// --- schema ---
	mcpSrv.AddTool(
		mcp.NewTool("schema",
			mcp.WithDescription("Return tables and views with column names and types. "+
				"More efficient than calling describe() N times for databases with many tables. "+
				"Use table_pattern (LIKE filter, e.g. 'work%') to limit results. "+
				"Set compact='true' for a token-efficient dict format: {\"table\":[\"col:TYPE\",...]}. "+
				"Spans all attached databases."),
			mcp.WithString("table_pattern", mcp.Description("LIKE pattern to filter table names (e.g. 'works', 'work%')")),
			mcp.WithString("compact", mcp.Description("Return compact dict {table:[col:TYPE,...]} instead of flat rows. 'true' or 'false' (default: false)")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			role := auth.GetRoleFromContext(ctx)
			if ok, _ := authorizer.CheckPermission(role, "*", auth.OperationQuery); !ok {
				return mcp.NewToolResultText("Error: insufficient query permission"), nil
			}
			tablePattern := req.GetString("table_pattern", "")
			compact := req.GetString("compact", "false") == "true"

			patternClause := ""
			if tablePattern != "" {
				// Use LIKE pattern; escape single quotes defensively
				safe := strings.ReplaceAll(tablePattern, "'", "''")
				patternClause = fmt.Sprintf(" AND c.table_name LIKE '%s'", safe)
			}
			sql := fmt.Sprintf(`
				SELECT c.table_catalog, c.table_name, c.column_name, c.data_type
				FROM information_schema.columns c
				JOIN information_schema.tables t
					ON c.table_name = t.table_name AND c.table_schema = t.table_schema
				WHERE t.table_schema NOT IN ('information_schema', 'pg_catalog')
				  AND t.table_type IN ('BASE TABLE', 'VIEW')
				  AND c.table_name NOT IN ('api_keys', 'roles', 'permissions', 'trusted_users')
				%s
				ORDER BY c.table_catalog, c.table_name, c.ordinal_position
			`, patternClause)

			if !compact {
				return runQueryTool(dbMgr, sql, 10000)
			}

			// Compact mode: build {"table_name": ["col:TYPE", ...], ...}
			_, rowData, _, err := queryRowsRaw(dbMgr, sql, 10000)
			if err != nil {
				return mcp.NewToolResultText("Error: " + err.Error()), nil
			}
			// Use ordered insertion to preserve table order
			type entry struct {
				name string
				cols []string
			}
			seen := map[string]int{}
			var order []entry
			for _, row := range rowData {
				tableName, _ := row["table_name"].(string)
				colName, _ := row["column_name"].(string)
				dataType, _ := row["data_type"].(string)
				colEntry := colName + ":" + dataType
				if idx, ok := seen[tableName]; ok {
					order[idx].cols = append(order[idx].cols, colEntry)
				} else {
					seen[tableName] = len(order)
					order = append(order, entry{name: tableName, cols: []string{colEntry}})
				}
			}
			// Build ordered JSON manually to preserve table insertion order
			var sb strings.Builder
			sb.WriteByte('{')
			for i, e := range order {
				if i > 0 {
					sb.WriteByte(',')
				}
				nameB, _ := json.Marshal(e.name)
				sb.Write(nameB)
				sb.WriteByte(':')
				colsB, _ := json.Marshal(e.cols)
				sb.Write(colsB)
			}
			sb.WriteByte('}')
			return mcp.NewToolResultText(sb.String()), nil
		},
	)

	// --- value_counts ---
	mcpSrv.AddTool(
		mcp.NewTool("value_counts",
			mcp.WithDescription("Count occurrences of each distinct value in a column. "+
				"Useful for understanding cardinality and dominant categories. "+
				"Returns value and n sorted by frequency descending."),
			mcp.WithString("table", mcp.Required(), mcp.Description("Table name")),
			mcp.WithString("column", mcp.Required(), mcp.Description("Column name (no dots)")),
			mcp.WithNumber("limit", mcp.Description("Max distinct values to return (default 20)")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			role := auth.GetRoleFromContext(ctx)
			if ok, _ := authorizer.CheckPermission(role, "*", auth.OperationQuery); !ok {
				return mcp.NewToolResultText("Error: insufficient query permission"), nil
			}
			table := req.GetString("table", "")
			column := req.GetString("column", "")
			if !isSimpleIdentifier(table) {
				return mcp.NewToolResultText("Error: invalid table name"), nil
			}
			if strings.Contains(column, ".") || !isSimpleIdentifier(column) {
				return mcp.NewToolResultText("Error: invalid column name (dots not allowed)"), nil
			}
			tablePart := table
			if idx := strings.LastIndex(table, "."); idx >= 0 {
				tablePart = table[idx+1:]
			}
			if auth.IsInternalTable(tablePart) {
				return mcp.NewToolResultText("Error: access to internal auth tables is forbidden"), nil
			}
			limitVal := req.GetInt("limit", 20)
			if limitVal <= 0 || limitVal > 10000 {
				limitVal = 20
			}
			sql := fmt.Sprintf(
				"SELECT %s AS value, COUNT(*) AS n FROM %s GROUP BY 1 ORDER BY 2 DESC LIMIT %d",
				column, table, limitVal,
			)
			return runQueryTool(dbMgr, sql, limitVal)
		},
	)

	// --- sample ---
	mcpSrv.AddTool(
		mcp.NewTool("sample",
			mcp.WithDescription("Return an unbiased random sample of rows from a table using "+
				"reservoir sampling with a fixed seed (reproducible). "+
				"Better than LIMIT for exploratory analysis as it avoids first-row bias."),
			mcp.WithString("table", mcp.Required(), mcp.Description("Table name")),
			mcp.WithNumber("n", mcp.Description("Number of rows to sample (default 5)")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			role := auth.GetRoleFromContext(ctx)
			if ok, _ := authorizer.CheckPermission(role, "*", auth.OperationQuery); !ok {
				return mcp.NewToolResultText("Error: insufficient query permission"), nil
			}
			table := req.GetString("table", "")
			if !isSimpleIdentifier(table) {
				return mcp.NewToolResultText("Error: invalid table name"), nil
			}
			tablePart := table
			if idx := strings.LastIndex(table, "."); idx >= 0 {
				tablePart = table[idx+1:]
			}
			if auth.IsInternalTable(tablePart) {
				return mcp.NewToolResultText("Error: access to internal auth tables is forbidden"), nil
			}
			n := req.GetInt("n", 5)
			if n <= 0 || n > 10000 {
				n = 5
			}
			sql := fmt.Sprintf("SELECT * FROM %s USING SAMPLE %d ROWS (reservoir, 42)", table, n)
			return runQueryTool(dbMgr, sql, n)
		},
	)

	// --- column_search ---
	mcpSrv.AddTool(
		mcp.NewTool("column_search",
			mcp.WithDescription("Find which tables contain a column matching the given name (case-insensitive LIKE search). "+
				"Useful when you know a field name but not which table it lives in."),
			mcp.WithString("column_name", mcp.Required(), mcp.Description("Column name or LIKE pattern (e.g. 'work_id', '%author%')")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			role := auth.GetRoleFromContext(ctx)
			if ok, _ := authorizer.CheckPermission(role, "*", auth.OperationQuery); !ok {
				return mcp.NewToolResultText("Error: insufficient query permission"), nil
			}
			colName := req.GetString("column_name", "")
			if strings.TrimSpace(colName) == "" {
				return mcp.NewToolResultText("Error: column_name is required"), nil
			}
			safe := strings.ReplaceAll(colName, "'", "''")
			sql := fmt.Sprintf(`
				SELECT c.table_catalog, c.table_name, c.column_name, c.data_type, c.ordinal_position
				FROM information_schema.columns c
				JOIN information_schema.tables t
					ON c.table_name = t.table_name AND c.table_schema = t.table_schema
				WHERE t.table_schema NOT IN ('information_schema', 'pg_catalog')
				  AND t.table_type IN ('BASE TABLE', 'VIEW')
				  AND c.table_name NOT IN ('api_keys', 'roles', 'permissions', 'trusted_users')
				  AND LOWER(c.column_name) LIKE LOWER('%s')
				ORDER BY c.table_name, c.ordinal_position
			`, safe)
			return runQueryTool(dbMgr, sql, 500)
		},
	)

	// --- row_counts ---
	mcpSrv.AddTool(
		mcp.NewTool("row_counts",
			mcp.WithDescription("Return row count(s). If 'table' is given, count that table only. "+
				"If omitted, counts all non-internal tables. "+
				"For parquet-backed views DuckDB reads row-group metadata — typically fast even for billions of rows."),
			mcp.WithString("table", mcp.Description("Table or view name (omit for all tables)")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			role := auth.GetRoleFromContext(ctx)
			if ok, _ := authorizer.CheckPermission(role, "*", auth.OperationQuery); !ok {
				return mcp.NewToolResultText("Error: insufficient query permission"), nil
			}
			table := req.GetString("table", "")
			if table != "" {
				if !isSimpleIdentifier(table) {
					return mcp.NewToolResultText("Error: invalid table name"), nil
				}
				tablePart := table
				if idx := strings.LastIndex(table, "."); idx >= 0 {
					tablePart = table[idx+1:]
				}
				if auth.IsInternalTable(tablePart) {
					return mcp.NewToolResultText("Error: access to internal auth tables is forbidden"), nil
				}
				sql := fmt.Sprintf("SELECT '%s' AS table_name, COUNT(*) AS row_count FROM %s", table, table)
				return runQueryTool(dbMgr, sql, 1)
			}
			// All tables: query names from information_schema then build UNION ALL
			listSQL := `
				SELECT t.table_name
				FROM information_schema.tables t
				WHERE t.table_schema NOT IN ('information_schema', 'pg_catalog')
				  AND t.table_type IN ('BASE TABLE', 'VIEW')
				  AND t.table_name NOT IN ('api_keys', 'roles', 'permissions', 'trusted_users')
				ORDER BY t.table_name
			`
			_, tableRows, _, err := queryRowsRaw(dbMgr, listSQL, 2000)
			if err != nil {
				return mcp.NewToolResultText("Error: " + err.Error()), nil
			}
			if len(tableRows) == 0 {
				return mcp.NewToolResultText(`{"tables":[]}`), nil
			}
			var parts []string
			for _, row := range tableRows {
				name, _ := row["table_name"].(string)
				if name == "" {
					continue
				}
				parts = append(parts, fmt.Sprintf("SELECT '%s' AS table_name, COUNT(*) AS row_count FROM %s",
					strings.ReplaceAll(name, "'", "''"), name))
			}
			sql := strings.Join(parts, " UNION ALL ") + " ORDER BY table_name"
			return runQueryTool(dbMgr, sql, 2000)
		},
	)

	// --- sample_by_id_range ---
	mcpSrv.AddTool(
		mcp.NewTool("sample_by_id_range",
			mcp.WithDescription("Return a reservoir-sampled subset of rows filtered by an integer ID range. "+
				"Efficient for parquet-backed tables sorted by an ID column: DuckDB uses row-group "+
				"statistics to skip out-of-range groups before sampling. "+
				"Typical use: explore a slice of a large table without a full scan."),
			mcp.WithString("table", mcp.Required(), mcp.Description("Table or view name")),
			mcp.WithString("id_column", mcp.Description("Integer column to filter on (default: work_id)")),
			mcp.WithNumber("start", mcp.Description("Range start (inclusive)")),
			mcp.WithNumber("end", mcp.Description("Range end (inclusive)")),
			mcp.WithNumber("n", mcp.Description("Number of rows to sample (default 5)")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			role := auth.GetRoleFromContext(ctx)
			if ok, _ := authorizer.CheckPermission(role, "*", auth.OperationQuery); !ok {
				return mcp.NewToolResultText("Error: insufficient query permission"), nil
			}
			table := req.GetString("table", "")
			if !isSimpleIdentifier(table) {
				return mcp.NewToolResultText("Error: invalid table name"), nil
			}
			tablePart := table
			if idx := strings.LastIndex(table, "."); idx >= 0 {
				tablePart = table[idx+1:]
			}
			if auth.IsInternalTable(tablePart) {
				return mcp.NewToolResultText("Error: access to internal auth tables is forbidden"), nil
			}
			idCol := req.GetString("id_column", "work_id")
			if !isSimpleIdentifier(idCol) {
				return mcp.NewToolResultText("Error: invalid id_column name"), nil
			}
			n := req.GetInt("n", 5)
			if n <= 0 || n > 10000 {
				n = 5
			}

			start := req.GetInt("start", 0)
			end := req.GetInt("end", 0)
			var sql string
			if start != 0 || end != 0 {
				if end == 0 {
					end = start
				}
				// Wrap the WHERE filter in a subquery before applying USING SAMPLE.
				// DuckDB's reservoir sampler silently returns 0 rows when USING SAMPLE
				// is combined with a WHERE clause directly on parquet-backed views;
				// materialising the filter in a subquery first avoids this.
				sql = fmt.Sprintf(
					"SELECT * FROM (SELECT * FROM %s WHERE %s BETWEEN %d AND %d) USING SAMPLE %d ROWS (reservoir, 42)",
					table, idCol, start, end, n,
				)
			} else {
				// No range specified — fall back to plain sample
				sql = fmt.Sprintf("SELECT * FROM %s USING SAMPLE %d ROWS (reservoir, 42)", table, n)
			}
			return runQueryTool(dbMgr, sql, n)
		},
	)

	// --- user-defined table macros ---
	// Enumerate macros at startup and register each as an MCP tool.
	if macros, err := discoverTableMacros(dbMgr); err == nil {
		for _, m := range macros {
			registerMacroTool(mcpSrv, dbMgr, authorizer, maxRows, m)
		}
	}

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

// queryRowsRaw executes sql, reads up to limit rows, and returns raw column/row data.
func queryRowsRaw(dbMgr *database.Manager, sql string, limit int) (cols []string, data []map[string]any, truncated bool, err error) {
	rows, err := dbMgr.QueryMain(sql)
	if err != nil {
		return nil, nil, false, err
	}
	defer rows.Close()

	cols, err = rows.Columns()
	if err != nil {
		return nil, nil, false, err
	}

	values := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range cols {
		ptrs[i] = &values[i]
	}

	for rows.Next() {
		if len(data) >= limit {
			truncated = true
			break
		}
		if err = rows.Scan(ptrs...); err != nil {
			return nil, nil, false, err
		}
		row := make(map[string]any, len(cols))
		for i, col := range cols {
			switch v := values[i].(type) {
			case nil:
				row[col] = nil
			case []byte:
				row[col] = string(v)
			case duckdb.Decimal:
				// Serialize DECIMAL as a plain float64 so JSON output is a number,
				// not the raw {Width, Scale, Value} struct from the Go driver.
				row[col] = v.Float64()
			default:
				row[col] = v
			}
		}
		data = append(data, row)
	}
	return cols, data, truncated, nil
}

// runQueryTool executes sql, reads up to limit rows, and returns them as JSON text.
func runQueryTool(dbMgr *database.Manager, sql string, limit int) (*mcp.CallToolResult, error) {
	cols, data, truncated, err := queryRowsRaw(dbMgr, sql, limit)
	if err != nil {
		return mcp.NewToolResultText("Error: " + err.Error()), nil
	}

	out := map[string]any{
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

// builtinMCPToolNames is the set of tool names reserved for built-in tools.
// User macro names that conflict are skipped.
var builtinMCPToolNames = map[string]bool{
	"query": true, "execute": true, "export": true,
	"list_tables": true, "describe": true, "database_info": true,
	"summarize": true, "schema": true, "value_counts": true, "sample": true,
	"column_search": true, "row_counts": true, "sample_by_id_range": true,
}

// macroInfo holds metadata for a user-defined DuckDB table macro.
type macroInfo struct {
	Name       string
	Params     []string // parameter names in order
	ParamTypes []string // SQL type for each parameter
	Definition string   // truncated SQL body (for tool description)
}

// discoverTableMacros queries duckdb_functions() for user-defined table macros.
// It uses to_json() to convert the VARCHAR[] parameter columns to JSON strings
// that are safe to unmarshal regardless of how the DuckDB Go driver serialises arrays.
func discoverTableMacros(dbMgr *database.Manager) ([]macroInfo, error) {
	sql := `
		SELECT function_name,
		       to_json(parameters)::varchar   AS params_json,
		       to_json(parameter_types)::varchar AS types_json,
		       macro_definition
		FROM duckdb_functions()
		WHERE function_type = 'table_macro'
		  AND database_name NOT IN ('system', 'temp')
		ORDER BY function_name
	`
	_, rows, _, err := queryRowsRaw(dbMgr, sql, 1000)
	if err != nil {
		return nil, err
	}
	var macros []macroInfo
	for _, row := range rows {
		name, _ := row["function_name"].(string)
		if name == "" {
			continue
		}
		var params, types []string
		if v, _ := row["params_json"].(string); v != "" && v != "null" {
			_ = json.Unmarshal([]byte(v), &params)
		}
		if v, _ := row["types_json"].(string); v != "" && v != "null" {
			_ = json.Unmarshal([]byte(v), &types)
		}
		def, _ := row["macro_definition"].(string)
		if len(def) > 300 {
			def = def[:300] + "…"
		}
		macros = append(macros, macroInfo{
			Name:       name,
			Params:     params,
			ParamTypes: types,
			Definition: def,
		})
	}
	return macros, nil
}

// registerMacroTool adds a user-defined table macro as an MCP tool.
// Each parameter becomes an optional MCP argument (string or number based on SQL type).
// The call uses named-argument syntax so params with defaults can be omitted.
func registerMacroTool(srv *mcpserver.MCPServer, dbMgr *database.Manager, authorizer *auth.Authorizer, maxRows int, m macroInfo) {
	if builtinMCPToolNames[m.Name] {
		return // don't shadow built-in tools
	}
	opts := []mcp.ToolOption{
		mcp.WithDescription(fmt.Sprintf("User-defined table macro. SQL: %s", m.Definition)),
	}
	for i, param := range m.Params {
		sqlType := "VARCHAR"
		if i < len(m.ParamTypes) {
			sqlType = m.ParamTypes[i]
		}
		desc := mcp.Description("SQL type: " + sqlType)
		if isNumericSQLType(sqlType) {
			opts = append(opts, mcp.WithNumber(param, desc))
		} else {
			opts = append(opts, mcp.WithString(param, desc))
		}
	}

	macro := m // capture for closure
	srv.AddTool(mcp.NewTool(macro.Name, opts...), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		role := auth.GetRoleFromContext(ctx)
		if ok, _ := authorizer.CheckPermission(role, "*", auth.OperationQuery); !ok {
			return mcp.NewToolResultText("Error: insufficient query permission"), nil
		}
		// Build named-arg call using only provided params so DuckDB defaults apply
		// for missing args: SELECT * FROM macro(p1 := 'val', p2 := 42)
		provided := req.GetArguments()
		var args []string
		for _, param := range macro.Params {
			rawVal, ok := provided[param]
			if !ok {
				continue // omit — DuckDB will use the declared default
			}
			// Use the raw JSON value type to determine quoting rather than the
			// SQL declared type, because DuckDB reports NULL types for params
			// whose type is inferred at call time from their default value.
			switch v := rawVal.(type) {
			case float64:
				// JSON number: pass unquoted (integer if whole, float otherwise)
				if v == float64(int64(v)) {
					args = append(args, fmt.Sprintf("%s := %d", param, int64(v)))
				} else {
					args = append(args, fmt.Sprintf("%s := %g", param, v))
				}
			case bool:
				args = append(args, fmt.Sprintf("%s := %t", param, v))
			default:
				// String (or unknown): single-quote escape
				s := req.GetString(param, "")
				safe := strings.ReplaceAll(s, "'", "''")
				args = append(args, fmt.Sprintf("%s := '%s'", param, safe))
			}
		}
		sql := fmt.Sprintf("SELECT * FROM %s(%s)", macro.Name, strings.Join(args, ", "))
		return runQueryTool(dbMgr, sql, maxRows)
	})
}

// isNumericSQLType returns true for SQL types that should be passed as numbers.
func isNumericSQLType(t string) bool {
	t = strings.ToUpper(strings.TrimSpace(t))
	for _, prefix := range []string{"INT", "BIGINT", "SMALLINT", "TINYINT", "HUGEINT",
		"DOUBLE", "FLOAT", "REAL", "DECIMAL", "NUMERIC"} {
		if strings.HasPrefix(t, prefix) {
			return true
		}
	}
	return false
}
