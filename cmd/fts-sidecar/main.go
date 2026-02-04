// FTS Sidecar Service for Caddy DuckDB Module
//
// This service provides full-text search capabilities using LanceDB.
// It runs as a separate HTTP service that the main Caddy DuckDB module
// can proxy requests to for the /find endpoint.
//
// Endpoints:
//   GET  /health           - Health check
//   GET  /search           - Full-text search
//   POST /index            - Create/update FTS index
//   GET  /indexes          - List indexed tables
//   DELETE /index/{table}  - Delete an index
//
// Environment Variables:
//   FTS_PORT              - HTTP port (default: 8701)
//   FTS_INDEX_PATH        - Path to Lance indexes (default: /data/fts)
//   FTS_LOG_LEVEL         - Log level: debug, info, warn, error (default: info)

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/lancedb/lancedb-go/pkg/contracts"
	"github.com/lancedb/lancedb-go/pkg/lancedb"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Config holds the service configuration
type Config struct {
	Port      int
	IndexPath string
	LogLevel  string
}

// FTSService manages LanceDB connections and FTS operations
type FTSService struct {
	config     Config
	logger     *zap.Logger
	mu         sync.RWMutex
	db         contracts.IConnection
	tables     map[string]contracts.ITable
	tablesMeta map[string]*TableMeta
}

// TableMeta stores metadata about indexed tables
type TableMeta struct {
	Name       string    `json:"name"`
	FTSColumns []string  `json:"fts_columns"`
	RowCount   int64     `json:"row_count"`
	IndexedAt  time.Time `json:"indexed_at"`
}

// SearchRequest represents a search query
type SearchRequest struct {
	Query     string   `json:"query"`
	Table     string   `json:"table"`
	Columns   []string `json:"columns,omitempty"`   // Columns to return
	Limit     int      `json:"limit,omitempty"`     // Max results (default: 10)
	Offset    int      `json:"offset,omitempty"`    // For pagination
	Filter    string   `json:"filter,omitempty"`    // SQL filter expression
	Highlight bool     `json:"highlight,omitempty"` // Highlight matches
}

// SearchResponse represents search results
type SearchResponse struct {
	Query           string                   `json:"query"`
	Table           string                   `json:"table"`
	Hits            []map[string]interface{} `json:"hits"`
	TotalHits       int                      `json:"total_hits"`
	ExecutionTimeMs int64                    `json:"execution_time_ms"`
}

// IndexRequest represents a request to create/update an FTS index
type IndexRequest struct {
	Table      string   `json:"table"`       // Table name in Lance
	Source     string   `json:"source"`      // Path to parquet file
	FTSColumns []string `json:"fts_columns"` // Columns to index for FTS
	Replace    bool     `json:"replace"`     // Replace existing index
}

// IndexResponse represents the result of an index operation
type IndexResponse struct {
	Table           string   `json:"table"`
	FTSColumns      []string `json:"fts_columns"`
	RowCount        int64    `json:"row_count"`
	IndexTimeMs     int64    `json:"index_time_ms"`
	Success         bool     `json:"success"`
	Message         string   `json:"message,omitempty"`
}

// ErrorResponse represents an error response
type ErrorResponse struct {
	Error   string `json:"error"`
	Message string `json:"message"`
	Code    int    `json:"code"`
}

func main() {
	config := loadConfig()
	logger := setupLogger(config.LogLevel)
	defer logger.Sync()

	service, err := NewFTSService(config, logger)
	if err != nil {
		logger.Fatal("Failed to initialize FTS service", zap.Error(err))
	}
	defer service.Close()

	// Setup HTTP routes
	mux := http.NewServeMux()
	mux.HandleFunc("/health", service.handleHealth)
	mux.HandleFunc("/search", service.handleSearch)
	mux.HandleFunc("/index", service.handleIndex)
	mux.HandleFunc("/indexes", service.handleListIndexes)

	server := &http.Server{
		Addr:         fmt.Sprintf(":%d", config.Port),
		Handler:      logMiddleware(logger, mux),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	// Graceful shutdown
	done := make(chan bool)
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-quit
		logger.Info("Shutting down FTS service...")

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		if err := server.Shutdown(ctx); err != nil {
			logger.Error("Server shutdown error", zap.Error(err))
		}
		close(done)
	}()

	logger.Info("Starting FTS sidecar service",
		zap.Int("port", config.Port),
		zap.String("index_path", config.IndexPath),
	)

	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Fatal("Server failed", zap.Error(err))
	}

	<-done
	logger.Info("FTS service stopped")
}

