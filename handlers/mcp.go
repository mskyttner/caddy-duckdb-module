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
// docsDir, if non-empty, is scanned for *.md files that are registered as
// additional MCP resources at duckdb://docs/<stem>. This allows operators to
// provide deployment-specific documentation (schema guides, domain references)
// without recompiling the binary.
func NewMCPHandler(
	dbMgr *database.Manager,
	authorizer *auth.Authorizer,
	exportHandler *ExportHandler,
	logger *zap.Logger,
	maxRows int,
	docsDir string,
	publicExportsURL string,
) *MCPHandler {
	if maxRows <= 0 {
		maxRows = 500
	}

	srv := mcp.NewServer(&mcp.Implementation{
		Name:    "caddy-duckdb",
		Version: "1.0.0",
	}, nil)

	resources := registerDocResources(srv, docsDir)
	registerHelpTool(srv)

	// publicExportsURL is set when public (no-auth) exports are configured.
	// Exposed via database_info so LLMs can plan cross-domain imports.
	pubExportsURL := publicExportsURL

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
			Name: "database_info",
			Description: "Get a structured overview of the database: attached catalogs, active search_path, " +
				"tables, views, macros (names + parameters, no bodies), loaded extensions, and available MCP resources. " +
				"Use this first to understand what is available before writing queries. " +
				"To fetch a macro body: FROM duckdb_functions() WHERE function_name='x' SELECT macro_definition.",
			InputSchema: buildSchema(), // no parameters
		},
		func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if ok, res := checkPerm(req, auth.OperationQuery); !ok {
				return res, nil
			}
			return runDatabaseInfoTool(dbMgr, resources, pubExportsURL)
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
			return runSummarizeTool(dbMgr, summarizeSQL)
		},
	)

	// --- schema ---
	srv.AddTool(
		&mcp.Tool{
			Name: "schema",
			Description: "Return tables and views with column names and types. " +
				"More efficient than calling describe() N times for databases with many tables. " +
				"Use table_pattern (LIKE filter, e.g. 'work%') to limit results. " +
				"By default returns a token-efficient dict format: {\"table\":[\"col:TYPE\",...]}. " +
				"Set compact='false' for full flat rows. Spans all attached databases.",
			InputSchema: buildSchema(
				strProp("table_pattern", "LIKE pattern to filter table names (e.g. 'works', 'work%')", false),
				strProp("compact", "Return compact dict {table:[col:TYPE,...]} instead of flat rows. 'true' or 'false' (default: true)", false),
			),
		},
		func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if ok, res := checkPerm(req, auth.OperationQuery); !ok {
				return res, nil
			}
			tablePattern := argString(req, "table_pattern", "")
			compact := argString(req, "compact", "true") == "true"

			patternClause := ""
			if tablePattern != "" {
				safe := strings.ReplaceAll(tablePattern, "'", "''")
				patternClause = fmt.Sprintf(" AND c.table_name LIKE '%s'", safe)
			}
			// Always select table_catalog for ordering and multi-catalog key disambiguation.
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
				// Omit table_catalog in flat mode; use database_info() for catalog context.
				flatSQL := fmt.Sprintf(`
					SELECT c.table_name, c.column_name, c.data_type
					FROM information_schema.columns c
					JOIN information_schema.tables t
						ON c.table_name = t.table_name AND c.table_schema = t.table_schema
					WHERE t.table_schema NOT IN ('information_schema', 'pg_catalog')
					  AND t.table_type IN ('BASE TABLE', 'VIEW')
					  AND c.table_name NOT IN ('api_keys', 'roles', 'permissions', 'trusted_users')
					%s
					ORDER BY c.table_catalog, c.table_name, c.ordinal_position
				`, patternClause)
				return runQueryTool(dbMgr, flatSQL, 10000)
			}

			_, rowData, _, err := queryRowsRaw(dbMgr, sql, 10000)
			if err != nil {
				return textResult("Error: " + err.Error()), nil
			}
			// Detect multi-catalog to qualify keys as "catalog.table" only when needed.
			seenCatalogs := map[string]bool{}
			for _, row := range rowData {
				if cat, _ := row["table_catalog"].(string); cat != "" {
					seenCatalogs[cat] = true
				}
			}
			multiCatalog := len(seenCatalogs) > 1
			type entry struct {
				name string
				cols []string
			}
			seen := map[string]int{}
			var order []entry
			for _, row := range rowData {
				catalog, _ := row["table_catalog"].(string)
				tableName, _ := row["table_name"].(string)
				colName, _ := row["column_name"].(string)
				dataType, _ := row["data_type"].(string)
				key := tableName
				if multiCatalog {
					key = catalog + "." + tableName
				}
				colEntry := colName + ":" + dataType
				if idx, ok := seen[key]; ok {
					order[idx].cols = append(order[idx].cols, colEntry)
				} else {
					seen[key] = len(order)
					order = append(order, entry{name: key, cols: []string{colEntry}})
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
				SELECT c.table_name, c.column_name, c.data_type, c.ordinal_position
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

	// --- server_status ---
	srv.AddTool(
		&mcp.Tool{
			Name: "server_status",
			Description: "Return current DuckDB memory usage and key runtime settings. " +
				"Call this before planning a sequence of heavy queries to understand available " +
				"memory headroom and whether a previous query has spilled to disk. " +
				"The 'spill_bytes' field is the key signal: a non-zero value means a prior " +
				"query overflowed RAM and performance may be degraded — simplify the next " +
				"query or add filters to reduce result size.",
			InputSchema: buildSchema(),
		},
		func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if ok, res := checkPerm(req, auth.OperationQuery); !ok {
				return res, nil
			}

			// Collect per-tag memory usage from duckdb_memory().
			_, memRows, _, err := queryRowsRaw(dbMgr, `
				SELECT tag, memory_usage_bytes, temporary_storage_bytes
				FROM duckdb_memory()
				ORDER BY tag
			`, 100)
			if err != nil {
				return textResult("Error querying duckdb_memory(): " + err.Error()), nil
			}

			type tagEntry struct {
				MemoryBytes int64 `json:"memory_bytes"`
				SpillBytes  int64 `json:"spill_bytes"`
			}
			tags := make(map[string]tagEntry)
			var totalMem, totalSpill int64
			for _, row := range memRows {
				tag, _ := row["tag"].(string)
				mem := toInt64(row["memory_usage_bytes"])
				spill := toInt64(row["temporary_storage_bytes"])
				tags[tag] = tagEntry{MemoryBytes: mem, SpillBytes: spill}
				totalMem += mem
				totalSpill += spill
			}

			// Collect key settings from duckdb_settings().
			_, settingRows, _, err := queryRowsRaw(dbMgr, `
				SELECT name, value
				FROM duckdb_settings()
				WHERE name IN ('memory_limit','threads','worker_threads','temp_directory','max_memory')
				ORDER BY name
			`, 20)
			if err != nil {
				return textResult("Error querying duckdb_settings(): " + err.Error()), nil
			}

			settings := make(map[string]string)
			for _, row := range settingRows {
				name, _ := row["name"].(string)
				val, _ := row["value"].(string)
				if name != "" {
					settings[name] = val
				}
			}

			out := map[string]interface{}{
				"memory_used_bytes": totalMem,
				"spill_bytes":       totalSpill,
				"memory_used_mb":    totalMem / (1024 * 1024),
				"spill_to_disk":     totalSpill > 0,
				"settings":          settings,
				"by_tag":            tags,
			}
			b, err := json.Marshal(out)
			if err != nil {
				return textResult("Error serializing server_status: " + err.Error()), nil
			}
			return textResult(string(b)), nil
		},
	)

	// --- explain ---
	srv.AddTool(
		&mcp.Tool{
			Name: "explain",
			Description: "Show the query plan for a SQL statement without executing it. " +
				"Use this to understand how DuckDB will execute a query — check for full table scans, " +
				"join order, and filter pushdown before running an expensive query. " +
				"Set analyze=true to actually execute the query and return real timing and row counts " +
				"(costs more but gives accurate statistics).",
			InputSchema: buildSchema(
				strProp("sql", "SQL statement to explain", true),
				boolProp("analyze", "If true, execute the query and return actual run-time statistics (EXPLAIN ANALYZE). Default false."),
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
			prefix := "EXPLAIN "
			if argBool(req, "analyze", false) {
				prefix = "EXPLAIN ANALYZE "
			}
			return runQueryTool(dbMgr, prefix+sql, 500)
		},
	)

	// --- view_definition ---
	srv.AddTool(
		&mcp.Tool{
			Name: "view_definition",
			Description: "Return the SQL definition of a view. " +
				"Use this to understand exactly what a view computes before querying it, " +
				"or to inspect its column derivations and joins. " +
				"Fully qualify the name with catalog prefix (e.g. diva.my_view) when multiple catalogs are attached.",
			InputSchema: buildSchema(
				strProp("view_name", "Name of the view (optionally catalog-qualified, e.g. diva.my_view)", true),
			),
		},
		func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if ok, res := checkPerm(req, auth.OperationQuery); !ok {
				return res, nil
			}
			raw := strings.TrimSpace(argString(req, "view_name", ""))
			if raw == "" {
				return textResult("Error: view_name argument is required"), nil
			}
			// Split optional catalog prefix: "catalog.view" or bare "view"
			catalog, name := "", raw
			if parts := strings.SplitN(raw, ".", 2); len(parts) == 2 {
				catalog, name = parts[0], parts[1]
			}
			var sql string
			if catalog != "" {
				sql = fmt.Sprintf(
					"FROM duckdb_views() SELECT view_name, sql AS definition "+
						"WHERE database_name = %s AND view_name = %s AND NOT internal",
					quoteLiteral(catalog), quoteLiteral(name),
				)
			} else {
				sql = fmt.Sprintf(
					"FROM duckdb_views() SELECT view_name, database_name, sql AS definition "+
						"WHERE view_name = %s AND NOT internal",
					quoteLiteral(name),
				)
			}
			return runQueryTool(dbMgr, sql, 10)
		},
	)

	// --- relationships ---
	srv.AddTool(
		&mcp.Tool{
			Name: "relationships",
			Description: "Discover implied join relationships by finding column names shared across multiple tables. " +
				"DuckDB databases (especially harvested or parquet-backed ones) rarely declare foreign keys, " +
				"so shared column names are the practical join graph. " +
				"Results are grouped by normalised (lower-case) column name and sorted by how many tables share it. " +
				"Use min_tables to raise the threshold (default 2). " +
				"Use table_name to focus on what a specific table can join to. " +
				"Use max_tables to filter out ubiquitous infrastructure columns (e.g. a 'pid' column in 60 tables).",
			InputSchema: buildSchema(
				strProp("table_name", "Optional: restrict to columns that appear in this table (bare or catalog-qualified)", false),
				numProp("min_tables", "Minimum number of tables sharing the column name (default 2)"),
				numProp("max_tables", "Maximum number of tables — omit or 0 to show all (useful to suppress universal key columns)"),
			),
		},
		func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if ok, res := checkPerm(req, auth.OperationQuery); !ok {
				return res, nil
			}
			minTables := argInt(req, "min_tables", 2)
			if minTables < 2 {
				minTables = 2
			}
			maxTables := argInt(req, "max_tables", 0)
			tableFilter := strings.TrimSpace(argString(req, "table_name", ""))

			// Strip optional catalog prefix for the table name filter
			filterName := tableFilter
			if parts := strings.SplitN(tableFilter, ".", 2); len(parts) == 2 {
				filterName = parts[1]
			}

			having := fmt.Sprintf("n_tables >= %d", minTables)
			if maxTables > 0 {
				having += fmt.Sprintf(" AND n_tables <= %d", maxTables)
			}
			if filterName != "" {
				having += fmt.Sprintf(" AND list_contains(tables, %s)", quoteLiteral(filterName))
			}

			sql := fmt.Sprintf(`
				FROM duckdb_columns()
				SELECT
					lower(column_name)                                    AS join_key,
					list(DISTINCT column_name ORDER BY column_name)       AS name_variants,
					list(DISTINCT table_name  ORDER BY table_name)        AS tables,
					list(DISTINCT data_type   ORDER BY data_type)         AS data_types,
					count(DISTINCT table_name)                            AS n_tables
				WHERE NOT internal
				GROUP BY lower(column_name)
				HAVING %s
				ORDER BY n_tables DESC, join_key`, having)
			return runQueryTool(dbMgr, sql, 200)
		},
	)

	// --- time_range ---
	srv.AddTool(
		&mcp.Tool{
			Name: "time_range",
			Description: "Summarise all DATE and TIMESTAMP columns in a table: min, max, span (as human-readable interval), " +
				"total rows, non-null count, and outlier count (years before 1900 or more than 2 years in the future). " +
				"Use this before writing time-filtered queries to understand the actual temporal extent of the data " +
				"and spot data quality issues like garbage dates (year 1000) or far-future pre-prints.",
			InputSchema: buildSchema(
				strProp("table_name", "Table name (bare or catalog-qualified, e.g. diva.pub)", true),
			),
		},
		func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if ok, res := checkPerm(req, auth.OperationQuery); !ok {
				return res, nil
			}
			raw := strings.TrimSpace(argString(req, "table_name", ""))
			if raw == "" {
				return textResult("Error: table_name argument is required"), nil
			}
			return runTimeRangeTool(dbMgr, raw)
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
// If the api_key query parameter is present and no X-API-Key header is set,
// it is promoted to the header so the MCP SDK forwards it in req.Extra.Header
// for per-tool defence-in-depth auth checks. This allows clients that cannot
// set custom headers (e.g. claude.ai remote MCP integration) to authenticate
// via query parameter, consistent with the REST API.
func (h *MCPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-API-Key") == "" {
		if key := r.URL.Query().Get("api_key"); key != "" {
			r = r.Clone(r.Context())
			r.Header.Set("X-API-Key", key)
		}
	}
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

// runDatabaseInfoTool returns a structured JSON overview: catalogs, search_path,
// tables, views, macros, and available MCP resources.
func runDatabaseInfoTool(dbMgr *database.Manager, resources []ResourceInfo, publicExportsURL string) (*mcp.CallToolResult, error) {
	run := func(sql string, limit int) ([]map[string]any, error) {
		_, rows, _, err := queryRowsRaw(dbMgr, sql, limit)
		return rows, err
	}

	catalogs, err := run(`
		FROM duckdb_databases()
		SELECT database_name, type, path, readonly
		WHERE NOT internal
		ORDER BY database_name
	`, 50)
	if err != nil {
		return textResult("Error: " + err.Error()), nil
	}

	settings, err := run(`SELECT current_setting('search_path') AS search_path, current_catalog`, 1)
	if err != nil {
		return textResult("Error: " + err.Error()), nil
	}

	// schema_name omitted — almost always 'main'; database_name already identifies the catalog.
	// estimated_size in duckdb_tables() is an estimated row count (not bytes).
	tables, err := run(`
		FROM duckdb_tables()
		SELECT database_name, table_name, comment, column_count, estimated_size AS estimated_row_count
		WHERE NOT internal
		  AND table_name NOT IN ('api_keys','roles','permissions','trusted_users')
		ORDER BY database_name, table_name
	`, 2000)
	if err != nil {
		return textResult("Error: " + err.Error()), nil
	}

	views, err := run(`
		FROM duckdb_views()
		SELECT database_name, view_name, comment
		WHERE NOT internal
		  AND view_name NOT IN ('api_keys','roles','permissions','trusted_users')
		ORDER BY database_name, view_name
	`, 2000)
	if err != nil {
		return textResult("Error: " + err.Error()), nil
	}

	// schema_name and macro_definition omitted for token efficiency.
	// Fetch a macro body: FROM duckdb_functions() WHERE function_name='x' SELECT macro_definition
	macros, err := run(`
		FROM duckdb_functions()
		SELECT database_name, function_name, function_type, description, parameters
		WHERE function_type IN ('table_macro','macro')
		  AND database_name NOT IN ('system')
		ORDER BY function_type, function_name
	`, 500)
	if err != nil {
		return textResult("Error: " + err.Error()), nil
	}

	extensions, err := run(`
		FROM duckdb_extensions()
		SELECT extension_name, extension_version
		WHERE loaded = true
		ORDER BY extension_name
	`, 100)
	if err != nil {
		return textResult("Error: " + err.Error()), nil
	}

	// Enrich macros with operator-provided descriptions and param_types from the
	// macro_descriptions table (memory.macro_descriptions). This table is
	// deployment-specific so errors are silently ignored.
	if macroDescs, err := run(`FROM macro_descriptions SELECT macro_name, description, param_types`, 500); err == nil {
		descByName := make(map[string]map[string]any, len(macroDescs))
		for _, d := range macroDescs {
			if name, ok := d["macro_name"].(string); ok {
				descByName[name] = d
			}
		}
		for _, m := range macros {
			if name, ok := m["function_name"].(string); ok {
				if d, found := descByName[name]; found {
					if desc := d["description"]; desc != nil {
						m["description"] = desc
					}
					if pt := d["param_types"]; pt != nil {
						m["param_types"] = pt
					}
				}
			}
		}
	}

	// Strip null-valued fields from all row maps to reduce token count.
	// (e.g. "comment":null and "description":null on every row adds significant noise)
	for _, section := range [][]map[string]any{tables, views, macros, catalogs} {
		for _, row := range section {
			for k, v := range row {
				if v == nil {
					delete(row, k)
				}
			}
		}
	}

	out := map[string]any{
		"catalogs":   catalogs,
		"tables":     tables,
		"views":      views,
		"macros":     macros,
		"extensions": extensions,
	}
	if len(settings) > 0 {
		out["search_path"] = settings[0]["search_path"]
		out["current_catalog"] = settings[0]["current_catalog"]
	}
	if len(resources) > 0 {
		type resInfo struct {
			URI         string `json:"uri"`
			Name        string `json:"name"`
			Description string `json:"description"`
		}
		rs := make([]resInfo, len(resources))
		for i, r := range resources {
			rs[i] = resInfo{URI: r.URI, Name: r.Name, Description: r.Description}
		}
		out["resources"] = rs
	}
	if publicExportsURL != "" {
		out["export_base_url"] = publicExportsURL
	}

	b, err := json.Marshal(out)
	if err != nil {
		return textResult("Error: " + err.Error()), nil
	}
	return textResult(string(b)), nil
}

// runSummarizeTool runs a SUMMARIZE query and strips null-valued stat fields
// from each row so non-numeric columns don't produce noise like "avg": null.
func runSummarizeTool(dbMgr *database.Manager, sql string) (*mcp.CallToolResult, error) {
	cols, data, _, err := queryRowsRaw(dbMgr, sql, 500)
	if err != nil {
		return textResult("Error: " + err.Error()), nil
	}
	// Strip numeric-only stats when NULL (avg, std, q25, median, q75 are NULL for
	// non-numeric columns). Preserve count, null_count, approx_unique, min, max —
	// these are meaningful for all types including varchar.
	numericOnlyStats := map[string]bool{"avg": true, "std": true, "q25": true, "median": true, "q75": true}
	for _, row := range data {
		for k, v := range row {
			if v == nil && numericOnlyStats[k] {
				delete(row, k)
			}
		}
	}
	b, err := json.Marshal(map[string]any{
		"columns": cols,
		"rows":    data,
		"count":   len(data),
	})
	if err != nil {
		return textResult("Error: " + err.Error()), nil
	}
	return textResult(string(b)), nil
}

// runTimeRangeTool detects all DATE/TIMESTAMP columns in the given table via
// duckdb_columns(), then builds a single UNION ALL query to return min, max,
// span (as varchar interval), total rows, non-null count, and outlier count
// (year < 1900 or year > current_year + 2) for each column.
func runTimeRangeTool(dbMgr *database.Manager, tableName string) (*mcp.CallToolResult, error) {
	// Split optional catalog prefix
	catalog, name := "", tableName
	if parts := strings.SplitN(tableName, ".", 2); len(parts) == 2 {
		catalog, name = parts[0], parts[1]
	}

	// Fetch date/timestamp columns for this table
	var colSQL string
	if catalog != "" {
		colSQL = fmt.Sprintf(
			"FROM duckdb_columns() SELECT column_name "+
				"WHERE database_name = %s AND table_name = %s AND NOT internal "+
				"AND data_type IN ('DATE','TIMESTAMP','TIMESTAMP WITH TIME ZONE','TIMESTAMPTZ') "+
				"ORDER BY column_index",
			quoteLiteral(catalog), quoteLiteral(name),
		)
	} else {
		colSQL = fmt.Sprintf(
			"FROM duckdb_columns() SELECT column_name "+
				"WHERE table_name = %s AND NOT internal "+
				"AND data_type IN ('DATE','TIMESTAMP','TIMESTAMP WITH TIME ZONE','TIMESTAMPTZ') "+
				"ORDER BY column_index",
			quoteLiteral(name),
		)
	}
	_, colRows, _, err := queryRowsRaw(dbMgr, colSQL, 100)
	if err != nil {
		return textResult("Error fetching columns: " + err.Error()), nil
	}
	if len(colRows) == 0 {
		return textResult("No DATE or TIMESTAMP columns found in " + tableName), nil
	}

	// Build UNION ALL query — one SELECT per date column
	var parts []string
	futureYear := "EXTRACT('year' FROM current_date) + 2"
	for _, row := range colRows {
		col, ok := row["column_name"].(string)
		if !ok {
			continue
		}
		// Quote column name in case it contains mixed case or reserved words
		qcol := `"` + strings.ReplaceAll(col, `"`, `""`) + `"`
		frag := fmt.Sprintf(`
			SELECT
				%s                                                                  AS column_name,
				MIN(%s)::VARCHAR                                                    AS min,
				MAX(%s)::VARCHAR                                                    AS max,
				age(MAX(%s)::DATE, MIN(%s)::DATE)::VARCHAR                         AS span,
				COUNT(*)                                                            AS total_rows,
				COUNT(%s)                                                           AS non_null,
				COUNT(*) FILTER (
					EXTRACT('year' FROM %s) < 1900 OR
					EXTRACT('year' FROM %s) > %s
				)                                                                   AS outliers
			FROM %s`,
			quoteLiteral(col), qcol, qcol, qcol, qcol, qcol, qcol, qcol, futureYear, tableName)
		parts = append(parts, frag)
	}
	unionSQL := strings.Join(parts, "\nUNION ALL\n")

	_, data, _, err := queryRowsRaw(dbMgr, unionSQL, 100)
	if err != nil {
		return textResult("Error: " + err.Error()), nil
	}
	b, err := json.Marshal(map[string]any{
		"table":   tableName,
		"columns": data,
	})
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
	"server_status": true,
	"explain":       true, "view_definition": true,
	"relationships": true, "time_range": true,
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
