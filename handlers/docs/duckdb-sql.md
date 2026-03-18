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
SELECT region, product, sum(sales) FROM sales GROUP BY ALL;
```
Groups by all non-aggregated columns automatically. No need to repeat column names.

### ORDER BY ALL
```sql
SELECT age, sum(score) FROM t GROUP BY ALL ORDER BY ALL;
```
Orders by all selected columns left to right. `ORDER BY ALL DESC` reverses.

### SELECT * EXCLUDE
```sql
SELECT * EXCLUDE (col1, col2) FROM my_table;
```
All columns except the listed ones.

### SELECT * REPLACE
```sql
SELECT * REPLACE (price * 1.2 AS price) FROM products;
```
All columns, with named columns overridden by expressions.

### UNION BY NAME
```sql
SELECT a, b FROM t1
UNION ALL BY NAME
SELECT b, c FROM t2;
```
Combines results matching columns by name rather than position. Missing columns become NULL.

### Prefix Aliases (colon syntax)
```sql
SELECT total: sum(amount) FROM orders;
-- equivalent to: SELECT sum(amount) AS total FROM orders
```

### Column Aliases in WHERE / GROUP BY / HAVING
```sql
SELECT price * 0.9 AS discounted FROM products WHERE discounted < 50;
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
SELECT col1, col2, FROM t GROUP BY col1, col2,;
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
SELECT id, COLUMNS('.*_count') FROM stats;
```
Selects all columns whose name matches the regex.

### COLUMNS() with Lambda
```sql
SELECT COLUMNS(col -> col LIKE '%amount%') FROM orders;
```
Selects columns where the lambda returns true.

### COLUMNS() with EXCLUDE / REPLACE
```sql
SELECT max(COLUMNS(* EXCLUDE id)) FROM t;
SELECT max(COLUMNS(* REPLACE ts::DATE AS ts)) FROM events;
```

### STRUCT.* Expansion
```sql
SELECT address.* FROM customers;
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
SELECT
    count(*) AS total,
    count(*) FILTER (WHERE status = 'active') AS active_count
FROM users;
```
Per-aggregate filtering without affecting other aggregates.

### GROUPING SETS / CUBE / ROLLUP
```sql
SELECT region, product, sum(sales)
FROM sales
GROUP BY GROUPING SETS ((region), (product), (region, product), ());
```
Multiple aggregation levels in one query. `()` is the grand total.

### Top-N Per Group
```sql
SELECT grp, max(val, 3) FROM t GROUP BY grp;
-- returns an array of the top 3 values per group
```
Also: `min(arg, n)`, `arg_max(arg, val, n)`, `max_by(arg, val, n)`.

### count() Shorthand
```sql
SELECT count() FROM t;  -- same as count(*)
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
SELECT person.name FROM ...;
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
SELECT [x * 2 FOR x IN values IF x > 0] FROM t;
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
SELECT * FROM trades ASOF JOIN quotes
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
SELECT * FROM t1 POSITIONAL JOIN t2;
```
Joins row 1 with row 1, row 2 with row 2, etc.

---

## Data Import

### Direct File Queries
```sql
SELECT * FROM 'data.parquet';
SELECT * FROM 'data.csv';
SELECT * FROM 'data/*.parquet';          -- glob pattern
SELECT * FROM read_parquet(['a.parquet', 'b.parquet'], union_by_name=true);
```
No import step needed — query files directly. Schema is auto-detected.

### Cache Remote or Large Files
When querying a large or remote file multiple times, cache it first:
```sql
CREATE TABLE cached AS SELECT * FROM 'large_file.parquet';
-- then query cached instead
SELECT * FROM cached WHERE ...;
```

### Multiple Files with Different Schemas
```sql
SELECT * FROM read_parquet('parts/*.parquet', union_by_name=true);
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
SELECT my_timestamp_col::DATE FROM t;
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
SELECT age(MAX(publication_date)::DATE, MIN(publication_date)::DATE)::VARCHAR AS span
FROM works;
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
SELECT date_trunc('year', publication_date) AS year, COUNT(*) AS n
FROM works
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
SELECT TRY_STRPTIME(my_varchar_date, '%Y-%m-%d') FROM t;
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
SELECT * FROM works
WHERE publication_date BETWEEN '1900-01-01' AND (current_date + INTERVAL '2 years');

-- count outliers only
SELECT COUNT(*) FILTER (
    EXTRACT('year' FROM publication_date) < 1900
    OR EXTRACT('year' FROM publication_date) > EXTRACT('year' FROM current_date) + 2
) AS outliers
FROM works;
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
SELECT months.month, COALESCE(COUNT(w.publication_date), 0) AS n
FROM months
LEFT JOIN works w ON date_trunc('month', w.publication_date) = months.month
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
SELECT mycolumn FROM t;  -- works; displays as MyColumn
```

### Auto-renamed Duplicate Columns
In joins producing two columns with the same name, DuckDB automatically appends `:1`, `:2`, etc.

### Automatic JSON Parsing
```sql
SELECT data[0].name FROM 'records.json';
```
JSON arrays and objects are parsed into DuckDB lists and structs automatically.

### Implicit Type Casts
DuckDB casts between compatible types automatically in comparisons and joins — e.g. INTEGER vs VARCHAR in a join condition.

### Underscores in Numeric Literals
```sql
SELECT 1_000_000 AS one_million;
```
