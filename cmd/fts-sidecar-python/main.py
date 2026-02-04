#!/usr/bin/env python3
"""
FTS Sidecar Service for Caddy DuckDB Module (Python Version)

This service provides full-text search capabilities using LanceDB.
It runs as a separate HTTP service that the main Caddy DuckDB module
can proxy requests to for the /find endpoint.

Endpoints:
    GET  /health           - Health check
    GET  /search           - Full-text search (add autocomplete=true for minimal response)
    POST /index            - Create/update FTS index
    GET  /indexes          - List indexed tables
    DELETE /index?table=X  - Delete an index

Environment Variables:
    FTS_PORT              - HTTP port (default: 8701)
    FTS_INDEX_PATH        - Path to Lance indexes (default: /data/fts)
    FTS_LOG_LEVEL         - Log level: DEBUG, INFO, WARNING, ERROR (default: INFO)

Requirements:
    pip install lancedb pyarrow fastapi uvicorn

Usage:
    python main.py
    # or
    uvicorn main:app --host 0.0.0.0 --port 8701
"""

import logging
import os
import shutil
import sys
import time
from contextlib import asynccontextmanager
from datetime import datetime
from pathlib import Path
from typing import Any

import pyarrow.parquet as pq

try:
    import lancedb
    from fastapi import FastAPI, HTTPException, Query
    from fastapi.responses import JSONResponse
    from pydantic import BaseModel
    import uvicorn
except ImportError as e:
    print(f"Missing dependency: {e}")
    print("\nInstall required packages:")
    print("  pip install lancedb pyarrow fastapi uvicorn pydantic")
    sys.exit(1)


# Configuration
class Config:
    port: int = int(os.getenv("FTS_PORT", "8701"))
    index_path: str = os.getenv("FTS_INDEX_PATH", "/data/fts")
    log_level: str = os.getenv("FTS_LOG_LEVEL", "INFO").upper()


config = Config()

# Setup logging
logging.basicConfig(
    level=getattr(logging, config.log_level, logging.INFO),
    format="%(asctime)s - %(name)s - %(levelname)s - %(message)s",
)
logger = logging.getLogger("fts-sidecar")


# Models
class SearchRequest(BaseModel):
    query: str
    table: str
    columns: list[str] | None = None
    limit: int = 10
    offset: int = 0
    filter: str | None = None
    highlight: bool = False


class SearchResponse(BaseModel):
    query: str
    table: str
    hits: list[dict[str, Any]]
    total_hits: int
    execution_time_ms: int


class IndexRequest(BaseModel):
    table: str
    source: str
    fts_columns: list[str]
    replace: bool = False
    # Autocomplete field mappings (optional - for minimal response mode)
    id_field: str | None = None       # Field to use as 'id' in autocomplete
    display_field: str | None = None  # Field to use as 'display' in autocomplete
    hint_fields: list[str] | None = None  # Fields to combine into 'hint'


class IndexResponse(BaseModel):
    table: str
    fts_columns: list[str]
    row_count: int
    index_time_ms: int
    success: bool
    message: str | None = None


class TableMeta(BaseModel):
    name: str
    fts_columns: list[str]
    row_count: int
    indexed_at: str
    # Autocomplete field mappings (optional - for minimal response mode)
    id_field: str | None = None           # Field to use as 'id' in autocomplete
    display_field: str | None = None      # Field to use as 'display' in autocomplete
    hint_fields: list[str] | None = None  # Fields to combine into 'hint'


