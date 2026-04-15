package duckdb

import (
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"github.com/caddyserver/caddy/v2/caddyconfig/httpcaddyfile"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"github.com/google/uuid"
	"github.com/tobilg/caddy-duckdb-module/auth"
	"github.com/tobilg/caddy-duckdb-module/database"
	"github.com/tobilg/caddy-duckdb-module/handlers"
	"go.uber.org/zap"
)

func init() {
	caddy.RegisterModule(DuckDB{})
	httpcaddyfile.RegisterHandlerDirective("duckdb", parseCaddyfile)
}

// DuckDB is a Caddy module that provides a REST API for DuckDB operations.
type DuckDB struct {
	// DatabasePath is the path to the main DuckDB database file.
	// If empty, an in-memory database will be used.
	DatabasePath string `json:"database_path,omitempty"`

	// AuthDatabasePath is the path to the internal authentication database.
	// This is required and must be file-based for persistence.
	AuthDatabasePath string `json:"auth_database_path,omitempty"`

	// QueryTimeout is the maximum duration for query execution.
	// Default is 10 seconds.
	QueryTimeout caddy.Duration `json:"query_timeout,omitempty"`

	// MaxRowsPerPage is the default number of rows per page when pagination is used.
	// Default is 100.
	MaxRowsPerPage int `json:"max_rows_per_page,omitempty"`

	// AbsoluteMaxRows is the safety limit - the maximum number of rows that can be returned
	// in a single request, even when pagination is not requested.
	// Set to 0 to disable the limit (not recommended for production).
	// Default is 10000.
	AbsoluteMaxRows int `json:"absolute_max_rows,omitempty"`

	// Threads is the number of threads DuckDB should use.
	// Default is 4.
	Threads int `json:"threads,omitempty"`

	// AccessMode determines the access mode for the main database.
	// Valid values are "read_only" or "read_write" (default).
	AccessMode string `json:"access_mode,omitempty"`

	// MemoryLimit is the maximum memory DuckDB can use (e.g., "4GB", "512MB").
	// If empty, DuckDB defaults to 80% of available RAM.
	MemoryLimit string `json:"memory_limit,omitempty"`

	// EnableObjectCache enables DuckDB's object cache for faster repeated queries.
	// Default is false.
	EnableObjectCache bool `json:"enable_object_cache,omitempty"`

	// TempDirectory is the directory for DuckDB temporary files when spilling to disk.
	// If empty, uses system default.
	TempDirectory string `json:"temp_directory,omitempty"`

	// InitFilePath is the path to a SQL file to execute on database startup.
	// Use this to load extensions, configure settings, or run initialization queries.
	InitFilePath string `json:"init_file,omitempty"`

	// FTSServiceURL is the URL of the FTS sidecar service for full-text search.
	// If empty, the /find endpoint will not be available.
	// Example: "http://fts-sidecar:8701"
	FTSServiceURL string `json:"fts_service_url,omitempty"`

	// TrustedUserHeader is the name of the HTTP header that carries a pre-authenticated
	// identity (e.g. "X-Vouch-User" set by vouch-proxy via forward_auth).
	// When set, requests with this header bypass API key auth and are looked up
	// in the trusted_users table. Both auth modes remain active simultaneously.
	TrustedUserHeader string `json:"trusted_user_header,omitempty"`

	// CORSOrigins is an optional list of allowed CORS origins for browser clients.
	// Use "*" to allow all origins, or list exact origins.
	// Example: ["http://localhost:5522", "https://ui.example.com"]
	CORSOrigins []string `json:"cors_origins,omitempty"`

	// ExportsDir is the filesystem directory where POST /duckdb/export writes result files.
	// If empty, the export endpoint returns 503. The directory is created if it does not exist.
	// Example: "/data/exports"
	ExportsDir string `json:"exports_dir,omitempty"`

	// ExportsURL is the URL prefix under which exported files are served.
	// Should match the file_server route in your Caddyfile.
	// Default: "<route_prefix>/exports"
	ExportsURL string `json:"exports_url,omitempty"`

	// ExportTTLMinutes is the default lifetime of exported files in minutes.
	// Files are removed by a background cleanup goroutine after this duration.
	// Default: 60
	ExportTTLMinutes int `json:"export_ttl_minutes,omitempty"`

	// MaxMCPRows is the maximum number of rows returned by the MCP query tool.
	// Larger result sets are truncated with a note directing the LLM to use the export tool.
	// Default: 500
	MaxMCPRows int `json:"max_mcp_rows,omitempty"`

	// MCPDocsDir is an optional directory of *.md files that are served as
	// additional MCP resources at duckdb://docs/<stem>. Use this to provide
	// deployment-specific documentation (schema guides, domain references,
	// query patterns) without recompiling the binary.
	// Env: DUCKDB_MCP_DOCS_DIR
	MCPDocsDir string `json:"mcp_docs_dir,omitempty"`

	// PublicExportsDir is the filesystem directory for auth-free public export files.
	// When set, export(public=true) is available via the MCP tool and the files
	// are downloadable without authentication at PublicExportsURL.
	// Env: DUCKDB_PUBLIC_EXPORTS_DIR
	PublicExportsDir string `json:"public_exports_dir,omitempty"`

	// PublicExportsURL is the base URL under which auth-free public export
	// files are served. Defaults to "<route_prefix>/public-exports" when
	// PublicExportsDir is set. The database_info MCP tool advertises this URL
	// so partner instances know where to fetch exported files.
	// Example: "https://api.example.com/duckdb/public-exports"
	// Env: DUCKDB_PUBLIC_EXPORTS_URL
	PublicExportsURL string `json:"public_exports_url,omitempty"`

	logger            *zap.Logger
	dbMgr             *database.Manager
	authorizer        *auth.Authorizer
	authMw            *auth.Middleware
	crudHandler       *handlers.CRUDHandler
	queryHandler      *handlers.QueryHandler
	openAPIHandler    *handlers.OpenAPIHandler
	macroHandler      *handlers.MacroHandler
	viewHandler       *handlers.ViewHandler
	columnsHandler    *handlers.ColumnsHandler
	ftsHandler        *handlers.FTSHandler
	httpserverHandler *handlers.HTTPServerHandler
	executeHandler    *handlers.ExecuteHandler
	exportHandler     *handlers.ExportHandler
	mcpHandler        *handlers.MCPHandler
	routePrefix       string // set from DUCKDB_ROUTE_PREFIX env var, defaults to /duckdb
}

