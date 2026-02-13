package database

import (
	"database/sql"
	"fmt"
	"math"
	"strings"
	"time"

	"go.uber.org/zap"
)

// InsertResult represents the result of an insert operation.
type InsertResult struct {
	RowsAffected int64
}

// UpdateResult represents the result of an update operation.
type UpdateResult struct {
	RowsAffected int64
}

// DeleteResult represents the result of a delete operation.
type DeleteResult struct {
	RowsAffected int64
}

const (
	maxRetries     = 3
	baseRetryDelay = 50 * time.Millisecond
)

// isTransactionConflict checks if an error is a DuckDB transaction conflict.
func isTransactionConflict(err error) bool {
	if err == nil {
		return false
	}
	// DuckDB transaction conflict errors contain "Transaction conflict" or "Conflict"
	errStr := strings.ToLower(err.Error())
	return strings.Contains(errStr, "transaction conflict") ||
		strings.Contains(errStr, "conflict on table")
}

// retryOnConflict executes a function with exponential backoff retry on transaction conflicts.
func retryOnConflict(fn func() error) error {
	var lastErr error
	for attempt := 0; attempt < maxRetries; attempt++ {
		err := fn()
		if err == nil {
			return nil
		}

		if !isTransactionConflict(err) {
			// Not a conflict, return immediately
			return err
		}

		lastErr = err
		if attempt < maxRetries-1 {
			// Exponential backoff: 50ms, 100ms, 200ms
			delay := baseRetryDelay * time.Duration(math.Pow(2, float64(attempt)))
			time.Sleep(delay)
		}
	}
	return fmt.Errorf("transaction failed after %d retries: %w", maxRetries, lastErr)
}

// Insert inserts a single row into the specified table.
// Automatically retries on transaction conflicts with exponential backoff.
// Uses prepared statements with schema normalization for optimal performance.
// User API: clients can omit nullable columns - they will be set to NULL internally.
func (m *Manager) Insert(table string, data map[string]interface{}) (*InsertResult, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("no data provided for insert")
	}

	// Get table schema for normalization
	columns, err := m.getTableColumns(table)
	if err != nil {
		return nil, fmt.Errorf("failed to get table schema: %w", err)
	}

	var result *InsertResult
	err = retryOnConflict(func() error {
		// Get or create prepared statement for this table
		stmt, err := m.getOrPrepareInsert(table, columns)
		if err != nil {
			return fmt.Errorf("failed to prepare insert statement: %w", err)
		}

		// Normalize data to match all columns (NULL for omitted columns)
		values := make([]interface{}, len(columns))
		for i, col := range columns {
			if val, ok := data[col]; ok {
				values[i] = val
			} else {
				values[i] = nil // NULL for omitted columns
			}
		}

		// Use transaction for atomicity
		tx, err := m.BeginTxMain()
		if err != nil {
			return fmt.Errorf("failed to begin transaction: %w", err)
		}
		defer tx.Rollback()

		// Use the prepared statement within the transaction
		txStmt := tx.Stmt(stmt)
		execResult, err := txStmt.Exec(values...)
		if err != nil {
			return fmt.Errorf("failed to execute insert: %w", err)
		}

		if err := tx.Commit(); err != nil {
			return fmt.Errorf("failed to commit transaction: %w", err)
		}

		rowsAffected, _ := execResult.RowsAffected()
		result = &InsertResult{RowsAffected: rowsAffected}
		return nil
	})

	return result, err
}

// getOrPrepareInsert gets or creates a prepared INSERT statement for a table.
func (m *Manager) getOrPrepareInsert(table string, columns []string) (*sql.Stmt, error) {
	stmtKey := fmt.Sprintf("%s:insert", table)

	// Check cache first
	if cached, ok := m.preparedStmts.Load(stmtKey); ok {
		return cached.(*sql.Stmt), nil
	}

	// Build INSERT query with all columns
	placeholders := make([]string, len(columns))
	for i := range columns {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
	}

	query := fmt.Sprintf(
		"INSERT INTO %s (%s) VALUES (%s)",
		table,
		strings.Join(columns, ", "),
		strings.Join(placeholders, ", "),
	)

	// Prepare statement
	stmt, err := m.mainDB.Prepare(query)
	if err != nil {
		return nil, fmt.Errorf("failed to prepare statement: %w", err)
	}

	// Store in cache
	m.preparedStmts.Store(stmtKey, stmt)

	m.logger.Debug("Prepared INSERT statement",
		zap.String("table", table),
		zap.Int("columns", len(columns)),
	)

	return stmt, nil
}