# FTS Service
class FTSService:
    def __init__(self):
        self.db: lancedb.db.DBConnection | None = None
        self.tables: dict[str, lancedb.table.Table] = {}
        self.tables_meta: dict[str, TableMeta] = {}

    def init(self):
        """Initialize the LanceDB connection."""
        # Ensure index directory exists
        Path(config.index_path).mkdir(parents=True, exist_ok=True)

        # Connect to LanceDB
        self.db = lancedb.connect(config.index_path)
        logger.info(f"Connected to LanceDB at {config.index_path}")

        # Load existing tables
        self._load_existing_tables()

    def _load_existing_tables(self):
        """Load existing tables from the database."""
        if self.db is None:
            return

        table_names = self.db.table_names()
        for name in table_names:
            try:
                table = self.db.open_table(name)
                self.tables[name] = table

                # Get row count
                count = table.count_rows()

                self.tables_meta[name] = TableMeta(
                    name=name,
                    fts_columns=[],  # We don't know the FTS columns from existing tables
                    row_count=count,
                    indexed_at=datetime.now().isoformat(),
                )
                logger.info(f"Loaded existing table: {name} ({count} rows)")
            except Exception as e:
                logger.warning(f"Failed to open table {name}: {e}")

    def close(self):
        """Close all resources."""
        self.tables.clear()
        self.tables_meta.clear()
        self.db = None

    def search(
        self,
        table_name: str,
        query: str,
        fts_column: str,
        limit: int = 10,
        offset: int = 0,
        columns: list[str] | None = None,
        filter_expr: str | None = None,
    ) -> tuple[list[dict], int]:
        """Perform full-text search."""
        if table_name not in self.tables:
            raise ValueError(f"Table '{table_name}' not found")

        table = self.tables[table_name]

        # Build search query
        search_builder = table.search(query, query_type="fts")

        # Apply limit (with offset accounted)
        search_builder = search_builder.limit(limit + offset)

        # Execute search
        results = search_builder.to_pandas()

        # Apply offset
        if offset > 0:
            results = results.iloc[offset:]

        # Apply limit after offset
        results = results.head(limit)

        # Select columns if specified
        if columns:
            # Always include _score if available
            cols_to_select = [c for c in columns if c in results.columns]
            if "_score" in results.columns and "_score" not in cols_to_select:
                cols_to_select.append("_score")
            if cols_to_select:
                results = results[cols_to_select]

        # Convert to list of dicts
        hits = results.to_dict(orient="records")

        return hits, len(hits)

    def create_index(
        self,
        table_name: str,
        source_path: str,
        fts_columns: list[str],
        replace: bool = False,
        id_field: str | None = None,
        display_field: str | None = None,
        hint_fields: list[str] | None = None,
    ) -> tuple[int, int]:
        """Create a new FTS index from a parquet file."""
        if self.db is None:
            raise RuntimeError("Database not initialized")

        # Check if table exists
        if table_name in self.tables:
            if not replace:
                raise ValueError(
                    f"Table '{table_name}' already exists. Set replace=true to overwrite."
                )
            # Drop existing table
            self.db.drop_table(table_name)
            del self.tables[table_name]
            del self.tables_meta[table_name]

        # Check source file
        if not Path(source_path).exists():
            raise ValueError(f"Source file not found: {source_path}")

        start_time = time.time()

        # Read parquet file
        logger.info(f"Reading parquet file: {source_path}")
        parquet_table = pq.read_table(source_path)
        row_count = parquet_table.num_rows

        # Validate FTS columns exist
        available_cols = parquet_table.schema.names
        invalid_cols = set(fts_columns) - set(available_cols)
        if invalid_cols:
            raise ValueError(
                f"FTS columns not found in source: {invalid_cols}. "
                f"Available columns: {available_cols}"
            )

        # Create table from parquet data
        logger.info(f"Creating Lance table: {table_name}")
        table = self.db.create_table(table_name, parquet_table.to_pandas())

        # Create FTS index
        logger.info(f"Creating FTS index on columns: {fts_columns}")
        table.create_fts_index(fts_columns, use_tantivy=True, replace=True)

        # Store table reference
        self.tables[table_name] = table
        self.tables_meta[table_name] = TableMeta(
            name=table_name,
            fts_columns=fts_columns,
            row_count=row_count,
            indexed_at=datetime.now().isoformat(),
            id_field=id_field,
            display_field=display_field,
            hint_fields=hint_fields,
        )

        index_time_ms = int((time.time() - start_time) * 1000)
        logger.info(
            f"FTS index created: {table_name} ({row_count} rows in {index_time_ms}ms)"
        )

        return row_count, index_time_ms

    def delete_index(self, table_name: str):
        """Delete an index."""
        if self.db is None:
            raise RuntimeError("Database not initialized")

        if table_name not in self.tables:
            raise ValueError(f"Table '{table_name}' not found")

        self.db.drop_table(table_name)
        del self.tables[table_name]
        del self.tables_meta[table_name]

        logger.info(f"Deleted index: {table_name}")

    def list_indexes(self) -> list[TableMeta]:
        """List all indexed tables."""
        return list(self.tables_meta.values())

    def autocomplete(
        self,
        table_name: str,
        query: str,
        limit: int = 10,
        filter_expr: str | None = None,
    ) -> list[dict]:
        """Perform autocomplete search with minimal response."""
        if table_name not in self.tables:
            raise ValueError(f"Table '{table_name}' not found")

        table = self.tables[table_name]
        meta = self.tables_meta[table_name]

        # Build search query - use prefix-style matching
        # Tantivy supports prefix queries with wildcards
        search_query = query
        if not query.endswith("*"):
            search_query = f"{query}*"

        try:
            search_builder = table.search(search_query, query_type="fts")
            search_builder = search_builder.limit(limit)
            results_df = search_builder.to_pandas()
        except Exception as e:
            # Fall back to regular FTS if prefix search fails
            logger.debug(f"Prefix search failed, falling back to FTS: {e}")
            search_builder = table.search(query, query_type="fts")
            search_builder = search_builder.limit(limit)
            results_df = search_builder.to_pandas()

        # Map results to autocomplete format
        results = []
        for _, row in results_df.iterrows():
            result = {}

            # Get ID field
            if meta.id_field and meta.id_field in row:
                result["id"] = str(row[meta.id_field])
            elif "id" in row:
                result["id"] = str(row["id"])
            elif "pid" in row:
                result["id"] = str(row["pid"])
            else:
                # Use first column as fallback
                result["id"] = str(row.iloc[0]) if len(row) > 0 else ""

            # Get display field
            if meta.display_field and meta.display_field in row:
                result["display"] = str(row[meta.display_field] or "")
            elif "Title" in row:
                result["display"] = str(row["Title"] or "")
            elif "display_name" in row:
                result["display"] = str(row["display_name"] or "")
            elif "name" in row:
                result["display"] = str(row["name"] or "")
            else:
                result["display"] = ""

            # Build hint from configured fields
            if meta.hint_fields:
                hint_parts = []
                for field in meta.hint_fields:
                    if field in row and row[field]:
                        value = str(row[field])
                        # Truncate long values
                        if len(value) > 50:
                            value = value[:47] + "..."
                        hint_parts.append(value)
                if hint_parts:
                    result["hint"] = ", ".join(hint_parts)

            # Include relevance score
            if "_score" in row:
                result["_score"] = float(row["_score"])

            results.append(result)

        return results