func loadConfig() Config {
	port := 8701
	if p := os.Getenv("FTS_PORT"); p != "" {
		if parsed, err := strconv.Atoi(p); err == nil {
			port = parsed
		}
	}

	indexPath := "/data/fts"
	if p := os.Getenv("FTS_INDEX_PATH"); p != "" {
		indexPath = p
	}

	logLevel := "info"
	if l := os.Getenv("FTS_LOG_LEVEL"); l != "" {
		logLevel = l
	}

	return Config{
		Port:      port,
		IndexPath: indexPath,
		LogLevel:  logLevel,
	}
}

func setupLogger(level string) *zap.Logger {
	var zapLevel zapcore.Level
	switch strings.ToLower(level) {
	case "debug":
		zapLevel = zapcore.DebugLevel
	case "warn":
		zapLevel = zapcore.WarnLevel
	case "error":
		zapLevel = zapcore.ErrorLevel
	default:
		zapLevel = zapcore.InfoLevel
	}

	config := zap.Config{
		Level:            zap.NewAtomicLevelAt(zapLevel),
		Development:      false,
		Encoding:         "json",
		EncoderConfig:    zap.NewProductionEncoderConfig(),
		OutputPaths:      []string{"stdout"},
		ErrorOutputPaths: []string{"stderr"},
	}

	logger, err := config.Build()
	if err != nil {
		panic(fmt.Sprintf("Failed to create logger: %v", err))
	}
	return logger
}

func logMiddleware(logger *zap.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		// Wrap response writer to capture status code
		wrapped := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}
		next.ServeHTTP(wrapped, r)

		logger.Info("HTTP request",
			zap.String("method", r.Method),
			zap.String("path", r.URL.Path),
			zap.Int("status", wrapped.statusCode),
			zap.Duration("duration", time.Since(start)),
			zap.String("remote_addr", r.RemoteAddr),
		)
	})
}

type responseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

// NewFTSService creates a new FTS service instance
func NewFTSService(config Config, logger *zap.Logger) (*FTSService, error) {
	// Ensure index directory exists
	if err := os.MkdirAll(config.IndexPath, 0755); err != nil {
		return nil, fmt.Errorf("failed to create index directory: %w", err)
	}

	// Connect to LanceDB
	db, err := lancedb.Connect(context.Background(), config.IndexPath, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to LanceDB: %w", err)
	}

	service := &FTSService{
		config:     config,
		logger:     logger,
		db:         db,
		tables:     make(map[string]contracts.ITable),
		tablesMeta: make(map[string]*TableMeta),
	}

	// Load existing tables
	if err := service.loadExistingTables(); err != nil {
		logger.Warn("Failed to load existing tables", zap.Error(err))
	}

	return service, nil
}

func (s *FTSService) loadExistingTables() error {
	tableNames, err := s.db.TableNames(context.Background())
	if err != nil {
		return fmt.Errorf("failed to list tables: %w", err)
	}

	for _, name := range tableNames {
		table, err := s.db.OpenTable(context.Background(), name)
		if err != nil {
			s.logger.Warn("Failed to open table", zap.String("table", name), zap.Error(err))
			continue
		}

		s.tables[name] = table

		// Try to get row count
		count, err := table.Count(context.Background())
		if err != nil {
			s.logger.Warn("Failed to get table count", zap.String("table", name), zap.Error(err))
			count = 0
		}

		s.tablesMeta[name] = &TableMeta{
			Name:     name,
			RowCount: count,
		}

		s.logger.Info("Loaded existing table", zap.String("table", name), zap.Int64("rows", int64(count)))
	}

	return nil
}

