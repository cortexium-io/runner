package workspace

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReviewEvidenceCopiesOnlySelectedBytesAndSealsThem(t *testing.T) {
	source := t.TempDir()
	reports := filepath.Join(source, "test-results")
	if err := os.Mkdir(reports, 0o700); err != nil {
		t.Fatal(err)
	}
	content := []byte(`{"complete":true,"candidate":"historical"}`)
	if err := os.WriteFile(filepath.Join(reports, "receipt.json"), content, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, ".env"), []byte("not evidence"), 0o600); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "evidence")
	candidate := Candidate{CommitOID: strings.Repeat("a", 40), TreeOID: strings.Repeat("b", 40)}
	evidence, err := copyReviewEvidence(t.Context(), source, destination, candidate, []string{"test-results", "missing/report.json"}, DefaultSnapshotLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer evidence.root.Close()
	data, err := os.ReadFile(filepath.Join(destination, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest ReviewEvidenceManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.CandidateCommit != candidate.CommitOID || manifest.CandidateTree != candidate.TreeOID || manifest.SourceRoot != source || len(manifest.Files) != 1 || manifest.Files[0].Path != "test-results/receipt.json" || len(manifest.MissingPaths) != 1 || manifest.MissingPaths[0] != "missing/report.json" {
		t.Fatalf("wrong candidate or selection binding: %#v", manifest)
	}
	if _, err := os.Stat(filepath.Join(destination, "files", ".env")); !os.IsNotExist(err) {
		t.Fatalf("unselected private file exposed: %v", err)
	}
	copyPath := filepath.Join(destination, "files", "test-results", "receipt.json")
	if err := os.WriteFile(filepath.Join(reports, "receipt.json"), []byte("later producer change"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(copyPath)
	if err != nil || string(got) != string(content) {
		t.Fatalf("snapshot is not independent: %q, %v", got, err)
	}
	if err := evidence.verify(t.Context()); err != nil {
		t.Fatal(err)
	}
	// Even byte-identical replacement cannot reuse the review's evidence seal.
	// Keep the original inode allocated: Linux can otherwise reuse it during
	// immediate unlink/recreate, making this a filesystem-timing fixture.
	original, err := os.Open(copyPath)
	if err != nil {
		t.Fatal(err)
	}
	defer original.Close()
	if err := os.Remove(copyPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(copyPath, content, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := evidence.verify(t.Context()); err == nil {
		t.Fatal("replacement evidence retained its authority")
	}
}

func TestReviewEvidenceRejectsLinksAdministrationAndLimits(t *testing.T) {
	for _, test := range []struct {
		name   string
		setup  func(*testing.T, string)
		limits SnapshotLimits
	}{
		{"external file symlink", func(t *testing.T, root string) {
			if err := os.Symlink(filepath.Join(t.TempDir(), "secret"), filepath.Join(root, "report")); err != nil {
				t.Fatal(err)
			}
		}, DefaultSnapshotLimits()},
		{"directory symlink", func(t *testing.T, root string) {
			if err := os.Symlink(t.TempDir(), filepath.Join(root, "nested")); err != nil {
				t.Fatal(err)
			}
		}, DefaultSnapshotLimits()},
		{"hard link", func(t *testing.T, root string) {
			if err := os.Link(filepath.Join(root, "receipt"), filepath.Join(root, "alias")); err != nil {
				t.Fatal(err)
			}
		}, DefaultSnapshotLimits()},
		{"Git administration", func(t *testing.T, root string) {
			if err := os.Mkdir(filepath.Join(root, ".git"), 0o700); err != nil {
				t.Fatal(err)
			}
		}, DefaultSnapshotLimits()},
		{"file limit", func(t *testing.T, root string) {}, SnapshotLimits{MaxEntries: 20, MaxFileBytes: 1, MaxTotalBytes: 100}},
		{"total limit", func(t *testing.T, root string) {
			if err := os.WriteFile(filepath.Join(root, "other"), []byte("123456"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, SnapshotLimits{MaxEntries: 20, MaxFileBytes: 8, MaxTotalBytes: 10}},
		{"entry limit", func(t *testing.T, root string) {
			if err := os.WriteFile(filepath.Join(root, "other"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}, SnapshotLimits{MaxEntries: 2, MaxFileBytes: 1024, MaxTotalBytes: 1024}},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := t.TempDir()
			reports := filepath.Join(source, "reports")
			if err := os.Mkdir(reports, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(reports, "receipt"), []byte("123456"), 0o600); err != nil {
				t.Fatal(err)
			}
			test.setup(t, reports)
			if evidence, err := copyReviewEvidence(t.Context(), source, filepath.Join(t.TempDir(), "evidence"), Candidate{}, []string{"reports"}, test.limits); err == nil {
				evidence.root.Close()
				t.Fatal("unsafe or oversized evidence accepted")
			}
		})
	}
}