// Update updates rows in the specified table based on the where clause.
// Automatically retries on transaction conflicts with exponential backoff.
// Uses prepared statements for common UPDATE patterns (cached by column signature).
// Deprecated: Use UpdateWithFilters for full operator support.
func (m *Manager) Update(table string, set map[string]interface{}, where map[string]interface{}) (*UpdateResult, error) {
	if len(set) == 0 {
		return nil, fmt.Errorf("no data provided for update")
	}
	if len(where) == 0 {
		return nil, fmt.Errorf("no where clause provided for update (use DELETE with caution)")
	}

	var result *UpdateResult
	err := retryOnConflict(func() error {
		// Try to get or prepare an UPDATE statement for this column pattern
		stmt, setCols, whereCols, err := m.getOrPrepareUpdate(table, set, where)
		if err != nil {
			return fmt.Errorf("failed to prepare update statement: %w", err)
		}

		// Build values array in the order expected by the prepared statement
		values := make([]interface{}, 0, len(set)+len(where))
		for _, col := range setCols {
			values = append(values, set[col])
		}
		for _, col := range whereCols {
			values = append(values, where[col])
		}

		// Use transaction for atomicity
		tx, err := m.BeginTxMain()
		if err != nil {
			return fmt.Errorf("failed to begin transaction: %w", err)
		}
		defer tx.Rollback()

		// Use the prepared statement within the transaction
		txStmt := tx.Stmt(stmt)
		execResult, err := txStmt.Exec(values...)
		if err != nil {
			return fmt.Errorf("failed to execute update: %w", err)
		}

		if err := tx.Commit(); err != nil {
			return fmt.Errorf("failed to commit transaction: %w", err)
		}

		rowsAffected, _ := execResult.RowsAffected()
		result = &UpdateResult{RowsAffected: rowsAffected}
		return nil
	})

	return result, err
}

// UpdateWithFilters updates rows in the specified table based on filter conditions.
// Supports all filter operators (eq, ne, gt, gte, lt, lte, like, in).
// Automatically retries on transaction conflicts with exponential backoff.
func (m *Manager) UpdateWithFilters(table string, set map[string]interface{}, filters []Filter) (*UpdateResult, error) {
	if len(set) == 0 {
		return nil, fmt.Errorf("no data provided for update")
	}
	if len(filters) == 0 {
		return nil, fmt.Errorf("no filters provided for update (safety check)")
	}

	// Build SET clause with stable column order
	setCols := make([]string, 0, len(set))
	for col := range set {
		setCols = append(setCols, col)
	}
	// Sort for consistent query generation
	sortStrings := func(s []string) {
		for i := 0; i < len(s); i++ {
			for j := i + 1; j < len(s); j++ {
				if s[i] > s[j] {
					s[i], s[j] = s[j], s[i]
				}
			}
		}
	}
	sortStrings(setCols)

	// Build UPDATE query dynamically
	query := fmt.Sprintf("UPDATE %s SET ", table)
	values := make([]interface{}, 0)
	paramIndex := 1

	// Build SET clause
	setClauses := make([]string, len(setCols))
	for i, col := range setCols {
		setClauses[i] = fmt.Sprintf("%s = $%d", col, paramIndex)
		values = append(values, set[col])
		paramIndex++
	}
	query += strings.Join(setClauses, ", ")

	// Build WHERE clause from filters
	whereClauses := make([]string, 0, len(filters))
	for _, f := range filters {
		clause, val := f.ToSQL(paramIndex)
		whereClauses = append(whereClauses, clause)
		if val != nil {
			values = append(values, val)
			paramIndex++
		}
	}
	query += " WHERE " + strings.Join(whereClauses, " AND ")

	var result *UpdateResult
	err := retryOnConflict(func() error {
		// Use transaction for atomicity
		tx, err := m.BeginTxMain()
		if err != nil {
			return fmt.Errorf("failed to begin transaction: %w", err)
		}
		defer tx.Rollback()

		execResult, err := tx.Exec(query, values...)
		if err != nil {
			return fmt.Errorf("failed to execute update: %w", err)
		}

		if err := tx.Commit(); err != nil {
			return fmt.Errorf("failed to commit transaction: %w", err)
		}

		rowsAffected, _ := execResult.RowsAffected()
		result = &UpdateResult{RowsAffected: rowsAffected}
		return nil
	})

	return result, err
}

// getOrPrepareUpdate gets or creates a prepared UPDATE statement for a specific column pattern.
// Returns the statement and the ordered column lists for SET and WHERE clauses.
func (m *Manager) getOrPrepareUpdate(table string, set map[string]interface{}, where map[string]interface{}) (*sql.Stmt, []string, []string, error) {
	// Create stable column lists (sorted to ensure consistent cache keys)
	setCols := make([]string, 0, len(set))
	for col := range set {
		setCols = append(setCols, col)
	}
	whereCols := make([]string, 0, len(where))
	for col := range where {
		whereCols = append(whereCols, col)
	}

	// Sort for cache key stability
	sortStrings := func(s []string) {
		for i := 0; i < len(s); i++ {
			for j := i + 1; j < len(s); j++ {
				if s[i] > s[j] {
					s[i], s[j] = s[j], s[i]
				}
			}
		}
	}
	sortStrings(setCols)
	sortStrings(whereCols)

	// Build cache key based on column pattern
	stmtKey := fmt.Sprintf("%s:update:set=%s:where=%s", table, strings.Join(setCols, ","), strings.Join(whereCols, ","))

	// Check cache first
	if cached, ok := m.preparedStmts.Load(stmtKey); ok {
		return cached.(*sql.Stmt), setCols, whereCols, nil
	}

	// Build UPDATE query
	setClauses := make([]string, len(setCols))
	for i, col := range setCols {
		setClauses[i] = fmt.Sprintf("%s = $%d", col, i+1)
	}

	whereClauses := make([]string, len(whereCols))
	for i, col := range whereCols {
		whereClauses[i] = fmt.Sprintf("%s = $%d", col, len(setCols)+i+1)
	}

	query := fmt.Sprintf(
		"UPDATE %s SET %s WHERE %s",
		table,
		strings.Join(setClauses, ", "),
		strings.Join(whereClauses, " AND "),
	)

	// Prepare statement
	stmt, err := m.mainDB.Prepare(query)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to prepare statement: %w", err)
	}

	// Store in cache
	m.preparedStmts.Store(stmtKey, stmt)

	m.logger.Debug("Prepared UPDATE statement",
		zap.String("table", table),
		zap.Int("set_columns", len(setCols)),
		zap.Int("where_columns", len(whereCols)),
	)

	return stmt, setCols, whereCols, nil
}

