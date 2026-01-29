package handlers

import (
	"net/http/httptest"
	"testing"
)

func TestEncodeCursor(t *testing.T) {
	tests := []struct {
		name    string
		cursor  *Cursor
		wantErr bool
	}{
		{
			name:    "nil cursor",
			cursor:  nil,
			wantErr: false,
		},
		{
			name: "simple cursor",
			cursor: &Cursor{
				SortColumns:    []string{"id"},
				SortValues:     []interface{}{100},
				SortDirections: []string{"asc"},
				Offset:         0,
			},
			wantErr: false,
		},
		{
			name: "multi-column cursor",
			cursor: &Cursor{
				SortColumns:    []string{"created_at", "id"},
				SortValues:     []interface{}{"2024-01-15", 42},
				SortDirections: []string{"desc", "asc"},
				Offset:         5,
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded, err := EncodeCursor(tt.cursor)

			if (err != nil) != tt.wantErr {
				t.Errorf("EncodeCursor() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if tt.cursor == nil {
				if encoded != "" {
					t.Errorf("EncodeCursor(nil) = %v, want empty string", encoded)
				}
				return
			}

			// Verify we can decode it back
			decoded, err := DecodeCursor(encoded)
			if err != nil {
				t.Errorf("DecodeCursor() error = %v", err)
				return
			}

			if len(decoded.SortColumns) != len(tt.cursor.SortColumns) {
				t.Errorf("SortColumns length mismatch: got %d, want %d", len(decoded.SortColumns), len(tt.cursor.SortColumns))
			}
		})
	}
}

func TestDecodeCursor(t *testing.T) {
	tests := []struct {
		name    string
		encoded string
		wantNil bool
		wantErr bool
	}{
		{
			name:    "empty string",
			encoded: "",
			wantNil: true,
			wantErr: false,
		},
		{
			name:    "initial cursor (*)",
			encoded: "*",
			wantNil: true,
			wantErr: false,
		},
		{
			name:    "invalid base64",
			encoded: "not-valid-base64!!!",
			wantNil: false,
			wantErr: true,
		},
		{
			name:    "invalid json after decode",
			encoded: "bm90LWpzb24=", // "not-json" base64 encoded
			wantNil: false,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cursor, err := DecodeCursor(tt.encoded)

			if (err != nil) != tt.wantErr {
				t.Errorf("DecodeCursor() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if tt.wantNil && cursor != nil {
				t.Errorf("DecodeCursor() = %v, want nil", cursor)
			}
		})
	}
}

func TestParseCursorPagination(t *testing.T) {
	tests := []struct {
		name        string
		query       string
		wantCursor  string
		wantIsCur   bool
		wantInitial bool
	}{
		{
			name:        "no cursor param",
			query:       "",
			wantCursor:  "",
			wantIsCur:   false,
			wantInitial: false,
		},
		{
			name:        "initial cursor",
			query:       "cursor=*",
			wantCursor:  "*",
			wantIsCur:   true,
			wantInitial: true,
		},
		{
			name:        "encoded cursor",
			query:       "cursor=eyJzIjpbImlkIl0sInYiOlsxMDBdLCJkIjpbImFzYyJdLCJvIjowfQ==",
			wantCursor:  "eyJzIjpbImlkIl0sInYiOlsxMDBdLCJkIjpbImFzYyJdLCJvIjowfQ==",
			wantIsCur:   true,
			wantInitial: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/?"+tt.query, nil)
			cursor, isCursor, isInitial := ParseCursorPagination(req)

			if cursor != tt.wantCursor {
				t.Errorf("ParseCursorPagination() cursor = %v, want %v", cursor, tt.wantCursor)
			}
			if isCursor != tt.wantIsCur {
				t.Errorf("ParseCursorPagination() isCursor = %v, want %v", isCursor, tt.wantIsCur)
			}
			if isInitial != tt.wantInitial {
				t.Errorf("ParseCursorPagination() isInitial = %v, want %v", isInitial, tt.wantInitial)
			}
		})
	}
}

func TestBuildNextCursor(t *testing.T) {
	tests := []struct {
		name          string
		sortColumns   []string
		sortDirs      []string
		lastRowValues []interface{}
		offset        int
		wantErr       bool
	}{
		{
			name:          "valid single column",
			sortColumns:   []string{"id"},
			sortDirs:      []string{"asc"},
			lastRowValues: []interface{}{100},
			offset:        0,
			wantErr:       false,
		},
		{
			name:          "valid multi column",
			sortColumns:   []string{"created_at", "id"},
			sortDirs:      []string{"desc", "asc"},
			lastRowValues: []interface{}{"2024-01-15", 42},
			offset:        0,
			wantErr:       false,
		},
		{
			name:          "empty sort columns",
			sortColumns:   []string{},
			sortDirs:      []string{},
			lastRowValues: []interface{}{},
			offset:        0,
			wantErr:       true,
		},
		{
			name:          "mismatch columns and values",
			sortColumns:   []string{"id", "name"},
			sortDirs:      []string{"asc"},
			lastRowValues: []interface{}{100},
			offset:        0,
			wantErr:       true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cursor, err := BuildNextCursor(tt.sortColumns, tt.sortDirs, tt.lastRowValues, tt.offset)

			if (err != nil) != tt.wantErr {
				t.Errorf("BuildNextCursor() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr && cursor == nil {
				t.Error("BuildNextCursor() returned nil cursor, want non-nil")
			}
		})
	}
}

func TestCursorRoundTrip(t *testing.T) {
	// Test that we can encode and decode a cursor correctly
	original := &Cursor{
		SortColumns:    []string{"created_at", "id"},
		SortValues:     []interface{}{"2024-01-15T10:30:00Z", float64(12345)},
		SortDirections: []string{"desc", "asc"},
		Offset:         3,
	}

	encoded, err := EncodeCursor(original)
	if err != nil {
		t.Fatalf("EncodeCursor() error = %v", err)
	}

	decoded, err := DecodeCursor(encoded)
	if err != nil {
		t.Fatalf("DecodeCursor() error = %v", err)
	}

	// Check all fields
	if len(decoded.SortColumns) != len(original.SortColumns) {
		t.Errorf("SortColumns length: got %d, want %d", len(decoded.SortColumns), len(original.SortColumns))
	}
	for i, col := range original.SortColumns {
		if decoded.SortColumns[i] != col {
			t.Errorf("SortColumns[%d]: got %v, want %v", i, decoded.SortColumns[i], col)
		}
	}

	if len(decoded.SortDirections) != len(original.SortDirections) {
		t.Errorf("SortDirections length: got %d, want %d", len(decoded.SortDirections), len(original.SortDirections))
	}

	if decoded.Offset != original.Offset {
		t.Errorf("Offset: got %d, want %d", decoded.Offset, original.Offset)
	}
}
