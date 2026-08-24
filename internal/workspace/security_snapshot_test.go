package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/subprocess"
)

func TestCaptureSnapshotIncludesIgnoredAndConcealedWorktreeControlFiles(t *testing.T) {
	repo := initGitRepo(t)
	writeSnapshotTestFile(t, filepath.Join(repo, ".gitignore"), "concealed/\n.gitmodules\n")
	writeSnapshotTestFile(t, filepath.Join(repo, ".gitattributes"), "*.txt text\n")
	writeSnapshotTestFile(t, filepath.Join(repo, ".gitmodules"), "# concealed root control\n")
	writeSnapshotTestFile(t, filepath.Join(repo, "concealed", "deep", ".gitignore"), "*.key\n")
	writeSnapshotTestFile(t, filepath.Join(repo, "concealed", "deep", ".gitattributes"), "*.bin binary\n")
	writeSnapshotTestFile(t, filepath.Join(repo, "concealed", "ordinary.txt"), "ignored payload\n")

	before, err := captureDefaultSnapshotState(t.Context(), subprocess.OSRunner{}, repo, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{".gitignore", ".gitattributes", ".gitmodules", "concealed/deep/.gitignore", "concealed/deep/.gitattributes"} {
		if _, found := before.worktree[path]; !found {
			t.Fatalf("protected control file %q was not captured: %#v", path, before.worktree)
		}
	}

	writeSnapshotTestFile(t, filepath.Join(repo, "concealed", "deep", ".gitattributes"), "*.bin -diff\n")
	after, err := captureDefaultSnapshotState(t.Context(), subprocess.OSRunner{}, repo, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if before.Fingerprint == after.Fingerprint || !containsString(before.ChangedPaths(after), "concealed/deep/.gitattributes") {
		t.Fatalf("ignored protected control drift was not identified: paths=%#v", before.ChangedPaths(after))
	}
}

func TestCaptureSnapshotRejectsConcealedControlFileCreatedAfterDiscovery(t *testing.T) {
	repo := initGitRepo(t)
	deep := filepath.Join(repo, "concealed", "deep")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	baseline, err := captureDefaultSnapshotState(t.Context(), subprocess.OSRunner{}, repo, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	runner := &snapshotMutationRunner{
		Runner: subprocess.OSRunner{},
		match:  "ls-files --modified --deleted --others",
		mutate: func() {
			writeSnapshotTestFile(t, filepath.Join(deep, ".gitattributes"), "*.bin binary\n")
		},
	}
	after, err := captureDefaultSnapshotState(t.Context(), runner, repo, 30*time.Second)
	if err == nil || after.Fingerprint != "" || !strings.Contains(err.Error(), "verify protected worktree control-file discovery") {
		t.Fatalf("concealed control file created after discovery was certified: baseline=%q snapshot=%#v error=%v", baseline.Fingerprint, after, err)
	}
}

func TestCaptureSnapshotRejectsProtectedControlContentChangedAfterEnumeration(t *testing.T) {
	repo := initGitRepo(t)
	path := filepath.Join(repo, ".gitignore")
	writeSnapshotTestFile(t, path, "before/\n")
	runner := &snapshotMutationRunner{
		Runner: subprocess.OSRunner{},
		match:  "ls-files --modified --deleted --others",
		mutate: func() {
			writeSnapshotTestFile(t, path, "changed/\n")
		},
	}
	if snapshot, err := captureDefaultSnapshotState(t.Context(), runner, repo, 30*time.Second); err == nil || snapshot.Fingerprint != "" || !strings.Contains(err.Error(), "verify protected worktree control-file discovery") {
		t.Fatalf("protected control content changed after enumeration was certified: snapshot=%#v error=%v", snapshot, err)
	}
}

func TestCaptureSnapshotRejectsIndexCreatedAfterMissingIndexWasPinned(t *testing.T) {
	repo := initEmptyGitRepo(t)
	runner := &snapshotMutationRunner{
		Runner: subprocess.OSRunner{},
		match:  "ls-files --modified --deleted --others",
		mutate: func() {
			runGitTest(t, repo, "read-tree", "--empty")
		},
	}
	if snapshot, err := captureDefaultSnapshotState(t.Context(), runner, repo, 30*time.Second); err == nil || snapshot.Fingerprint != "" || !strings.Contains(err.Error(), "verify missing worktree Git index") {
		t.Fatalf("Git index created during snapshot was certified: snapshot=%#v error=%v", snapshot, err)
	}
}

func TestCaptureSnapshotIncludesProtectedGitMetadata(t *testing.T) {
	repo := initGitRepo(t)
	gitDirectory := filepath.Join(repo, ".git")
	other := initGitRepo(t)
	replacements := map[string]func(){
		"default hook": func() {
			writeSnapshotTestFile(t, filepath.Join(gitDirectory, "hooks", "pre-push"), "#!/bin/sh\nexit 0\n")
		},
		"repository exclude": func() {
			writeSnapshotTestFile(t, filepath.Join(gitDirectory, "info", "exclude"), "*.private\n")
		},
		"repository attributes": func() {
			writeSnapshotTestFile(t, filepath.Join(gitDirectory, "info", "attributes"), "*.txt -diff\n")
		},
		"repository grafts": func() {
			writeSnapshotTestFile(t, filepath.Join(gitDirectory, "info", "grafts"), "# no active grafts\n")
		},
		"sparse checkout": func() {
			writeSnapshotTestFile(t, filepath.Join(gitDirectory, "info", "sparse-checkout"), "/*\n")
		},
		"object alternates": func() {
			writeSnapshotTestFile(t, filepath.Join(gitDirectory, "objects", "info", "alternates"), filepath.Join(other, ".git", "objects")+"\n")
		},
	}
	for name, mutate := range replacements {
		t.Run(name, func(t *testing.T) {
			before, err := captureDefaultSnapshotState(t.Context(), subprocess.OSRunner{}, repo, 30*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			mutate()
			after, err := captureDefaultSnapshotState(t.Context(), subprocess.OSRunner{}, repo, 30*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			if before.Fingerprint == after.Fingerprint || !containsString(before.ChangedControlState(after), "protected Git metadata") {
				t.Fatalf("%s drift did not change protected Git metadata: %#v", name, before.ChangedControlState(after))
			}
		})
	}
}

func TestCaptureSnapshotIncludesReplacementReferences(t *testing.T) {
	repo := initGitRepo(t)
	writeSnapshotTestFile(t, filepath.Join(repo, "second.txt"), "second\n")
	runGitTest(t, repo, "add", "second.txt")
	runGitTest(t, repo, "commit", "-m", "second commit")
	before, err := captureDefaultSnapshotState(t.Context(), subprocess.OSRunner{}, repo, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repo, "replace", "HEAD", "HEAD^")
	after, err := captureDefaultSnapshotState(t.Context(), subprocess.OSRunner{}, repo, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if before.Fingerprint == after.Fingerprint || !containsString(before.ChangedControlState(after), "replacement references") {
		t.Fatalf("replacement reference drift did not change replacement-reference state: %#v", before.ChangedControlState(after))
	}
}

func TestCaptureSnapshotRejectsReplacementReferenceCreatedAfterEnumeration(t *testing.T) {
	repo := initGitRepo(t)
	writeSnapshotTestFile(t, filepath.Join(repo, "second.txt"), "second\n")
	runGitTest(t, repo, "add", "second.txt")
	runGitTest(t, repo, "commit", "-m", "second commit")
	if err := os.MkdirAll(filepath.Join(repo, ".git", "refs", "replace"), 0o755); err != nil {
		t.Fatal(err)
	}
	runner := &snapshotMutationRunner{
		Runner: subprocess.OSRunner{},
		match:  "ls-files --modified --deleted --others",
		mutate: func() {
			runGitTest(t, repo, "replace", "HEAD", "HEAD^")
		},
	}
	if snapshot, err := captureDefaultSnapshotState(t.Context(), runner, repo, 30*time.Second); err == nil || snapshot.Fingerprint != "" || !strings.Contains(err.Error(), "protected common Git metadata") {
		t.Fatalf("replacement ref created after enumeration was certified: snapshot=%#v error=%v", snapshot, err)
	}
}

func TestCaptureSnapshotRejectsPackedReplacementReferenceMutationAfterEnumeration(t *testing.T) {
	repo := initGitRepo(t)
	writeSnapshotTestFile(t, filepath.Join(repo, "second.txt"), "second\n")
	runGitTest(t, repo, "add", "second.txt")
	runGitTest(t, repo, "commit", "-m", "second commit")
	current := strings.TrimSpace(runGitTest(t, repo, "rev-parse", "HEAD"))
	previous := strings.TrimSpace(runGitTest(t, repo, "rev-parse", "HEAD^"))
	branch := strings.TrimSpace(runGitTest(t, repo, "symbolic-ref", "--short", "HEAD"))
	runGitTest(t, repo, "replace", current, previous)
	runGitTest(t, repo, "pack-refs", "--all")
	looseHead := filepath.Join(repo, ".git", "refs", "heads", filepath.FromSlash(branch))
	if err := os.MkdirAll(filepath.Dir(looseHead), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(looseHead, []byte(current+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	packedPath := filepath.Join(repo, ".git", "packed-refs")
	packed, err := os.ReadFile(packedPath)
	if err != nil {
		t.Fatal(err)
	}
	oldRecord := previous + " refs/replace/" + current + "\n"
	newRecord := current + " refs/replace/" + current + "\n"
	if !strings.Contains(string(packed), oldRecord) {
		t.Fatalf("packed references have no replacement record %q", oldRecord)
	}
	runner := &snapshotMutationRunner{
		Runner: subprocess.OSRunner{},
		match:  "ls-files --modified --deleted --others",
		mutate: func() {
			changed := strings.Replace(string(packed), oldRecord, newRecord, 1)
			if err := os.WriteFile(packedPath, []byte(changed), 0o600); err != nil {
				t.Errorf("mutate packed replacement reference: %v", err)
			}
		},
	}
	if snapshot, err := captureDefaultSnapshotState(t.Context(), runner, repo, 30*time.Second); err == nil || snapshot.Fingerprint != "" || !strings.Contains(err.Error(), "packed replacement references") {
		t.Fatalf("packed replacement ref changed after enumeration was certified: snapshot=%#v error=%v", snapshot, err)
	}
}

func TestCaptureSnapshotAllowsUnrelatedPackedReferenceMutation(t *testing.T) {
	repo := initGitRepo(t)
	writeSnapshotTestFile(t, filepath.Join(repo, "second.txt"), "second\n")
	runGitTest(t, repo, "add", "second.txt")
	runGitTest(t, repo, "commit", "-m", "second commit")
	current := strings.TrimSpace(runGitTest(t, repo, "rev-parse", "HEAD"))
	previous := strings.TrimSpace(runGitTest(t, repo, "rev-parse", "HEAD^"))
	branch := strings.TrimSpace(runGitTest(t, repo, "symbolic-ref", "--short", "HEAD"))
	runGitTest(t, repo, "replace", current, previous)
	runGitTest(t, repo, "tag", "snapshot-unrelated", previous)
	runGitTest(t, repo, "pack-refs", "--all")
	looseHead := filepath.Join(repo, ".git", "refs", "heads", filepath.FromSlash(branch))
	if err := os.MkdirAll(filepath.Dir(looseHead), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(looseHead, []byte(current+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	packedPath := filepath.Join(repo, ".git", "packed-refs")
	packed, err := os.ReadFile(packedPath)
	if err != nil {
		t.Fatal(err)
	}
	oldRecord := previous + " refs/tags/snapshot-unrelated\n"
	newRecord := current + " refs/tags/snapshot-unrelated\n"
	if !strings.Contains(string(packed), oldRecord) {
		t.Fatalf("packed references have no unrelated tag record %q", oldRecord)
	}
	runner := &snapshotMutationRunner{
		Runner: subprocess.OSRunner{},
		match:  "ls-files --modified --deleted --others",
		mutate: func() {
			changed := strings.Replace(string(packed), oldRecord, newRecord, 1)
			if err := os.WriteFile(packedPath, []byte(changed), 0o600); err != nil {
				t.Errorf("mutate unrelated packed reference: %v", err)
			}
		},
	}
	if snapshot, err := captureDefaultSnapshotState(t.Context(), runner, repo, 30*time.Second); err != nil || snapshot.Fingerprint == "" {
		t.Fatalf("unrelated packed ref invalidated replacement state: snapshot=%#v error=%v", snapshot, err)
	}
}

func TestCaptureSnapshotRejectsProtectedGitMetadataReplacementRace(t *testing.T) {
	repo := initGitRepo(t)
	hook := filepath.Join(repo, ".git", "hooks", "pre-commit")
	writeSnapshotTestFile(t, hook, "#!/bin/sh\nexit 0\n")
	runner := &snapshotMutationRunner{
		Runner: subprocess.OSRunner{},
		match:  "ls-files --modified --deleted --others",
		mutate: func() {
			if err := os.Rename(hook, hook+".replaced"); err != nil {
				t.Errorf("move protected hook: %v", err)
				return
			}
			if err := os.WriteFile(hook, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
				t.Errorf("replace protected hook: %v", err)
			}
		},
	}
	if snapshot, err := captureDefaultSnapshotState(t.Context(), runner, repo, 30*time.Second); err == nil || snapshot.Fingerprint != "" || !strings.Contains(err.Error(), "protected common Git metadata") {
		t.Fatalf("protected hook replacement was certified: snapshot=%#v error=%v", snapshot, err)
	}
}

func TestCaptureSnapshotIncludesInitializedUninitializedAndNestedSubmodules(t *testing.T) {
	leaf := initGitRepo(t)
	child := initGitRepo(t)
	runGitTest(t, child, "-c", "protocol.file.allow=always", "submodule", "add", leaf, "nested/leaf")
	runGitTest(t, child, "commit", "-am", "add nested submodule")
	parent := initGitRepo(t)
	runGitTest(t, parent, "-c", "protocol.file.allow=always", "submodule", "add", child, "modules/child")
	runGitTest(t, parent, "commit", "-am", "add child submodule")
	runGitTest(t, parent, "-c", "protocol.file.allow=always", "submodule", "update", "--init", "--recursive")

	initialized, err := captureDefaultSnapshotState(t.Context(), subprocess.OSRunner{}, parent, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := captureDefaultSnapshotState(t.Context(), subprocess.OSRunner{}, parent, 30*time.Second)
	if err != nil || initialized.Fingerprint != repeated.Fingerprint {
		t.Fatalf("unchanged initialized nested submodule snapshot was unstable: first=%q second=%q error=%v", initialized.Fingerprint, repeated.Fingerprint, err)
	}
	if _, found := initialized.worktree["modules/child"]; !found {
		t.Fatalf("indexed submodule path was not represented: %#v", initialized.worktree)
	}

	nestedFile := filepath.Join(parent, "modules", "child", "nested", "leaf", "README.md")
	writeSnapshotTestFile(t, nestedFile, "nested dirty state\n")
	dirty, err := captureDefaultSnapshotState(t.Context(), subprocess.OSRunner{}, parent, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if initialized.Fingerprint == dirty.Fingerprint || !containsString(initialized.ChangedPaths(dirty), "modules/child") {
		t.Fatalf("nested submodule dirtiness did not change its parent path: %#v", initialized.ChangedPaths(dirty))
	}
	runGitTest(t, filepath.Join(parent, "modules", "child", "nested", "leaf"), "checkout", "--", "README.md")

	childCheckout := filepath.Join(parent, "modules", "child")
	runGitTest(t, childCheckout, "config", "user.name", "Runner Test")
	runGitTest(t, childCheckout, "config", "user.email", "runner@example.invalid")
	writeSnapshotTestFile(t, filepath.Join(childCheckout, "head-move.txt"), "move head\n")
	runGitTest(t, childCheckout, "add", "head-move.txt")
	runGitTest(t, childCheckout, "commit", "-m", "move submodule head")
	headMoved, err := captureDefaultSnapshotState(t.Context(), subprocess.OSRunner{}, parent, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if initialized.Fingerprint == headMoved.Fingerprint {
		t.Fatal("initialized submodule HEAD moved without changing the parent snapshot")
	}

	runGitTest(t, parent, "submodule", "deinit", "-f", "modules/child")
	uninitialized, err := captureDefaultSnapshotState(t.Context(), subprocess.OSRunner{}, parent, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	repeatedUninitialized, err := captureDefaultSnapshotState(t.Context(), subprocess.OSRunner{}, parent, 30*time.Second)
	if err != nil || uninitialized.Fingerprint != repeatedUninitialized.Fingerprint {
		t.Fatalf("unchanged uninitialized submodule snapshot was unstable: first=%q second=%q error=%v", uninitialized.Fingerprint, repeatedUninitialized.Fingerprint, err)
	}
	if initialized.Fingerprint == uninitialized.Fingerprint {
		t.Fatal("initialized and uninitialized submodule states shared a fingerprint")
	}
	writeSnapshotTestFile(t, filepath.Join(parent, "modules", "child", "concealed", "secret.txt"), "hidden under deinitialized gitlink\n")
	if snapshot, err := captureDefaultSnapshotState(t.Context(), subprocess.OSRunner{}, parent, 30*time.Second); err == nil || snapshot.Fingerprint != "" || !strings.Contains(err.Error(), "uninitialized indexed submodule") {
		t.Fatalf("hidden content under an uninitialized submodule was certified: snapshot=%#v error=%v", snapshot, err)
	}
}

func TestCaptureSnapshotRejectsMissingOrSymlinkedIndexedSubmodule(t *testing.T) {
	source := initGitRepo(t)
	for _, test := range []struct {
		name   string
		mutate func(t *testing.T, path string)
	}{
		{name: "missing", mutate: func(t *testing.T, path string) {
			if err := os.RemoveAll(path); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "symlink", mutate: func(t *testing.T, path string) {
			if err := os.RemoveAll(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(source, path); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			parent := initGitRepo(t)
			runGitTest(t, parent, "-c", "protocol.file.allow=always", "submodule", "add", source, "module")
			runGitTest(t, parent, "commit", "-am", "add submodule")
			test.mutate(t, filepath.Join(parent, "module"))
			if snapshot, err := captureDefaultSnapshotState(t.Context(), subprocess.OSRunner{}, parent, 30*time.Second); err == nil || snapshot.Fingerprint != "" {
				t.Fatalf("%s indexed submodule was certified: snapshot=%#v error=%v", test.name, snapshot, err)
			}
		})
	}
}

func TestCaptureSnapshotRejectsIndexedSubmoduleReplacementRace(t *testing.T) {
	source := initGitRepo(t)
	parent := initGitRepo(t)
	runGitTest(t, parent, "-c", "protocol.file.allow=always", "submodule", "add", source, "module")
	runGitTest(t, parent, "commit", "-am", "add submodule")
	module := filepath.Join(parent, "module")
	runner := &snapshotMutationRunner{
		Runner: subprocess.OSRunner{},
		match:  "status --porcelain",
		mutate: func() {
			if err := os.Rename(module, module+".replaced"); err != nil {
				t.Errorf("move indexed submodule: %v", err)
				return
			}
			if err := os.Mkdir(module, 0o755); err != nil {
				t.Errorf("replace indexed submodule directory: %v", err)
			}
		},
	}
	if snapshot, err := captureDefaultSnapshotState(t.Context(), runner, parent, 30*time.Second); err == nil || snapshot.Fingerprint != "" {
		t.Fatalf("indexed submodule replacement race was certified: snapshot=%#v error=%v", snapshot, err)
	}
}

func writeSnapshotTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
