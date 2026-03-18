# DuckDB Nested Types and Functions

Reference for DuckDB's nested types (LIST, STRUCT, MAP), regular expressions,
JSON, and the QUALIFY clause. These are the features most likely to differ from
other SQL dialects. For date/time, friendly-SQL syntax, and query patterns see
`duckdb://docs/sql-syntax`.

---

## LIST

A `LIST` holds an ordered sequence of values of the same type. Equivalent to
PostgreSQL `ARRAY` — DuckDB uses `LIST` terminology but provides `array_`
aliases for compatibility.

### Creating lists
```sql
SELECT [1, 2, 3];                          -- literal
SELECT ['duck', 'goose', NULL, 'heron'];   -- NULLs allowed
SELECT [['a', 'b'], ['c']];                -- nested list
SELECT list_value(1, 2, 3);               -- function form

-- Aggregate a column into a list
SELECT list(col ORDER BY col) FROM t;      -- ordered list
SELECT array_agg(col) FROM t;              -- PostgreSQL alias
```

Column type: `INTEGER[]` or `INTEGER[3]` for fixed-length arrays.

### Indexing and slicing (1-based)
```sql
SELECT ['a','b','c'][1];       -- 'a'  (1-based)
SELECT ['a','b','c'][-1];      -- 'c'  (last element)
SELECT ['a','b','c'][2:3];     -- ['b','c']
SELECT ['a','b','c'][:2];      -- ['a','b']
SELECT ['a','b','c'][-2:];     -- ['b','c']
SELECT list_extract(['a','b','c'], 2);     -- 'b'
SELECT list_slice(['a','b','c'], 2, 3);   -- ['b','c']
```

### Length and membership
```sql
SELECT len([1,2,3]);                        -- 3 (alias: length, array_length)
SELECT list_count([1,NULL,3]);              -- 2 (non-NULL count)
SELECT list_contains([1,2,3], 2);          -- true
SELECT list_has_any([1,2], [2,3]);         -- true  (operator: &&)
SELECT list_has_all([1,2,3], [1,2]);       -- true  (operator: @>)
SELECT list_position([10,20,30], 20);      -- 2 (1-based, NULL if absent)
```

### Transformation with lambdas
```sql
-- list_transform (alias: list_apply, apply)
SELECT list_transform([1,2,3], x -> x * 2);          -- [2,4,6]
SELECT [1,2,3].list_transform(x -> x * 2);           -- dot-chaining form

-- list_filter
SELECT list_filter([1,2,3,4], x -> x > 2);           -- [3,4]

-- list_reduce (left fold)
SELECT list_reduce([1,2,3,4], (acc, x) -> acc + x);  -- 10

-- List comprehension (syntactic sugar)
SELECT [x * 2 FOR x IN [1,2,3,4] IF x > 1];          -- [4,6,8]
```

### Sorting and reordering
```sql
SELECT list_sort([3,1,2]);                  -- [1,2,3]
SELECT list_reverse_sort([3,1,2]);          -- [3,2,1]
SELECT list_reverse([1,2,3]);              -- [3,2,1]
SELECT list_grade_up([3,1,2]);             -- [2,3,1] (sort permutation)
```

### Set operations
```sql
SELECT list_distinct([1,1,2,3,2]);         -- [1,2,3]  (alias: list_unique)
SELECT list_intersect([1,2,3], [2,3,4]);  -- [2,3]
SELECT list_except([1,2,3], [2,3,4]);     -- [1]
SELECT list_union([1,2], [2,3]);           -- [1,2,3]
```

### Concatenation and modification
```sql
SELECT list_concat([1,2], [3,4]);          -- [1,2,3,4]  (alias: array_cat, ||)
SELECT list_append([1,2], 3);             -- [1,2,3]
SELECT list_prepend(0, [1,2]);            -- [0,1,2]
SELECT list_select([10,20,30], [3,1]);    -- [30,10] (pick by index list)
```

