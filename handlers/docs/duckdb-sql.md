# DuckDB Friendly SQL Reference

DuckDB extends standard SQL with syntax that reduces verbosity and enables richer queries. Use these features when writing SQL against this server.

---

## Table Operations

### CREATE OR REPLACE TABLE
```sql
CREATE OR REPLACE TABLE my_table AS SELECT 1 AS id;
```
Replaces the table if it exists — no need for `DROP TABLE IF EXISTS`.

### CREATE TABLE AS SELECT (CTAS)
```sql
CREATE TABLE star_ships AS SELECT 'Enterprise' AS name, 'NCC-1701' AS registry;
```
Schema is inferred from the SELECT — no column definitions needed.

### INSERT INTO … BY NAME
```sql
INSERT INTO proverbs BY NAME SELECT 'Resistance is futile' AS borg_proverb;
```
Matches columns by name rather than position.

### INSERT OR IGNORE / INSERT OR REPLACE
```sql
INSERT OR IGNORE INTO users VALUES (1, 'picard');
INSERT OR REPLACE INTO users VALUES (1, 'picard');
```

### MERGE INTO — Upsert Without a Primary Key
```sql
MERGE INTO people
    USING (SELECT 3 AS id, 'Sarah' AS name, 95_000.0 AS salary) AS src
    ON (src.id = people.id)
    WHEN MATCHED THEN UPDATE SET salary = src.salary
    WHEN NOT MATCHED THEN INSERT;
```
Alternative to `INSERT OR REPLACE` when the target has no primary key — match condition is arbitrary SQL.

```sql
-- Multiple clauses: conditional update, delete, insert; RETURNING shows what happened
MERGE INTO people
    USING src USING (id)
    WHEN MATCHED AND people.salary < 100_000.0 THEN UPDATE SET salary = src.salary
    WHEN MATCHED AND people.salary >= 100_000.0 THEN DELETE
    WHEN NOT MATCHED THEN INSERT BY NAME
    RETURNING merge_action, *;
```

```sql
-- Delete rows absent from the source (full sync pattern)
MERGE INTO target
    USING source USING (id)
    WHEN MATCHED THEN UPDATE
    WHEN NOT MATCHED BY SOURCE THEN DELETE;
```
`WHEN NOT MATCHED BY SOURCE` deletes rows in the target that have no matching row in the source — useful for keeping target in sync with source.

### DESCRIBE / SUMMARIZE
```sql
DESCRIBE my_table;
SUMMARIZE my_table;
```
`DESCRIBE` shows column names and types. `SUMMARIZE` returns per-column statistics (min, max, nulls, approx distinct).

---

## Query Simplification

### FROM-First Syntax
```sql
FROM my_table SELECT col1, col2;
FROM my_table;                    -- equivalent to SELECT * FROM my_table
```
Start with `FROM` instead of `SELECT` — matches logical execution order.

### GROUP BY ALL
```sql
FROM sales SELECT region, product, sum(sales) GROUP BY ALL;
```
Groups by all non-aggregated columns automatically. No need to repeat column names.

### ORDER BY ALL
```sql
FROM t SELECT age, sum(score) GROUP BY ALL ORDER BY ALL;
```
Orders by all selected columns left to right. `ORDER BY ALL DESC` reverses.

### SELECT * EXCLUDE
```sql
FROM my_table SELECT * EXCLUDE (col1, col2);
```
All columns except the listed ones.

### SELECT * REPLACE
```sql
FROM products SELECT * REPLACE (price * 1.2 AS price);
```
All columns, with named columns overridden by expressions.

### UNION BY NAME
```sql
FROM t1 SELECT a, b
UNION ALL BY NAME
FROM t2 SELECT b, c;
```
Combines results matching columns by name rather than position. Missing columns become NULL.

### Prefix Aliases (colon syntax)
```sql
FROM orders SELECT total: sum(amount);
-- equivalent to: SELECT sum(amount) AS total FROM orders
```

