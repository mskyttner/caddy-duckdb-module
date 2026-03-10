# Caddy DuckDB Module

A Caddy server module that provides a REST API for DuckDB database operations with built-in authentication and authorization.

## Features

- **Dual Database Architecture**: Separate main database (file or in-memory) and internal auth database
- **CRUD Operations**: RESTful API for Create, Read, Update, Delete operations on tables
- **Raw SQL Queries**: Execute custom SQL queries with proper authorization
- **Write SQL (Execute)**: INSERT/UPDATE/DELETE/DDL via dedicated endpoint with `can_execute` permission
- **Bulk Export**: Run any SQL and get a server-side file URL — token-efficient for LLM clients
- **MCP Endpoint**: Model Context Protocol streamable-HTTP endpoint for LLM tool use
- **Multi-Format Responses**: JSON, CSV, Parquet, Apache Arrow IPC
- **Advanced Querying**: Pagination, sorting, filtering, sparse fieldsets, group-by aggregation, keyset cursor pagination
- **Column Schema**: Endpoint for column types and optional statistics
- **httpserver Compatibility**: POST raw SQL in body — compatible with duck-ui
- **Authentication**: API key (header, query param, Basic auth) + trusted-user-header (SSO/vouch-proxy)
- **Authorization**: Role-based permissions at table level, with separate `can_execute` permission for write SQL
- **CORS Support**: Configurable allowed origins
- **Transactional Writes**: All write operations are atomic
- **SQL Injection Protection**: Query parameters and input validation
- **Configurable Timeouts**: Query timeout protection
- **Swagger UI + OpenAPI spec**: Built-in interactive docs at `/duckdb/docs`

## Quick Start

Get up and running in under 2 minutes:

```bash
# Clone the repository
git clone https://github.com/mskyttner/caddy-duckdb-module.git
cd caddy-duckdb-module

# Build the server and tools
make build build-tools

# Initialize auth database and create an admin API key
./tools/auth-db init -d data/auth.db
./tools/auth-db key add -d data/auth.db -r admin
# Save the displayed API key!

# Start the server
make run
```

Test the API:

```bash
# Replace YOUR_API_KEY with the key from setup output
curl -H "X-API-Key: YOUR_API_KEY" \
     -H "Content-Type: application/json" \
     -d '{"sql": "SELECT 1 AS test"}' \
     http://localhost:8080/duckdb/query
```

For more end-to-end examples including filtering, pagination, updates, and deletes, see [EXAMPLE_QUERIES.md](EXAMPLE_QUERIES.md).