// Delete deletes rows from the specified table based on the where clause.
// Automatically retries on transaction conflicts with exponential backoff.
// Uses prepared statements for common DELETE patterns (cached by column signature).
// Deprecated: Use DeleteWithFilters for full operator support.
func (m *Manager) Delete(table string, where map[string]interface{}) (*DeleteResult, error) {
	if len(where) == 0 {
		return nil, fmt.Errorf("no where clause provided for delete (safety check)")
	}

	var result *DeleteResult
	err := retryOnConflict(func() error {
		// Try to get or prepare a DELETE statement for this column pattern
		stmt, whereCols, err := m.getOrPrepareDelete(table, where)
		if err != nil {
			return fmt.Errorf("failed to prepare delete statement: %w", err)
		}

		// Build values array in the order expected by the prepared statement
		values := make([]interface{}, len(whereCols))
		for i, col := range whereCols {
			values[i] = where[col]
		}

		// Use transaction for atomicity
		tx, err := m.BeginTxMain()
		if err != nil {
			return fmt.Errorf("failed to begin transaction: %w", err)
		}
		defer tx.Rollback()

		// Use the prepared statement within the transaction
		txStmt := tx.Stmt(stmt)
		execResult, err := txStmt.Exec(values...)
		if err != nil {
			return fmt.Errorf("failed to execute delete: %w", err)
		}

		if err := tx.Commit(); err != nil {
			return fmt.Errorf("failed to commit transaction: %w", err)
		}

		rowsAffected, _ := execResult.RowsAffected()
		result = &DeleteResult{RowsAffected: rowsAffected}
		return nil
	})

	return result, err
}

// DeleteWithFilters deletes rows from the specified table based on filter conditions.
// Supports all filter operators (eq, ne, gt, gte, lt, lte, like, in).
// Automatically retries on transaction conflicts with exponential backoff.
func (m *Manager) DeleteWithFilters(table string, filters []Filter) (*DeleteResult, error) {
	if len(filters) == 0 {
		return nil, fmt.Errorf("no filters provided for delete (safety check)")
	}

	// Build DELETE query dynamically based on filters
	query := fmt.Sprintf("DELETE FROM %s", table)
	values := make([]interface{}, 0)
	paramIndex := 1

	// Build WHERE clause from filters
	whereClauses := make([]string, 0, len(filters))
	for _, f := range filters {
		clause, val := f.ToSQL(paramIndex)
		whereClauses = append(whereClauses, clause)
		if val != nil {
			values = append(values, val)
			paramIndex++
		}
	}
	query += " WHERE " + strings.Join(whereClauses, " AND ")

	var result *DeleteResult
	err := retryOnConflict(func() error {
		// Use transaction for atomicity
		tx, err := m.BeginTxMain()
		if err != nil {
			return fmt.Errorf("failed to begin transaction: %w", err)
		}
		defer tx.Rollback()

		execResult, err := tx.Exec(query, values...)
		if err != nil {
			return fmt.Errorf("failed to execute delete: %w", err)
		}

		if err := tx.Commit(); err != nil {
			return fmt.Errorf("failed to commit transaction: %w", err)
		}

		rowsAffected, _ := execResult.RowsAffected()
		result = &DeleteResult{RowsAffected: rowsAffected}
		return nil
	})

	return result, err
}

// CountWithFilters returns the count of rows matching the given filters.
// Useful for dry-run delete operations to preview affected rows.
func (m *Manager) CountWithFilters(table string, filters []Filter) (int64, error) {
	return m.Count(table, filters)
}

// getOrPrepareDelete gets or creates a prepared DELETE statement for a specific column pattern.
// Returns the statement and the ordered column list for WHERE clause.
func (m *Manager) getOrPrepareDelete(table string, where map[string]interface{}) (*sql.Stmt, []string, error) {
	// Create stable column list (sorted to ensure consistent cache keys)
	whereCols := make([]string, 0, len(where))
	for col := range where {
		whereCols = append(whereCols, col)
	}

	// Sort for cache key stability
	sortStrings := func(s []string) {
		for i := 0; i < len(s); i++ {
			for j := i + 1; j < len(s); j++ {
				if s[i] > s[j] {
					s[i], s[j] = s[j], s[i]
				}
			}
		}
	}
	sortStrings(whereCols)

	// Build cache key based on column pattern
	stmtKey := fmt.Sprintf("%s:delete:where=%s", table, strings.Join(whereCols, ","))

	// Check cache first
	if cached, ok := m.preparedStmts.Load(stmtKey); ok {
		return cached.(*sql.Stmt), whereCols, nil
	}

	// Build DELETE query
	whereClauses := make([]string, len(whereCols))
	for i, col := range whereCols {
		whereClauses[i] = fmt.Sprintf("%s = $%d", col, i+1)
	}

	query := fmt.Sprintf(
		"DELETE FROM %s WHERE %s",
		table,
		strings.Join(whereClauses, " AND "),
	)

	// Prepare statement
	stmt, err := m.mainDB.Prepare(query)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to prepare statement: %w", err)
	}

	// Store in cache
	m.preparedStmts.Store(stmtKey, stmt)

	m.logger.Debug("Prepared DELETE statement",
		zap.String("table", table),
		zap.Int("where_columns", len(whereCols)),
	)

	return stmt, whereCols, nil
}