// CaddyModule returns the Caddy module information.
func (DuckDB) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "http.handlers.duckdb",
		New: func() caddy.Module { return new(DuckDB) },
	}
}

// Provision sets up the DuckDB module.
func (d *DuckDB) Provision(ctx caddy.Context) error {
	d.logger = ctx.Logger(d)

	// Set route prefix from environment variable, with /duckdb as default
	if envPrefix := os.Getenv("DUCKDB_ROUTE_PREFIX"); envPrefix != "" {
		d.routePrefix = envPrefix
	} else {
		d.routePrefix = "/duckdb"
	}
	// Ensure route prefix starts with /
	if !strings.HasPrefix(d.routePrefix, "/") {
		d.routePrefix = "/" + d.routePrefix
	}
	// Remove trailing slash if present
	d.routePrefix = strings.TrimSuffix(d.routePrefix, "/")

	if d.QueryTimeout == 0 {
		d.QueryTimeout = caddy.Duration(10_000_000_000) // 10 seconds in nanoseconds
	}
	if d.MaxRowsPerPage == 0 {
		d.MaxRowsPerPage = 100
	}
	if d.AbsoluteMaxRows == 0 {
		d.AbsoluteMaxRows = 10000
	}
	if d.Threads == 0 {
		d.Threads = 4
	}
	if d.AccessMode == "" {
		d.AccessMode = "read_write"
	}

	// Set optional settings from environment variables if not configured
	if d.InitFilePath == "" {
		if envInitFile := os.Getenv("DUCKDB_INIT_FILE"); envInitFile != "" {
			d.InitFilePath = envInitFile
		}
	}
	if d.MemoryLimit == "" {
		if envMemLimit := os.Getenv("DUCKDB_MEMORY_LIMIT"); envMemLimit != "" {
			d.MemoryLimit = envMemLimit
		}
	}
	if d.TempDirectory == "" {
		if envTempDir := os.Getenv("DUCKDB_TEMP_DIRECTORY"); envTempDir != "" {
			d.TempDirectory = envTempDir
		}
	}
	if len(d.CORSOrigins) == 0 {
		if envCORS := os.Getenv("DUCKDB_CORS_ORIGINS"); envCORS != "" {
			d.CORSOrigins = strings.Fields(envCORS)
		}
	}

	// Validate AuthDatabasePath
	if d.AuthDatabasePath == "" {
		return fmt.Errorf("auth_database_path is required")
	}

	// Initialize database manager
	var err error
	d.dbMgr, err = database.NewManager(database.Config{
		MainDBPath:        d.DatabasePath,
		AuthDBPath:        d.AuthDatabasePath,
		Threads:           d.Threads,
		AccessMode:        d.AccessMode,
		MemoryLimit:       d.MemoryLimit,
		EnableObjectCache: d.EnableObjectCache,
		TempDirectory:     d.TempDirectory,
		InitFilePath:      d.InitFilePath,
		QueryTimeout:      time.Duration(d.QueryTimeout),
		Logger:            d.logger,
	})
	if err != nil {
		return fmt.Errorf("failed to initialize database manager: %v", err)
	}

	// Initialize authorizer
	d.authorizer = auth.NewAuthorizer(d.dbMgr.AuthDB())
	d.authMw = auth.NewMiddleware(d.authorizer)

	// Initialize handlers
	d.crudHandler = handlers.NewCRUDHandler(d.dbMgr, d.authorizer, d.MaxRowsPerPage, d.AbsoluteMaxRows, d.logger)
	d.queryHandler = handlers.NewQueryHandler(d.dbMgr, d.authorizer, d.logger)
	d.openAPIHandler = handlers.NewOpenAPIHandler()
	d.macroHandler = handlers.NewMacroHandler(d.dbMgr, d.authorizer, d.MaxRowsPerPage, d.AbsoluteMaxRows, d.logger)
	d.viewHandler = handlers.NewViewHandler(d.dbMgr, d.authorizer, d.MaxRowsPerPage, d.AbsoluteMaxRows, d.logger)
	d.columnsHandler = handlers.NewColumnsHandler(d.dbMgr, d.authorizer, d.logger)
	d.httpserverHandler = handlers.NewHTTPServerHandler(d.dbMgr, d.authorizer, d.logger)
	d.executeHandler = handlers.NewExecuteHandler(d.dbMgr, d.authorizer, d.logger)

	// Initialize export handler (env var fallbacks for optional settings)
	if d.ExportsDir == "" {
		if envExportsDir := os.Getenv("DUCKDB_EXPORTS_DIR"); envExportsDir != "" {
			d.ExportsDir = envExportsDir
		}
	}
	exportsURL := d.ExportsURL
	if exportsURL == "" {
		if envExportsURL := os.Getenv("DUCKDB_EXPORTS_URL"); envExportsURL != "" {
			exportsURL = envExportsURL
		} else {
			exportsURL = d.routePrefix + "/exports"
		}
	}
	if d.ExportTTLMinutes == 0 {
		if envTTL := os.Getenv("DUCKDB_EXPORT_TTL_MINUTES"); envTTL != "" {
			if ttl, err := strconv.Atoi(envTTL); err == nil {
				d.ExportTTLMinutes = ttl
			}
		}
	}
	exportTTL := time.Duration(d.ExportTTLMinutes) * time.Minute
	if exportTTL == 0 {
		exportTTL = time.Hour
	}
	if d.MaxMCPRows == 0 {
		if envMaxRows := os.Getenv("DUCKDB_MAX_MCP_ROWS"); envMaxRows != "" {
			if rows, err := strconv.Atoi(envMaxRows); err == nil {
				d.MaxMCPRows = rows
			}
		}
	}
	if d.MCPDocsDir == "" {
		d.MCPDocsDir = os.Getenv("DUCKDB_MCP_DOCS_DIR")
	}
	// Public exports: only enabled when PublicExportsDir is configured.
	if d.PublicExportsDir == "" {
		d.PublicExportsDir = os.Getenv("DUCKDB_PUBLIC_EXPORTS_DIR")
	}
	// pubExportsURL is only computed (non-empty) when the feature is active.
	// An empty value suppresses export_base_url from database_info output.
	var pubExportsURL string
	if d.PublicExportsDir != "" {
		pubExportsURL = d.PublicExportsURL
		if pubExportsURL == "" {
			if envPubURL := os.Getenv("DUCKDB_PUBLIC_EXPORTS_URL"); envPubURL != "" {
				pubExportsURL = envPubURL
			} else {
				pubExportsURL = d.routePrefix + "/public-exports"
			}
		}
		d.PublicExportsURL = pubExportsURL // store resolved value
	}
	d.exportHandler = handlers.NewExportHandler(d.dbMgr, d.authorizer, d.logger, d.ExportsDir, exportsURL, d.PublicExportsDir, pubExportsURL, exportTTL)
	if d.ExportsDir != "" {
		d.exportHandler.StartCleanup(ctx, 10*time.Minute)
		d.logger.Info("Export handler initialized",
			zap.String("exports_dir", d.ExportsDir),
			zap.String("exports_url", exportsURL),
			zap.Duration("export_ttl", exportTTL),
		)
	}

	// Initialize MCP handler
	d.mcpHandler = handlers.NewMCPHandler(d.dbMgr, d.authorizer, d.exportHandler, d.logger, d.MaxMCPRows, d.MCPDocsDir, d.PublicExportsURL)

	// Initialize FTS handler if service URL is configured
	if d.FTSServiceURL == "" {
		if envFTSURL := os.Getenv("DUCKDB_FTS_SERVICE_URL"); envFTSURL != "" {
			d.FTSServiceURL = envFTSURL
		}
	}
	if d.FTSServiceURL != "" {
		d.ftsHandler = handlers.NewFTSHandler(d.FTSServiceURL, d.authorizer, d.logger)
		d.logger.Info("FTS handler initialized", zap.String("service_url", d.FTSServiceURL))
	}

	d.logger.Info("DuckDB module provisioned",
		zap.String("route_prefix", d.routePrefix),
		zap.String("main_db", d.DatabasePath),
		zap.String("auth_db", d.AuthDatabasePath),
		zap.Duration("query_timeout", time.Duration(d.QueryTimeout)),
		zap.Int("max_rows_per_page", d.MaxRowsPerPage),
		zap.Int("absolute_max_rows", d.AbsoluteMaxRows),
		zap.Int("threads", d.Threads),
		zap.String("access_mode", d.AccessMode),
		zap.String("memory_limit", d.MemoryLimit),
		zap.Bool("enable_object_cache", d.EnableObjectCache),
		zap.String("temp_directory", d.TempDirectory),
		zap.String("init_file", d.InitFilePath),
		zap.String("fts_service_url", d.FTSServiceURL),
	)

	return nil
}