### Flattening and unnesting
```sql
SELECT list_flatten([[1,2],[3,4]]);        -- [1,2,3,4] (one level only)
SELECT flatten([[1,2],[3,4]]);             -- alias

-- Unnest to rows (lateral expand)
SELECT unnest([10,20,30]);                 -- three rows: 10, 20, 30
SELECT unnest(items) FROM t;               -- expands list column to rows

-- Multiple lists in one SELECT: zip (not cross join) — shorter list padded with NULLs
SELECT unnest([1,2,3]), unnest([10,11]);
-- rows: (1,10), (2,11), (3,NULL)

-- Track original index while unnesting
SELECT unnest(l) AS x, generate_subscripts(l, 1) AS idx
FROM (VALUES ([10,20,30])) t(l);
-- rows: (10,1), (20,2), (30,3)

-- Recursive unnest: flattens nested lists fully, then expands struct fields as columns
-- Note: does NOT recurse into lists that are nested inside structs
SELECT unnest([[[1,2],[3,4]]], recursive := true);  -- rows: 1, 2, 3, 4

-- Control flattening depth (stops before fully flattening)
SELECT unnest([[[1,2],[3,4]]], max_depth := 2);     -- rows: [1,2], [3,4]
```

### Applying aggregate functions to a list
```sql
SELECT list_aggregate([1,2,3,4], 'sum');          -- 10
SELECT list_aggregate([1,2,3,4], 'mean');         -- 2.5
SELECT list_aggregate([1,2,3,4], 'string_agg', ', '); -- '1, 2, 3, 4'
-- Alias: list_aggr, array_aggr, aggregate
```

### Vector similarity (for embeddings)
```sql
SELECT list_cosine_similarity([1.0,0.0], [0.0,1.0]);  -- 0.0
SELECT list_distance([1.0,0.0], [0.0,1.0]);           -- sqrt(2)
SELECT list_cosine_distance([1.0,0.0], [0.0,1.0]);    -- 1.0  (1 - similarity)
-- Operators: list1 <-> list2 (distance), list1 <=> list2 (cosine_distance)
```

---

## STRUCT

A `STRUCT` is an ordered set of named fields, each potentially of a different
type. All rows in a STRUCT column must have the same keys. Similar to PostgreSQL
`ROW` but with enforced schema.

### Creating structs
```sql
SELECT {'name': 'Alice', 'age': 30};                  -- literal
SELECT struct_pack(name := 'Alice', age := 30);       -- function (no quotes on keys)
SELECT row('Alice', 30);                               -- unnamed struct (tuple)

-- From query columns
SELECT d AS person FROM (SELECT 'Alice' AS name, 30 AS age) d;
```

Column type: `STRUCT(name VARCHAR, age INTEGER)`.

### Accessing fields
```sql
SELECT person.name FROM t;                   -- dot notation
SELECT person['name'] FROM t;                -- bracket notation (constant string)
SELECT struct_extract(person, 'name');        -- function form
SELECT struct_extract(row(10,20), 1);         -- unnamed struct: 1-based index
```

Key with spaces: `person."first name"` or `person['first name']`.

Dot notation resolution order (avoids ambiguity):
1. `schema.table.column`
2. `table.column`
3. `column.struct_field`

### Modifying structs
```sql
SELECT struct_insert({'a':1}, b := 2);               -- {'a':1,'b':2}  add fields
SELECT struct_update({'a':1,'b':2}, b := 99);         -- {'a':1,'b':99} update
SELECT struct_update({'a':1,'b':2}, b := 3, c := 4); -- add + update
SELECT struct_concat({'a':1}, {'b':2});               -- {'a':1,'b':2}  merge
SELECT struct_contains({'a':1,'b':2}, 'a');           -- true
SELECT struct_position(row(10,20,30), 20);            -- 2
```

### Expanding struct fields to columns
```sql
-- unnest expands all fields as separate columns
SELECT unnest(person) FROM t;               -- columns: name, age

-- star notation (also allows EXCLUDE/REPLACE)
SELECT person.* FROM t;
SELECT person.* EXCLUDE ('age') FROM t;    -- drop age column

-- Recursive struct unnest: fully expands nested structs to flat columns
SELECT unnest({'a': 42, 'b': {'bb': {'bbb': 1}}}, recursive := true);
-- columns: a=42, bbb=1  (intermediate keys collapsed)

-- keep_parent_names: preserves full dot-path in column names
SELECT unnest({'a': 42, 'b': {'bb': {'bbb': 1}}},
              recursive := true, keep_parent_names := true);
-- columns: a=42, b.bb.bbb=1
```

