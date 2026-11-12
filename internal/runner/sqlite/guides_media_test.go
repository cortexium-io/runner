package sqlite

import (
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/guidesmedia"
	"github.com/cortexium-io/runner/internal/runner"
)

func TestGuidesAuthorityPersistsWithoutRewritingLegacyAndRetainsWholeSet(t *testing.T) {
	legacy := normalizedRun(2)
	s := openNormalized(t, legacy)
	var original []byte
	if err := s.db.QueryRow("SELECT data FROM runs WHERE id=?", legacy.ID).Scan(&original); err != nil {
		t.Fatal(err)
	}
	x, _ := guidesmedia.NewIndex("run_import", "attempt_import", strings.Repeat("a", 64))
	data, _ := x.Encode()
	b, _ := x.Bind(data)
	imported := runner.Run{
		ID:           x.RunID,
		ProjectID:    "jacks-break",
		RecordFormat: 6,
		Status:       "Completed",
		Process:      runner.Process{ID: "stage-guides-media"},
	}
	artifacts := []runner.Artifact{{ID: b.IndexID, RunID: imported.ID, ProjectID: imported.ProjectID, Bytes: int64(len(data))}}
	for _, a := range x.Assets {
		artifacts = append(artifacts, runner.Artifact{ID: a.ArtifactID, RunID: imported.ID, ProjectID: imported.ProjectID, Bytes: a.Bytes})
	}
	if err := s.Save(t.Context(), imported, artifacts, nil); err != nil {
		t.Fatal(err)
	}
	root := runner.Run{
		ID:           "run_media",
		ProjectID:    "jacks-break",
		RecordFormat: 6,
		Status:       "Queued",
		Repository:   "/owned/source",
		Source:       &runner.SourceSettings{Ref: strings.Repeat("1", 40)},
		GuidesMedia:  &b,
		Process:      runner.Process{ID: "publish-guides-media", GuidesMedia: guidesmedia.AssetFormat},
		Inputs:       map[string]runner.Value{"media_index": {ArtifactID: b.IndexID}},
	}
	if err := s.Save(t.Context(), root, nil, nil); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM artifact_uses WHERE run_id=?", root.ID).Scan(&count); err != nil || count != 37 {
		t.Fatal("whole-set references not retained", count, err)
	}
	for _, change := range []func(*runner.Run){func(r *runner.Run) { r.GuidesMedia.HelperSHA256 = strings.Repeat("b", 64) }, func(r *runner.Run) { r.Source.Ref = strings.Repeat("2", 40) }, func(r *runner.Run) { r.Repository = "/foreign" }, func(r *runner.Run) {
		r.RecordFormat = 3
		r.GuidesMedia = nil
		r.Process.GuidesMedia = ""
		r.Process.ID = "old"
	}, func(r *runner.Run) { r.Inputs["media_index"] = runner.Value{ArtifactID: "foreign/index"} }} {
		changed := runner.Clone(root)
		change(&changed)
		if err := s.Save(t.Context(), changed, nil, nil); err == nil {
			t.Fatal("persistent authority drift accepted")
		}
	}
	var retained []byte
	if err := s.db.QueryRow("SELECT data FROM runs WHERE id=?", legacy.ID).Scan(&retained); err != nil || string(retained) != string(original) {
		t.Fatal("legacy operational bytes rewritten", err)
	}
	loaded, err := s.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range loaded {
		if r.ID == root.ID {
			found = true
			if !reflect.DeepEqual(r.GuidesMedia, root.GuidesMedia) || r.Source.Ref != root.Source.Ref {
				t.Fatal("authority lost on rejoin")
			}
		}
	}
	if !found {
		t.Fatal("new run lost")
	}
}

// An old qualified binary must refuse the new record, using only a disposable
// operational fixture. This never opens or rewinds the installed database.
func TestGuidesOldQualifiedReaderRefusesNewFormat(t *testing.T) {
	binary := os.Getenv("RUNNER_GUIDES_OLD_READER")
	if binary == "" {
		t.Skip("explicit qualified old binary compatibility proof")
	}
	raw, err := os.ReadFile(binary)
	if err != nil || guidesmedia.Digest(raw) != "f6c9c314840308062a921f0de6165b2cafa16488c1dbaf2e17a603bf38741be8" {
		t.Fatal("old reader identity differs", err)
	}
	root := t.TempDir()
	s, err := Open(filepath.Join(root, "state"), normalizedPolicy())
	if err != nil {
		t.Fatal(err)
	}
	record := runner.Run{
		ID:           "run_media_compat",
		ProjectID:    "jacks-break",
		RecordFormat: 6,
		Status:       "Completed",
		Process:      runner.Process{ID: "stage-guides-media"},
	}
	if err = s.Save(t.Context(), record, nil, nil); err != nil {
		s.Close()
		t.Fatal(err)
	}
	s.Close()
	fixture := filepath.Join("..", "..", "..", "testdata", "config", "demo")
	configRoot := filepath.Join(root, "config")
	err = filepath.WalkDir(fixture, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(fixture, path)
		if err != nil {
			return err
		}
		target := filepath.Join(configRoot, rel)
		if entry.IsDir() {
			return os.MkdirAll(target, 0700)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0600)
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithTimeout(t.Context(), 3*time.Second)
	defer stop()
	cmd := exec.CommandContext(ctx, binary, "serve", "--data", filepath.Join(root, "state"), "--config", filepath.Join(configRoot, "instance.yaml"), "--listen", "127.0.0.1:0")
	output, err := cmd.CombinedOutput()
	if err == nil || ctx.Err() != nil || !strings.Contains(string(output), "unsupported run record format") {
		t.Fatal("old reader did not promptly refuse record6", string(output), err)
	}
	reopened, err := Open(filepath.Join(root, "state"), normalizedPolicy())
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	loaded, err := reopened.Load(t.Context())
	if err != nil || len(loaded) != 1 || loaded[0].RecordFormat != 6 || loaded[0].ID != record.ID {
		t.Fatal("old refusal changed new history", loaded, err)
	}
	t.Log("qualified e7fc030 old Core refused format6 before service/dispatch; current Store reloaded unchanged record; no operational rewind")
}
