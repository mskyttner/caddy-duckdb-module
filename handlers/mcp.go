package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/tobilg/caddy-duckdb-module/auth"
	"github.com/tobilg/caddy-duckdb-module/database"
	"github.com/tobilg/caddy-duckdb-module/formats"
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

	srv := mcp.NewServer(&mcp.Implementation{
		Name:    "caddy-duckdb",
		Version: "1.0.0",
	}, nil)

	registerDocResources(srv)
	registerHelpTool(srv)

	// checkPerm authenticates the API key from the request header and checks
	// the given operation permission. Returns false and writes an error result
	// if auth fails. The Caddy middleware already enforced 401; this is defence-in-depth.
	checkPerm := func(req *mcp.CallToolRequest, op auth.Operation) (bool, *mcp.CallToolResult) {
		apiKey, _ := authorizer.AuthenticateAPIKey(apiKeyFromRequest(req))
		var roleName string
		if apiKey != nil {
			roleName = apiKey.RoleName
		}
		if ok, _ := authorizer.CheckPermission(roleName, "*", op); !ok {
			return false, textResult("Error: insufficient permission")
		}
		return true, nil
	}

	// --- query ---
	srv.AddTool(
		&mcp.Tool{
			Name: "query",
			Description: fmt.Sprintf(
				"Execute a read-only SQL query (SELECT, WITH, FROM, SHOW, DESCRIBE, EXPLAIN). "+
					"Results are capped at max_rows (default %d) to protect context window size. "+
					"Use the export tool for large result sets. "+
					"For DuckDB-specific syntax (FROM-first, GROUP BY ALL, lambdas, PIVOT, etc.) "+
					"call the help tool or fetch the duckdb://docs/sql-syntax resource before writing complex queries. "+
					"For multi-table joins on large parquet-backed databases: "+
					"(1) use CTEs to narrow down the key IDs from the most selective filter first, "+
					"(2) never use SELECT * — project only the columns you need, "+
					"(3) place the smallest/most selective table on the left side of each join. "+
					"Pattern: WITH ids AS (SELECT id FROM small_table WHERE filter) "+
					"SELECT specific_cols FROM ids JOIN large_table USING (id).", maxRows),
			InputSchema: buildSchema(
				strProp("sql", "Read-only SQL statement", true),
				numProp("max_rows", "Max rows to return"),
			),
		},
		func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if ok, res := checkPerm(req, auth.OperationQuery); !ok {
				return res, nil
			}
			sql := argString(req, "sql", "")
			if strings.TrimSpace(sql) == "" {
				return textResult("Error: sql argument is required"), nil
			}
			if containsInternalTables(sql) {
				return textResult("Error: access to internal auth tables is forbidden"), nil
			}
			limit := maxRows
			if n := argInt(req, "max_rows", 0); n > 0 {
				limit = n
			}
			return runQueryTool(dbMgr, sql, limit)
		},
	)

	// --- execute ---
	srv.AddTool(
		&mcp.Tool{
			Name:        "execute",
			Description: "Execute a write SQL statement (INSERT, UPDATE, DELETE, CREATE TABLE AS SELECT, COPY, etc.). Requires execute permission on your role.",
			InputSchema: buildSchema(
				strProp("sql", "Write SQL statement", true),
			),
		},
		func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if ok, res := checkPerm(req, auth.OperationExecute); !ok {
				return res, nil
			}
			sql := argString(req, "sql", "")
			if strings.TrimSpace(sql) == "" {
				return textResult("Error: sql argument is required"), nil
			}
			if containsInternalTables(sql) {
				return textResult("Error: access to internal auth tables is forbidden"), nil
			}
			result, err := dbMgr.ExecMain(sql)
			if err != nil {
				return textResult("Error: " + err.Error()), nil
			}
			rowsAffected, _ := result.RowsAffected()
			return textResult(fmt.Sprintf("OK: %d rows affected", rowsAffected)), nil
		},
	)

	// --- export ---
	srv.AddTool(
		&mcp.Tool{
			Name:        "export",
			Description: "Execute a SQL query and write results to a file. Returns a download URL instead of row data — use this for large result sets to avoid filling the context window. Supported formats: parquet (default), csv, json.",
			InputSchema: buildSchema(
				strProp("sql", "SQL SELECT query to export", true),
				enumProp("format", "Output format: parquet (default), csv, json", "parquet", "csv", "json"),
				numProp("ttl_minutes", "File lifetime in minutes (0 = server default)"),
			),
		},
		func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if ok, res := checkPerm(req, auth.OperationQuery); !ok {
				return res, nil
			}
			if exportHandler == nil || exportHandler.exportsDir == "" {
				return textResult("Error: export directory not configured on this server (set exports_dir in Caddyfile)"), nil
			}
			sql := argString(req, "sql", "")
			if strings.TrimSpace(sql) == "" {
				return textResult("Error: sql argument is required"), nil
			}
			if containsInternalTables(sql) {
				return textResult("Error: access to internal auth tables is forbidden"), nil
			}
			format := argString(req, "format", "parquet")
			ttlMinutes := argInt(req, "ttl_minutes", 0)

			resp, err := exportHandler.runExport(sql, format, ttlMinutes)
			if err != nil {
				return textResult("Error: " + err.Error()), nil
			}
			b, _ := json.Marshal(resp)
			return textResult(string(b)), nil
		},
	)

	// --- list_tables ---
	srv.AddTool(
		&mcp.Tool{
			Name:        "list_tables",
			Description: "List tables (and optionally views) in the database with column counts.",
			InputSchema: buildSchema(
				strProp("schema", "Schema name to filter by (default: main)", false),
				strProp("include_views", "Include views: 'true' or 'false' (default: false)", false),
			),
		},
		func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if ok, res := checkPerm(req, auth.OperationQuery); !ok {
				return res, nil
			}
			schema := argString(req, "schema", "main")
			tableTypes := "'BASE TABLE'"
			if argString(req, "include_views", "false") == "true" {
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
	srv.AddTool(
		&mcp.Tool{
			Name:        "describe",
			Description: "Get the column schema for a table or view.",
			InputSchema: buildSchema(
				strProp("table", "Table or view name", true),
			),
		},
		func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if ok, res := checkPerm(req, auth.OperationQuery); !ok {
				return res, nil
			}
			table := argString(req, "table", "")
			if table == "" || !isSimpleIdentifier(table) {
				return textResult("Error: valid table name is required"), nil
			}
			return runQueryTool(dbMgr, "DESCRIBE "+table, 500)
		},
	)

	// --- database_info ---
	srv.AddTool(
		&mcp.Tool{
			Name:        "database_info",
			Description: "Get an overview of the database: tables, schemas, and loaded extensions.",
			InputSchema: buildSchema(), // no parameters
		},
		func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if ok, res := checkPerm(req, auth.OperationQuery); !ok {
				return res, nil
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
	srv.AddTool(
		&mcp.Tool{
			Name: "summarize",
			Description: "Run DuckDB SUMMARIZE on a table or SQL query. " +
				"Returns per-column statistics: min, max, avg, std, q25, median, q75, " +
				"count, null_count, approx_unique. Provide either 'table' (table name) " +
				"or 'sql' (a SELECT query), not both. Note: performs a full table scan.",
			InputSchema: buildSchema(
				strProp("table", "Table or view name to summarize", false),
				strProp("sql", "SQL SELECT query to summarize", false),
			),
		},
		func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if ok, res := checkPerm(req, auth.OperationQuery); !ok {
				return res, nil
			}
			table := argString(req, "table", "")
			userSQL := argString(req, "sql", "")
			if table == "" && userSQL == "" {
				return textResult("Error: one of 'table' or 'sql' is required"), nil
			}
			if table != "" && userSQL != "" {
				return textResult("Error: provide either 'table' or 'sql', not both"), nil
			}
			var summarizeSQL string
			if table != "" {
				if !isSimpleIdentifier(table) {
					return textResult("Error: invalid table name"), nil
				}
				tablePart := table
				if idx := strings.LastIndex(table, "."); idx >= 0 {
					tablePart = table[idx+1:]
				}
				if auth.IsInternalTable(tablePart) {
					return textResult("Error: access to internal auth tables is forbidden"), nil
				}
				summarizeSQL = "SUMMARIZE " + table
			} else {
				if strings.TrimSpace(userSQL) == "" {
					return textResult("Error: sql argument is empty"), nil
				}
				if containsInternalTables(userSQL) {
					return textResult("Error: access to internal auth tables is forbidden"), nil
				}
				summarizeSQL = "SUMMARIZE (" + userSQL + ")"
			}
			return runQueryTool(dbMgr, summarizeSQL, 500)
		},
	)

	// --- schema ---
	srv.AddTool(
		&mcp.Tool{
			Name: "schema",
			Description: "Return tables and views with column names and types. " +
				"More efficient than calling describe() N times for databases with many tables. " +
				"Use table_pattern (LIKE filter, e.g. 'work%') to limit results. " +
				"Set compact='true' for a token-efficient dict format: {\"table\":[\"col:TYPE\",...]}. " +
				"Spans all attached databases.",
			InputSchema: buildSchema(
				strProp("table_pattern", "LIKE pattern to filter table names (e.g. 'works', 'work%')", false),
				strProp("compact", "Return compact dict {table:[col:TYPE,...]} instead of flat rows. 'true' or 'false' (default: false)", false),
			),
		},
		func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if ok, res := checkPerm(req, auth.OperationQuery); !ok {
				return res, nil
			}
			tablePattern := argString(req, "table_pattern", "")
			compact := argString(req, "compact", "false") == "true"

			patternClause := ""
			if tablePattern != "" {
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

			_, rowData, _, err := queryRowsRaw(dbMgr, sql, 10000)
			if err != nil {
				return textResult("Error: " + err.Error()), nil
			}
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
			return textResult(sb.String()), nil
		},
	)

	// --- value_counts ---
	srv.AddTool(
		&mcp.Tool{
			Name: "value_counts",
			Description: "Count occurrences of each distinct value in a column. " +
				"Useful for understanding cardinality and dominant categories. " +
				"Returns value and n sorted by frequency descending.",
			InputSchema: buildSchema(
				strProp("table", "Table name", true),
				strProp("column", "Column name (no dots)", true),
				numProp("limit", "Max distinct values to return (default 20)"),
			),
		},
		func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if ok, res := checkPerm(req, auth.OperationQuery); !ok {
				return res, nil
			}
			table := argString(req, "table", "")
			column := argString(req, "column", "")
			if !isSimpleIdentifier(table) {
				return textResult("Error: invalid table name"), nil
			}
			if strings.Contains(column, ".") || !isSimpleIdentifier(column) {
				return textResult("Error: invalid column name (dots not allowed)"), nil
			}
			tablePart := table
			if idx := strings.LastIndex(table, "."); idx >= 0 {
				tablePart = table[idx+1:]
			}
			if auth.IsInternalTable(tablePart) {
				return textResult("Error: access to internal auth tables is forbidden"), nil
			}
			limitVal := argInt(req, "limit", 20)
			if limitVal <= 0 || limitVal > 10000 {
				limitVal = 20
			}
			sql := fmt.Sprintf(
				"WITH counts AS (SELECT %s AS value, COUNT(*) AS n FROM %s GROUP BY 1) "+
					"SELECT value, n, "+
					"round(100.0 * n / sum(n) OVER (), 2) AS pct, "+
					"round(100.0 * sum(n) OVER (ORDER BY n DESC) / sum(n) OVER (), 2) AS cumulative_pct "+
					"FROM counts ORDER BY n DESC LIMIT %d",
				column, table, limitVal,
			)
			return runQueryTool(dbMgr, sql, limitVal)
		},
	)

	// --- sample ---
	srv.AddTool(
		&mcp.Tool{
			Name: "sample",
			Description: "Return an unbiased random sample of rows from a table using " +
				"reservoir sampling with a fixed seed (reproducible). " +
				"Better than LIMIT for exploratory analysis as it avoids first-row bias.",
			InputSchema: buildSchema(
				strProp("table", "Table name", true),
				numProp("n", "Number of rows to sample (default 5)"),
			),
		},
		func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if ok, res := checkPerm(req, auth.OperationQuery); !ok {
				return res, nil
			}
			table := argString(req, "table", "")
			if !isSimpleIdentifier(table) {
				return textResult("Error: invalid table name"), nil
			}
			tablePart := table
			if idx := strings.LastIndex(table, "."); idx >= 0 {
				tablePart = table[idx+1:]
			}
			if auth.IsInternalTable(tablePart) {
				return textResult("Error: access to internal auth tables is forbidden"), nil
			}
			n := argInt(req, "n", 5)
			if n <= 0 || n > 10000 {
				n = 5
			}
			sql := fmt.Sprintf("SELECT * FROM %s USING SAMPLE %d ROWS (reservoir, 42)", table, n)
			return runQueryTool(dbMgr, sql, n)
		},
	)

	// --- column_search ---
	srv.AddTool(
		&mcp.Tool{
			Name: "column_search",
			Description: "Find which tables contain a column matching the given name (case-insensitive LIKE search). " +
				"Useful when you know a field name but not which table it lives in.",
			InputSchema: buildSchema(
				strProp("column_name", "Column name or LIKE pattern (e.g. 'work_id', '%author%')", true),
			),
		},
		func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if ok, res := checkPerm(req, auth.OperationQuery); !ok {
				return res, nil
			}
			colName := argString(req, "column_name", "")
			if strings.TrimSpace(colName) == "" {
				return textResult("Error: column_name is required"), nil
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
	srv.AddTool(
		&mcp.Tool{
			Name: "row_counts",
			Description: "Return row count(s). If 'table' is given, count that table only. " +
				"If omitted, counts all non-internal tables. " +
				"For parquet-backed views DuckDB reads row-group metadata — typically fast even for billions of rows.",
			InputSchema: buildSchema(
				strProp("table", "Table or view name (omit for all tables)", false),
			),
		},
		func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if ok, res := checkPerm(req, auth.OperationQuery); !ok {
				return res, nil
			}
			table := argString(req, "table", "")
			if table != "" {
				if !isSimpleIdentifier(table) {
					return textResult("Error: invalid table name"), nil
				}
				tablePart := table
				if idx := strings.LastIndex(table, "."); idx >= 0 {
					tablePart = table[idx+1:]
				}
				if auth.IsInternalTable(tablePart) {
					return textResult("Error: access to internal auth tables is forbidden"), nil
				}
				sql := fmt.Sprintf("SELECT '%s' AS table_name, COUNT(*) AS row_count FROM %s", table, table)
				return runQueryTool(dbMgr, sql, 1)
			}
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
				return textResult("Error: " + err.Error()), nil
			}
			if len(tableRows) == 0 {
				return textResult(`{"tables":[]}`), nil
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
	srv.AddTool(
		&mcp.Tool{
			Name: "sample_by_id_range",
			Description: "Return a reservoir-sampled subset of rows filtered by an integer ID range. " +
				"Efficient for parquet-backed tables sorted by an ID column: DuckDB uses row-group " +
				"statistics to skip out-of-range groups before sampling. " +
				"Typical use: explore a slice of a large table without a full scan.",
			InputSchema: buildSchema(
				strProp("table", "Table or view name", true),
				strProp("id_column", "Integer column to filter on (default: work_id)", false),
				numProp("start", "Range start (inclusive)"),
				numProp("end", "Range end (inclusive)"),
				numProp("n", "Number of rows to sample (default 5)"),
			),
		},
		func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if ok, res := checkPerm(req, auth.OperationQuery); !ok {
				return res, nil
			}
			table := argString(req, "table", "")
			if !isSimpleIdentifier(table) {
				return textResult("Error: invalid table name"), nil
			}
			tablePart := table
			if idx := strings.LastIndex(table, "."); idx >= 0 {
				tablePart = table[idx+1:]
			}
			if auth.IsInternalTable(tablePart) {
				return textResult("Error: access to internal auth tables is forbidden"), nil
			}
			idCol := argString(req, "id_column", "work_id")
			if !isSimpleIdentifier(idCol) {
				return textResult("Error: invalid id_column name"), nil
			}
			n := argInt(req, "n", 5)
			if n <= 0 || n > 10000 {
				n = 5
			}

			start := argInt(req, "start", 0)
			end := argInt(req, "end", 0)
			var sql string
			if start != 0 || end != 0 {
				if end == 0 {
					end = start
				}
				sql = fmt.Sprintf(
					"SELECT * FROM (SELECT * FROM %s WHERE %s BETWEEN %d AND %d) USING SAMPLE %d ROWS (reservoir, 42)",
					table, idCol, start, end, n,
				)
			} else {
				sql = fmt.Sprintf("SELECT * FROM %s USING SAMPLE %d ROWS (reservoir, 42)", table, n)
			}
			return runQueryTool(dbMgr, sql, n)
		},
	)

	// --- user-defined macros (table and scalar) ---
	if macros, err := discoverMacros(dbMgr); err == nil {
		for _, m := range macros {
			if m.IsScalar {
				registerScalarMacroTool(srv, dbMgr, authorizer, m)
			} else {
				registerMacroTool(srv, dbMgr, authorizer, maxRows, m)
			}
		}
	}

	httpSrv := mcp.NewStreamableHTTPHandler(
		func(r *http.Request) *mcp.Server { return srv },
		&mcp.StreamableHTTPOptions{
			Stateless:    true,
			JSONResponse: true, // return application/json rather than SSE; suits stateless request/response model
		},
	)

	return &MCPHandler{httpHandler: httpSrv}
}

// ServeHTTP delegates to the underlying StreamableHTTPHandler.
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
			row[col] = formats.SanitizeValue(values[i])
		}
		data = append(data, row)
	}
	return cols, data, truncated, nil
}