### Column Aliases in WHERE / GROUP BY / HAVING
```sql
FROM products SELECT price * 0.9 AS discounted WHERE discounted < 50;
```
Use aliases defined in SELECT directly in other clauses — no subquery needed.

### Reusable Column Aliases
```sql
SELECT
    'hello world' AS msg,
    instr(msg, 'world') AS pos,
    substr(msg, pos) AS suffix;
```
Later columns in SELECT can reference earlier aliases in the same statement.

### Trailing Commas
```sql
FROM t SELECT col1, col2, GROUP BY col1, col2,;
```
Trailing commas are valid — makes commenting out columns easier.

---

## Table Transformation

### PIVOT
```sql
PIVOT sales ON year USING sum(amount) GROUP BY product;
```
Long → wide: unique values of `year` become columns.

### UNPIVOT
```sql
UNPIVOT wide_table ON COLUMNS(* EXCLUDE product) INTO NAME year VALUE amount;
```
Wide → long: column names become row values.

---

## Column Operations

### COLUMNS() with Regex
```sql
FROM stats SELECT id, COLUMNS('.*_count');
```
Selects all columns whose name matches the regex.

### COLUMNS() with Lambda
```sql
FROM orders SELECT COLUMNS(col -> col LIKE '%amount%');
```
Selects columns where the lambda returns true.

### COLUMNS() with EXCLUDE / REPLACE
```sql
FROM t SELECT max(COLUMNS(* EXCLUDE id));
FROM events SELECT max(COLUMNS(* REPLACE ts::DATE AS ts));
```

### COLUMNS() with Regex Rename (capture groups)
```sql
-- Strip a common prefix using capture groups in the regex
FROM financial_data SELECT id, COLUMNS('(adjusted_)(.*)') AS '\2';
-- adjusted_revenue → revenue, adjusted_profit → profit
-- columns not matching the regex are dropped

-- Rename with a suffix instead
FROM prices SELECT COLUMNS('(.*)(_usd)') AS '\1';
-- amount_usd → amount, cost_usd → cost
```
`\1`–`\9` refer to capture groups in the regex. Only columns matching the full pattern are included; non-matching columns must be selected separately.

### *COLUMNS() — Splat as Named Function Arguments
```sql
-- Pack all non-id columns into a struct (field names preserved)
FROM t SELECT id, struct_pack(*COLUMNS(* EXCLUDE id)) AS metrics;

-- Combine with regex rename: strip prefix then fold into struct
WITH renamed AS (
    FROM financial_data SELECT id, COLUMNS('(adjusted_)(.*)') AS '\2'
)
FROM renamed SELECT id, struct_pack(*COLUMNS(renamed.* EXCLUDE id)) AS metrics;
-- result: id=1, metrics={'revenue': 100.5, 'profit': 20.1}
```
The `*` prefix splats the selected columns as *named arguments* into the function — equivalent to writing `struct_pack(revenue := revenue, profit := profit)` by hand. Works with any variadic function that accepts named arguments (`struct_pack`, `row`, etc.).

### STRUCT.* Expansion
```sql
FROM customers SELECT address.*;
```
Expands a struct column into individual columns.

### Automatic Struct Creation
```sql
FROM orders SELECT orders;  -- each row becomes a struct
```

---

## Advanced Aggregation

### FILTER Clause
```sql
FROM users SELECT
    count(*) AS total,
    count(*) FILTER (WHERE status = 'active') AS active_count;
```
Per-aggregate filtering without affecting other aggregates.

### GROUPING SETS / CUBE / ROLLUP
```sql
FROM sales
SELECT region, product, sum(sales)
GROUP BY GROUPING SETS ((region), (product), (region, product), ());
```
Multiple aggregation levels in one query. `()` is the grand total.

### Top-N Per Group
```sql
FROM t SELECT grp, max(val, 3) GROUP BY grp;
-- returns an array of the top 3 values per group
```
Also: `min(arg, n)`, `arg_max(arg, val, n)`, `max_by(arg, val, n)`.