// Close closes all resources
func (s *FTSService) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, table := range s.tables {
		table.Close()
	}

	if s.db != nil {
		s.db.Close()
	}

	return nil
}

// handleHealth returns service health status
func (s *FTSService) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		s.sendError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	s.mu.RLock()
	tableCount := len(s.tables)
	s.mu.RUnlock()

	response := map[string]interface{}{
		"status":       "ok",
		"service":      "fts-sidecar",
		"table_count":  tableCount,
		"index_path":   s.config.IndexPath,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// handleSearch performs FTS search
func (s *FTSService) handleSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		s.sendError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req SearchRequest

	if r.Method == http.MethodPost {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			s.sendError(w, "Invalid JSON body", http.StatusBadRequest)
			return
		}
	} else {
		// Parse from query params
		req.Query = r.URL.Query().Get("q")
		req.Table = r.URL.Query().Get("table")
		if cols := r.URL.Query().Get("columns"); cols != "" {
			req.Columns = strings.Split(cols, ",")
		}
		if limit := r.URL.Query().Get("limit"); limit != "" {
			if l, err := strconv.Atoi(limit); err == nil {
				req.Limit = l
			}
		}
		if offset := r.URL.Query().Get("offset"); offset != "" {
			if o, err := strconv.Atoi(offset); err == nil {
				req.Offset = o
			}
		}
		req.Filter = r.URL.Query().Get("filter")
		req.Highlight = r.URL.Query().Get("highlight") == "true"
	}

	// Validate request
	if req.Query == "" {
		s.sendError(w, "Query parameter 'q' is required", http.StatusBadRequest)
		return
	}
	if req.Table == "" {
		s.sendError(w, "Table parameter is required", http.StatusBadRequest)
		return
	}
	if req.Limit <= 0 {
		req.Limit = 10
	}
	if req.Limit > 1000 {
		req.Limit = 1000
	}

	// Perform search
	startTime := time.Now()

	s.mu.RLock()
	table, exists := s.tables[req.Table]
	s.mu.RUnlock()

	if !exists {
		s.sendError(w, fmt.Sprintf("Table '%s' not found", req.Table), http.StatusNotFound)
		return
	}

	// Perform FTS search
	ctx := context.Background()
	var results []map[string]interface{}
	var searchErr error

	// Get the FTS column from metadata
	s.mu.RLock()
	meta := s.tablesMeta[req.Table]
	s.mu.RUnlock()

	ftsColumn := "text" // default
	if meta != nil && len(meta.FTSColumns) > 0 {
		ftsColumn = meta.FTSColumns[0]
	}

	// Use LanceDB FTS search
	if req.Filter != "" {
		results, searchErr = s.searchWithFilter(ctx, table, ftsColumn, req.Query, req.Filter, req.Limit)
	} else {
		results, searchErr = s.search(ctx, table, ftsColumn, req.Query, req.Limit)
	}

	if searchErr != nil {
		s.logger.Error("Search failed", zap.Error(searchErr), zap.String("table", req.Table))
		s.sendError(w, fmt.Sprintf("Search failed: %s", searchErr.Error()), http.StatusInternalServerError)
		return
	}

	// Apply offset if needed
	if req.Offset > 0 && req.Offset < len(results) {
		results = results[req.Offset:]
	} else if req.Offset >= len(results) {
		results = []map[string]interface{}{}
	}

	// Filter columns if specified
	if len(req.Columns) > 0 {
		results = filterColumns(results, req.Columns)
	}

	executionTime := time.Since(startTime).Milliseconds()

	response := SearchResponse{
		Query:           req.Query,
		Table:           req.Table,
		Hits:            results,
		TotalHits:       len(results),
		ExecutionTimeMs: executionTime,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

func (s *FTSService) search(ctx context.Context, table contracts.ITable, column, query string, limit int) ([]map[string]interface{}, error) {
	// Use LanceDB FTS search - returns []map[string]interface{} directly
	results, err := table.FullTextSearch(ctx, column, query)
	if err != nil {
		return nil, err
	}

	// Apply limit
	if limit > 0 && len(results) > limit {
		results = results[:limit]
	}

	return results, nil
}

func (s *FTSService) searchWithFilter(ctx context.Context, table contracts.ITable, column, query, filter string, limit int) ([]map[string]interface{}, error) {
	// Use LanceDB FTS search with filter - returns []map[string]interface{} directly
	results, err := table.FullTextSearchWithFilter(ctx, column, query, filter)
	if err != nil {
		return nil, err
	}

	// Apply limit
	if limit > 0 && len(results) > limit {
		results = results[:limit]
	}

	return results, nil
}

func filterColumns(results []map[string]interface{}, columns []string) []map[string]interface{} {
	columnSet := make(map[string]bool)
	for _, col := range columns {
		columnSet[strings.TrimSpace(col)] = true
	}
	// Always include score
	columnSet["_score"] = true

	filtered := make([]map[string]interface{}, len(results))
	for i, row := range results {
		newRow := make(map[string]interface{})
		for k, v := range row {
			if columnSet[k] {
				newRow[k] = v
			}
		}
		filtered[i] = newRow
	}
	return filtered
}

// handleIndex creates or updates an FTS index
func (s *FTSService) handleIndex(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		s.createIndex(w, r)
	case http.MethodDelete:
		s.deleteIndex(w, r)
	default:
		s.sendError(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *FTSService) createIndex(w http.ResponseWriter, r *http.Request) {
	var req IndexRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.sendError(w, "Invalid JSON body", http.StatusBadRequest)
		return
	}

	// Validate request
	if req.Table == "" {
		s.sendError(w, "Table name is required", http.StatusBadRequest)
		return
	}
	if req.Source == "" {
		s.sendError(w, "Source parquet file path is required", http.StatusBadRequest)
		return
	}
	if len(req.FTSColumns) == 0 {
		s.sendError(w, "At least one FTS column is required", http.StatusBadRequest)
		return
	}

	// Check if source file exists
	if _, err := os.Stat(req.Source); os.IsNotExist(err) {
		s.sendError(w, fmt.Sprintf("Source file not found: %s", req.Source), http.StatusBadRequest)
		return
	}

	startTime := time.Now()
	ctx := context.Background()

	s.logger.Info("Creating FTS index",
		zap.String("table", req.Table),
		zap.String("source", req.Source),
		zap.Strings("fts_columns", req.FTSColumns),
	)

	// Check if table already exists
	s.mu.Lock()
	existingTable, exists := s.tables[req.Table]
	if exists {
		if !req.Replace {
			s.mu.Unlock()
			s.sendError(w, fmt.Sprintf("Table '%s' already exists. Set replace=true to overwrite.", req.Table), http.StatusConflict)
			return
		}
		// Close and remove existing table
		existingTable.Close()
		delete(s.tables, req.Table)
		delete(s.tablesMeta, req.Table)
	}
	s.mu.Unlock()

	// Read parquet file and create Lance table
	// This uses LanceDB's ability to ingest from parquet
	table, rowCount, err := s.createTableFromParquet(ctx, req.Table, req.Source)
	if err != nil {
		s.logger.Error("Failed to create table from parquet", zap.Error(err))
		s.sendError(w, fmt.Sprintf("Failed to create table: %s", err.Error()), http.StatusInternalServerError)
		return
	}

	// Create FTS index on specified columns
	err = table.CreateIndex(ctx, req.FTSColumns, contracts.IndexTypeFts)
	if err != nil {
		s.logger.Error("Failed to create FTS index", zap.Error(err))
		table.Close()
		s.sendError(w, fmt.Sprintf("Failed to create FTS index: %s", err.Error()), http.StatusInternalServerError)
		return
	}

	// Store table reference
	s.mu.Lock()
	s.tables[req.Table] = table
	s.tablesMeta[req.Table] = &TableMeta{
		Name:       req.Table,
		FTSColumns: req.FTSColumns,
		RowCount:   rowCount,
		IndexedAt:  time.Now(),
	}
	s.mu.Unlock()

	indexTime := time.Since(startTime).Milliseconds()

	s.logger.Info("FTS index created successfully",
		zap.String("table", req.Table),
		zap.Int64("rows", rowCount),
		zap.Int64("time_ms", indexTime),
	)

	response := IndexResponse{
		Table:       req.Table,
		FTSColumns:  req.FTSColumns,
		RowCount:    rowCount,
		IndexTimeMs: indexTime,
		Success:     true,
		Message:     "Index created successfully",
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(response)
}

func (s *FTSService) createTableFromParquet(ctx context.Context, tableName, parquetPath string) (contracts.ITable, int64, error) {
	// Read parquet file using Arrow
	// LanceDB can create tables from various sources
	// For now, we'll use a simple approach

	// The LanceDB Go SDK should support creating tables from parquet
	// This is a simplified implementation

	// First, try to drop the table if it exists (for replace functionality)
	_ = s.db.DropTable(ctx, tableName)

	// Create table from parquet file
	// Note: The exact API depends on lancedb-go version
	// We may need to read parquet with Arrow and then create the table

	// For now, let's use a file-based approach
	lancePath := filepath.Join(s.config.IndexPath, tableName+".lance")

	// Remove existing lance directory if present
	os.RemoveAll(lancePath)

	// The LanceDB Go SDK may not have direct parquet import
	// We need to read parquet with Arrow and create table from records
	table, err := s.createTableFromParquetWithArrow(ctx, tableName, parquetPath)
	if err != nil {
		return nil, 0, err
	}

	count, err := table.Count(ctx)
	if err != nil {
		return table, 0, nil
	}

	return table, count, nil
}

func (s *FTSService) createTableFromParquetWithArrow(ctx context.Context, tableName, parquetPath string) (contracts.ITable, error) {
	// Read parquet using Arrow
	// This requires the Arrow parquet reader

	// For now, return an error indicating this needs the full Arrow integration
	// In production, you would:
	// 1. Use arrow/go/v18/parquet to read the file
	// 2. Get the Arrow schema and records
	// 3. Create a LanceDB table from those records

	return nil, fmt.Errorf("parquet import requires Arrow integration - use Python indexer or pre-converted Lance files")
}

func (s *FTSService) deleteIndex(w http.ResponseWriter, r *http.Request) {
	tableName := r.URL.Query().Get("table")
	if tableName == "" {
		// Try to get from path
		parts := strings.Split(r.URL.Path, "/")
		if len(parts) > 2 {
			tableName = parts[len(parts)-1]
		}
	}

	if tableName == "" {
		s.sendError(w, "Table name is required", http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	table, exists := s.tables[tableName]
	if !exists {
		s.mu.Unlock()
		s.sendError(w, fmt.Sprintf("Table '%s' not found", tableName), http.StatusNotFound)
		return
	}

	table.Close()
	delete(s.tables, tableName)
	delete(s.tablesMeta, tableName)
	s.mu.Unlock()

	// Drop the table from LanceDB
	if err := s.db.DropTable(context.Background(), tableName); err != nil {
		s.logger.Warn("Failed to drop table", zap.String("table", tableName), zap.Error(err))
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"message": fmt.Sprintf("Index '%s' deleted", tableName),
	})
}

// handleListIndexes lists all indexed tables
func (s *FTSService) handleListIndexes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.sendError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	s.mu.RLock()
	indexes := make([]*TableMeta, 0, len(s.tablesMeta))
	for _, meta := range s.tablesMeta {
		indexes = append(indexes, meta)
	}
	s.mu.RUnlock()

	response := map[string]interface{}{
		"indexes": indexes,
		"count":   len(indexes),
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

func (s *FTSService) sendError(w http.ResponseWriter, message string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(ErrorResponse{
		Error:   http.StatusText(code),
		Message: message,
		Code:    code,
	})
}
