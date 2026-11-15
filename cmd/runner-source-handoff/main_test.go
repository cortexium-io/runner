package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sourceFixture(t *testing.T, history ...bool) (sourceSpec, string) {
	t.Helper()
	repo, destination, state, scratch := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	invoke := func(input []byte, args ...string) []byte {
		t.Helper()
		value, err := git(t.Context(), repo, nil, input, 1<<20, args...)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	invoke(nil, "init", "--quiet")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("original\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if len(history) > 0 && history[0] {
		if err := os.MkdirAll(filepath.Join(repo, "docs", "evidence"), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, "docs", "evidence", "historical.bin"), bytes.Repeat([]byte("x"), 2<<20), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, "docs", "evidence", "fixture.json"), []byte(`{"required_fixture":true}`), 0644); err != nil {
			t.Fatal(err)
		}
	}
	invoke(nil, "add", "--all")
	invoke(nil, "-c", "user.name=Fixture", "-c", "user.email=fixture@localhost", "commit", "--quiet", "-m", "base")
	base := strings.TrimSpace(string(invoke(nil, "rev-parse", "HEAD")))
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("retained candidate\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(repo, "frontend"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "frontend", "with spaces.ts"), []byte{0, 1, 2, 255}, 0755); err != nil {
		t.Fatal(err)
	}
	invoke(nil, "add", "--all")
	tree := strings.TrimSpace(string(invoke(nil, "write-tree")))
	patch := invoke(nil, "diff", "--cached", "--binary", "--full-index", base)
	patchPath, checkPath := filepath.Join(state, "changes.patch"), filepath.Join(state, "checks.json")
	check := []byte(`{"executed":393,"failed":0,"skipped":0}`)
	if err := os.WriteFile(patchPath, patch, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(checkPath, check, 0600); err != nil {
		t.Fatal(err)
	}
	return sourceSpec{
		Repository:  repo,
		Base:        base,
		Tree:        tree,
		Patch:       patchPath,
		PatchSHA256: digest(patch),
		Checks:      checkPath,
		CheckSHA256: digest(check),
		Destination: destination,
		State:       state,
	}, scratch
}