// Select executes a SELECT query with optional column selection, filters, sorting, and pagination.
// If columns is nil or empty, SELECT * is used.
// This is a read-only operation and does not use transactions for better performance.
func (m *Manager) Select(table string, columns []string, filters []Filter, sorts []Sort, limit, offset int) (*sql.Rows, error) {
	// Build column list
	columnList := "*"
	if len(columns) > 0 {
		columnList = strings.Join(columns, ", ")
	}

	query := fmt.Sprintf("SELECT %s FROM %s", columnList, table)
	values := make([]interface{}, 0)
	paramIndex := 1

	// Add WHERE clause if filters exist
	if len(filters) > 0 {
		whereClauses := make([]string, 0, len(filters))
		for _, f := range filters {
			clause, val := f.ToSQL(paramIndex)
			whereClauses = append(whereClauses, clause)
			if val != nil {
				values = append(values, val)
				paramIndex++
			}
		}
		query += " WHERE " + strings.Join(whereClauses, " AND ")
	}

	// Add ORDER BY clause if sorts exist
	if len(sorts) > 0 {
		sortClauses := make([]string, 0, len(sorts))
		for _, s := range sorts {
			sortClauses = append(sortClauses, s.ToSQL())
		}
		query += " ORDER BY " + strings.Join(sortClauses, ", ")
	}

	// Add LIMIT and OFFSET
	if limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", limit)
	}
	if offset > 0 {
		query += fmt.Sprintf(" OFFSET %d", offset)
	}

	return m.QueryMain(query, values...)
}

// CursorInfo contains cursor state for keyset pagination.
type CursorInfo struct {
	SortColumns    []string
	SortValues     []interface{}
	SortDirections []string
	Offset         int
}

// SelectWithCursor executes a SELECT query with cursor-based (keyset) pagination.
// This provides efficient pagination for large datasets by using WHERE conditions
// instead of OFFSET.
func (m *Manager) SelectWithCursor(table string, columns []string, filters []Filter, sorts []Sort, limit int, cursor *CursorInfo) (*sql.Rows, error) {
	// Build column list
	columnList := "*"
	if len(columns) > 0 {
		columnList = strings.Join(columns, ", ")
	}

	query := fmt.Sprintf("SELECT %s FROM %s", columnList, table)
	values := make([]interface{}, 0)
	paramIndex := 1

	// Collect all WHERE conditions
	whereClauses := make([]string, 0)

	// Add filter conditions
	if len(filters) > 0 {
		for _, f := range filters {
			clause, val := f.ToSQL(paramIndex)
			whereClauses = append(whereClauses, clause)
			if val != nil {
				values = append(values, val)
				paramIndex++
			}
		}
	}

	// Add cursor condition for keyset pagination
	if cursor != nil && len(cursor.SortColumns) > 0 && len(cursor.SortValues) > 0 {
		cursorCondition, cursorValues := buildCursorCondition(cursor, paramIndex)
		if cursorCondition != "" {
			whereClauses = append(whereClauses, cursorCondition)
			values = append(values, cursorValues...)
			paramIndex += len(cursorValues)
		}
	}

	// Add WHERE clause if conditions exist
	if len(whereClauses) > 0 {
		query += " WHERE " + strings.Join(whereClauses, " AND ")
	}

	// Add ORDER BY clause - required for cursor pagination
	if len(sorts) > 0 {
		sortClauses := make([]string, 0, len(sorts))
		for _, s := range sorts {
			sortClauses = append(sortClauses, s.ToSQL())
		}
		query += " ORDER BY " + strings.Join(sortClauses, ", ")
	}

	// Add LIMIT (fetch one extra to detect if there are more results)
	if limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", limit+1)
	}

	return m.QueryMain(query, values...)
}

