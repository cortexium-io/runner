package main

import (
	"encoding/json"
	"testing"
)

func TestPublicationPathRetainsNativeIdentity(t *testing.T) {
	for _, tt := range []struct{ game, id, want string }{
		{"sudoku", "sudoku-generated:sdk7-9x9-e-123", "/sudoku/sdk7-9x9-e-123"},
		{"suguru", "suguru-generated:sgr10-10x15-m5-hard-2", "/suguru/sgr10-10x15-m5-hard-2"},
		{"shikaku", "shk1-15-h-23", "/shikaku/shk1-15-h-23"},
	} {
		got, err := publicationPath(tt.game, map[string]any{"game": tt.game, "id": tt.id, "contentRevision": "1"})
		if err != nil || got != tt.want {
			t.Fatal(got, err)
		}
	}
	for _, identity := range []map[string]any{
		{"game": "sudoku", "id": "sdk7-9x9-e-123", "contentRevision": "1"},
		{"game": "sudoku", "id": "sudoku-generated:../other", "contentRevision": "1"},
		{"game": "suguru", "id": "sudoku-generated:sdk7-9x9-e-123", "contentRevision": "1"},
		{"game": "sudoku", "id": "sudoku-generated:sdk7-9x9-e-123", "contentRevision": "2"},
	} {
		if _, err := publicationPath("sudoku", identity); err == nil {
			t.Fatal("unbound route accepted", identity)
		}
	}
}

func TestNonogramGeometryUsesValidatedActualClues(t *testing.T) {
	publication := func(rows, columns [][]int) json.RawMessage {
		data, _ := json.Marshal(map[string]any{"definition": map[string]any{"size": 5, "rows": rows, "columns": columns}})
		return data
	}
	a := [][]int{{1}, {1}, {1}, {1}, {1}}
	b := [][]int{{1, 1, 1}, {1}, {1}, {1}, {1}}
	small, err := publicationNonogramGeometry(5, publication(a, a))
	if err != nil {
		t.Fatal(err)
	}
	wide, err := publicationNonogramGeometry(5, publication(b, b))
	if err != nil || small.RowTextUnits == wide.RowTextUnits || small.ColumnClues == wide.ColumnClues {
		t.Fatal("different payload tracks collapsed", small, wide, err)
	}
	for _, bad := range []json.RawMessage{publication(a[:4], a), publication([][]int{{6}, {1}, {1}, {1}, {1}}, a), publication([][]int{{3, 3}, {1}, {1}, {1}, {1}}, a), json.RawMessage(`{}`)} {
		if _, err = publicationNonogramGeometry(5, bad); err == nil {
			t.Fatal("unbound/invalid geometry accepted", string(bad))
		}
	}
}
