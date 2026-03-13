package handlers

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/tobilg/caddy-duckdb-module/database"
	"go.uber.org/zap"
)

func setupMacroTestDB(t *testing.T) (*database.Manager, func()) {
	t.Helper()
	cfg := database.Config{
		MainDBPath:   ":memory:",
		AuthDBPath:   ":memory:",
		Threads:      1,
		AccessMode:   "read_write",
		QueryTimeout: 30 * time.Second,
		Logger:       zap.NewNop(),
	}
	mgr, err := database.NewManagerForTesting(cfg)
	if err != nil {
		t.Fatalf("create manager: %v", err)
	}
	return mgr, func() { mgr.Close() }
}

// TestDiscoverMacros_ScalarWithComment verifies that a scalar macro with
// COMMENT ON MACRO (no TABLE keyword) is discovered with IsScalar=true
// and its comment used as the tool description.
func TestDiscoverMacros_ScalarWithComment(t *testing.T) {
	mgr, cleanup := setupMacroTestDB(t)
	defer cleanup()

	_, err := mgr.ExecMain(`CREATE OR REPLACE MACRO doi_url(doi) AS 'https://doi.org/' || doi`)
	if err != nil {
		t.Fatalf("create scalar macro: %v", err)
	}
	_, err = mgr.ExecMain(`COMMENT ON MACRO doi_url IS 'Return canonical DOI URL'`)
	if err != nil {
		t.Fatalf("comment on scalar macro: %v", err)
	}

	macros, err := discoverMacros(mgr)
	if err != nil {
		t.Fatalf("discoverMacros: %v", err)
	}

	var found *macroInfo
	for i := range macros {
		if macros[i].Name == "doi_url" {
			found = &macros[i]
			break
		}
	}
	if found == nil {
		t.Fatal("doi_url not found in discovered macros")
	}
	if !found.IsScalar {
		t.Error("expected IsScalar=true for scalar macro")
	}
	if found.Comment != "Return canonical DOI URL" {
		t.Errorf("unexpected comment: %q", found.Comment)
	}
	if desc := macroDescription(*found); desc != "Return canonical DOI URL" {
		t.Errorf("macroDescription should return comment, got %q", desc)
	}
}

// TestDiscoverMacros_TableWithComment verifies that a table macro with
// COMMENT ON MACRO TABLE is discovered with IsScalar=false and its comment
// used as the tool description.
func TestDiscoverMacros_TableWithComment(t *testing.T) {
	mgr, cleanup := setupMacroTestDB(t)
	defer cleanup()

	_, err := mgr.ExecMain(`
		CREATE OR REPLACE MACRO top_n(n) AS TABLE
		  SELECT range AS id FROM range(n)
	`)
	if err != nil {
		t.Fatalf("create table macro: %v", err)
	}
	_, err = mgr.ExecMain(`COMMENT ON MACRO TABLE top_n IS 'Return the first n integers'`)
	if err != nil {
		t.Fatalf("comment on table macro: %v", err)
	}

	macros, err := discoverMacros(mgr)
	if err != nil {
		t.Fatalf("discoverMacros: %v", err)
	}

	var found *macroInfo
	for i := range macros {
		if macros[i].Name == "top_n" {
			found = &macros[i]
			break
		}
	}
	if found == nil {
		t.Fatal("top_n not found in discovered macros")
	}
	if found.IsScalar {
		t.Error("expected IsScalar=false for table macro")
	}
	if found.Comment != "Return the first n integers" {
		t.Errorf("unexpected comment: %q", found.Comment)
	}
}

// TestDiscoverMacros_MetadataOverride verifies that memory.macro_descriptions
// overrides both the description and parameter types for a macro.
func TestDiscoverMacros_MetadataOverride(t *testing.T) {
	mgr, cleanup := setupMacroTestDB(t)
	defer cleanup()

	_, err := mgr.ExecMain(`CREATE OR REPLACE MACRO pmid_url(pmid) AS 'https://pubmed.ncbi.nlm.nih.gov/' || pmid::VARCHAR`)
	if err != nil {
		t.Fatalf("create macro: %v", err)
	}
	_, err = mgr.ExecMain(`
		CREATE OR REPLACE TABLE memory.macro_descriptions (
			macro_name  VARCHAR PRIMARY KEY,
			description VARCHAR,
			param_types JSON
		)
	`)
	if err != nil {
		t.Fatalf("create metadata table: %v", err)
	}
	_, err = mgr.ExecMain(`
		INSERT INTO memory.macro_descriptions VALUES
		('pmid_url', 'Return PubMed URL for a PMID integer', '{"pmid": "BIGINT"}')
	`)
	if err != nil {
		t.Fatalf("insert metadata: %v", err)
	}

	macros, err := discoverMacros(mgr)
	if err != nil {
		t.Fatalf("discoverMacros: %v", err)
	}

	var found *macroInfo
	for i := range macros {
		if macros[i].Name == "pmid_url" {
			found = &macros[i]
			break
		}
	}
	if found == nil {
		t.Fatal("pmid_url not found")
	}
	if found.Comment != "Return PubMed URL for a PMID integer" {
		t.Errorf("description not overridden: %q", found.Comment)
	}
	if len(found.ParamTypes) == 0 || !strings.EqualFold(found.ParamTypes[0], "BIGINT") {
		t.Errorf("param type not overridden, got: %v", found.ParamTypes)
	}

	// Schema should expose pmid as number, not string.
	schema := buildDynamicSchema(found.Params, found.ParamTypes)
	var s map[string]any
	if err := json.Unmarshal(schema, &s); err != nil {
		t.Fatalf("unmarshal schema: %v", err)
	}
	props := s["properties"].(map[string]any)
	pmidProp := props["pmid"].(map[string]any)
	if pmidProp["type"] != "number" {
		t.Errorf("expected pmid type=number, got %q", pmidProp["type"])
	}
}

// TestLoadMacroMetadata_MissingTable verifies that loadMacroMetadata returns
// an empty map without error when memory.macro_descriptions does not exist.
func TestLoadMacroMetadata_MissingTable(t *testing.T) {
	mgr, cleanup := setupMacroTestDB(t)
	defer cleanup()

	meta := loadMacroMetadata(mgr)
	if len(meta) != 0 {
		t.Errorf("expected empty map, got %d entries", len(meta))
	}
}