### count() Shorthand
```sql
FROM t SELECT count();  -- same as count(*)
```

---

## Nested Types (Lists and Structs)

### List Literals
```sql
SELECT ['a', 'b', 'c'] AS items;
SELECT items[2:3] AS slice;     -- 1-based, inclusive
```

### Struct Literals
```sql
SELECT {name: 'Alice', age: 30} AS person;
FROM t SELECT person.name;
```

### Lambda Functions
```sql
-- list_transform: apply function to each element
SELECT list_transform([1,2,3], x -> x * 2);

-- list_filter: keep elements matching condition
SELECT list_filter([1,2,3,4], x -> x > 2);

-- list_reduce: fold to single value
SELECT list_reduce([1,2,3,4], (acc, x) -> acc + x);
```

### List Comprehensions
```sql
FROM t SELECT [x * 2 FOR x IN values IF x > 0];
```

---

## String Operations

### String Slicing
```sql
SELECT 'hello world'[1:5];   -- 'hello'  (1-based)
SELECT 'hello world'[:-6];   -- 'hello'
```

### String Formatters
```sql
SELECT format('{} {}', 'Hello', 'World');       -- 'Hello World'
SELECT printf('%s = %d', 'answer', 42);         -- 'answer = 42'
```

### Function Chaining (dot operator)
```sql
SELECT ('make it so').upper().string_split(' ').list_aggr('string_agg', '-');
-- 'MAKE-IT-SO'
```
Any function can be called as `value.function(remaining_args)`.

---

## Special Join Types

### ASOF JOIN
```sql
FROM trades ASOF JOIN quotes
ON trades.ticker = quotes.ticker AND trades.ts >= quotes.ts;
```
For each row in `trades`, finds the latest matching row in `quotes` where `quotes.ts <= trades.ts`. Essential for time-series alignment.

### LATERAL JOIN
```sql
SELECT c.name, top3.product
FROM customers c,
LATERAL (
    SELECT product FROM orders WHERE customer_id = c.id
    ORDER BY amount DESC LIMIT 3
) top3;
```
Subquery can reference columns from the preceding table.

### POSITIONAL JOIN
```sql
FROM t1 POSITIONAL JOIN t2;
```
Joins row 1 with row 1, row 2 with row 2, etc.

---

## Data Import

### Direct File Queries
```sql
FROM 'data.parquet';
FROM 'data.csv';
FROM 'data/*.parquet';          -- glob pattern
FROM read_parquet(['a.parquet', 'b.parquet'], union_by_name=true);
```
No import step needed — query files directly. Schema is auto-detected.

### Cache Remote or Large Files
When querying a large or remote file multiple times, cache it first:
```sql
CREATE TABLE cached AS FROM 'large_file.parquet';
-- then query cached instead
FROM cached WHERE ...;
```

### Multiple Files with Different Schemas
```sql
FROM read_parquet('parts/*.parquet', union_by_name=true);
```
`union_by_name=true` aligns columns by name rather than position when schemas differ.

---

## Date, Time and Intervals

### Casting to date and timestamp types
```sql
-- varchar → date / timestamp
SELECT '2024-06-11'::DATE;
SELECT '2024-06-11 17:20:06.289'::TIMESTAMP;
SELECT CAST('2024-06-11' AS DATE);

-- timestamp → date (truncate time component)
SELECT now()::DATE;
FROM t SELECT my_timestamp_col::DATE;
```

### Date arithmetic
```sql
-- offset by days (integer addition)
SELECT '2024-06-11'::DATE + 1;            -- next day
SELECT '2024-06-11'::DATE - 7;            -- one week ago

-- offset by interval
SELECT date_add('2024-06-11'::DATE, INTERVAL '1 month');
SELECT date_add('2024-06-11'::DATE, INTERVAL '1 year');
SELECT '2025-01-15'::DATE + INTERVAL '3 months';   -- returns TIMESTAMP, cast to DATE if needed
SELECT ('2025-01-15'::DATE + INTERVAL '3 months')::DATE;
```

