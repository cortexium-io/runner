package store

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/cortexium-io/runner/internal/runner/config"
)

func TestCompetingQualityChangesKeepHistoryAndExecutionPolicy(t *testing.T) {
	ctx := t.Context()
	old := config.Quality{Definition: "original"}
	settings := config.Settings{Quality: old, Config: config.Config{Policy: config.Policy{MaxParallelism: 3}}}
	dir := filepath.Join(t.TempDir(), "state")
	s, err := Create(ctx, dir, settings)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	other, err := Open(ctx, dir, false)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	start, results := make(chan struct{}), make(chan error, 2)
	for i, db := range []*Store{s, other} {
		q := config.Quality{Definition: []string{"new one", "new two"}[i]}
		go func() { <-start; results <- db.ApplyQuality(ctx, old.Revision(), q) }()
	}
	close(start)
	wins := 0
	for range 2 {
		err = <-results
		if err == nil {
			wins++
		} else if !errors.Is(err, ErrConflict) {
			t.Fatal(err)
		}
	}
	if wins != 1 {
		t.Fatal("competing defaults applied", wins)
	}
	current, _, err := s.Settings(ctx)
	if err != nil || current.Config.Policy.MaxParallelism != 3 {
		t.Fatal("policy lost", err)
	}
	if q, err := other.Quality(ctx, old.Revision()); err != nil || q.Definition != old.Definition {
		t.Fatal("history changed", q, err)
	}
	if q, err := other.Quality(ctx, current.Quality.Revision()); err != nil || q.Revision() != current.Quality.Revision() {
		t.Fatal("default history missing", err)
	}
}
