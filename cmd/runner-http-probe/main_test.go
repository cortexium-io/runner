package main

import "testing"

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