// buildCursorCondition builds the WHERE condition for keyset pagination.
// For a cursor with columns (a, b) and values (va, vb) with ASC direction:
// WHERE (a > va) OR (a = va AND b > vb)
func buildCursorCondition(cursor *CursorInfo, startParamIndex int) (string, []interface{}) {
	if cursor == nil || len(cursor.SortColumns) == 0 {
		return "", nil
	}

	values := make([]interface{}, 0)
	conditions := make([]string, 0)
	paramIndex := startParamIndex

	// Build progressive conditions for each sort column
	for i := range cursor.SortColumns {
		// Build equality conditions for all previous columns
		eqParts := make([]string, 0)
		for j := 0; j < i; j++ {
			eqParts = append(eqParts, fmt.Sprintf("%s = $%d", cursor.SortColumns[j], paramIndex))
			values = append(values, cursor.SortValues[j])
			paramIndex++
		}

		// Add the comparison condition for current column
		dir := "asc"
		if i < len(cursor.SortDirections) {
			dir = strings.ToLower(cursor.SortDirections[i])
		}

		op := ">"
		if dir == "desc" {
			op = "<"
		}

		compPart := fmt.Sprintf("%s %s $%d", cursor.SortColumns[i], op, paramIndex)
		values = append(values, cursor.SortValues[i])
		paramIndex++

		// Combine: (eq1 AND eq2 AND ... AND comp)
		if len(eqParts) > 0 {
			conditions = append(conditions, "("+strings.Join(eqParts, " AND ")+" AND "+compPart+")")
		} else {
			conditions = append(conditions, "("+compPart+")")
		}
	}

	// Combine all conditions with OR
	if len(conditions) == 0 {
		return "", nil
	}

	return "(" + strings.Join(conditions, " OR ") + ")", values
}

// Count returns the total number of rows in a table matching the filters.
// This is a read-only operation and does not use transactions for better performance.
func (m *Manager) Count(table string, filters []Filter) (int64, error) {
	query := fmt.Sprintf("SELECT COUNT(*) FROM %s", table)
	values := make([]interface{}, 0)
	paramIndex := 1

	// Add WHERE clause if filters exist
	if len(filters) > 0 {
		whereClauses := make([]string, 0, len(filters))
		for _, f := range filters {
			clause, val := f.ToSQL(paramIndex)
			whereClauses = append(whereClauses, clause)
			if val != nil {
				values = append(values, val)
				paramIndex++
			}
		}
		query += " WHERE " + strings.Join(whereClauses, " AND ")
	}

	var count int64
	err := m.QueryRowScanMain(query, []interface{}{&count}, values...)
	return count, err
}

// Filter represents a query filter.
type Filter struct {
	Column   string
	Operator string
	Value    interface{}
}

// ToSQL converts the filter to SQL.
func (f Filter) ToSQL(paramIndex int) (string, interface{}) {
	switch f.Operator {
	case "eq":
		return fmt.Sprintf("%s = $%d", f.Column, paramIndex), f.Value
	case "ne":
		return fmt.Sprintf("%s != $%d", f.Column, paramIndex), f.Value
	case "gt":
		return fmt.Sprintf("%s > $%d", f.Column, paramIndex), f.Value
	case "gte":
		return fmt.Sprintf("%s >= $%d", f.Column, paramIndex), f.Value
	case "lt":
		return fmt.Sprintf("%s < $%d", f.Column, paramIndex), f.Value
	case "lte":
		return fmt.Sprintf("%s <= $%d", f.Column, paramIndex), f.Value
	case "like":
		return fmt.Sprintf("%s LIKE $%d", f.Column, paramIndex), f.Value
	case "in":
		// For IN operator, value should be a slice
		return fmt.Sprintf("%s IN $%d", f.Column, paramIndex), f.Value
	default:
		return fmt.Sprintf("%s = $%d", f.Column, paramIndex), f.Value
	}
}

// Sort represents a sort order.
type Sort struct {
	Column    string
	Direction string
}

// ToSQL converts the sort to SQL.
func (s Sort) ToSQL() string {
	dir := "ASC"
	if strings.ToLower(s.Direction) == "desc" {
		dir = "DESC"
	}
	return fmt.Sprintf("%s %s", s.Column, dir)
}

// MacroInfo represents information about a table macro.
type MacroInfo struct {
	Name       string   `json:"name"`
	Parameters []string `json:"parameters"`
}

// ViewInfo represents information about a view.
type ViewInfo struct {
	Name string `json:"name"`
}

// TableInfo represents information about a table or view accessible via /api/{name}.
type TableInfo struct {
	Name     string `json:"name"`
	Type     string `json:"type"`                // "table" or "view"
	ReadOnly bool   `json:"read_only,omitempty"` // true for views
}

// ListAPIMacros returns all table macros with api_ prefix in the main schema.
func (m *Manager) ListAPIMacros() ([]MacroInfo, error) {
	query := `
		SELECT function_name, parameters
		FROM duckdb_functions()
		WHERE function_type = 'table_macro'
		  AND schema_name = 'main'
		  AND function_name LIKE 'api_%'
		ORDER BY function_name
	`

	rows, err := m.QueryMain(query)
	if err != nil {
		return nil, fmt.Errorf("failed to list macros: %w", err)
	}
	defer rows.Close()

	macros := make([]MacroInfo, 0)
	for rows.Next() {
		var name string
		var params interface{}
		if err := rows.Scan(&name, &params); err != nil {
			return nil, fmt.Errorf("failed to scan macro info: %w", err)
		}

		// Convert parameters to string slice
		paramsList := make([]string, 0)
		if params != nil {
			// DuckDB returns parameters as a list
			if paramSlice, ok := params.([]interface{}); ok {
				for _, p := range paramSlice {
					if pStr, ok := p.(string); ok {
						paramsList = append(paramsList, pStr)
					}
				}
			}
		}

		macros = append(macros, MacroInfo{
			Name:       name,
			Parameters: paramsList,
		})
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating macros: %w", err)
	}

	return macros, nil
}