For Docker deployment, see [Docker](#docker) section below.

For LLM integration (MCP and token-saving strategies), see [docs/llms.txt](docs/llms.txt).

## Building

### Prerequisites

- Go 1.24 or later
- CGO enabled (required for DuckDB bindings)
- C compiler (gcc, clang, or MSVC depending on platform)
- xcaddy (for building Caddy with custom modules)

### Important Notes

This module uses the official DuckDB Go driver (`github.com/duckdb/duckdb-go/v2`) which:
- Requires CGO to be enabled
- Downloads platform-specific binaries automatically during build
- Supports Linux (amd64, arm64), macOS (amd64, arm64), and Windows (amd64)

### Build with Go

The recommended way to build Caddy with the DuckDB module is using the provided build configuration:

```bash
# Clone the repository
git clone https://github.com/mskyttner/caddy-duckdb-module.git
cd caddy-duckdb-module

# Download dependencies
go mod download

# Build Caddy with DuckDB module and tools
make build build-tools
```

This will produce a `caddy` binary (~107MB) and `tools/auth-db` in the current directory.

### Alternative: Build with xcaddy

You can also use xcaddy for published versions:

```bash
# Install xcaddy
go install github.com/caddyserver/xcaddy/cmd/xcaddy@latest

# Build from GitHub (CGO_ENABLED=1 is required!)
CGO_ENABLED=1 xcaddy build --with github.com/mskyttner/caddy-duckdb-module

# Build from local source
CGO_ENABLED=1 xcaddy build --with github.com/mskyttner/caddy-duckdb-module=.
```

**Important:** You must set `CGO_ENABLED=1` when using xcaddy. Without it, the DuckDB C bindings won't compile and the build will fail with "undefined: bindings.Type" errors.

### Module-Only Build (For Testing)

To verify the module compiles correctly without building the full Caddy binary:

```bash
CGO_ENABLED=1 go build
```

This creates a package archive (not an executable) for testing purposes only.

### Build Troubleshooting

**Network Issues:**
If you encounter network errors downloading dependencies, ensure you have internet access. The DuckDB bindings will be downloaded automatically from GitHub on first build.

**CGO Errors:**
If you get CGO-related errors, ensure:
- CGO is enabled: `export CGO_ENABLED=1`
- A C compiler is installed (gcc on Linux, clang on macOS, MSVC on Windows)

**Platform-Specific Issues:**
- **Linux**: Install build-essential: `apt-get install build-essential` (Debian/Ubuntu)
- **macOS**: Install Xcode Command Line Tools: `xcode-select --install`
- **Windows**: Install MinGW-w64 or use Visual Studio Build Tools

## Configuration

### Caddyfile

```caddyfile
:8080 {
    route /duckdb/* {
        duckdb {
            # Main database path (optional, defaults to in-memory)
            database_path /data/main.db

            # Auth database path (required)
            auth_database_path /data/auth.db

            # Query timeout (default: 10s)
            query_timeout 10s

            # Max rows per page (default: 100)
            max_rows_per_page 100

            # Safety limit - max rows without pagination (default: 10000, 0 to disable)
            absolute_max_rows 10000

            # Number of threads (default: 4)
            threads 4

            # Access mode: read_only or read_write (default: read_write)
            access_mode read_write

            # Memory limit (optional, e.g., "4GB", "512MB". Default: 80% of RAM)
            # memory_limit 4GB

            # Enable object cache for faster repeated queries (optional, default: false)
            # enable_object_cache true

            # Temporary directory for spilling to disk (optional)
            # temp_directory /tmp/duckdb-temp

            # SQL file to run at startup (optional)
            # init_file /data/init.sql

            # Full-text search sidecar URL (optional)
            # fts_service_url http://fts-sidecar:8701

            # Allowed CORS origins — space-separated, or "*" for all (optional)
            # cors_origins http://localhost:5522 https://myapp.example.com

            # Trusted user header for SSO/vouch-proxy auth (optional)
            # trusted_user_header X-Vouch-User

            # Export endpoint settings (optional; exports_dir required to enable)
            # exports_dir        /data/exports
            # exports_url        /duckdb/exports
            # export_ttl_minutes 60

            # MCP row limit (default: 500)
            # max_mcp_rows 500
        }
    }
}
```

### Configuration Parameters

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `database_path` | string | `:memory:` | Path to main database file. Omit for in-memory database. |
| `auth_database_path` | string | *required* | Path to authentication database (must be file-based). |
| `query_timeout` | duration | `10s` | Maximum query execution time. |
| `max_rows_per_page` | int | `100` | Default page size when pagination is used. |
| `absolute_max_rows` | int | `10000` | Safety limit - max rows without pagination. Set to `0` to disable. |
| `threads` | int | `4` | Number of threads for DuckDB query execution. |
| `access_mode` | string | `read_write` | Database access mode: `read_only` or `read_write`. |
| `memory_limit` | string | *80% of RAM* | Max memory DuckDB can use (e.g., `"4GB"`, `"512MB"`). Optional. |
| `enable_object_cache` | bool | `false` | Enable DuckDB's object cache for faster repeated queries. Optional. |
| `temp_directory` | string | *system default* | Directory for temporary files when spilling to disk. Optional. |
| `init_file` | string | *unset* | SQL file to execute once on startup. Optional. |
| `fts_service_url` | string | *unset* | Full-text search sidecar URL (e.g. `http://fts:8701`). Optional. |
| `cors_origins` | string | *unset* | Space-separated allowed CORS origins, or `*`. Optional. |
| `trusted_user_header` | string | *unset* | HTTP header carrying a pre-authenticated username (e.g. `X-Vouch-User`). Optional. |
| `exports_dir` | string | *unset* | Filesystem directory for export files. Required to enable `/export`. |
| `exports_url` | string | `<prefix>/exports` | URL prefix for serving exported files. |
| `export_ttl_minutes` | int | `60` | How long exported files are kept before cleanup. |
| `max_mcp_rows` | int | `500` | Maximum rows returned by the MCP query tool. |

**Performance Tuning:**
- **`threads`**: Set to number of CPU cores for best performance
- **`memory_limit`**: Prevent DuckDB from consuming too much memory
- **`enable_object_cache`**: Useful for analytical workloads with repeated queries on parquet/S3
- **`temp_directory`**: Important for queries that exceed memory limits

**Safety Features:**
- **`absolute_max_rows`**: Prevents accidentally large responses when pagination is not specified
- **`query_timeout`**: Protects against long-running queries

## Docker

### Pre-built Images

Docker images are available from GitHub Container Registry:

```bash
docker pull ghcr.io/mskyttner/caddy-duckdb-module:latest
```

Available tags:
- `latest` - Latest stable release from main branch
- `x.y.z` - Specific version (e.g., `1.0.0`)
- `sha-<commit>` - Specific commit

Supported platforms: `linux/amd64`

### Quick Start with Docker

```bash
# Set image name
export IMAGE=ghcr.io/mskyttner/caddy-duckdb-module:latest

# Create data directory and initialize auth database
mkdir -p data
./tools/auth-db init -d data/auth.db
./tools/auth-db key add -d data/auth.db -r admin
# Save the displayed API key!

# Start the container
docker run -d \
  --name caddy-duckdb \
  -p 8080:8080 \
  -v $(pwd)/data:/data \
  $IMAGE

# Verify it's running
curl http://localhost:8080/duckdb/health
# {"status":"ok"}
```

### Using Docker Compose

```bash
# Prefer podman-compose if available
podman-compose up -d

# Or docker-compose
docker-compose up -d
```

### Health Check

The container includes a health check endpoint at `/{DUCKDB_ROUTE_PREFIX}/health` (default: `/duckdb/health`):

```bash
curl http://localhost:8080/duckdb/health
# {"status":"ok"}
```

This endpoint requires no authentication.

### Environment Variables

All settings can be configured via environment variables:

| Variable | Default | Description |
|----------|---------|-------------|
| `DUCKDB_PORT` | `8080` | Server port |
| `DUCKDB_ROUTE_PREFIX` | `/duckdb` | API route prefix |
| `DUCKDB_DATABASE_PATH` | `/data/main.db` | Main database path |
| `DUCKDB_AUTH_DATABASE_PATH` | `/data/auth.db` | Auth database path |
| `DUCKDB_THREADS` | `4` | Number of query threads |
| `DUCKDB_MEMORY_LIMIT` | *(80% RAM)* | Memory limit (e.g., `4GB`) |
| `DUCKDB_QUERY_TIMEOUT` | `10s` | Query timeout |
| `DUCKDB_ACCESS_MODE` | `read_write` | `read_only` or `read_write` |
| `DUCKDB_MAX_ROWS_PER_PAGE` | `100` | Default pagination size |
| `DUCKDB_ABSOLUTE_MAX_ROWS` | `10000` | Max rows without pagination |
| `DUCKDB_ENABLE_OBJECT_CACHE` | `false` | Object cache for parquet/S3 |
| `DUCKDB_TEMP_DIRECTORY` | *(unset)* | Spill directory |
| `DUCKDB_INIT_FILE` | *(unset)* | SQL file run on startup |
| `DUCKDB_FTS_SERVICE_URL` | *(unset)* | FTS sidecar URL |
| `DUCKDB_CORS_ORIGINS` | *(unset)* | Space-separated allowed origins, or `*` |
| `DUCKDB_EXPORTS_DIR` | *(unset)* | Directory for export files (required to enable `/export`) |
| `DUCKDB_EXPORTS_URL` | `<prefix>/exports` | URL prefix for exported files |
| `DUCKDB_EXPORT_TTL_MINUTES` | `60` | Export file lifetime in minutes |
| `DUCKDB_MAX_MCP_ROWS` | `500` | Max rows returned by MCP query tool |

Example with custom settings:

```bash
docker run -d \
  --name caddy-duckdb \
  -p 8080:8080 \
  -e DUCKDB_THREADS=8 \
  -e DUCKDB_MEMORY_LIMIT=4GB \
  -e DUCKDB_QUERY_TIMEOUT=30s \
  -e DUCKDB_EXPORTS_DIR=/data/exports \
  -v $(pwd)/data:/data \
  caddy-duckdb
```

### Custom Caddyfile

Mount your own Caddyfile for advanced configuration:

```bash
docker run -d \
  --name caddy-duckdb \
  -p 8080:8080 \
  -v $(pwd)/data:/data \
  -v $(pwd)/my-caddyfile:/etc/caddy/Caddyfile:ro \
  caddy-duckdb
```

See `examples/Caddyfile.docker` for a full reference Caddyfile with all options documented.

## Authentication & Authorization

### Authentication Methods

Three equivalent methods are supported:

```bash
# 1. X-API-Key header (preferred)
curl -H "X-API-Key: your-key" http://localhost:8080/duckdb/api/users

# 2. Query parameter
curl "http://localhost:8080/duckdb/api/users?api_key=your-key"

# 3. HTTP Basic auth (username = "apikey", password = key)
curl -u "apikey:your-key" http://localhost:8080/duckdb/api/users
```

### Trusted-User-Header Auth (SSO / vouch-proxy)

When running behind vouch-proxy or another forward-auth proxy, you can configure the module to trust a header containing the pre-authenticated username:

```caddyfile
duckdb {
    trusted_user_header X-Vouch-User
    # ...
}
```

Users are stored in the `trusted_users` table of the auth database and are managed via `auth-db user`:

```bash
./tools/auth-db user add -d data/auth.db -u alice@example.com -r admin
./tools/auth-db user list -d data/auth.db
./tools/auth-db user remove -d data/auth.db -u alice@example.com
```

The trusted header is checked before the API key. If the header is present and the user is known, no API key is required. OPTIONS preflight requests bypass auth entirely (required for CORS).

### Auth Database Setup

The auth database must be initialized **before** starting the server:

```bash
# Build the CLI tool
make build-tools

# Initialize auth database with default roles (admin, editor, reader)
./tools/auth-db init -d data/auth.db

# Create an admin API key
./tools/auth-db key add -d data/auth.db -r admin
```

### Built-in Roles

- **admin**: Full CRUD + raw SQL queries + write SQL (execute) on all tables
- **editor**: CRUD on all tables, no raw SQL or execute
- **reader**: Read-only on all tables

### Creating and Managing API Keys

```bash
# Add a key for a role
./tools/auth-db key add -d data/auth.db -r admin

# List all keys (truncated)
./tools/auth-db key list -d data/auth.db

# List all keys with full key values
./tools/auth-db key list -d data/auth.db --show-keys

# Remove a key
./tools/auth-db key remove -d data/auth.db -k <key>

# With expiration
./tools/auth-db key add -d data/auth.db -r reader -e 2025-12-31T23:59:59Z
```

### Custom Roles and Permissions

```bash
# Create a custom role
./tools/auth-db role add -d data/auth.db -n analyst --desc "Data analyst"

# Grant permissions (operations: c=create, r=read, u=update, d=delete, q=query, e=execute)
./tools/auth-db permission add -d data/auth.db -r analyst -t reports -o r,q

# Grant all CRUD operations on all tables
./tools/auth-db permission add -d data/auth.db -r analyst -t "*" -o crud

# List all roles and permissions
./tools/auth-db role list -d data/auth.db
./tools/auth-db permission list -d data/auth.db
```

### Migrating Existing Auth Databases

If you have an auth database from before the `execute` permission was added, run:

```bash
./tools/auth-db migrate -d data/auth.db
```

This adds the `can_execute` column to the permissions table (idempotent — safe to run multiple times).

### Auth Database Info

```bash
./tools/auth-db info -d data/auth.db
```

## API Endpoints

All endpoints are under the configured route prefix (default: `/duckdb`). All requests require authentication unless noted.

### Summary

| Endpoint | Method | Auth | Description |
|----------|--------|------|-------------|
| `/api/{table}` | GET | key | List rows with filtering/pagination |
| `/api/{table}` | POST | key | Insert a row |
| `/api/{table}` | PUT | key | Update rows |
| `/api/{table}` | DELETE | key | Delete rows |
| `/api/{table}/columns` | GET | key | Column schema + optional statistics |
| `/view/{name}/columns` | GET | key | Same for views |
| `/query` | POST | key | Run read-only SQL (parameterized) |
| `/query/{sql}/result.{fmt}` | GET | key | Read-only SQL via URL |
| `/execute` | POST | key + `can_execute` | Run write SQL (INSERT/UPDATE/DELETE/DDL) |
| `/export` | POST | key | Run SQL → server file → return URL |
| `/mcp` | POST | key | MCP streamable-HTTP endpoint |
| `/` | POST | key | httpserver-compatible: raw SQL body |
| `/` | HEAD | key | Endpoint probe (duck-ui) |
| `/openapi.json` | GET | none | OpenAPI 3.0 spec |
| `/docs/` | GET | none | Swagger UI |
| `/health` | GET | none | Health check |
| `/find` | GET | key | Full-text search (requires FTS sidecar) |

### CRUD Operations

Base path: `/duckdb/api/{table}`

#### Create (POST)

```bash
curl -X POST http://localhost:8080/duckdb/api/users \
  -H "X-API-Key: your-api-key" \
  -H "Content-Type: application/json" \
  -d '{"name": "John Doe", "email": "john@example.com", "age": 30}'
# {"success":true,"rows_affected":1}
```

#### Read (GET)

```bash
# Basic query
curl "http://localhost:8080/duckdb/api/users" -H "X-API-Key: your-api-key"

# Pagination
curl "http://localhost:8080/duckdb/api/users?page=1&limit=50" -H "X-API-Key: your-api-key"

# Filter
curl "http://localhost:8080/duckdb/api/users?filter=age:gt:18,status:eq:active" -H "X-API-Key: your-api-key"

# Sort
curl "http://localhost:8080/duckdb/api/users?sort=created_at:desc,name:asc" -H "X-API-Key: your-api-key"

# Sparse fieldsets — return only specified columns
curl "http://localhost:8080/duckdb/api/users?select=id,name,email" -H "X-API-Key: your-api-key"

# Group-by aggregation
curl "http://localhost:8080/duckdb/api/users?group_by=status" -H "X-API-Key: your-api-key"
# {"group_by":[{"key":"active","count":42},{"key":"inactive","count":8}]}

# Keyset (cursor) pagination — use cursor=* for first page
curl "http://localhost:8080/duckdb/api/users?cursor=*&limit=20" -H "X-API-Key: your-api-key"
# Response includes "next_cursor" in meta when more pages exist

# With HATEOAS links
curl "http://localhost:8080/duckdb/api/users?page=2&limit=10&links=true" -H "X-API-Key: your-api-key"
```

##### Filter Operators

- `eq`: Equal
- `ne`: Not equal
- `gt`: Greater than
- `gte`: Greater than or equal
- `lt`: Less than
- `lte`: Less than or equal
- `like`: SQL LIKE pattern
- `in`: IN clause (use pipe `|` to separate values)

#### Update (PUT)

```bash
curl -X PUT http://localhost:8080/duckdb/api/users \
  -H "X-API-Key: your-api-key" \
  -H "Content-Type: application/json" \
  -d '{"where": {"id": 1}, "set": {"age": 31}}'
# {"success":true,"rows_affected":1}
```

#### Delete (DELETE)

```bash
curl -X DELETE "http://localhost:8080/duckdb/api/users?where=id:eq:1" \
  -H "X-API-Key: your-api-key"
# {"success":true,"rows_affected":1}
```

### Column Schema Endpoint

```bash
# Standard format — column names and types
curl "http://localhost:8080/duckdb/api/users/columns" -H "X-API-Key: your-api-key"

# Transform format — {name: type} map for DuckDB json_transform()
curl "http://localhost:8080/duckdb/api/users/columns?format=transform" -H "X-API-Key: your-api-key"

# Summarize format — adds min/max/approx_unique/null_percentage stats
curl "http://localhost:8080/duckdb/api/users/columns?format=summarize" -H "X-API-Key: your-api-key"

# Add statistics to standard format
curl "http://localhost:8080/duckdb/api/users/columns?stats=true&sample=5000" -H "X-API-Key: your-api-key"

# Same for views
curl "http://localhost:8080/duckdb/view/active_users/columns" -H "X-API-Key: your-api-key"
```

### Raw SQL Queries

Endpoint: `POST /duckdb/query` — Requires `can_query` permission (admin role by default).

**POST Method** (supports parameterized queries):

```bash
curl -X POST http://localhost:8080/duckdb/query \
  -H "X-API-Key: your-api-key" \
  -H "Content-Type: application/json" \
  -d '{"sql": "SELECT * FROM users WHERE age > ?", "params": [18]}'
```

**GET Method** (read-only, bookmarkable):

```bash
# Pattern: /duckdb/query/{urlEncodedSQL}/result.{format}
curl "http://localhost:8080/duckdb/query/SELECT%20*%20FROM%20users/result.json" \
  -H "X-API-Key: your-api-key"
```

GET is limited to SELECT, SHOW, DESCRIBE, EXPLAIN queries.

### Execute Endpoint (Write SQL)

`POST /duckdb/execute` — Requires the `can_execute` permission (separate from `can_query`). Admin role has this by default; other roles must be explicitly granted.

```bash
# INSERT
curl -X POST http://localhost:8080/duckdb/execute \
  -H "X-API-Key: your-admin-key" \
  -H "Content-Type: application/json" \
  -d '{"sql": "INSERT INTO users (name, email) VALUES (?, ?)", "params": ["Alice", "alice@example.com"]}'
# {"success":true,"rows_affected":1}

# DDL
curl -X POST http://localhost:8080/duckdb/execute \
  -H "X-API-Key: your-admin-key" \
  -H "Content-Type: application/json" \
  -d '{"sql": "CREATE TABLE products (id INTEGER PRIMARY KEY, name VARCHAR, price DOUBLE)"}'
# {"success":true,"rows_affected":0}
```

If the database was created before this feature, run `./tools/auth-db migrate -d data/auth.db` to add the `can_execute` column.

### Export Endpoint (Token-Efficient Bulk Access)

`POST /duckdb/export` — Runs SQL, writes the result to a server-side file, and returns a download URL plus metadata. Ideal for LLM clients that need large datasets without consuming context tokens.

Requires `exports_dir` to be configured. Supports parquet (default), csv, and json.

```bash
curl -X POST http://localhost:8080/duckdb/export \
  -H "X-API-Key: your-api-key" \
  -H "Content-Type: application/json" \
  -d '{"sql": "SELECT * FROM users WHERE age > 18", "format": "parquet"}'
```

Response (~15 tokens regardless of result size):
```json
{
  "url": "/duckdb/exports/abc123.parquet",
  "filename": "abc123.parquet",
  "format": "parquet",
  "rows": 42,
  "size_bytes": 1234,
  "expires_at": "2025-01-01T01:00:00Z"
}
```

Exported files are served as static files from the configured `exports_dir` and are cleaned up after `export_ttl_minutes` (default: 60).

### httpserver-Compatible Endpoint

`POST /duckdb/` — Accepts raw SQL in the request body; compatible with [duck-ui](https://github.com/caioricciuti/duck-ui) and other ClickHouse-compatible clients.

```bash
# Raw SQL body
curl -X POST http://localhost:8080/duckdb/ \
  -H "X-API-Key: your-api-key" \
  -d "SELECT * FROM users LIMIT 5"

# HEAD probe (used by duck-ui on startup)
curl -I -X HEAD http://localhost:8080/duckdb/ -H "X-API-Key: your-api-key"
```

Format negotiation order: `?default_format=` → `X-ClickHouse-Format:` header → `format:` header → `Accept:` header → default (JSONCompact).

Only read-only SQL is accepted (SELECT, SHOW, DESCRIBE, EXPLAIN). Write queries return 403.

**duck-ui connection settings:**
- Host: `http://<host>/duckdb`
- Authentication: `api_key` with your API key

### MCP Endpoint

`POST /duckdb/mcp` — Model Context Protocol streamable-HTTP endpoint for LLM clients. Implements JSON-RPC 2.0 over HTTP.

See [docs/llms.txt](docs/llms.txt) for a complete LLM integration guide.

Available MCP tools:

| Tool | Description |
|------|-------------|
| `query` | Run read-only SQL, returns up to `max_mcp_rows` rows (default: 500) |
| `execute` | Run write SQL (requires `can_execute` permission) |
| `export` | Run SQL → server file → return URL (requires `exports_dir`) |
| `list_tables` | List all non-internal tables |
| `describe` | Column schema for a table or view |
| `database_info` | Database statistics and metadata |

```bash
# Initialize MCP session
curl -X POST http://localhost:8080/duckdb/mcp \
  -H "X-API-Key: your-api-key" \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}}'

# List available tools
curl -X POST http://localhost:8080/duckdb/mcp \
  -H "X-API-Key: your-api-key" \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}'

# Call the query tool
curl -X POST http://localhost:8080/duckdb/mcp \
  -H "X-API-Key: your-api-key" \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"query","arguments":{"sql":"SELECT * FROM users LIMIT 5"}}}'
```

### Response Formats

Both `/api` and `/query` endpoints support multiple output formats:

| Format | Accept Header | File Extension | Best For |
|--------|---------------|----------------|----------|
| JSON | `application/json` | `.json` | Web APIs, debugging |
| CSV | `text/csv` | `.csv` | Spreadsheets, simple exports |
| Parquet | `application/parquet` | `.parquet` | Analytics, data lakes (5-10x smaller) |
| Arrow IPC | `application/vnd.apache.arrow.stream` | `.arrow` | Data pipelines, zero-copy transfers |

```bash
# Using Accept header
curl http://localhost:8080/duckdb/api/users -H "X-API-Key: key" -H "Accept: text/csv"
curl http://localhost:8080/duckdb/api/users -H "X-API-Key: key" -H "Accept: application/parquet" -o data.parquet

# Using file extension (GET query)
curl "http://localhost:8080/duckdb/query/SELECT%20*%20FROM%20users/result.parquet" -H "X-API-Key: key" -o data.parquet
```

## Security Features

1. **SQL Injection Protection**: All queries use parameterized statements
2. **Input Validation**: Table and column names are sanitized
3. **Internal Table Protection**: Auth tables (`api_keys`, `roles`, `permissions`, `trusted_users`) cannot be accessed via API
4. **API Key Hashing**: Keys are stored as bcrypt hashes
5. **Transactional Writes**: All modifications are atomic
6. **Query Timeouts**: Prevents long-running queries
7. **Role-Based Access**: Fine-grained permissions at table level
8. **Execute Permission**: Write SQL requires explicit `can_execute` grant (separate from read)
9. **CORS Preflight**: OPTIONS requests bypass auth; CORS origins are strictly enforced
10. **Request ID Tracing**: All requests include a unique request ID for distributed tracing

### Rate Limiting

Rate limiting is intentionally **not** implemented in this module. Caddy has excellent rate limiting plugins:

```caddyfile
:8080 {
    rate_limit {
        zone duckdb_api {
            key {remote_host}
            events 100
            window 1m
        }
    }

    route /duckdb/* {
        duckdb { # ... }
    }
}
```

### OpenAPI Specification

A complete OpenAPI 3.0 specification is available at `/duckdb/openapi.json`:

```bash
curl http://localhost:8080/duckdb/openapi.json
```

**Swagger UI** (interactive docs) is available at `/duckdb/docs/`.

For local development without Docker:

```bash
make swagger-ui    # Downloads to ./swagger-ui-dist
```

## Example Usage

### 1. Setup Auth Database

```bash
./tools/auth-db init -d data/auth.db
./tools/auth-db key add -d data/auth.db -r admin
# Output: ✓ API key created successfully!
#         API Key: <your-generated-key>
```

### 2. Start the Server

```bash
make run
```

### 3. Create a Table

```bash
curl -X POST http://localhost:8080/duckdb/query \
  -H "X-API-Key: my-admin-key" \
  -H "Content-Type: application/json" \
  -d '{
    "sql": "CREATE TABLE users (id INTEGER PRIMARY KEY, name VARCHAR, email VARCHAR, age INTEGER)"
  }'
```

### 4. Use CRUD Operations

See the [API Endpoints](#api-endpoints) section and [EXAMPLE_QUERIES.md](EXAMPLE_QUERIES.md) for full examples.

## Development

### Project Structure

```
caddy-duckdb-module/
├── module.go              # Caddy module entry point, ServeHTTP, config
├── config.go              # Configuration structs
├── database/
│   ├── manager.go         # Database connection manager, auth schema init
│   └── operations.go      # CRUD operations
├── auth/
│   ├── models.go          # Auth data structures (APIKey, Role, Permission, TrustedUser)
│   ├── authorizer.go      # API key + trusted user caching and lookup
│   └── middleware.go      # Auth middleware, internal table helpers
├── handlers/
│   ├── crud.go            # CRUD handlers
│   ├── query.go           # Query handler
│   ├── execute.go         # Execute handler (write SQL)
│   ├── export.go          # Export handler (SQL → file → URL)
│   ├── mcp.go             # MCP streamable-HTTP endpoint
│   ├── httpserver.go      # httpserver-compatible handler (POST /)
│   ├── columns.go         # Column schema endpoint
│   ├── params.go          # Parameter parsing
│   ├── internal_tables.go # Shared helpers: containsInternalTables, stripSQLComments
│   └── openapi.go         # OpenAPI 3.0 specification handler
├── formats/
│   ├── json.go            # JSON formatter
│   ├── csv.go             # CSV formatter
│   ├── parquet.go         # Apache Parquet formatter
│   └── arrow.go           # Apache Arrow IPC formatter
├── tools/
│   └── auth-db-src/
│       └── main.go        # auth-db CLI tool source
├── docs/
│   └── llms.txt           # LLM integration guide (MCP + token-saving)
└── examples/
    └── Caddyfile.docker   # Reference Caddyfile for Docker with all env vars
```

### Make Targets

| Target | Description |
|--------|-------------|
| `make build` | Build the Caddy binary |
| `make build-tools` | Build the auth-db CLI tool |
| `make build-all` | Build both |
| `make run` | Build and run with example Caddyfile |
| `make test` | Run all tests |
| `make fmt` | Format code |
| `make vet` | Run go vet |
| `make lint` | Run fmt + vet |
| `make clean` | Remove build artifacts |
| `make swagger-ui` | Download Swagger UI to ./swagger-ui-dist |
| `make docker-build` | Build Docker image |
| `make docker-run` | Run with docker-compose |
| `make help` | Show all available targets |

### Running Tests

```bash
make test
# or directly:
go test ./...
```

Integration tests in `example_queries_test.go` cover all endpoints and features using an in-memory DuckDB instance.

## Concurrency and Multi-User Support

### Concurrent Operations

This module handles multiple simultaneous requests efficiently:

- **Concurrent Reads**: Multiple users can read simultaneously without blocking
- **Concurrent Inserts**: Multiple users can insert into the same or different tables
- **Mixed Read/Write**: Reads are never blocked by writes (DuckDB MVCC)
- **Connection Pooling**: Configured to support `threads * 2` concurrent connections

Write operations automatically retry on conflicts with exponential backoff (up to 3 attempts).

### Important Limitations

**Single-Process Only:**
DuckDB is designed for single-process access. This module works within one Caddy server instance but **does not support**:
- Multiple Caddy instances writing to the same database file
- Distributed deployments with shared database files

For multi-instance deployments:
1. Use **read-only replicas**: Configure additional instances with `access_mode read_only`
2. Use a **single writer** instance for all write operations

## Limitations

- **Multi-Process Writes**: Not supported — only one Caddy instance can write to a database file
- Internal auth tables (`api_keys`, `roles`, `permissions`, `trusted_users`) cannot be queried via the API
- DELETE operations require a WHERE clause for safety
- Network connectivity required for initial dependency download

## Contributing

Contributions are welcome! Please:

1. Fork the repository
2. Create a feature branch
3. Make your changes
4. Add tests
5. Run `go fmt ./...` and `go vet ./...`
6. Submit a pull request

## License

[MIT License](LICENSE)

## Credits

Built with:
- [Caddy](https://caddyserver.com/)
- [DuckDB](https://duckdb.org/)
- [duckdb-go](https://github.com/duckdb/duckdb-go)
- [mcp-go](https://github.com/mark3labs/mcp-go)