func TestMaterializeRetainedTreePreservesOriginAndLaterEdits(t *testing.T) {
	spec, scratch := sourceFixture(t)
	indexPath := filepath.Join(spec.Repository, ".git", "index")
	indexBefore, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	proof, err := materialize(t.Context(), spec, scratch, 1<<20)
	if err != nil || proof.Phase != "ready" || proof.Source.Tree != spec.Tree || proof.Commit == "" || proof.Files != 2 {
		t.Fatal("exact retained source did not materialize", proof, err)
	}
	indexAfter, err := os.ReadFile(indexPath)
	if err != nil || !bytes.Equal(indexBefore, indexAfter) {
		t.Fatal("materialization changed the old repository index", err)
	}
	if data, err := os.ReadFile(filepath.Join(spec.Destination, "README.md")); err != nil || string(data) != "retained candidate\n" {
		t.Fatal("retained source content changed", string(data), err)
	}
	if data, err := os.ReadFile(filepath.Join(spec.Destination, "frontend", "with spaces.ts")); err != nil || !bytes.Equal(data, []byte{0, 1, 2, 255}) {
		t.Fatal("binary or spaced path was not preserved", data, err)
	}
	// A later task owns these edits. Infrastructure replay must not reapply
	// the initial patch or remove unrelated work after handoff.
	if err := os.WriteFile(filepath.Join(spec.Destination, "README.md"), []byte("later agent edit"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(spec.Destination, "new.ts"), []byte("new work"), 0644); err != nil {
		t.Fatal(err)
	}
	again, err := materialize(t.Context(), spec, t.TempDir(), 1<<20)
	if err != nil || again.Commit != proof.Commit {
		t.Fatal("handoff replay changed authority", again, err)
	}
	if data, _ := os.ReadFile(filepath.Join(spec.Destination, "README.md")); string(data) != "later agent edit" {
		t.Fatal("replay overwrote later source edits")
	}
	if _, err := os.Stat(filepath.Join(spec.Destination, "new.ts")); err != nil {
		t.Fatal("replay removed later work", err)
	}
}

func TestMaterializeRejectsChangedAuthorityAndOverlappingWorkspaces(t *testing.T) {
	for _, kind := range []string{"patch digest", "check digest", "tree", "source alias", "state overlap", "unclaimed source"} {
		t.Run(kind, func(t *testing.T) {
			spec, scratch := sourceFixture(t)
			switch kind {
			case "patch digest":
				spec.PatchSHA256 = strings.Repeat("0", 64)
			case "check digest":
				spec.CheckSHA256 = strings.Repeat("0", 64)
			case "tree":
				spec.Tree = strings.Repeat("0", 40)
			case "source alias":
				alias := filepath.Join(t.TempDir(), "source-alias")
				if err := os.Symlink(spec.Repository, alias); err != nil {
					t.Fatal(err)
				}
				spec.Destination = alias
			case "state overlap":
				spec.Destination = spec.State
			case "unclaimed source":
				if err := os.WriteFile(filepath.Join(spec.Destination, "owner.txt"), []byte("preserve"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := materialize(t.Context(), spec, scratch, 1<<20); err == nil {
				t.Fatal("unsafe handoff was accepted")
			}
			if data, err := os.ReadFile(filepath.Join(spec.Repository, "README.md")); err != nil || string(data) != "retained candidate\n" {
				t.Fatal("failed handoff modified old source", err)
			}
		})
	}
}

func TestMaterializeRejoinsPartialImportWithoutResettingChangedFiles(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(map[bool]string{false: "checkpoint after commit", true: "edited partial source"}[changed], func(t *testing.T) {
			spec, scratch := sourceFixture(t)
			proof, err := materialize(t.Context(), spec, scratch, 1<<20)
			if err != nil {
				t.Fatal(err)
			}
			proof.Phase = "importing" // crash before the ready checkpoint
			if err := writeJSON(filepath.Join(spec.Destination, markerName), proof); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(filepath.Join(spec.Destination, "frontend", "with spaces.ts")); err != nil {
				t.Fatal(err)
			}
			if changed {
				if err := os.WriteFile(filepath.Join(spec.Destination, "README.md"), []byte("preserve unexpected edit"), 0644); err != nil {
					t.Fatal(err)
				}
			}
			resumed, err := materialize(t.Context(), spec, t.TempDir(), 1<<20)
			if changed {
				if err == nil {
					t.Fatal("partial import overwrote unexpected edits")
				}
				if data, _ := os.ReadFile(filepath.Join(spec.Destination, "README.md")); string(data) != "preserve unexpected edit" {
					t.Fatal("partial handoff destroyed source")
				}
			} else if err != nil || resumed.Commit != proof.Commit || resumed.Phase != "ready" {
				t.Fatal("partial import did not resume the existing committed snapshot", resumed, err)
			}
		})
	}
}

func TestSourceListingRejectsUnsafeOrOversizedEntries(t *testing.T) {
	if _, _, err := sourceFiles([]byte("100644 blob hash 4\tdocs/evidence/Fixture.json\x00"), 1024, sourceSelection{
		exclude: []string{"docs/evidence"},
		include: map[string]bool{"docs/evidence/Fixture.json": true},
	}); err != nil {
		t.Fatal("exact required fixture was rejected after host path folding", err)
	}
	for _, entry := range []string{
		"120000 blob hash 4\tlink\x00",
		"100644 blob hash 4\t../escape\x00",
		"100644 blob hash 4\t.git/config\x00",
		"100644 blob hash 4\t" + markerName + "\x00",
		"100644 blob hash 16777217\toversized\x00",
	} {
		if _, _, err := sourceFiles([]byte(entry), 32<<20); err == nil {
			t.Fatal("unsafe source inventory was accepted", entry)
		}
	}
	_, scratch := sourceFixture(t)
	if err := writeJSON(filepath.Join(scratch, "test.json"), map[string]string{"state": "ready"}); err != nil {
		t.Fatal(err)
	}
	data, err := regularInput(filepath.Join(scratch, "test.json"), 1024)
	if err != nil || !json.Valid(data) {
		t.Fatal("bounded receipt read failed", err)
	}
}

func TestMaterializeProjectionKeepsImportedFixturesAndAllRetainedChanges(t *testing.T) {
	spec, scratch := sourceFixture(t, true)
	spec.Exclude = "docs/evidence"
	spec.Include = "docs/evidence/fixture.json"
	proof, err := materialize(t.Context(), spec, scratch, 1<<20)
	if err != nil || proof.Source.Tree != spec.Tree || proof.MaterializedTree == spec.Tree || proof.Files != 3 || proof.Bytes >= 1<<20 {
		t.Fatal("projection did not preserve full authority and bound copied bytes", proof, err)
	}
	if _, err := os.Stat(filepath.Join(spec.Destination, "docs", "evidence", "historical.bin")); !os.IsNotExist(err) {
		t.Fatal("unrelated historical payload was copied", err)
	}
	if data, err := os.ReadFile(filepath.Join(spec.Destination, "docs", "evidence", "fixture.json")); err != nil || string(data) != `{"required_fixture":true}` {
		t.Fatal("required exact fixture was lost", string(data), err)
	}
	if data, err := os.ReadFile(filepath.Join(spec.Repository, "docs", "evidence", "historical.bin")); err != nil || len(data) != 2<<20 {
		t.Fatal("projection deleted the retained historical original", err)
	}
	other, scratch := sourceFixture(t)
	other.Exclude = "frontend" // The accepted patch added this source.
	retained, err := materialize(t.Context(), other, scratch, 1<<20)
	if err != nil || len(retained.RetainedChanges) != 1 || retained.RetainedChanges[0] != "frontend/with spaces.ts" {
		t.Fatal("projection did not derive its accepted-change exception", retained, err)
	}
	if data, err := os.ReadFile(filepath.Join(other.Destination, "frontend", "with spaces.ts")); err != nil || !bytes.Equal(data, []byte{0, 1, 2, 255}) {
		t.Fatal("projection lost accepted source changes", string(data), err)
	}
}

func TestChangedFileReceiptRemainsReplayableForLargeInventories(t *testing.T) {
	selection, err := projectionSelection("docs/evidence", "")
	if err != nil {
		t.Fatal(err)
	}
	var names bytes.Buffer
	for i := 0; i < 200; i++ {
		name := "docs/evidence/" + strings.Repeat("long-name-", 10) + string(rune('一'+i)) + ".md"
		names.WriteString(name)
		names.WriteByte(0)
	}
	var receipt sourceReceipt
	retainChangedFiles(names.Bytes(), &selection, &receipt)
	if receipt.RetainedCount != 200 || len(receipt.RetainedChanges) >= 200 || receipt.RetainedSHA256 != digest(names.Bytes()) {
		t.Fatal("full change inventory was not bound independently of its bounded name sample", receipt)
	}
	for _, name := range bytes.Split(names.Bytes(), []byte{0}) {
		if len(name) != 0 && !selection.keeps(string(name)) {
			t.Fatal("an accepted change was lost outside the receipt sample")
		}
	}
	filename := filepath.Join(t.TempDir(), "handoff.json")
	if err := writeJSON(filename, receipt); err != nil {
		t.Fatal(err)
	}
	data, err := regularInput(filename, receiptLimit)
	if err != nil || json.Unmarshal(data, &receipt) != nil || receipt.RetainedCount != 200 {
		t.Fatal("bounded receipt cannot be reopened", err)
	}
	if _, err := boundedJSON(strings.Repeat("x", receiptLimit)); err == nil {
		t.Fatal("writer accepted metadata its replay reader cannot consume")
	}
}