# Create service instance
fts_service = FTSService()


# FastAPI app with lifespan
@asynccontextmanager
async def lifespan(app: FastAPI):
    # Startup
    logger.info(f"Starting FTS sidecar service on port {config.port}")
    logger.info(f"Index path: {config.index_path}")
    fts_service.init()
    yield
    # Shutdown
    logger.info("Shutting down FTS sidecar service")
    fts_service.close()


app = FastAPI(
    title="FTS Sidecar Service",
    description="Full-text search sidecar for Caddy DuckDB Module using LanceDB",
    version="1.0.0",
    lifespan=lifespan,
)


# Endpoints
@app.get("/health")
async def health():
    """Health check endpoint."""
    return {
        "status": "ok",
        "service": "fts-sidecar",
        "table_count": len(fts_service.tables),
        "index_path": config.index_path,
    }


@app.get("/search")
async def search_get(
    q: str = Query(..., description="Search query"),
    table: str = Query(..., description="Table name"),
    limit: int = Query(10, ge=1, le=1000, description="Maximum results"),
    offset: int = Query(0, ge=0, description="Offset for pagination"),
    columns: str | None = Query(None, description="Comma-separated columns to return"),
    filter: str | None = Query(None, description="SQL filter expression"),
    highlight: bool = Query(False, description="Highlight matches"),
    autocomplete: bool = Query(False, description="Return minimal autocomplete response"),
):
    """
    Perform full-text search (GET).

    With autocomplete=true, returns a minimal response with only id, display, hint fields.
    Configure field mappings when creating the index.
    """
    if autocomplete:
        return await _do_autocomplete(query=q, table=table, limit=min(limit, 10), filter_expr=filter)
    return await _do_search(
        query=q,
        table=table,
        limit=limit,
        offset=offset,
        columns=columns.split(",") if columns else None,
        filter_expr=filter,
    )