### INTERVAL literals
```sql
-- keyword syntax
SELECT INTERVAL '3 days';
SELECT INTERVAL '1 year 3 months';
SELECT INTERVAL '7 hours 45 minutes 10 seconds';

-- cast syntax
SELECT '3.5 years'::INTERVAL;
SELECT '1 month 15 days'::INTERVAL;

-- duration constructor functions
SELECT to_years(3), to_months(11), to_days(5);
SELECT to_hours(12), to_minutes(45), to_seconds(30);

-- interval arithmetic
SELECT INTERVAL '5 months' + INTERVAL '3 days';
SELECT INTERVAL '3 days' * 5;
SELECT INTERVAL '7 years' / 2;
```

### age() — human-readable duration between two dates
```sql
-- returns an INTERVAL; cast to VARCHAR for a readable string
SELECT age(current_date, '2001-12-20'::DATE);                  -- interval value
SELECT age(current_date, '2001-12-20'::DATE)::VARCHAR;         -- e.g. "24 years 2 months 29 days"

-- span of a date column in a table
FROM works SELECT age(MAX(publication_date)::DATE, MIN(publication_date)::DATE)::VARCHAR AS span;
```

### EXTRACT and date_part — get individual components
```sql
SELECT EXTRACT('year'  FROM my_date);   -- integer year
SELECT EXTRACT('month' FROM my_date);
SELECT EXTRACT('day'   FROM my_date);
SELECT EXTRACT('dow'   FROM my_date);   -- day of week 0=Sun..6=Sat

-- shorthand functions (equivalent)
SELECT year(my_date), month(my_date), day(my_date);

-- date_part is identical to EXTRACT
SELECT date_part('year', my_date);
```

### date_trunc — truncate to time bucket
```sql
-- truncate to start of period (useful for grouping)
SELECT date_trunc('year',    my_date);   -- 2024-01-01
SELECT date_trunc('month',   my_date);   -- 2024-06-01
SELECT date_trunc('week',    my_date);   -- Monday of that week
SELECT date_trunc('quarter', my_date);
SELECT date_trunc('decade',  my_date);

-- typical time-series aggregation pattern
FROM works
SELECT date_trunc('year', publication_date) AS year, COUNT(*) AS n
GROUP BY ALL
ORDER BY year;
```

### strftime / strptime — format and parse
```sql
-- format date/timestamp as string
SELECT strftime(my_date, '%Y-%m');          -- '2024-06'
SELECT strftime(my_date, '%Y');             -- '2024'
SELECT strftime(my_date, '%d %b %Y');       -- '11 Jun 2024'

-- parse string to timestamp
SELECT strptime('2024-06-11 17:20:06', '%Y-%m-%d %H:%M:%S');
SELECT strptime('June 11 2024', '%B %d %Y');

-- TRY_STRPTIME returns NULL instead of error on bad input
FROM t SELECT TRY_STRPTIME(my_varchar_date, '%Y-%m-%d');
```

### last_day, dayname, monthname
```sql
SELECT last_day('2024-06-11'::DATE);         -- 2024-06-30
SELECT dayname('2024-06-11'::DATE);          -- 'Tuesday'
SELECT monthname('2024-06-11'::DATE);        -- 'June'
```

### Outlier and quality filters
```sql
-- filter out garbage dates (year 1000, far-future pre-prints etc.)
FROM works
WHERE publication_date BETWEEN '1900-01-01' AND (current_date + INTERVAL '2 years');

-- count outliers only
FROM works SELECT COUNT(*) FILTER (
    EXTRACT('year' FROM publication_date) < 1900
    OR EXTRACT('year' FROM publication_date) > EXTRACT('year' FROM current_date) + 2
) AS outliers;
```

