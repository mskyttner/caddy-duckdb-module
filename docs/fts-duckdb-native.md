# Native DuckDB Full-Text Search with Lance

For simpler deployments without the FTS sidecar, you can use DuckDB's Lance extension directly. This approach is ~3x slower than the Python sidecar (~200ms vs ~70ms for 1M documents) but avoids extra infrastructure.

## Prerequisites

```sql
INSTALL lance FROM community;
LOAD lance;
```

## 1. Create Lance File from Table

Export your table to a Lance file:

```sql
-- Export table to parquet first
COPY works TO '/data/works.parquet' (FORMAT PARQUET);
```

Then create the Lance file (via Python or DuckDB):

```sql
-- DuckDB can read parquet and write to lance
COPY (SELECT * FROM '/data/works.parquet')
TO '/data/works.lance' (FORMAT LANCE);
```

## 2. Create Inverted Index

```sql
CREATE INDEX abstract_idx ON '/data/works.lance' (abstract) USING INVERTED;

-- Verify index was created
SHOW INDEXES ON '/data/works.lance';
```

Default index settings (ngram 3-3 tokenization):
- `base_tokenizer`: simple
- `ngram_min_length`: 3, `ngram_max_length`: 3
- `remove_stop_words`: true
- `ascii_folding`: true

## 3. Create the `api_find` Macro

```sql
CREATE OR REPLACE MACRO api_find(search_query, result_limit := 10) AS TABLE
SELECT * FROM lance_fts(
    '/data/works.lance',
    'abstract',
    search_query,
    prefilter := true,
    k := result_limit
);
```

## 4. Usage

```sql
-- Basic search
SELECT * FROM api_find('machine learning');

-- With custom limit
SELECT * FROM api_find('climate change', result_limit := 50);

-- With additional filtering
SELECT id, title, abstract
FROM api_find('neural network', result_limit := 100)
WHERE year > 2020;
```

## Performance

| Dataset Size | Query Latency |
|--------------|---------------|
| 1M documents | ~200ms |
| 200K documents | ~50ms |

For latency-sensitive applications (<100ms), use the [Python FTS sidecar](../cmd/fts-sidecar-python/) instead.

## Complete Setup Script

Save as `setup_fts.sql`:

```sql
-- Load extension
INSTALL lance FROM community;
LOAD lance;

-- Create inverted index (run once)
CREATE INDEX IF NOT EXISTS abstract_idx
ON '/data/works.lance' (abstract) USING INVERTED;

-- Create search macro
CREATE OR REPLACE MACRO api_find(search_query, result_limit := 10) AS TABLE
SELECT * FROM lance_fts(
    '/data/works.lance',
    'abstract',
    search_query,
    prefilter := true,
    k := result_limit
);
```

Run with: `duckdb /data/main.db < setup_fts.sql`
