package handlers

import (
	"context"
	_ "embed"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

//go:embed docs/duckdb-sql.md
var duckdbSQLDoc string

//go:embed docs/duckdb-visualization.md
var duckdbVizDoc string

// registerDocResources adds Markdown reference documents as MCP resources.
// LLM clients that support resources can fetch these before writing queries
// to learn DuckDB-specific syntax and best practices.
func registerDocResources(srv *mcp.Server) {
	type docResource struct {
		uri         string
		name        string
		description string
		content     string
	}

	docs := []docResource{
		{
			uri:  "duckdb://docs/sql-syntax",
			name: "duckdb_sql_syntax",
			description: "DuckDB friendly SQL reference: GROUP BY ALL, FROM-first, SELECT * EXCLUDE/REPLACE, " +
				"COLUMNS(), PIVOT/UNPIVOT, ASOF JOIN, list/struct literals, lambda functions, " +
				"function chaining, direct file queries, and more.",
			content: duckdbSQLDoc,
		},
		{
			uri:         "duckdb://docs/visualization",
			name:        "duckdb_visualization",
			description: "Query patterns for time series, bar charts, scatter plots, and heatmaps — ready-to-adapt SQL templates for common chart types.",
			content:     duckdbVizDoc,
		},
	}

	for _, d := range docs {
		srv.AddResource(
			&mcp.Resource{
				URI:         d.uri,
				Name:        d.name,
				Description: d.description,
				MIMEType:    "text/markdown",
			},
			func(_ context.Context, _ *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
				return &mcp.ReadResourceResult{
					Contents: []*mcp.ResourceContents{{
						URI:      d.uri,
						MIMEType: "text/markdown",
						Text:     d.content,
					}},
				}, nil
			},
		)
	}
}
