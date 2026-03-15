package handlers

import (
	"context"
	_ "embed"
	"os"
	"path/filepath"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// docSection is a parsed heading + its content from a Markdown document.
type docSection struct {
	id      string // slug, e.g. "group-by-all"
	title   string // original text, e.g. "GROUP BY ALL"
	level   int    // heading depth (2 = ##, 3 = ###)
	source  string // which doc it came from, e.g. "sql-syntax"
	content string // full text of the section including the heading line
}

// parseSections splits a Markdown string into sections at ## and ### headings.
func parseSections(doc, source string) []docSection {
	var sections []docSection
	var cur *docSection
	var buf strings.Builder

	flush := func() {
		if cur != nil {
			cur.content = strings.TrimRight(buf.String(), "\n")
			sections = append(sections, *cur)
			buf.Reset()
		}
	}

	for _, line := range strings.Split(doc, "\n") {
		if level, title, ok := parseHeading(line); ok && level >= 2 {
			flush()
			cur = &docSection{id: slugify(title), title: title, level: level, source: source}
		}
		buf.WriteString(line)
		buf.WriteByte('\n')
	}
	flush()
	return sections
}

func parseHeading(line string) (level int, title string, ok bool) {
	i := 0
	for i < len(line) && line[i] == '#' {
		i++
	}
	if i == 0 || i >= len(line) || line[i] != ' ' {
		return 0, "", false
	}
	return i, strings.TrimSpace(line[i+1:]), true
}

func slugify(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '_' || r == '/':
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

//go:embed docs/duckdb-sql.md
var duckdbSQLDoc string

//go:embed docs/duckdb-visualization.md
var duckdbVizDoc string

// registerHelpTool registers the built-in help MCP tool backed by the embedded
// Markdown docs. Call with no topic for a table of contents; call with a
// section ID or keyword to retrieve matching section content.
func registerHelpTool(srv *mcp.Server) {
	sections := append(
		parseSections(duckdbSQLDoc, "sql-syntax"),
		parseSections(duckdbVizDoc, "visualization")...,
	)

	srv.AddTool(
		&mcp.Tool{
			Name: "help",
			Description: "DuckDB documentation browser. " +
				"Call with no arguments for a table of contents (section IDs and titles). " +
				"Call with a topic to retrieve that section's content — use the section ID " +
				"from the table of contents, or any keyword that appears in a section title. " +
				"Covers DuckDB-friendly SQL features (sql-syntax) and chart query patterns " +
				"including ASCII textplot (visualization).",
			InputSchema: buildSchema(
				strProp("topic", "Section ID or title keyword. Omit to list all sections.", false),
			),
		},
		func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			topic := strings.TrimSpace(argString(req, "topic", ""))

			if topic == "" {
				var sb strings.Builder
				sb.WriteString("| id | title | doc |\n|---|---|---|\n")
				for _, s := range sections {
					indent := strings.Repeat("  ", s.level-2)
					sb.WriteString("| `")
					sb.WriteString(s.id)
					sb.WriteString("` | ")
					sb.WriteString(indent)
					sb.WriteString(s.title)
					sb.WriteString(" | ")
					sb.WriteString(s.source)
					sb.WriteString(" |\n")
				}
				return textResult(sb.String()), nil
			}

			needle := strings.ToLower(topic)
			var matches []string
			for _, s := range sections {
				if s.id == needle || strings.Contains(s.id, needle) || strings.Contains(strings.ToLower(s.title), needle) {
					matches = append(matches, s.content)
				}
			}
			if len(matches) == 0 {
				return textResult("No sections found for: " + topic +
					". Call help() with no arguments to see available section IDs."), nil
			}
			return textResult(strings.Join(matches, "\n\n---\n\n")), nil
		},
	)
}

// registerDocResources adds Markdown reference documents as MCP resources.
// LLM clients that support resources can fetch these before writing queries
// to learn DuckDB-specific syntax and best practices.
//
// Two resources are always registered from embedded docs:
//   - duckdb://docs/sql-syntax
//   - duckdb://docs/visualization
//
// If docsDir is non-empty, every *.md file found directly in that directory
// is also registered as duckdb://docs/<stem> (filename without extension).
// This lets operators add deployment-specific guides (e.g. schema references,
// domain-specific query guidance) without recompiling the binary.
func registerDocResources(srv *mcp.Server, docsDir string) {
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

	// Append any operator-provided .md files from the configured docs directory.
	if docsDir != "" {
		entries, _ := os.ReadDir(docsDir)
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			path := filepath.Join(docsDir, e.Name())
			data, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			stem := strings.TrimSuffix(e.Name(), ".md")
			docs = append(docs, docResource{
				uri:         "duckdb://docs/" + stem,
				name:        stem,
				description: "Operator-provided documentation: " + stem,
				content:     string(data),
			})
		}
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
