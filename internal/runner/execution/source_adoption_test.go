package execution

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/runner"
)

func TestSourceAdoptionProtectsOwnedSeedAndCandidateBeforeExecution(t *testing.T) {
	f := newSourceReleaseFixture(t, true)
	w := f.r.workspaces
	source, err := w.prepareAdoptedSource(t.Context(), f.a, f.head, f.tree, captureDigest(f.patch))
	if err != nil {
		t.Fatal(err)
	}
	again, err := w.prepareAdoptedSource(t.Context(), f.a, f.head, f.tree, captureDigest(f.patch))
	if err != nil || *again != *source {
		t.Fatal("adoption replay changed source", err)
	}
	data, err := w.disk.Read("artifacts/"+source.PatchID, f.a.Limits.MaxArtifact)
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, []byte("\n# tamper\n")...)
	if err = w.disk.Write("artifacts/"+source.PatchID, data, f.a.Limits.MaxArtifact); err != nil {
		t.Fatal(err)
	}
	if _, err = w.prepareAdoptedSource(t.Context(), f.a, f.head, f.tree, captureDigest(f.patch)); err == nil || !strings.Contains(err.Error(), "authenticated intent") {
		t.Fatal("changed helper seed accepted", err)
	}
	next := runner.Clone(f.a)
	next.AttemptID = "owner_after_adoption"
	next.Setup.Profile = runner.Profile{Harness: "codex", Access: "workspace-write"}
	next.Inputs = map[string]runner.Value{"patch": {ArtifactID: source.PatchID}}
	if _, err = w.Prepare(t.Context(), next); err == nil {
		t.Fatal("tampered candidate reached execution")
	}
}

func TestSourceAdoptionCannotReplaceDirtyPristineBaseline(t *testing.T) {
	f := newSourceReleaseFixture(t, true)
	w := f.r.workspaces
	r, err := w.load(f.a.RunID)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(r.WorkDir, "unretained.txt")
	if err = os.WriteFile(path, []byte("preserve original edits"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = w.prepareAdoptedSource(context.Background(), f.a, f.head, f.tree, captureDigest(f.patch)); err == nil {
		t.Fatal("uncaptured source replaced")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "preserve original edits" {
		t.Fatal("uncaptured source lost", err)
	}
	r, err = w.load(f.a.RunID)
	if err != nil || r.AdoptedSource != nil || r.SeedPatchID != "" {
		t.Fatal("invalid source intent retained", err)
	}
}