> Note: `struct.*` is limited to top-level struct columns and non-aggregate expressions.
> `recursive := true` on structs does NOT recurse into any list fields within the struct.

### Nested schema evolution (DuckDB 1.3+)
```sql
ALTER TABLE t ADD COLUMN s.new_field INTEGER;
ALTER TABLE t DROP COLUMN s.old_field;
ALTER TABLE t RENAME s.old_name TO new_name;
```

---

## MAP

A `MAP` stores key-value pairs where keys and values each have a single type,
but different rows may have different keys. Use `MAP` when the schema varies
per row; use `STRUCT` when the schema is fixed.

`MAP` returns `NULL` for missing keys (vs STRUCT which throws an error).

### Creating maps
```sql
SELECT MAP {'key1': 10, 'key2': 20};                          -- literal
SELECT MAP(['key1','key2'], [10, 20]);                         -- from two lists
SELECT map_from_entries([{'k':'a','v':1},{'k':'b','v':2}]);   -- from struct list
SELECT map_from_entries([('a', 1), ('b', 2)]);                -- from tuple list

-- Typed map column
CREATE TABLE t (col MAP(VARCHAR, INTEGER));
```

### Accessing values
```sql
SELECT m['key1'] FROM t;                    -- NULL if missing (alias: map_extract_value)
SELECT map_extract_value(m, 'key1');        -- scalar: value or NULL
SELECT map_extract(m, 'key1');             -- list form: [value] or []
SELECT element_at(m, 'key1');              -- alias for map_extract
```

### Introspection
```sql
SELECT map_keys(m);                         -- LIST of keys
SELECT map_values(m);                       -- LIST of values
SELECT map_entries(m);                      -- LIST of {key, value} structs
SELECT cardinality(m);                      -- number of entries
SELECT map_contains(m, 'key1');            -- bool: key present?
SELECT map_contains_value(m, 42);          -- bool: value present?
SELECT map_contains_entry(m, 'key1', 42); -- bool: exact pair present?
```

### Merging maps
```sql
-- On key collision, last map wins
SELECT map_concat(MAP{'a':1,'b':2}, MAP{'b':99,'c':3});
-- Result: {a=1, b=99, c=3}
```

### When to use MAP vs STRUCT
| | STRUCT | MAP |
|---|---|---|
| Keys | Fixed, known at schema time | Variable per row |
| Key type | Always VARCHAR | Any type |
| Value types | Can differ per field | Single type for all values |
| Missing key | Error | Returns NULL |
| Access | Dot / bracket | Bracket / `map_extract_value` |

---

## Regular Expressions (RE2)