// runQueryTool executes sql, reads up to limit rows, and returns them as JSON text.
func runQueryTool(dbMgr *database.Manager, sql string, limit int) (*mcp.CallToolResult, error) {
	cols, data, truncated, err := queryRowsRaw(dbMgr, sql, limit)
	if err != nil {
		return textResult("Error: " + err.Error()), nil
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
		return textResult("Error: " + err.Error()), nil
	}
	return textResult(string(b)), nil
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
	Name         string
	DatabaseName string   // catalog name from duckdb_functions() e.g. "memory", "diva"
	Params       []string // parameter names in order
	ParamTypes   []string // SQL type for each parameter
	Definition   string   // truncated SQL body (for tool description fallback)
	Comment      string   // from COMMENT ON MACRO TABLE/MACRO … IS '…'
	IsScalar     bool     // true for scalar macros, false for table macros
}

// macroMetadata holds optional overrides from memory.macro_descriptions.
type macroMetadata struct {
	Description string
	ParamTypes  map[string]string // param name → SQL type override
}

// loadMacroMetadata tries to read memory.macro_descriptions for type and description
// overrides. Returns an empty map silently if the table does not exist.
func loadMacroMetadata(dbMgr *database.Manager) map[string]macroMetadata {
	result := make(map[string]macroMetadata)
	_, rows, _, err := queryRowsRaw(dbMgr, `
		SELECT macro_name,
		       description,
		       param_types::varchar AS param_types_json
		FROM memory.macro_descriptions
		WHERE macro_name IS NOT NULL
	`, 10000)
	if err != nil {
		return result // table absent — silently ignore
	}
	for _, row := range rows {
		name, _ := row["macro_name"].(string)
		if name == "" {
			continue
		}
		meta := macroMetadata{}
		meta.Description, _ = row["description"].(string)
		if pt, _ := row["param_types_json"].(string); pt != "" && pt != "null" {
			_ = json.Unmarshal([]byte(pt), &meta.ParamTypes)
		}
		result[name] = meta
	}
	return result
}

