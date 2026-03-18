# DuckDB Domain Metadata

How to enrich a DuckDB database with human- and machine-readable metadata:
comments, tags, macro descriptions, and how to query the built-in system catalog
functions. Covering operators building deployments on top of this module.

For query syntax see `duckdb://docs/sql-syntax`; for nested type functions see
`duckdb://docs/functions`.

---

## COMMENT ON

DuckDB supports inline documentation for most schema objects. Comments are
stored in the catalog and visible via `duckdb_*()` system functions and
`DESCRIBE`.

```sql
-- Tables and views
COMMENT ON TABLE  publications IS 'All publication records from DiVA export';
COMMENT ON VIEW   publications_recent IS 'Publications from the last 5 years';

-- Columns
COMMENT ON COLUMN publications.pid     IS 'DiVA persistent identifier (integer)';
COMMENT ON COLUMN publications.pub_year IS 'Year of publication (NULL if unknown)';

-- Schemas
COMMENT ON SCHEMA main IS 'Primary working schema';

-- Scalar macros (no TABLE keyword)
COMMENT ON MACRO ror_url IS 'Return canonical ROR URL for a ROR identifier';

-- Table macros (requires TABLE keyword)
COMMENT ON MACRO TABLE api_works IS 'Search works by title, author, year, DOI';
```

> **Key distinction:** scalar macros use `COMMENT ON MACRO`, table macros
> (those defined with `AS TABLE (...)`) require `COMMENT ON MACRO TABLE`.

Read back comments:
```sql
SELECT table_name, comment FROM duckdb_tables() WHERE comment IS NOT NULL;
SELECT column_name, comment FROM duckdb_columns() WHERE table_name = 'publications';
SELECT function_name, comment FROM duckdb_functions() WHERE comment IS NOT NULL;
```

---

## Tags

Objects can carry a `MAP(VARCHAR, VARCHAR)` of key-value tags, readable from
the `tags` column in most `duckdb_*()` functions.

```sql
-- Set tags at creation time
CREATE TABLE measurements (val DOUBLE) WITH (tags = {'env': 'prod', 'owner': 'alice'});

-- Read tags
SELECT table_name, tags FROM duckdb_tables() WHERE cardinality(tags) > 0;

-- Filter by tag value
SELECT table_name
FROM duckdb_tables()
WHERE tags['env'] = 'prod';
```

Tags are also available on views, indexes, schemas, and functions.

---

## macro_descriptions Table Pattern

`COMMENT ON MACRO` stores free-text documentation, but does not support
structured parameter type hints. For MCP tool discovery — and any tooling that
needs to know a parameter's SQL type — use a dedicated metadata table in
`:memory:` (or a writable schema).

```sql
CREATE OR REPLACE TABLE memory.macro_descriptions (
    macro_name  VARCHAR PRIMARY KEY,
    description VARCHAR,
    param_types JSON   -- maps param name → SQL type, e.g. '{"id": "BIGINT"}'
);

-- Register macros with type overrides
INSERT OR REPLACE INTO memory.macro_descriptions VALUES
    ('doi_url',     'Canonical DOI URL for a DOI string', '{}'),
    ('pmid_url',    'PubMed URL for a PubMed ID',          '{"pmid": "BIGINT"}'),
    ('openalex_work_url',
                    'OpenAlex URL for a work integer ID',  '{"work_id": "BIGINT"}');
```

Why `:memory:`? When the main database is mounted read-only, `memory` is the
only writable catalog. The table is populated by `init.sql` at startup.

Query pattern used by the MCP tool layer:
```sql
SELECT
    f.function_name,
    coalesce(d.description, f.comment, f.description) AS description,
    d.param_types,
    f.parameters,
    f.macro_definition
FROM duckdb_functions() f
LEFT JOIN memory.macro_descriptions d ON d.macro_name = f.function_name
WHERE f.function_type IN ('macro', 'table_macro')
  AND NOT f.internal
ORDER BY f.function_name;
```