// ExecuteMacro executes a table macro with the given parameters.
// Only macros with api_ prefix are allowed.
func (m *Manager) ExecuteMacro(name string, params map[string]string, limit, offset int) (*sql.Rows, error) {
	// Security check: only allow api_ prefixed macros
	if !strings.HasPrefix(name, "api_") {
		return nil, fmt.Errorf("only api_ prefixed macros are allowed")
	}

	// Build parameter list for the macro call
	paramValues := make([]interface{}, 0)
	paramPlaceholders := make([]string, 0)
	i := 1
	for _, v := range params {
		paramPlaceholders = append(paramPlaceholders, fmt.Sprintf("$%d", i))
		paramValues = append(paramValues, v)
		i++
	}

	// Build query: SELECT * FROM macro_name(params)
	var query string
	if len(paramPlaceholders) > 0 {
		query = fmt.Sprintf("SELECT * FROM %s(%s)", name, strings.Join(paramPlaceholders, ", "))
	} else {
		query = fmt.Sprintf("SELECT * FROM %s()", name)
	}

	// Add LIMIT and OFFSET
	if limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", limit)
	}
	if offset > 0 {
		query += fmt.Sprintf(" OFFSET %d", offset)
	}

	return m.QueryMain(query, paramValues...)
}

// ListTables returns all user tables and non-api views in the main schema.
// Excludes internal system tables. Views with api_ prefix are excluded as
// they are served separately via the /view/ endpoint.
func (m *Manager) ListTables() ([]TableInfo, error) {
	query := `
		SELECT table_name, table_type
		FROM information_schema.tables
		WHERE table_schema = 'main'
		  AND (table_type = 'BASE TABLE'
		    OR (table_type = 'VIEW' AND table_name NOT LIKE 'api_%'))
		ORDER BY table_name
	`

	rows, err := m.QueryMain(query)
	if err != nil {
		return nil, fmt.Errorf("failed to list tables: %w", err)
	}
	defer rows.Close()

	tables := make([]TableInfo, 0)
	for rows.Next() {
		var name, tableType string
		if err := rows.Scan(&name, &tableType); err != nil {
			return nil, fmt.Errorf("failed to scan table info: %w", err)
		}

		isView := tableType == "VIEW"
		t := TableInfo{
			Name: name,
			Type: "table",
		}
		if isView {
			t.Type = "view"
			t.ReadOnly = true
		}
		tables = append(tables, t)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating tables: %w", err)
	}

	return tables, nil
}

// ListAPIViews returns all views with api_ prefix in the main schema.
func (m *Manager) ListAPIViews() ([]ViewInfo, error) {
	query := `
		SELECT view_name
		FROM duckdb_views()
		WHERE schema_name = 'main'
		  AND view_name LIKE 'api_%'
		ORDER BY view_name
	`

	rows, err := m.QueryMain(query)
	if err != nil {
		return nil, fmt.Errorf("failed to list views: %w", err)
	}
	defer rows.Close()

	views := make([]ViewInfo, 0)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("failed to scan view info: %w", err)
		}

		views = append(views, ViewInfo{
			Name: name,
		})
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating views: %w", err)
	}

	return views, nil
}

// QueryView executes a SELECT query on a view with optional filters, sorting, and pagination.
// Only views with api_ prefix are allowed.
func (m *Manager) QueryView(name string, columns []string, filters []Filter, sorts []Sort, limit, offset int) (*sql.Rows, error) {
	// Security check: only allow api_ prefixed views
	if !strings.HasPrefix(name, "api_") {
		return nil, fmt.Errorf("only api_ prefixed views are allowed")
	}

	// Build column list
	columnList := "*"
	if len(columns) > 0 {
		columnList = strings.Join(columns, ", ")
	}

	query := fmt.Sprintf("SELECT %s FROM %s", columnList, name)
	values := make([]interface{}, 0)
	paramIndex := 1

	// Add WHERE clause if filters exist
	if len(filters) > 0 {
		whereClauses := make([]string, 0, len(filters))
		for _, f := range filters {
			clause, val := f.ToSQL(paramIndex)
			whereClauses = append(whereClauses, clause)
			if val != nil {
				values = append(values, val)
				paramIndex++
			}
		}
		query += " WHERE " + strings.Join(whereClauses, " AND ")
	}

	// Add ORDER BY clause if sorts exist
	if len(sorts) > 0 {
		sortClauses := make([]string, 0, len(sorts))
		for _, s := range sorts {
			sortClauses = append(sortClauses, s.ToSQL())
		}
		query += " ORDER BY " + strings.Join(sortClauses, ", ")
	}

	// Add LIMIT and OFFSET
	if limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", limit)
	}
	if offset > 0 {
		query += fmt.Sprintf(" OFFSET %d", offset)
	}

	return m.QueryMain(query, values...)
}