DuckDB uses the [RE2](https://github.com/google/re2/wiki/Syntax) engine.
Patterns do **not** require the whole string to match unless anchored with
`^`/`$`. Max 9 capture groups (`\1`..`\9`).

### Test and match
```sql
-- Contains the pattern (partial match)
SELECT regexp_matches('hello world', 'wor');         -- true
SELECT regexp_matches('hello world', '^hello$');     -- false (not whole string)

-- Whole string must match
SELECT regexp_full_match('hello', 'hel+o');          -- true
SELECT regexp_full_match('hello world', 'hel+o');    -- false

-- Options: 'i' case-insensitive, 'c' case-sensitive (default), 's' dot matches newline
SELECT regexp_matches('Hello', 'hello', 'i');        -- true
```

`regexp_matches` is optimized to `LIKE` when possible — pass `'c'` for best performance.

### Replace
```sql
SELECT regexp_replace('aabbcc', 'b+', 'X');          -- 'aaXcc'  (first match)
SELECT regexp_replace('aabbcc', 'b+', 'X', 'g');     -- 'aaXcc'  (global, same here)
SELECT regexp_replace('abc', '(a)(b)', '\2\1');      -- 'bac'    (backreferences \1..\9)
SELECT regexp_replace('hello world', '\w+', 'X', 'g'); -- 'X X'
```

### Extract
```sql
-- Extract first match (group 0 = whole match, 1 = first group)
SELECT regexp_extract('2024-03-15', '\d{4}');            -- '2024'
SELECT regexp_extract('2024-03-15', '(\d+)-(\d+)', 2);  -- '03'

-- Named groups → returns STRUCT
SELECT regexp_extract('2024-03-15', '(\d+)-(\d+)-(\d+)', ['y','m','d']);
-- {'y':'2024','m':'03','d':'15'}

-- All matches → returns LIST
SELECT regexp_extract_all('a1 b2 c3', '\d+');    -- ['1','2','3']
SELECT regexp_extract_all('a1 b2 c3', '(\w)(\d)', 1); -- ['a','b','c'] (group 1)
```

### Split
```sql
SELECT regexp_split_to_array('a, b,,c', ',\s*');   -- ['a','b','','c']
SELECT regexp_split_to_table('a b  c', '\s+');     -- rows: 'a', 'b', 'c'
```

### Options reference
| Option | Effect |
|--------|--------|
| `'c'` | Case-sensitive (default) |
| `'i'` | Case-insensitive |
| `'g'` | Global replace (only for `regexp_replace`) |
| `'l'` | Treat pattern as literal string |
| `'m'`/`'n'`/`'p'` | Newline-sensitive: `.` doesn't match `\n` |
| `'s'` | Non-newline-sensitive: `.` matches everything |

---

## JSON

The `JSON` logical type is a validated `VARCHAR`. JSON is stored as text;
casts between JSON and DuckDB types are fully supported.

### Type basics
```sql
SELECT '{"a": 1}'::JSON;                    -- validated JSON
SELECT '[1, null, "x"]'::JSON;
-- Cast to DuckDB types
SELECT '{"duck": 42}'::JSON::STRUCT(duck INTEGER);  -- {'duck': 42}
SELECT {duck: 42}::JSON;                    -- '{"duck":42}'
SELECT '2024-03-15'::DATE::JSON;            -- '"2024-03-15"'
```

`JSON` uses **0-based indexing** for arrays.

### Extraction operators
```sql
-- -> returns JSON,  ->> returns VARCHAR
SELECT j->'$.family'       FROM t;   -- "anatidae"  (JSON, with quotes)
SELECT j->>'$.family'      FROM t;   -- anatidae    (VARCHAR, no quotes)
SELECT j->'$.species[0]'   FROM t;   -- "duck"
SELECT j->'$.species[*]'   FROM t;   -- ["duck","goose","swan"]

-- JSONPointer syntax (use /field/index)
SELECT json_extract(j, '/species/0') FROM t;  -- "duck"

-- Low precedence: wrap -> in parens for comparisons
SELECT ((j)->'$.count') = 42 FROM t;
```

Two syntaxes for paths:
- **JSONPath**: `$.field`, `$.arr[0]`, `$.arr[#-1]` (last), `$."key.with.dot"`
- **JSONPointer**: `/field`, `/arr/0`

### Scalar functions
```sql
SELECT json_valid('{"a":1}');              -- true
SELECT json_valid('{bad}');                -- false
SELECT json_type('{"a":1}');              -- 'OBJECT'
SELECT json_type('[1,2]');                 -- 'ARRAY'
SELECT json_keys('{"a":1,"b":2}');         -- ['a','b']
SELECT json_array_length('[1,2,3]');       -- 3
SELECT json_structure('[{"a":1},{"a":2}]'); -- [{"a":"BIGINT"}]
SELECT json_contains('{"k":"v"}', '"v"'); -- true  (needle must be valid JSON)
SELECT json_exists('{"a":1}', '$.b');     -- false
```

### Efficient multi-path extraction
```sql
-- Slow: parses JSON twice
SELECT json_extract(j,'$.a'), json_extract(j,'$.b') FROM t;

-- Fast: parses once, returns LIST
WITH x AS (SELECT json_extract(j, ['$.a','$.b']) AS vals FROM t)
SELECT vals[1] AS a, vals[2] AS b FROM x;
```

### Aggregate functions
```sql
SELECT json_group_array(col) FROM t;              -- [val1, val2, ...]
SELECT json_group_object(key_col, val_col) FROM t; -- {"k1":v1, "k2":v2}
SELECT json_group_structure(j) FROM t;            -- union schema of all rows
```

### Transform JSON to nested types
```sql
-- json_transform / from_json: parse JSON to STRUCT/LIST using a schema spec
SELECT json_transform(j, '{"name":"VARCHAR","scores":["INTEGER"]}') FROM t;
-- Missing keys become NULL; extra keys are ignored

SELECT from_json_strict(j, '{"count":"INTEGER"}') FROM t;
-- Throws error on type mismatch (vs silently NULLing)
```

### Table functions (lateral joins)
```sql
-- json_each: one row per top-level key or array element
SELECT je.key, je.value, je.type
FROM t, json_each(t.j) AS je;

-- json_each with path
SELECT je.key, je.value
FROM t, json_each(t.j, '$.species') AS je;

-- json_tree: depth-first traversal of entire JSON tree
SELECT je.fullkey, je.value, je.type
FROM t, json_tree(t.j) AS je;
```

Result columns: `key`, `value`, `type`, `atom`, `id`, `parent`, `fullkey`, `path`.

---

## QUALIFY Clause

`QUALIFY` filters rows based on window function results — analogous to `HAVING`
for aggregates. Avoids wrapping in a CTE or subquery.

### Basic syntax
```sql
SELECT ..., window_fn() OVER (...) AS alias
FROM t
QUALIFY condition_on_window_result;
```

`QUALIFY` goes after the `WINDOW` clause (if present) and before `ORDER BY`.

### Deduplication — keep latest per group
```sql
-- Most common pattern: keep one row per partition
SELECT *
FROM t
QUALIFY ROW_NUMBER() OVER (PARTITION BY pid ORDER BY updated_at DESC) = 1;
```

### Can reference window alias from SELECT
```sql
SELECT
    pid,
    value,
    ROW_NUMBER() OVER (PARTITION BY pid ORDER BY value DESC) AS rn
FROM t
QUALIFY rn <= 3;   -- top 3 per pid
```

### With named WINDOW clause
```sql
SELECT pid, value, ROW_NUMBER() OVER w AS rn
FROM t
WINDOW w AS (PARTITION BY pid ORDER BY value DESC)
QUALIFY rn = 1;
```

### Equivalent CTE (for reference — QUALIFY is preferred)
```sql
WITH ranked AS (
    SELECT *, ROW_NUMBER() OVER (PARTITION BY pid ORDER BY updated_at DESC) AS rn
    FROM t
)
SELECT * FROM ranked WHERE rn = 1;
```

### Common QUALIFY patterns
```sql
-- Deduplicate: first occurrence per group
QUALIFY ROW_NUMBER() OVER (PARTITION BY id ORDER BY created_at) = 1

-- Top-N per group
QUALIFY ROW_NUMBER() OVER (PARTITION BY category ORDER BY score DESC) <= 5

-- Latest non-null value per group
QUALIFY LAST_VALUE(val IGNORE NULLS) OVER (PARTITION BY id ORDER BY ts) = val

-- Percentile filter: keep records in top 10% by score within group
QUALIFY PERCENT_RANK() OVER (PARTITION BY dept ORDER BY score DESC) <= 0.1
```

---

## Discovering Functions at Runtime

Use `duckdb_functions()` to query what's available in the current deployment:

```sql
-- Find all list-related scalar functions
SELECT DISTINCT ON (function_name)
    function_name, return_type, parameters, parameter_types
FROM duckdb_functions()
WHERE function_type = 'scalar'
  AND function_name LIKE 'list%'
ORDER BY function_name;

-- Find all aggregate functions
SELECT function_name, return_type, parameters
FROM duckdb_functions()
WHERE function_type = 'aggregate'
ORDER BY function_name;

-- Find user-defined table macros
SELECT function_name, parameters, macro_definition
FROM duckdb_functions()
WHERE function_type = 'table_macro';

-- Find window functions
SELECT DISTINCT function_name
FROM duckdb_functions()
WHERE function_type = 'window'
ORDER BY function_name;
```

> Note: `description` and `parameter_types` fields in `duckdb_functions()` are
> often empty for built-in functions and always empty for user macros.
> Use `COMMENT ON MACRO` or a `macro_descriptions` table for operator-provided
> descriptions. See `duckdb://docs/datadomain-meta` for details.
