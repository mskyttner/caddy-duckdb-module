# Dockerfile for Caddy DuckDB Extension
#
# Multi-stage build:
# - Stage 1: Build the Go binary with CGO
# - Stage 2: Create minimal runtime image
#
# The /data volume stores both main.db and auth.db databases.
# Mount this volume for data persistence.
#
# Usage:
#   docker build -t caddy-duckdb .
#   docker run -p 8080:8080 -v $(pwd)/data:/data caddy-duckdb
#
# Generate API key (run locally, not in container):
#   go build -o tools/create-api-key ./tools/create-api-key.go
#   ./tools/create-api-key -db ./data/auth.db

# =============================================================================
# Stage 1: Builder
# =============================================================================
FROM docker.io/golang:1.26-bookworm AS builder

# Install build dependencies for CGO and DuckDB
RUN apt-get update && apt-get install -y --no-install-recommends \
    gcc \
    g++ \
    libc6-dev \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /build

# Copy go.mod and go.sum first for better layer caching
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the main binary with optimizations
# -ldflags="-s -w" strips debug info and symbol table (~20% smaller)
RUN CGO_ENABLED=1 go build -ldflags="-s -w" -o caddy ./cmd/caddy

# =============================================================================
# Stage 2: Runtime
# =============================================================================
FROM docker.io/debian:bookworm-slim

# Swagger UI version to download (can be overridden at build time)
ARG SWAGGER_UI_VERSION=5.32.3

# Install minimal runtime dependencies
# - ca-certificates: Required for HTTPS/TLS connections
# - curl: Required for health checks and downloading Swagger UI
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    curl \
    unzip \
    && rm -rf /var/lib/apt/lists/*

# Create non-root user for security (with home directory for DuckDB/Caddy config)
RUN groupadd -r caddy && useradd -r -g caddy -m -d /home/caddy caddy

# Create directories with correct ownership upfront
RUN mkdir -p /data /app /etc/caddy /app/swagger-ui-dist /app/query-ui \
        /home/caddy/.config/caddy /home/caddy/.local/share/caddy && \
    chown -R caddy:caddy /data /app /etc/caddy /home/caddy

WORKDIR /app

# Download Swagger UI dist files from unpkg (npm CDN)
RUN cd /app/swagger-ui-dist && \
    for file in swagger-ui-bundle.js swagger-ui-standalone-preset.js swagger-ui.css \
                index.html swagger-initializer.js oauth2-redirect.html \
                favicon-16x16.png favicon-32x32.png; do \
        curl -fsSL "https://cdn.jsdelivr.net/npm/swagger-ui-dist@${SWAGGER_UI_VERSION}/${file}" -o "${file}"; \
    done && \
    # Configure Swagger UI to use relative path (works with any route prefix)
    # From /duckdb/docs/, ../openapi.json resolves to /duckdb/openapi.json
    sed -i 's|https://petstore.swagger.io/v2/swagger.json|../openapi.json|g' swagger-initializer.js && \
    chown -R caddy:caddy /app/swagger-ui-dist

# Copy binary from builder with correct ownership (avoids layer duplication)
COPY --from=builder --chown=caddy:caddy /build/caddy .

# Copy Query UI
COPY --chown=caddy:caddy assets/index.html /app/query-ui/index.html

# Copy Docker-specific configuration (uses /data volume paths)
COPY --chown=caddy:caddy examples/Caddyfile.docker /etc/caddy/Caddyfile

# Labels
LABEL org.opencontainers.image.title="Caddy DuckDB Module" \
      org.opencontainers.image.description="Caddy server with DuckDB REST API" \
      org.opencontainers.image.source="https://github.com/tobilg/caddy-duckdb-module" \
      org.opencontainers.image.licenses="MIT"

# Install DuckDB CLI and pre-download community extensions (Lance for FTS)
# The DuckDB version must match the version bundled in duckdb-go-bindings.
ARG DUCKDB_VERSION=1.5.2
RUN curl -fsSL "https://github.com/duckdb/duckdb/releases/download/v${DUCKDB_VERSION}/duckdb_cli-linux-amd64.zip" \
      -o /tmp/duckdb.zip && \
    unzip -o /tmp/duckdb.zip -d /usr/local/bin/ && \
    rm /tmp/duckdb.zip && \
    chmod +x /usr/local/bin/duckdb
# Pre-install extensions into caddy user's home so they're found at runtime.
# - lance:       vector/FTS index support (core extension as of DuckDB 1.5.2)
# - http_client: HTTP GET/POST functions; successor to http_request (community)
# - textplot:    ASCII chart functions (tp_bar, tp_sparkline, textplot_histogram)
# - markdown:    read_markdown() for parsing Markdown files into tables
RUN HOME=/home/caddy duckdb -c " \
    INSTALL lance; LOAD lance; \
    INSTALL http_client FROM community; LOAD http_client; \
    INSTALL textplot FROM community; LOAD textplot; \
    INSTALL markdown FROM community; LOAD markdown; \
    " && \
    chown -R caddy:caddy /home/caddy/.duckdb

# Copy embedded docs to /app/docs so they are accessible to DuckDB at runtime.
# User init.sql files can use: read_markdown('/app/docs/duckdb-sql.md') etc.
COPY --chown=caddy:caddy handlers/docs/ /app/docs/

# Expose default port
EXPOSE 8080

# Volume for persistent data
VOLUME ["/data"]

# Health check using the dedicated health endpoint
# Note: Uses default values; docker-compose.yml overrides this with env var support
HEALTHCHECK --interval=30s --timeout=10s --start-period=5s --retries=3 \
    CMD curl -fsS http://localhost:${DUCKDB_PORT:-8080}${DUCKDB_ROUTE_PREFIX:-/duckdb}/health || exit 1

# Run as non-root user
USER caddy

# Default command
ENTRYPOINT ["/app/caddy"]
CMD ["run", "--config", "/etc/caddy/Caddyfile", "--adapter", "caddyfile"]