// ViewExists checks if a view exists in the main database.
func (m *Manager) ViewExists(viewName string) (bool, error) {
	query := `
		SELECT COUNT(*)
		FROM duckdb_views()
		WHERE schema_name = 'main' AND view_name = $1
	`
	var count int
	err := m.QueryRowScanMain(query, []interface{}{&count}, viewName)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// MacroExists checks if a table macro exists in the main database.
func (m *Manager) MacroExists(macroName string) (bool, error) {
	query := `
		SELECT COUNT(*)
		FROM duckdb_functions()
		WHERE function_type = 'table_macro'
		  AND schema_name = 'main'
		  AND function_name = $1
	`
	var count int
	err := m.QueryRowScanMain(query, []interface{}{&count}, macroName)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// TableExists checks if a table exists in the main database.
func (m *Manager) TableExists(table string) (bool, error) {
	query := `
		SELECT COUNT(*)
		FROM information_schema.tables
		WHERE table_name = $1
	`
	var count int
	err := m.QueryRowScanMain(query, []interface{}{&count}, table)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// GroupByResult represents a single group in aggregation results.
type GroupByResult struct {
	Key            interface{} `json:"key"`
	KeyDisplayName string      `json:"key_display_name"`
	Count          int64       `json:"count"`
}

// SelectGroupBy executes a GROUP BY query and returns aggregated counts.
// Results are ordered by count descending.
func (m *Manager) SelectGroupBy(table, groupByCol string, filters []Filter) ([]GroupByResult, error) {
	query := fmt.Sprintf("SELECT %s, COUNT(*) as count FROM %s", groupByCol, table)
	values := make([]interface{}, 0)
	paramIndex := 1

	// Add WHERE clause if filters exist
	if len(filters) > 0 {
		whereClauses := make([]string, 0, len(filters))
		for _, f := range filters {
			clause, val := f.ToSQL(paramIndex)
			whereClauses = append(whereClauses, clause)
			if val != nil {
				values = append(values, val)
				paramIndex++
			}
		}
		query += " WHERE " + strings.Join(whereClauses, " AND ")
	}

	query += fmt.Sprintf(" GROUP BY %s ORDER BY count DESC", groupByCol)

	rows, err := m.QueryMain(query, values...)
	if err != nil {
		return nil, fmt.Errorf("failed to execute group by query: %w", err)
	}
	defer rows.Close()

	results := make([]GroupByResult, 0)
	for rows.Next() {
		var key interface{}
		var count int64
		if err := rows.Scan(&key, &count); err != nil {
			return nil, fmt.Errorf("failed to scan group by result: %w", err)
		}

		// Convert key to string for display name
		keyDisplayName := fmt.Sprintf("%v", key)

		results = append(results, GroupByResult{
			Key:            key,
			KeyDisplayName: keyDisplayName,
			Count:          count,
		})
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating group by results: %w", err)
	}

	return results, nil
}

// ColumnSchema represents schema information for a single column.
type ColumnSchema struct {
	Name     string
	Type     string
	Nullable bool
}

// ColumnStats represents statistics for a single column from SUMMARIZE.
type ColumnStats struct {
	ColumnName     string
	ColumnType     string
	Min            interface{}
	Max            interface{}
	ApproxUnique   int64
	Avg            *float64
	Std            *float64
	Q25            interface{}
	Q50            interface{}
	Q75            interface{}
	Count          int64
	NullPercentage float64
}

// TableSummary represents the complete table summary including stats and row count.
type TableSummary struct {
	TotalRows  int64
	SampleSize int
	Columns    []ColumnStats
}

// GetTableSchema returns column names, types, and nullability from information_schema.
func (m *Manager) GetTableSchema(table string) ([]ColumnSchema, error) {
	query := `
		SELECT column_name, data_type,
		       CASE WHEN is_nullable = 'YES' THEN true ELSE false END as nullable
		FROM information_schema.columns
		WHERE table_name = $1
		ORDER BY ordinal_position`

	rows, err := m.QueryMain(query, table)
	if err != nil {
		return nil, fmt.Errorf("failed to query table schema: %w", err)
	}
	defer rows.Close()

	var columns []ColumnSchema
	for rows.Next() {
		var col ColumnSchema
		if err := rows.Scan(&col.Name, &col.Type, &col.Nullable); err != nil {
			return nil, fmt.Errorf("failed to scan column info: %w", err)
		}
		columns = append(columns, col)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating columns: %w", err)
	}

	return columns, nil
}

// GetViewSchema returns column info for a view using DESCRIBE.
// Views don't have entries in information_schema.columns, so we use DESCRIBE.
func (m *Manager) GetViewSchema(viewName string) ([]ColumnSchema, error) {
	// DESCRIBE returns: column_name, column_type, null, key, default, extra
	query := fmt.Sprintf("DESCRIBE (FROM %s LIMIT 0)", QuoteIdentifier(viewName))
	rows, err := m.QueryMain(query)
	if err != nil {
		return nil, fmt.Errorf("failed to describe view: %w", err)
	}
	defer rows.Close()

	var columns []ColumnSchema
	for rows.Next() {
		var name, colType, nullStr string
		var key, defaultVal, extra interface{}
		if err := rows.Scan(&name, &colType, &nullStr, &key, &defaultVal, &extra); err != nil {
			return nil, fmt.Errorf("failed to scan column info: %w", err)
		}
		columns = append(columns, ColumnSchema{
			Name:     name,
			Type:     colType,
			Nullable: nullStr == "YES",
		})
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating columns: %w", err)
	}

	return columns, nil
}

// GetTableSummary returns SUMMARIZE statistics for a table with total row count.
func (m *Manager) GetTableSummary(table string, sampleSize int) (*TableSummary, error) {
	// First get total row count
	var totalRows int64
	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM %s", QuoteIdentifier(table))
	err := m.QueryRowScanMain(countQuery, []interface{}{&totalRows})
	if err != nil {
		return nil, fmt.Errorf("failed to count rows: %w", err)
	}

	// Run SUMMARIZE on a sample
	query := fmt.Sprintf("FROM (SUMMARIZE (FROM %s LIMIT %d))", QuoteIdentifier(table), sampleSize)
	rows, err := m.QueryMain(query)
	if err != nil {
		return nil, fmt.Errorf("failed to summarize table: %w", err)
	}
	defer rows.Close()

	summary := &TableSummary{
		TotalRows:  totalRows,
		SampleSize: sampleSize,
		Columns:    make([]ColumnStats, 0),
	}

	// SUMMARIZE returns: column_name, column_type, min, max, approx_unique, avg, std, q25, q50, q75, count, null_percentage
	for rows.Next() {
		var stats ColumnStats
		var avg, std interface{}
		if err := rows.Scan(
			&stats.ColumnName,
			&stats.ColumnType,
			&stats.Min,
			&stats.Max,
			&stats.ApproxUnique,
			&avg,
			&std,
			&stats.Q25,
			&stats.Q50,
			&stats.Q75,
			&stats.Count,
			&stats.NullPercentage,
		); err != nil {
			return nil, fmt.Errorf("failed to scan summary row: %w", err)
		}

		// Handle avg and std which may be null for non-numeric columns
		if avg != nil {
			if f, ok := avg.(float64); ok {
				stats.Avg = &f
			}
		}
		if std != nil {
			if f, ok := std.(float64); ok {
				stats.Std = &f
			}
		}

		summary.Columns = append(summary.Columns, stats)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating summary: %w", err)
	}

	return summary, nil
}

// GetViewSummary returns SUMMARIZE statistics for a view with total row count.
func (m *Manager) GetViewSummary(viewName string, sampleSize int) (*TableSummary, error) {
	// First get total row count
	var totalRows int64
	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM %s", QuoteIdentifier(viewName))
	err := m.QueryRowScanMain(countQuery, []interface{}{&totalRows})
	if err != nil {
		return nil, fmt.Errorf("failed to count rows: %w", err)
	}

	// Run SUMMARIZE on a sample
	query := fmt.Sprintf("FROM (SUMMARIZE (FROM %s LIMIT %d))", QuoteIdentifier(viewName), sampleSize)
	rows, err := m.QueryMain(query)
	if err != nil {
		return nil, fmt.Errorf("failed to summarize view: %w", err)
	}
	defer rows.Close()

	summary := &TableSummary{
		TotalRows:  totalRows,
		SampleSize: sampleSize,
		Columns:    make([]ColumnStats, 0),
	}

	// SUMMARIZE returns: column_name, column_type, min, max, approx_unique, avg, std, q25, q50, q75, count, null_percentage
	for rows.Next() {
		var stats ColumnStats
		var avg, std interface{}
		if err := rows.Scan(
			&stats.ColumnName,
			&stats.ColumnType,
			&stats.Min,
			&stats.Max,
			&stats.ApproxUnique,
			&avg,
			&std,
			&stats.Q25,
			&stats.Q50,
			&stats.Q75,
			&stats.Count,
			&stats.NullPercentage,
		); err != nil {
			return nil, fmt.Errorf("failed to scan summary row: %w", err)
		}

		// Handle avg and std which may be null for non-numeric columns
		if avg != nil {
			if f, ok := avg.(float64); ok {
				stats.Avg = &f
			}
		}
		if std != nil {
			if f, ok := std.(float64); ok {
				stats.Std = &f
			}
		}

		summary.Columns = append(summary.Columns, stats)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating summary: %w", err)
	}

	return summary, nil
}

// QuoteIdentifier quotes an identifier (table/view name) for safe use in SQL.
func QuoteIdentifier(identifier string) string {
	// DuckDB uses double quotes for identifiers
	return `"` + strings.ReplaceAll(identifier, `"`, `""`) + `"`
}

// ValidateColumns checks if all specified columns exist in the table schema.
// Returns an error listing the invalid columns if any are not found.
func (m *Manager) ValidateColumns(table string, columns []string) error {
	if len(columns) == 0 {
		return nil
	}

	// Get all columns for the table
	tableColumns, err := m.getTableColumns(table)
	if err != nil {
		return fmt.Errorf("failed to get table schema: %w", err)
	}

	// Build a set of valid column names for fast lookup
	validColumns := make(map[string]bool)
	for _, col := range tableColumns {
		validColumns[col] = true
	}

	// Check each requested column
	invalidColumns := make([]string, 0)
	for _, col := range columns {
		if !validColumns[col] {
			invalidColumns = append(invalidColumns, col)
		}
	}

	if len(invalidColumns) > 0 {
		return fmt.Errorf("invalid columns: %s", strings.Join(invalidColumns, ", "))
	}

	return nil
}
