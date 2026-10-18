package store

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/cortexium-io/runner/internal/runner/config"
	"github.com/cortexium-io/runner/internal/runner/core"
)

func TestConcurrentSourceImportKeepsAcceptedTaskAfterReopen(t *testing.T) {
	ctx := t.Context()
	dir := filepath.Join(t.TempDir(), "state")
	one, err := Create(ctx, dir, config.Settings{})
	if err != nil {
		t.Fatal(err)
	}
	two, err := Open(ctx, dir, false)
	if err != nil {
		t.Fatal(err)
	}
	source := core.Source{Provider: "github-issue", Connection: "owner/repo", ID: "I_request", Revision: "original", Title: "request", Body: "original"}
	start := make(chan struct{})
	results := make(chan core.Task, 2)
	errors := make(chan error, 2)
	for _, db := range []*Store{one, two} {
		go func() {
			<-start
			got, err := db.Import(ctx, core.Task{ID: core.NewID(), Source: &source, Stage: core.Draft, Versions: []core.Version{{Number: 1, Body: "original"}}})
			results <- got
			errors <- err
		}()
	}
	close(start)
	a, b := <-results, <-results
	for range 2 {
		if err = <-errors; err != nil {
			t.Fatal(err)
		}
	}
	if a.ID != b.ID || a.ID == "" {
		t.Fatal("concurrent import created multiple tasks")
	}
	a.Stage, a.Usage.Invocations = core.Ready, 3
	a.Approval = &core.Approval{Preview: "owner decision"}
	if err = one.Commit(ctx, a.Revision, a, nil, false); err != nil {
		t.Fatal(err)
	}
	one.Close()
	two.Close()
	db, err := Open(ctx, dir, false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	want, err := db.Task(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	source.Revision, source.Body = "edited", "changed external request"
	got, err := db.Import(ctx, core.Task{ID: core.NewID(), Source: &source, Stage: core.Draft})
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("reimport reset retained authority: %+v %v", got, err)
	}
	if source.Revision != "edited" || source.Body != "changed external request" {
		t.Fatal("import mutated the caller's source snapshot")
	}
	tasks, err := db.Tasks(ctx)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("mapping not unique after restart: %d %v", len(tasks), err)
	}
}