---

## System Catalog Functions

All `duckdb_*()` table functions return live catalog state. Use them to
introspect any database attached to the current connection.

### duckdb_tables()

Key columns: `database_name`, `schema_name`, `table_name`, `comment`,
`estimated_size`, `column_count`, `has_primary_key`, `temporary`, `sql`

```sql
-- All user tables across all attached databases
SELECT database_name, schema_name, table_name, comment, estimated_size
FROM duckdb_tables()
WHERE NOT internal
ORDER BY database_name, schema_name, table_name;

-- Tables with no comment (undocumented)
SELECT table_name FROM duckdb_tables()
WHERE comment IS NULL AND NOT internal AND NOT temporary;
```

### duckdb_columns()

Key columns: `database_name`, `schema_name`, `table_name`, `column_name`,
`column_index`, `data_type`, `is_nullable`, `column_default`, `comment`

```sql
-- All columns for a specific table
SELECT column_index, column_name, data_type, is_nullable, comment
FROM duckdb_columns()
WHERE table_name = 'publications'
ORDER BY column_index;

-- Find all BIGINT columns (useful for join key discovery)
SELECT table_name, column_name
FROM duckdb_columns()
WHERE data_type = 'BIGINT' AND NOT internal
ORDER BY table_name, column_name;
```

### duckdb_views()

Key columns: `database_name`, `schema_name`, `view_name`, `comment`,
`column_count`, `sql`, `is_bound`, `temporary`

```sql
SELECT view_name, comment, column_count
FROM duckdb_views()
WHERE NOT internal
ORDER BY view_name;

-- Full view SQL (for documentation or migration)
SELECT view_name, sql
FROM duckdb_views()
WHERE schema_name = 'main' AND NOT internal;
```

### duckdb_functions()

Key columns: `function_name`, `function_type`, `description`, `comment`,
`return_type`, `parameters`, `parameter_types`, `macro_definition`,
`examples`, `stability`, `categories`, `alias_of`, `has_side_effects`

**`function_type` values:**
| Value | Meaning |
|---|---|
| `scalar` | Regular scalar function (one return value per row) |
| `aggregate` | Aggregate function (GROUP BY, window) |
| `table` | Built-in table function (e.g. `read_parquet`) |
| `macro` | User-defined scalar macro |
| `table_macro` | User-defined table macro (returns rows) |
| `pragma` | Configuration/control pragma |

```sql
-- All user-defined macros with their definitions
SELECT function_name, function_type, parameters, macro_definition, comment
FROM duckdb_functions()
WHERE function_type IN ('macro', 'table_macro')
  AND NOT internal
ORDER BY function_name;

-- Find functions by keyword in name or description
SELECT DISTINCT ON (function_name) function_name, function_type, description
FROM duckdb_functions()
WHERE function_name LIKE '%json%'
   OR lower(description) LIKE '%json%'
ORDER BY function_name;

-- List aggregate functions (useful for discovering window function candidates)
SELECT DISTINCT function_name, return_type
FROM duckdb_functions()
WHERE function_type = 'aggregate'
ORDER BY function_name;

-- Find aliases (many DuckDB functions have PostgreSQL-compat aliases)
SELECT function_name, alias_of
FROM duckdb_functions()
WHERE alias_of IS NOT NULL
ORDER BY alias_of, function_name;
```

> Note: `description` and `parameter_types` are often empty for built-in
> functions and always empty for user macros. Use `COMMENT ON MACRO` or
> `memory.macro_descriptions` for operator-provided documentation.
> `parameter_types` returns `[NULL]` for parameters that have defaults —
> do not rely on it for type decisions.

### duckdb_constraints()

Key columns: `table_name`, `constraint_type`, `constraint_text`,
`constraint_column_names`, `referenced_table`, `referenced_column_names`

**`constraint_type` values:** `PRIMARY KEY`, `UNIQUE`, `NOT NULL`,
`CHECK`, `FOREIGN KEY`

