# FTS Sidecar Setup Guide

This guide shows how to set up full-text search using the Python FTS sidecar service, which provides faster search (~10-70ms) compared to native DuckDB Lance (~200ms).

## Architecture

```
┌─────────────────┐     ┌─────────────────┐
│  Caddy DuckDB   │────▶│  FTS Sidecar    │
│   (port 8080)   │     │  (port 8701)    │
│   /duckdb/find  │◀────│  LanceDB+Tantivy│
└────────┬────────┘     └────────┬────────┘
         │                       │
         ▼                       ▼
    ┌─────────┐           ┌─────────────┐
    │ DuckDB  │           │ Lance Index │
    │ (.db)   │           │  (.lance)   │
    └─────────┘           └─────────────┘
```

## Quick Start

### 1. Start Services

```bash
# Ensure FTS index directory is writable
mkdir -p data/fts && chmod 777 data/fts

# Start with FTS enabled
docker compose -f docker-compose.yml -f docker-compose.fts.yml up -d
```

For development with local volume mounts:
```bash
docker compose -f docker-compose.yml -f docker-compose.fts.yml -f docker-compose.override.yml up -d
```

### 2. Create FTS Index

Prepare your data as a parquet file, then create the index:

```bash
# Create index from parquet file
curl -X POST http://localhost:8701/index \
  -H "Content-Type: application/json" \
  -d '{
    "table": "works_food",
    "source": "/data/works_food.parquet",
    "fts_columns": ["title", "abstract"],
    "replace": true,
    "id_field": "id",
    "display_field": "title"
  }'
```

**Parameters:**
| Parameter | Required | Description |
|-----------|----------|-------------|
| `table` | Yes | Name for the FTS index |
| `source` | Yes | Path to parquet file (inside container) |
| `fts_columns` | Yes | Columns to index for full-text search |
| `replace` | No | Replace existing index (default: false) |
| `id_field` | No | Field to use as `id` in autocomplete |
| `display_field` | No | Field to use as `display` in autocomplete |
| `hint_fields` | No | Fields to combine into `hint` in autocomplete |

### 3. Search

**Full search:**
```bash
curl -H "X-API-Key: YOUR_KEY" \
  "http://localhost:8080/duckdb/find?q=food+safety&table=works_food&limit=10"
```

**Autocomplete mode:**
```bash
curl -H "X-API-Key: YOUR_KEY" \
  "http://localhost:8080/duckdb/find?q=nutri&table=works_food&limit=5&autocomplete=true"
```

## API Reference

### GET /duckdb/find

| Parameter | Default | Description |
|-----------|---------|-------------|
| `q` | required | Search query |
| `table` | required | FTS index name |
| `limit` | 10 | Max results (1-1000) |
| `offset` | 0 | Pagination offset |
| `columns` | all | Comma-separated columns to return |
| `autocomplete` | false | Return minimal response (id, display, hint) |

### Response (full search)

```json
{
  "query": "food safety",
  "table": "works_food",
  "hits": [
    {
      "title": "Food Safety Management...",
      "abstract": "...",
      "_score": 13.14
    }
  ],
  "total_hits": 2,
  "execution_time_ms": 12
}
```

### Response (autocomplete)

```json
{
  "query": "nutri",
  "table": "works_food",
  "results": [
    {
      "id": "4402710327",
      "display": "Creating a Diversified Sustainable Food System...",
      "_score": 20.25
    }
  ],
  "count": 3,
  "execution_time_ms": 10
}
```

## Performance

| Dataset | Index Time | Search | Autocomplete |
|---------|------------|--------|--------------|
| 45K docs | ~700ms | ~12ms | ~10ms |
| 200K docs | ~3s | ~15ms | ~12ms |
| 1M docs | ~15s | ~70ms | ~50ms |

## Management

**List indexes:**
```bash
curl http://localhost:8701/indexes
```

**Delete index:**
```bash
curl -X DELETE "http://localhost:8701/index?table=works_food"
```

**Health check:**
```bash
curl http://localhost:8701/health
```

## Troubleshooting

### Permission Denied

The FTS sidecar runs as non-root user `fts`. Ensure the index directory is writable:
```bash
chmod 777 data/fts
```

### Index Not Found After Restart

FTS column metadata is not persisted. Recreate the index after container restart:
```bash
curl -X POST http://localhost:8701/index -H "Content-Type: application/json" \
  -d '{"table": "works_food", "source": "/data/works_food.parquet", "fts_columns": ["title", "abstract"], "replace": true}'
```

### Volume Not Shared

Both containers must access the same `/data` volume. In development, add to `docker-compose.override.yml`:
```yaml
services:
  fts-sidecar:
    volumes:
      - ./data:/data
```