### generate_series for date sequences
```sql
-- generate one row per month between two dates
SELECT unnest(generate_series(
    '2020-01-01'::TIMESTAMP,
    '2024-12-01'::TIMESTAMP,
    INTERVAL '1 month'
))::DATE AS month;

-- fill gaps in a time series (LEFT JOIN pattern)
WITH months AS (
    SELECT unnest(generate_series(
        '2020-01-01'::TIMESTAMP, now(), INTERVAL '1 month'
    ))::DATE AS month
)
FROM months
LEFT JOIN works w ON date_trunc('month', w.publication_date) = months.month
SELECT months.month, COALESCE(COUNT(w.publication_date), 0) AS n
GROUP BY ALL ORDER BY month;
```

### Current date and time
```sql
SELECT current_date;       -- DATE: today
SELECT current_timestamp;  -- TIMESTAMP WITH TIME ZONE: now
SELECT now();              -- alias for current_timestamp
SELECT today();            -- alias for current_date
```

---

## Other Features

### Case Insensitivity with Preservation
```sql
CREATE TABLE t AS SELECT 1 AS "MyColumn";
FROM t SELECT mycolumn;  -- works; displays as MyColumn
```

### Auto-renamed Duplicate Columns
In joins producing two columns with the same name, DuckDB automatically appends `:1`, `:2`, etc.

### Automatic JSON Parsing
```sql
FROM 'records.json' SELECT data[0].name;
```
JSON arrays and objects are parsed into DuckDB lists and structs automatically.

### Implicit Type Casts
DuckDB casts between compatible types automatically in comparisons and joins — e.g. INTEGER vs VARCHAR in a join condition.

### Underscores in Numeric Literals
```sql
SELECT 1_000_000 AS one_million;
```

### Identifiers vs Literals
- Double quotes (`"`) for identifiers with spaces, special characters, or case-sensitivity: `"My Column"`
- Single quotes (`'`) for string literals: `'hello world'`

---

## Window Functions

### QUALIFY — Filter on Window Results
```sql
-- Top 2 products by sales in each category
FROM products
SELECT category, product_name, sales_amount
QUALIFY ROW_NUMBER() OVER (PARTITION BY category ORDER BY sales_amount DESC) <= 2;
```
`QUALIFY` filters rows after window functions are evaluated — no subquery needed.
Also works with `RANK()`, `DENSE_RANK()`, `LAG()`, `LEAD()`, etc.

```sql
-- Keep only the latest row per group
FROM events
QUALIFY ROW_NUMBER() OVER (PARTITION BY user_id ORDER BY ts DESC) = 1;
```

---

## Latest-per-Group Aggregation

### arg_max / arg_min
```sql
-- Get the name of the highest-paid employee per department
FROM employees SELECT department, arg_max(name, salary) AS top_earner GROUP BY department;

-- Get the value of another column at the row with the maximum date
FROM events SELECT id, arg_max(status, updated_at) AS latest_status GROUP BY id;
```
`arg_max(arg, val)` returns the value of `arg` at the row where `val` is maximum.
`arg_min(arg, val)` returns the value of `arg` at the row where `val` is minimum.

---

## JSON

### Arrow Operators
```sql
SELECT data->>'name'           -- returns TEXT (unquoted)
SELECT data->'user'->'id'      -- returns JSON (nested path)
SELECT data->>'$.user.id'      -- JSONPath syntax, returns TEXT
```
Use `->>'key'` when you want a plain string; use `->'key'` to keep it as JSON for further extraction.

### JSON Array Access
```sql
SELECT json_col->>0             -- first element as text
SELECT json_extract_string(json_col, '$.items[0].name')
```

---

## Schema Exploration