@app.post("/search")
async def search_post(request: SearchRequest) -> SearchResponse:
    """Perform full-text search (POST)."""
    return await _do_search(
        query=request.query,
        table=request.table,
        limit=request.limit,
        offset=request.offset,
        columns=request.columns,
        filter_expr=request.filter,
    )


async def _do_search(
    query: str,
    table: str,
    limit: int,
    offset: int,
    columns: list[str] | None,
    filter_expr: str | None,
) -> SearchResponse:
    """Internal search implementation."""
    start_time = time.time()

    # Get FTS column from metadata
    meta = fts_service.tables_meta.get(table)
    if meta is None:
        raise HTTPException(status_code=404, detail=f"Table '{table}' not found")

    fts_column = meta.fts_columns[0] if meta.fts_columns else "text"

    try:
        hits, total = fts_service.search(
            table_name=table,
            query=query,
            fts_column=fts_column,
            limit=limit,
            offset=offset,
            columns=columns,
            filter_expr=filter_expr,
        )
    except ValueError as e:
        raise HTTPException(status_code=404, detail=str(e))
    except Exception as e:
        logger.error(f"Search error: {e}")
        raise HTTPException(status_code=500, detail=f"Search failed: {e}")

    execution_time_ms = int((time.time() - start_time) * 1000)

    return SearchResponse(
        query=query,
        table=table,
        hits=hits,
        total_hits=total,
        execution_time_ms=execution_time_ms,
    )


async def _do_autocomplete(
    query: str,
    table: str,
    limit: int,
    filter_expr: str | None,
):
    """Internal autocomplete implementation - returns minimal response."""
    start_time = time.time()

    meta = fts_service.tables_meta.get(table)
    if meta is None:
        raise HTTPException(status_code=404, detail=f"Table '{table}' not found")

    try:
        results = fts_service.autocomplete(
            table_name=table,
            query=query,
            limit=limit,
            filter_expr=filter_expr,
        )
    except ValueError as e:
        raise HTTPException(status_code=404, detail=str(e))
    except Exception as e:
        logger.error(f"Autocomplete error: {e}")
        raise HTTPException(status_code=500, detail=f"Autocomplete failed: {e}")

    execution_time_ms = int((time.time() - start_time) * 1000)

    return {
        "query": query,
        "table": table,
        "results": results,
        "count": len(results),
        "execution_time_ms": execution_time_ms,
    }


@app.post("/index", status_code=201)
async def create_index(request: IndexRequest) -> IndexResponse:
    """Create or update an FTS index."""
    try:
        row_count, index_time_ms = fts_service.create_index(
            table_name=request.table,
            source_path=request.source,
            fts_columns=request.fts_columns,
            replace=request.replace,
            id_field=request.id_field,
            display_field=request.display_field,
            hint_fields=request.hint_fields,
        )
    except ValueError as e:
        raise HTTPException(status_code=400, detail=str(e))
    except Exception as e:
        logger.error(f"Index creation error: {e}")
        raise HTTPException(status_code=500, detail=f"Index creation failed: {e}")

    return IndexResponse(
        table=request.table,
        fts_columns=request.fts_columns,
        row_count=row_count,
        index_time_ms=index_time_ms,
        success=True,
        message="Index created successfully",
    )


@app.delete("/index")
async def delete_index(table: str = Query(..., description="Table name to delete")):
    """Delete an FTS index."""
    try:
        fts_service.delete_index(table)
    except ValueError as e:
        raise HTTPException(status_code=404, detail=str(e))
    except Exception as e:
        logger.error(f"Index deletion error: {e}")
        raise HTTPException(status_code=500, detail=f"Index deletion failed: {e}")

    return {"success": True, "message": f"Index '{table}' deleted"}


@app.get("/indexes")
async def list_indexes():
    """List all indexed tables."""
    indexes = fts_service.list_indexes()
    return {
        "indexes": [idx.model_dump() for idx in indexes],
        "count": len(indexes),
    }


# Error handlers
@app.exception_handler(Exception)
async def generic_exception_handler(request, exc):
    logger.error(f"Unhandled error: {exc}")
    return JSONResponse(
        status_code=500,
        content={
            "error": "Internal Server Error",
            "message": str(exc),
            "code": 500,
        },
    )


if __name__ == "__main__":
    uvicorn.run(
        "main:app",
        host="0.0.0.0",
        port=config.port,
        log_level=config.log_level.lower(),
    )