// Validate ensures the module configuration is valid.
func (d *DuckDB) Validate() error {
	if d.AccessMode != "read_only" && d.AccessMode != "read_write" {
		return fmt.Errorf("invalid access_mode: %s (must be 'read_only' or 'read_write')", d.AccessMode)
	}
	if d.MaxRowsPerPage <= 0 {
		return fmt.Errorf("max_rows_per_page must be greater than 0")
	}
	if d.AbsoluteMaxRows < 0 {
		return fmt.Errorf("absolute_max_rows must be >= 0 (0 disables the limit)")
	}
	if d.Threads <= 0 {
		return fmt.Errorf("threads must be greater than 0")
	}
	return nil
}

// ServeHTTP implements the caddyhttp.MiddlewareHandler interface.
func (d *DuckDB) ServeHTTP(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler) error {
	// Check if this is a DuckDB endpoint
	if !strings.HasPrefix(r.URL.Path, d.routePrefix) {
		return next.ServeHTTP(w, r)
	}

	// Extract or generate request ID for tracing
	requestID := r.Header.Get("X-Request-ID")
	if requestID == "" {
		requestID = uuid.New().String()
	}
	// Add request ID to context and response header
	ctx := auth.SetRequestID(r.Context(), requestID)
	r = r.WithContext(ctx)
	w.Header().Set("X-Request-ID", requestID)

	// CORS: inject headers on all responses; handle OPTIONS preflight before auth
	if len(d.CORSOrigins) > 0 {
		origin := r.Header.Get("Origin")
		if d.corsAllowed(origin) {
			allowOrigin := origin
			if allowOrigin == "" {
				allowOrigin = "*"
			}
			w.Header().Set("Access-Control-Allow-Origin", allowOrigin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, HEAD, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers",
				"X-API-Key, Authorization, Content-Type, Accept, format, "+
					"X-ClickHouse-Format, X-Request-ID")
			w.Header().Set("Access-Control-Expose-Headers", "X-Request-ID")
			w.Header().Set("Access-Control-Max-Age", "86400")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return nil // OPTIONS handled before auth — no key needed for preflight
		}
	}

	// Health check endpoint (no authentication required)
	if r.URL.Path == d.routePrefix+"/health" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
		return nil
	}

	// OpenAPI specification endpoint (no authentication required)
	if r.URL.Path == d.routePrefix+"/openapi.json" {
		d.openAPIHandler.ServeHTTP(w, r)
		return nil
	}

	// Export file downloads (no authentication required).
	// The UUID filename acts as a capability token; only files created by this
	// server instance are served (gated by the in-memory expiry map).
	if strings.HasPrefix(r.URL.Path, d.routePrefix+"/exports/") {
		d.exportHandler.ServeDownload(w, r, d.routePrefix+"/exports")
		return nil
	}

	// Public export file downloads (no authentication required).
	// Files are placed here by export(public=true) via the MCP tool.
	// The UUID filename is the capability token; only tracked files are served.
	if strings.HasPrefix(r.URL.Path, d.routePrefix+"/public-exports/") {
		d.exportHandler.ServePublicDownload(w, r, d.routePrefix+"/public-exports")
		return nil
	}

	// Authenticate all other requests.
	// Two modes operate simultaneously:
	//  1. Trusted user header (vouch-proxy / forward_auth integration) — checked first
	//  2. API key (X-API-Key header, api_key query param, or Basic auth)
	authenticated := false

	// --- Trusted user header auth ---
	if d.TrustedUserHeader != "" {
		if username := r.Header.Get(d.TrustedUserHeader); username != "" {
			roleName, err := d.authorizer.GetRoleForTrustedUser(username)
			if err == nil {
				syntheticKey := &auth.APIKey{
					Key:      username,
					RoleName: roleName,
					IsActive: true,
				}
				r = r.WithContext(auth.SetContextValues(r.Context(), syntheticKey, roleName))
				authenticated = true
			}
			// If err != nil: unknown/inactive user — fall through to API key auth
		}
	}

	// --- API key auth ---
	if !authenticated {
		apiKey := r.Header.Get("X-API-Key")
		if apiKey == "" {
			apiKey = r.URL.Query().Get("api_key")
		}
		if apiKey == "" {
			// Check for Basic auth - username must be "apikey", password is the API key
			if username, password, ok := r.BasicAuth(); ok && username == "apikey" {
				apiKey = password
			}
		}
		if apiKey != "" {
			key, err := d.authorizer.AuthenticateAPIKey(apiKey)
			if err == nil && key != nil {
				r = r.WithContext(auth.SetContextValues(r.Context(), key, key.RoleName))
				authenticated = true
			}
		}
	}

	if !authenticated {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"Unauthorized","message":"Missing API key: use X-API-Key header, api_key query parameter, or Basic auth with username 'apikey'","code":401}`))
		return nil
	}

	// Route based on path

	// httpserver-compatible root endpoint: raw SQL POST to /duckdb/
	if r.URL.Path == d.routePrefix || r.URL.Path == d.routePrefix+"/" {
		d.httpserverHandler.ServeHTTP(w, r)
		return nil
	}

	if strings.HasPrefix(r.URL.Path, d.routePrefix+"/find") {
		// Full-text search endpoint (requires FTS sidecar)
		if d.ftsHandler == nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte(`{"error":"Service Unavailable","message":"FTS service not configured. Set fts_service_url in Caddyfile or DUCKDB_FTS_SERVICE_URL environment variable.","code":503}`))
			return nil
		}
		d.ftsHandler.ServeHTTP(w, r)
		return nil
	} else if strings.HasPrefix(r.URL.Path, d.routePrefix+"/query") {
		// Raw SQL query endpoint
		d.queryHandler.ServeHTTP(w, r)
		return nil
	} else if strings.HasPrefix(r.URL.Path, d.routePrefix+"/macro") {
		// Macro execution endpoint
		d.macroHandler.ServeHTTP(w, r)
		return nil
	} else if strings.HasPrefix(r.URL.Path, d.routePrefix+"/view/") {
		// Check for /columns suffix BEFORE routing to view handler
		if strings.HasSuffix(r.URL.Path, "/columns") {
			d.columnsHandler.ServeHTTP(w, r)
			return nil
		}
		// View query endpoint
		d.viewHandler.ServeHTTP(w, r)
		return nil
	} else if r.URL.Path == d.routePrefix+"/api" || strings.HasPrefix(r.URL.Path, d.routePrefix+"/api/") {
		// Check for /columns suffix BEFORE routing to CRUD handler
		if strings.HasSuffix(r.URL.Path, "/columns") {
			d.columnsHandler.ServeHTTP(w, r)
			return nil
		}
		// CRUD operations endpoint
		d.crudHandler.ServeHTTP(w, r)
		return nil
	} else if r.URL.Path == d.routePrefix+"/execute" {
		// Raw SQL write endpoint (requires execute permission)
		d.executeHandler.ServeHTTP(w, r)
		return nil
	} else if r.URL.Path == d.routePrefix+"/export" {
		// Export query results to file (returns URL, not row data)
		d.exportHandler.ServeHTTP(w, r)
		return nil
	} else if strings.HasPrefix(r.URL.Path, d.routePrefix+"/exports/") {
		// Download a previously exported file
		d.exportHandler.ServeDownload(w, r, d.routePrefix+"/exports")
		return nil
	} else if strings.HasPrefix(r.URL.Path, d.routePrefix+"/mcp") {
		// MCP streamable-HTTP endpoint for LLM clients
		d.mcpHandler.ServeHTTP(w, r)
		return nil
	}

	// Unknown endpoint
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotFound)
	w.Write([]byte(`{"error":"Not Found","message":"Unknown DuckDB endpoint","code":404}`))
	return nil
}

// Cleanup performs cleanup when the module is unloaded.
func (d *DuckDB) Cleanup() error {
	if d.dbMgr != nil {
		return d.dbMgr.Close()
	}
	return nil
}

// UnmarshalCaddyfile implements caddyfile.Unmarshaler.
func (d *DuckDB) UnmarshalCaddyfile(dispenser *caddyfile.Dispenser) error {
	for dispenser.Next() {
		for dispenser.NextBlock(0) {
			switch dispenser.Val() {
			case "database_path":
				if !dispenser.Args(&d.DatabasePath) {
					return dispenser.ArgErr()
				}
			case "auth_database_path":
				if !dispenser.Args(&d.AuthDatabasePath) {
					return dispenser.ArgErr()
				}
			case "query_timeout":
				var timeout string
				if !dispenser.Args(&timeout) {
					return dispenser.ArgErr()
				}
				duration, err := caddy.ParseDuration(timeout)
				if err != nil {
					return dispenser.Errf("invalid query_timeout: %v", err)
				}
				d.QueryTimeout = caddy.Duration(duration)
			case "max_rows_per_page":
				var maxRowsStr string
				if !dispenser.Args(&maxRowsStr) {
					return dispenser.ArgErr()
				}
				maxRows, err := strconv.Atoi(maxRowsStr)
				if err != nil {
					return dispenser.Errf("invalid max_rows_per_page: %v", err)
				}
				d.MaxRowsPerPage = maxRows
			case "absolute_max_rows":
				var absMaxRowsStr string
				if !dispenser.Args(&absMaxRowsStr) {
					return dispenser.ArgErr()
				}
				absMaxRows, err := strconv.Atoi(absMaxRowsStr)
				if err != nil {
					return dispenser.Errf("invalid absolute_max_rows: %v", err)
				}
				d.AbsoluteMaxRows = absMaxRows
			case "threads":
				var threadsStr string
				if !dispenser.Args(&threadsStr) {
					return dispenser.ArgErr()
				}
				threads, err := strconv.Atoi(threadsStr)
				if err != nil {
					return dispenser.Errf("invalid threads: %v", err)
				}
				d.Threads = threads
			case "access_mode":
				if !dispenser.Args(&d.AccessMode) {
					return dispenser.ArgErr()
				}
			case "memory_limit":
				if !dispenser.Args(&d.MemoryLimit) {
					return dispenser.ArgErr()
				}
			case "enable_object_cache":
				var enableStr string
				if !dispenser.Args(&enableStr) {
					return dispenser.ArgErr()
				}
				enableStr = strings.ToLower(enableStr)
				d.EnableObjectCache = enableStr == "true" || enableStr == "yes" || enableStr == "1"
			case "temp_directory":
				if !dispenser.Args(&d.TempDirectory) {
					return dispenser.ArgErr()
				}
			case "init_file":
				if !dispenser.Args(&d.InitFilePath) {
					return dispenser.ArgErr()
				}
			case "fts_service_url":
				if !dispenser.Args(&d.FTSServiceURL) {
					return dispenser.ArgErr()
				}
			case "trusted_user_header":
				if !dispenser.Args(&d.TrustedUserHeader) {
					return dispenser.ArgErr()
				}
			case "cors_origins":
				d.CORSOrigins = dispenser.RemainingArgs()
				if len(d.CORSOrigins) == 0 {
					return dispenser.ArgErr()
				}
			case "exports_dir":
				if !dispenser.Args(&d.ExportsDir) {
					return dispenser.ArgErr()
				}
			case "exports_url":
				if !dispenser.Args(&d.ExportsURL) {
					return dispenser.ArgErr()
				}
			case "export_ttl_minutes":
				var ttlStr string
				if !dispenser.Args(&ttlStr) {
					return dispenser.ArgErr()
				}
				ttl, err := strconv.Atoi(ttlStr)
				if err != nil {
					return dispenser.Errf("invalid export_ttl_minutes: %v", err)
				}
				d.ExportTTLMinutes = ttl
			case "max_mcp_rows":
				var rowsStr string
				if !dispenser.Args(&rowsStr) {
					return dispenser.ArgErr()
				}
				rows, err := strconv.Atoi(rowsStr)
				if err != nil {
					return dispenser.Errf("invalid max_mcp_rows: %v", err)
				}
				d.MaxMCPRows = rows
			case "mcp_docs_dir":
				if !dispenser.Args(&d.MCPDocsDir) {
					return dispenser.ArgErr()
				}
			default:
				return dispenser.Errf("unknown subdirective: %s", dispenser.Val())
			}
		}
	}
	return nil
}

// parseCaddyfile unmarshals tokens from h into a new Middleware.
func parseCaddyfile(h httpcaddyfile.Helper) (caddyhttp.MiddlewareHandler, error) {
	var d DuckDB
	err := d.UnmarshalCaddyfile(h.Dispenser)
	return &d, err
}

// corsAllowed reports whether the given origin is permitted by the configured CORS policy.
func (d *DuckDB) corsAllowed(origin string) bool {
	for _, o := range d.CORSOrigins {
		if o == "*" || o == origin {
			return true
		}
	}
	return false
}

// Interface guards
var (
	_ caddy.Module                = (*DuckDB)(nil)
	_ caddy.Provisioner           = (*DuckDB)(nil)
	_ caddy.Validator             = (*DuckDB)(nil)
	_ caddy.CleanerUpper          = (*DuckDB)(nil)
	_ caddyhttp.MiddlewareHandler = (*DuckDB)(nil)
	_ caddyfile.Unmarshaler       = (*DuckDB)(nil)
)