// discoverMacros queries duckdb_functions() for all user-defined table and scalar macros
// across all attached databases. It merges results with any overrides in
// memory.macro_descriptions (description and per-parameter type overrides).
func discoverMacros(dbMgr *database.Manager) ([]macroInfo, error) {
	metadata := loadMacroMetadata(dbMgr)

	sql := `
		SELECT function_name,
		       database_name,
		       to_json(parameters)::varchar      AS params_json,
		       to_json(parameter_types)::varchar  AS types_json,
		       macro_definition,
		       comment,
		       function_type
		FROM duckdb_functions()
		WHERE function_type IN ('table_macro', 'macro')
		  AND database_name NOT IN ('system', 'temp')
		ORDER BY database_name, function_name
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
		dbName, _ := row["database_name"].(string)
		funcType, _ := row["function_type"].(string)

		var params, types []string
		if v, _ := row["params_json"].(string); v != "" && v != "null" {
			_ = json.Unmarshal([]byte(v), &params)
		}
		if v, _ := row["types_json"].(string); v != "" && v != "null" {
			_ = json.Unmarshal([]byte(v), &types)
		}

		comment, _ := row["comment"].(string)

		// Apply metadata overrides (description and per-param type overrides).
		if meta, ok := metadata[name]; ok {
			if meta.Description != "" {
				comment = meta.Description
			}
			if len(meta.ParamTypes) > 0 {
				if len(types) < len(params) {
					types = make([]string, len(params))
				}
				for i, p := range params {
					if t, ok := meta.ParamTypes[p]; ok {
						types[i] = t
					}
				}
			}
		}

		def, _ := row["macro_definition"].(string)
		if len(def) > 300 {
			def = def[:300] + "…"
		}
		macros = append(macros, macroInfo{
			Name:         name,
			DatabaseName: dbName,
			Params:       params,
			ParamTypes:   types,
			Definition:   def,
			Comment:      comment,
			IsScalar:     funcType == "macro",
		})
	}
	return macros, nil
}

// macroDescription returns the MCP tool description for a macro.
// A COMMENT on the macro takes precedence over the SQL body fallback.
func macroDescription(m macroInfo) string {
	if m.Comment != "" {
		return m.Comment
	}
	if m.IsScalar {
		return "User-defined scalar macro. SQL: " + m.Definition
	}
	return "User-defined table macro. SQL: " + m.Definition
}

// buildMacroArgs converts the provided MCP tool arguments into a slice of
// DuckDB named-argument strings (e.g. ["param := 42", "name := 'foo'"]).
// Only parameters listed in macro.Params that are present in the request are included.
func buildMacroArgs(macro macroInfo, req *mcp.CallToolRequest) []string {
	provided := argMap(req)
	var args []string
	for _, param := range macro.Params {
		rawVal, ok := provided[param]
		if !ok {
			continue
		}
		switch v := rawVal.(type) {
		case float64:
			if v == float64(int64(v)) {
				args = append(args, fmt.Sprintf("%s := %d", param, int64(v)))
			} else {
				args = append(args, fmt.Sprintf("%s := %g", param, v))
			}
		case bool:
			args = append(args, fmt.Sprintf("%s := %t", param, v))
		default:
			s := argString(req, param, "")
			safe := strings.ReplaceAll(s, "'", "''")
			args = append(args, fmt.Sprintf("%s := '%s'", param, safe))
		}
	}
	return args
}

// registerMacroTool adds a user-defined table macro as an MCP tool.
// The MCP tool name is always the bare function name for a clean LLM API.
// The SQL call uses the fully qualified catalog.function_name() form so macros
// in attached databases (e.g. diva.work_openalex) resolve correctly regardless
// of the current search_path or USE catalog state.
func registerMacroTool(srv *mcp.Server, dbMgr *database.Manager, authorizer *auth.Authorizer, maxRows int, m macroInfo) {
	if builtinMCPToolNames[m.Name] {
		return
	}

	// Build qualified call target: "catalog".function_name
	// Always quote the database name so names containing dashes or other
	// special characters (e.g. "walden-thin-26q1") are valid SQL identifiers.
	qualifiedName := m.Name
	if m.DatabaseName != "" {
		qualifiedName = `"` + m.DatabaseName + `".` + m.Name
	}

	macro := m                  // capture for closure
	callTarget := qualifiedName // capture for closure
	srv.AddTool(
		&mcp.Tool{
			Name:        macro.Name,
			Description: macroDescription(macro),
			InputSchema: buildDynamicSchema(macro.Params, macro.ParamTypes),
		},
		func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			apiKey, _ := authorizer.AuthenticateAPIKey(apiKeyFromRequest(req))
			var roleName string
			if apiKey != nil {
				roleName = apiKey.RoleName
			}
			if ok, _ := authorizer.CheckPermission(roleName, "*", auth.OperationQuery); !ok {
				return textResult("Error: insufficient query permission"), nil
			}
			args := buildMacroArgs(macro, req)
			sql := fmt.Sprintf("SELECT * FROM %s(%s)", callTarget, strings.Join(args, ", "))
			return runQueryTool(dbMgr, sql, maxRows)
		},
	)
}

// registerScalarMacroTool adds a user-defined scalar macro as an MCP tool.
// The macro is invoked as SELECT macro(args) AS result and the single value
// is returned as text content.
func registerScalarMacroTool(srv *mcp.Server, dbMgr *database.Manager, authorizer *auth.Authorizer, m macroInfo) {
	if builtinMCPToolNames[m.Name] {
		return
	}

	qualifiedName := m.Name
	if m.DatabaseName != "" {
		qualifiedName = `"` + m.DatabaseName + `".` + m.Name
	}

	macro := m
	callTarget := qualifiedName
	srv.AddTool(
		&mcp.Tool{
			Name:        macro.Name,
			Description: macroDescription(macro),
			InputSchema: buildDynamicSchema(macro.Params, macro.ParamTypes),
		},
		func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			apiKey, _ := authorizer.AuthenticateAPIKey(apiKeyFromRequest(req))
			var roleName string
			if apiKey != nil {
				roleName = apiKey.RoleName
			}
			if ok, _ := authorizer.CheckPermission(roleName, "*", auth.OperationQuery); !ok {
				return textResult("Error: insufficient query permission"), nil
			}
			args := buildMacroArgs(macro, req)
			sql := fmt.Sprintf("SELECT %s(%s) AS result", callTarget, strings.Join(args, ", "))
			_, rows, _, err := queryRowsRaw(dbMgr, sql, 1)
			if err != nil {
				return textResult("Error: " + err.Error()), nil
			}
			if len(rows) == 0 {
				return textResult(""), nil
			}
			for _, v := range rows[0] {
				if v == nil {
					return textResult(""), nil
				}
				return textResult(fmt.Sprintf("%v", v)), nil
			}
			return textResult(""), nil
		},
	)
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
