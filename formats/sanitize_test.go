package formats

import (
	"encoding/json"
	"testing"

	duckdb "github.com/duckdb/duckdb-go/v2"
)

func TestSanitizeValue_Map(t *testing.T) {
	m := duckdb.Map{"key1": "value1", "key2": int32(42)}
	got := SanitizeValue(m)
	// Must be JSON-serializable
	b, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("SanitizeValue(Map) produced non-serializable value: %v", err)
	}
	var result map[string]any
	if err := json.Unmarshal(b, &result); err != nil {
		t.Fatalf("unexpected unmarshal error: %v", err)
	}
	if result["key1"] != "value1" {
		t.Errorf("expected key1=value1, got %v", result["key1"])
	}
}

func TestSanitizeValue_NestedMap(t *testing.T) {
	inner := duckdb.Map{"inner_key": "inner_val"}
	outer := duckdb.Map{"nested": inner}
	got := SanitizeValue(outer)
	b, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("nested Map produced non-serializable value: %v", err)
	}
	var result map[string]any
	if err := json.Unmarshal(b, &result); err != nil {
		t.Fatalf("unexpected unmarshal error: %v", err)
	}
}

func TestSanitizeValue_UUID(t *testing.T) {
	var u duckdb.UUID
	copy(u[:], []byte{0x6b, 0xa7, 0xb8, 0x10, 0x9d, 0xad, 0x11, 0xd1, 0x80, 0xb4, 0x00, 0xc0, 0x4f, 0xd4, 0x30, 0xc8})
	got := SanitizeValue(u)
	s, ok := got.(string)
	if !ok {
		t.Fatalf("expected string, got %T", got)
	}
	if len(s) != 36 {
		t.Errorf("expected 36-char UUID string, got %q", s)
	}
}

func TestSanitizeValue_SliceWithMap(t *testing.T) {
	slice := []any{duckdb.Map{"k": "v"}, nil, "plain"}
	got := SanitizeValue(slice)
	b, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("slice with Map produced non-serializable value: %v", err)
	}
	var result []any
	if err := json.Unmarshal(b, &result); err != nil {
		t.Fatalf("unexpected unmarshal error: %v", err)
	}
	if len(result) != 3 {
		t.Errorf("expected 3 elements, got %d", len(result))
	}
}

func TestSanitizeValue_Nil(t *testing.T) {
	got := SanitizeValue(nil)
	if got != nil {
		t.Errorf("expected nil, got %v", got)
	}
}

func TestSanitizeValue_Bytes(t *testing.T) {
	got := SanitizeValue([]byte("hello"))
	if got != "hello" {
		t.Errorf("expected string 'hello', got %v", got)
	}
}

func TestSanitizeValue_Passthrough(t *testing.T) {
	cases := []any{int64(42), float64(3.14), "hello", true, nil}
	for _, c := range cases {
		got := SanitizeValue(c)
		b, err := json.Marshal(got)
		if err != nil {
			t.Errorf("SanitizeValue(%v) produced non-serializable value: %v", c, err)
		}
		_ = b
	}
}