```sql
-- All primary key columns
SELECT table_name, constraint_column_names
FROM duckdb_constraints()
WHERE constraint_type = 'PRIMARY KEY';

-- Foreign key relationships
SELECT
    table_name          AS from_table,
    constraint_column_names AS from_cols,
    referenced_table    AS to_table,
    referenced_column_names AS to_cols
FROM duckdb_constraints()
WHERE constraint_type = 'FOREIGN KEY';
```

### duckdb_indexes()

Key columns: `index_name`, `table_name`, `schema_name`, `is_unique`,
`is_primary`, `expressions`, `sql`

```sql
SELECT table_name, index_name, is_unique, is_primary, expressions
FROM duckdb_indexes()
WHERE NOT internal
ORDER BY table_name, index_name;
```

### duckdb_schemas()

Key columns: `database_name`, `schema_name`, `comment`, `internal`, `sql`

```sql
SELECT database_name, schema_name, comment
FROM duckdb_schemas()
WHERE NOT internal
ORDER BY database_name, schema_name;
```

### duckdb_extensions()

Key columns: `extension_name`, `loaded`, `installed`, `extension_version`,
`description`, `install_mode`, `installed_from`

```sql
-- Loaded extensions
SELECT extension_name, extension_version, description
FROM duckdb_extensions()
WHERE loaded
ORDER BY extension_name;

-- Community extensions (installed from outside core)
SELECT extension_name, installed_from, description
FROM duckdb_extensions()
WHERE install_mode = 'REPOSITORY'
ORDER BY extension_name;
```

---

## Composite Discovery Queries

### Full schema snapshot (tables + columns + comments)
```sql
SELECT
    t.schema_name,
    t.table_name,
    t.comment AS table_comment,
    c.column_name,
    c.data_type,
    c.is_nullable,
    c.comment AS column_comment
FROM duckdb_tables() t
JOIN duckdb_columns() c
  ON c.database_name = t.database_name
 AND c.schema_name   = t.schema_name
 AND c.table_name    = t.table_name
WHERE NOT t.internal
ORDER BY t.schema_name, t.table_name, c.column_index;
```

### Documentation coverage report
```sql
-- Which tables and columns are missing comments?
SELECT
    'table'         AS object_type,
    table_name      AS object_name,
    NULL            AS parent_name
FROM duckdb_tables()
WHERE comment IS NULL AND NOT internal AND NOT temporary
UNION ALL
SELECT
    'column',
    column_name,
    table_name
FROM duckdb_columns()
WHERE comment IS NULL AND NOT internal
ORDER BY object_type, parent_name, object_name;
```

### Search across all comments and descriptions
```sql
-- Find any object mentioning a concept
SELECT 'table' AS kind, table_name AS name, comment AS doc
FROM duckdb_tables()   WHERE lower(comment) LIKE '%author%'
UNION ALL
SELECT 'column', column_name, comment
FROM duckdb_columns()  WHERE lower(comment) LIKE '%author%'
UNION ALL
SELECT 'macro', function_name,
       coalesce(comment, description)
FROM duckdb_functions() WHERE lower(coalesce(comment, description, '')) LIKE '%author%'
ORDER BY kind, name;
```

### Macro inventory with type hints
```sql
-- Full macro listing with macro_descriptions overrides
SELECT
    f.function_name,
    f.function_type,
    f.parameters,
    d.param_types,
    coalesce(d.description, f.comment) AS description
FROM duckdb_functions() f
LEFT JOIN memory.macro_descriptions d USING (macro_name)
WHERE f.function_type IN ('macro', 'table_macro')
  AND NOT f.internal
ORDER BY f.function_name;
```

> Tip: if `memory.macro_descriptions` doesn't exist, wrap the join in a
> `TRY` block or use `SELECT * FROM memory.macro_descriptions` inside a
> conditional to avoid errors in deployments that don't use this pattern.