```sql
-- List all attached databases
FROM duckdb_databases() SELECT database_name, type, path, is_read_only;

-- List tables in a specific database
FROM information_schema.tables
SELECT schema_name, table_name, table_type
WHERE table_catalog = 'my_db';

-- Equivalent using catalog function
FROM duckdb_tables()
SELECT database_name, schema_name, table_name
WHERE database_name = 'my_db';

-- Get column details
FROM duckdb_columns()
SELECT column_name, data_type, is_nullable
WHERE database_name = 'my_db' AND table_name = 'my_table';

-- Column statistics
SUMMARIZE my_table;
```

---

## Persisting In-Memory Data to File

```sql
-- Attach a new file-based database
ATTACH '/path/to/output.db' AS output;

-- Copy everything from the in-memory default database into it
COPY FROM DATABASE memory TO output;

-- Detach when done
DETACH output;
```
Useful when you have built up tables in `:memory:` and want to save them before the session ends.

---

## Complex Types — MAP

```sql
-- Map literal
SELECT MAP(['a','b'], [1, 2]) AS m;

-- Access by key
FROM (SELECT MAP(['a','b'], [1, 2]) AS m) SELECT m['a'];

-- element_at (null-safe)
FROM t SELECT element_at(m, 'missing_key');
```

---

## SQL Quirks and Gotchas

Behaviours that differ from other SQL dialects or produce surprising results.

### Exponentiation Operator Precedence
```sql
SELECT -2^2;    -- returns 4.0, NOT -4.0
```
Unary minus has higher precedence than `^`, so `-2^2` is `(-2)^2 = 4`. Use `-(2^2)` or `pow(2, 2)` to get `-4`.

### NULL in IN vs. IN-List
```sql
SELECT 1 IN (0, NULL);     -- NULL  (UNKNOWN — standard SQL behaviour)
SELECT 1 IN [0, NULL];     -- false (list membership, not SQL IN)
```
`IN (...)` follows SQL three-valued logic; `IN [...]` tests list membership and treats NULL as a value.

### String Concatenation and NULL
```sql
SELECT concat('abc', NULL);  -- 'abc'   (NULL is ignored)
SELECT 'abc' || NULL;        -- NULL    (NULL propagates with ||)
```
Use `concat()` when NULL inputs should be silently skipped; use `||` when NULL should poison the result.

### Indexing: 1-Based vs. 0-Based
- **Lists, strings, window functions** (`row_number`, `rank`): **1-based**
- **JSON** (`->`, `->>`, `json_extract`): **0-based**

```sql
SELECT ['a','b','c'][1];          -- 'a'  (1-based)
SELECT '["a","b","c"]'::JSON->>0; -- 'a'  (0-based)
```

### USING SAMPLE Placement vs. Execution Order
```sql
FROM t WHERE col > 0 USING SAMPLE 10%;
```
`USING SAMPLE` is written after `WHERE`/`GROUP BY` but is applied *before* them — it samples the raw table, not the filtered result. To sample after filtering, wrap in a subquery:
```sql
FROM (FROM t WHERE col > 0) USING SAMPLE 10%;
```

### Column Deduplication in SELECT
```sql
CREATE TABLE tbl AS SELECT 1 AS a;
FROM (FROM tbl SELECT *, 2 AS a) SELECT a;  -- returns 1, not 2
```
When the same column name appears multiple times in a SELECT, the **first** occurrence wins and later ones are silently dropped.

### age() Uses current_date, Not current_timestamp
```sql
SELECT age('2000-01-01'::DATE);  -- current_date - date, returns an interval
```
`age(x)` computes `current_date - x`, not `current_timestamp - x`. The time-of-day component is never included.

### Implicit Type Coercions Worth Knowing
```sql
SELECT 1 = '1';    -- true  (string cast to integer)
SELECT 1 = ' 01 '; -- true  (whitespace and leading zeros stripped)
SELECT 1 = true;   -- true  (boolean cast to integer)
SELECT 't' = true; -- true  (PostgreSQL compatibility)
```
DuckDB aggressively casts across types in equality comparisons. Be explicit with `CAST` or `::` when exact type matching matters.
