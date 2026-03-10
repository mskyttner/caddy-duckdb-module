# Development Guide

This guide helps you set up a development environment for the Caddy DuckDB Extension.

## Prerequisites

### Required

- **Go 1.24+**: [Download Go](https://go.dev/dl/)
- **C Compiler**: Required for DuckDB's CGO bindings
  - **macOS**: `xcode-select --install`
  - **Linux (Debian/Ubuntu)**: `apt-get install build-essential`
  - **Linux (RHEL/Fedora)**: `dnf install gcc`
  - **Windows**: MinGW-w64 or Visual Studio Build Tools

### Optional

- **Make**: For using the Makefile (pre-installed on macOS/Linux)
- **Docker**: For containerized development and testing

### Verify Prerequisites

```bash
# Check Go version (should be 1.24+)
go version

# Check C compiler
gcc --version  # or clang --version on macOS
```

## Quick Start

```bash
# Clone the repository
git clone https://github.com/mskyttner/caddy-duckdb-module.git
cd caddy-duckdb-module

# Run setup (checks prerequisites, builds, generates API key)
make setup

# Start the server
make run
```

The setup script will:
1. Verify Go and C compiler are installed
2. Create the data directory (`/tmp/data`)
3. Download Go dependencies
4. Build the Caddy binary and API key tool
5. Generate an admin API key

Your API key will be displayed and saved to `/tmp/data/.api-key`.

## Available Make Commands

Run `make help` to see all available commands:

| Command | Description |
|---------|-------------|
| `make build` | Build the Caddy binary |
| `make build-tools` | Build the API key tool |
| `make test` | Run all tests |
| `make test-verbose` | Run tests with verbose output |
| `make run` | Build and run with example Caddyfile |
| `make run-json` | Build and run with JSON config |
| `make setup` | Full development setup |
| `make api-key` | Generate a new API key |
| `make clean` | Remove build artifacts |
| `make fmt` | Format Go code |
| `make vet` | Run go vet |
| `make lint` | Run all linters |
| `make docker-build` | Build Docker image |
| `make docker-run` | Run with docker-compose |
| `make docker-stop` | Stop docker-compose |

## Project Structure

```
caddy-duckdb-module/
├── cmd/
│   └── caddy/
│       └── main.go              # Caddy entry point
├── module.go                     # Caddy module registration, ServeHTTP, config
├── config.go                     # Configuration structs & response types
├── database/
│   ├── manager.go               # Connection pooling, auth schema init
│   ├── operations.go            # CRUD database operations
│   ├── operations_test.go       # Operations tests
│   └── prepared_statements_test.go
├── auth/
│   ├── models.go                # Auth data structures (APIKey, Role, Permission, TrustedUser)
│   ├── authorizer.go            # Permission checking, trusted user lookup
│   ├── authorizer_test.go       # Authorizer tests
│   └── middleware.go            # HTTP authentication middleware
├── handlers/
│   ├── crud.go                  # CRUD endpoint handler (/duckdb/api/{table})
│   ├── query.go                 # SQL query handler (/duckdb/query)
│   ├── execute.go               # Write SQL handler (/duckdb/execute)
│   ├── export.go                # Export handler (/duckdb/export)
│   ├── mcp.go                   # MCP streamable-HTTP endpoint (/duckdb/mcp)
│   ├── httpserver.go            # httpserver-compatible handler (POST /duckdb/)
│   ├── columns.go               # Column schema endpoint (/duckdb/api/{table}/columns)
│   ├── internal_tables.go       # Shared: containsInternalTables, stripSQLComments
│   ├── query_test.go            # Query handler tests
│   ├── params.go                # Request parameter parsing
│   └── openapi.go               # OpenAPI 3.0 specification handler
├── formats/
│   ├── json.go                  # JSON response formatter
│   ├── csv.go                   # CSV response formatter
│   ├── parquet.go               # Apache Parquet formatter
│   └── arrow.go                 # Apache Arrow IPC formatter
├── tools/
│   └── auth-db-src/             # Auth database CLI tool source
│       └── main.go              # CLI: init, key, role, permission, user, migrate, info
├── docs/
│   ├── llms.txt                 # LLM integration guide (MCP + token-saving)
│   ├── fts-duckdb-native.md     # FTS with DuckDB native search
│   └── fts-sidecar-setup.md    # FTS sidecar setup guide
├── examples/
│   └── Caddyfile.docker         # Reference Caddyfile with all env vars documented
├── scripts/
│   ├── setup.sh                 # Development setup script
│   └── pre-commit               # Pre-commit hook script
├── Makefile                      # Build and development commands
├── Dockerfile                    # Multi-stage Docker build
├── docker-compose.yml            # Docker Compose configuration
├── go.mod                        # Go module definition
└── go.sum                        # Go module checksums
```

## Development Workflow

### Making Changes

1. Create a feature branch:
   ```bash
   git checkout -b feature/my-feature
   ```

2. Make your changes

3. Format and lint:
   ```bash
   make lint
   ```

4. Run tests:
   ```bash
   make test
   ```

5. Test manually:
   ```bash
   make run
   # In another terminal:
   curl -H "X-API-Key: YOUR_KEY" http://localhost:8080/duckdb/query \
     -d '{"sql": "SELECT 1"}'
   ```

### Running Tests

```bash
# Run all tests
make test

# Run tests with verbose output
make test-verbose

# Run tests for a specific package
CGO_ENABLED=1 go test -v ./handlers/...
CGO_ENABLED=1 go test -v ./auth/...
CGO_ENABLED=1 go test -v ./database/...

# Run a specific test
CGO_ENABLED=1 go test -v ./handlers/... -run TestContainsInternalTables

# Run tests with coverage
make test-coverage
```

### Manual API Testing

Once the server is running (`make run`):

```bash
# Get your API key
cat /tmp/data/.api-key

# Test query endpoint
curl -H "X-API-Key: YOUR_KEY" \
     -H "Content-Type: application/json" \
     -d '{"sql": "SELECT 1 AS test"}' \
     http://localhost:8080/duckdb/query

# Create a table
curl -H "X-API-Key: YOUR_KEY" \
     -H "Content-Type: application/json" \
     -d '{"sql": "CREATE TABLE test (id INTEGER, name VARCHAR)"}' \
     http://localhost:8080/duckdb/query

# Insert data via CRUD API
curl -X POST http://localhost:8080/duckdb/api/test \
     -H "X-API-Key: YOUR_KEY" \
     -H "Content-Type: application/json" \
     -d '{"id": 1, "name": "Alice"}'

# Read data
curl http://localhost:8080/duckdb/api/test \
     -H "X-API-Key: YOUR_KEY"

# Get OpenAPI spec
curl http://localhost:8080/duckdb/openapi.json
```

## Debugging

### Enable Debug Logging

Caddy uses structured logging. To see more verbose output, you can modify the Caddyfile:

```caddyfile
{
    debug
}

:8080 {
    route /duckdb/* {
        duckdb {
            # your config
        }
    }
}
```

### Inspect the Auth Database

Use the `auth-db` CLI tool (do not use the DuckDB CLI directly on auth.db while the server is running):

```bash
# List API keys (truncated)
./tools/auth-db key list -d data/auth.db

# List API keys with full key values
./tools/auth-db key list -d data/auth.db --show-keys

# List roles
./tools/auth-db role list -d data/auth.db

# List permissions
./tools/auth-db permission list -d data/auth.db

# List trusted users (for header auth)
./tools/auth-db user list -d data/auth.db

# Database statistics
./tools/auth-db info -d data/auth.db
```

To inspect the main database, use the DuckDB CLI:

```bash
# Install DuckDB CLI (if not installed)
# macOS: brew install duckdb
# Linux: https://duckdb.org/docs/installation/

duckdb data/main.db
.tables
SELECT * FROM my_table LIMIT 10;
```

### Common Issues

**"undefined: bindings.Type" error**

CGO is not enabled. Always use:
```bash
CGO_ENABLED=1 go build ./...
```
Or use the Makefile which sets this automatically.

**"no C compiler found" error**

Install a C compiler:
- macOS: `xcode-select --install`
- Linux: `apt-get install build-essential`

**"database is locked" error**

Only one process can write to a DuckDB file at a time. Make sure you don't have multiple Caddy instances or DuckDB CLI sessions accessing the same file.

**API key not working**

1. Check the key exists: `SELECT * FROM api_keys;` in auth database
2. Verify the key is active: `is_active` should be `true`
3. Check the role has permissions: `SELECT * FROM permissions WHERE role_name = 'your_role';`

## Architecture Overview

### Request Flow

```
HTTP Request
    │
    ▼
┌─────────────────┐
│  Caddy Router   │
└────────┬────────┘
         │
         ▼
┌─────────────────┐
│  module.go      │  ◄── Entry point (ServeHTTP)
│  ServeHTTP()    │
└────────┬────────┘
         │
         ▼
┌─────────────────┐
│  auth/          │  ◄── API key validation & permission check
│  middleware.go  │
└────────┬────────┘
         │
         ├──────────────────┬───────────────────┐
         ▼                  ▼                   ▼
┌─────────────────┐ ┌─────────────────┐ ┌─────────────────┐
│  handlers/      │ │  handlers/      │ │  handlers/      │
│  crud.go        │ │  query.go       │ │  openapi.go     │
└────────┬────────┘ └────────┬────────┘ └─────────────────┘
         │                   │
         ▼                   ▼
┌─────────────────────────────────────┐
│           database/manager.go        │  ◄── Connection pooling
│           database/operations.go     │  ◄── Query execution
└────────────────┬────────────────────┘
                 │
                 ▼
┌─────────────────────────────────────┐
│           formats/*.go               │  ◄── Response serialization
│           (JSON, CSV, Parquet, Arrow)│
└─────────────────────────────────────┘
```

### Key Components

| Component | Responsibility |
|-----------|---------------|
| `module.go` | Caddy module registration, configuration, HTTP routing, CORS |
| `config.go` | Configuration structs, response types |
| `database/manager.go` | Connection pooling, auth schema validation |
| `database/operations.go` | CRUD operations, schema caching |
| `auth/authorizer.go` | Permission checking + trusted user lookup |
| `auth/middleware.go` | API key + trusted-user-header extraction |
| `handlers/crud.go` | RESTful CRUD operations |
| `handlers/query.go` | Read-only SQL query execution |
| `handlers/execute.go` | Write SQL execution (requires can_execute) |
| `handlers/export.go` | SQL → server file → URL (token-efficient) |
| `handlers/mcp.go` | MCP streamable-HTTP endpoint |
| `handlers/httpserver.go` | httpserver-compatible POST / endpoint |
| `handlers/columns.go` | Column schema + statistics endpoint |
| `handlers/internal_tables.go` | Shared helpers for internal table protection |
| `formats/*.go` | Response format serialization |

### Database Schema (Auth)

```sql
-- Roles table
CREATE TABLE roles (
    role_name VARCHAR PRIMARY KEY,
    description VARCHAR
);

-- Permissions table
CREATE TABLE permissions (
    id INTEGER PRIMARY KEY,
    role_name VARCHAR REFERENCES roles(role_name),
    table_name VARCHAR,    -- '*' for all tables
    can_create BOOLEAN,
    can_read BOOLEAN,
    can_update BOOLEAN,
    can_delete BOOLEAN,
    can_query BOOLEAN,     -- Read-only SQL access
    can_execute BOOLEAN    -- Write SQL access (INSERT/UPDATE/DELETE/DDL); added via migrate
);

-- API Keys table
CREATE TABLE api_keys (
    key VARCHAR PRIMARY KEY,
    role_name VARCHAR REFERENCES roles(role_name),
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    expires_at TIMESTAMP,
    is_active BOOLEAN DEFAULT true
);

-- Trusted users (for SSO/vouch-proxy header auth)
CREATE TABLE trusted_users (
    username   VARCHAR PRIMARY KEY,
    role_name  VARCHAR REFERENCES roles(role_name),
    note       VARCHAR,
    is_active  BOOLEAN DEFAULT true,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
```

> **Note:** `can_execute` was added after the initial release. Run `./tools/auth-db migrate -d data/auth.db` to add it to existing databases.

## Adding Features

### Adding a New Endpoint

1. Create handler in `handlers/`:
   ```go
   func (h *MyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
       // Implementation
   }
   ```

2. Register in `module.go`:
   ```go
   case "/duckdb/myendpoint":
       myHandler.ServeHTTP(w, r)
   ```

3. Add to OpenAPI spec in `handlers/openapi.go`

4. Add tests in `handlers/my_handler_test.go`

### Adding a New Response Format

1. Create formatter in `formats/`:
   ```go
   func WriteMyFormat(w http.ResponseWriter, data []map[string]interface{}) error {
       // Implementation
   }
   ```

2. Register in content negotiation (handlers)

3. Update OpenAPI spec with new Accept header

### Modifying Authentication

1. Update `auth/models.go` for new data structures
2. Update `auth/authorizer.go` for permission logic
3. Update schema initialization in `database/manager.go`
4. Add tests in `auth/authorizer_test.go`

## Pre-commit Hooks

Install the pre-commit hook to catch issues before committing:

```bash
cp scripts/pre-commit .git/hooks/pre-commit
chmod +x .git/hooks/pre-commit
```

The hook runs:
- `go fmt` - Code formatting
- `go vet` - Static analysis

## Useful Resources

- [Caddy Documentation](https://caddyserver.com/docs/)
- [Caddy Module Development](https://caddyserver.com/docs/extending-caddy)
- [DuckDB Documentation](https://duckdb.org/docs/)
- [DuckDB Go Driver](https://github.com/duckdb/duckdb-go)
- [Apache Arrow Go](https://pkg.go.dev/github.com/apache/arrow/go/v18)
