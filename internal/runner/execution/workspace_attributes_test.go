package execution

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/runner"
)

const builtinAttributeFixture = "# Preserve built-in text normalization\n* text=auto\ndocs/compatibility/*.patch -whitespace\n"

func attributeRepository(t *testing.T, format string) string {
	t.Helper()
	repo := t.TempDir()
	if format == "sha256" {
		fixtureGit(t, repo, "init", "--quiet", "--template=", "--object-format=sha256")
	}
	sourceRepositoryAt(t, repo)
	for name, data := range map[string][]byte{
		".gitattributes": []byte(builtinAttributeFixture),
		"text.txt":       []byte("one\ntwo\n"),
		"large.bin":      bytes.Repeat([]byte{0, '\r', '\n', 0xff}, 20<<10),
	} {
		if err := os.WriteFile(filepath.Join(repo, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	fixtureGit(t, repo, "add", "--all")
	fixtureGit(t, repo, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgSign=false", "commit", "--quiet", "-m", "built-in attribute fixture")
	return repo
}

func TestRootAttributesSupportOnlyBoundedBuiltInRules(t *testing.T) {
	for _, valid := range []string{"", builtinAttributeFixture, "  # preserved comment\r\n*.txt text=auto -whitespace\r\n", "/docs/[ab]*.patch -whitespace\n"} {
		if err := validateRootAttributes([]byte(valid)); err != nil {
			t.Fatal("ordinary built-in rules rejected", valid, err)
		}
	}
	for _, invalid := range []string{
		"* filter=custom\n", "* diff=custom\n", "* merge=custom\n", "* working-tree-encoding=UTF-16\n", "* ident\n", "* eol=crlf\n", "* export-subst\n", "* text\n", "* -text\n",
		"[attr]custom text=auto\n", "\"quoted path\" text=auto\n", "!negative text=auto\n", "* text=auto\x00", "* text=auto\v", strings.Repeat("#", maxRootAttributes+1),
	} {
		if err := validateRootAttributes([]byte(invalid)); err == nil {
			t.Fatal("unsupported attribute semantics admitted", invalid[:min(len(invalid), 100)])
		}
	}
}

func TestBuiltinAttributesRetainLiteralCaptureAndAuthenticateSeedBytes(t *testing.T) {
	for _, format := range []string{"sha1", "sha256"} {
		t.Run(format, func(t *testing.T) {
			repo, disk := attributeRepository(t, format), workspaceDisk(t)
			// Host defaults must not rewrite the source materialized by Runner.
			fixtureGit(t, repo, "config", "core.autocrlf", "true")
			fixtureGit(t, repo, "config", "core.eol", "crlf")
			w := &Workspaces{disk: disk}
			a := sourceAssignment(disk, repo, "attempt_attributes")
			a.Setup.Profile = runner.Profile{Harness: "codex", Access: "workspace-write"}
			r, err := w.Prepare(t.Context(), a)
			if err != nil {
				t.Fatal("built-in source attributes failed admission", err)
			}
			if data, err := os.ReadFile(filepath.Join(r.WorkDir, "text.txt")); err != nil || !bytes.Equal(data, []byte("one\ntwo\n")) {
				t.Fatal("host checkout conversion changed pinned source", data, err)
			}
			literal := []byte("one\r\ntwo\r\n")
			if err := os.WriteFile(filepath.Join(r.WorkDir, "text.txt"), literal, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := git(t.Context(), r.WorkDir, nil, nil, 4096, "diff", "--quiet", r.Base, "--", "text.txt"); err != nil {
				t.Fatal("fixture must expose normalized Git diff equivalence", err)
			}
			if _, err := w.validateSeed(t.Context(), r, nil, a.Limits.MaxArtifact, true); err == nil {
				t.Fatal("normalized equivalence authenticated different source bytes")
			}
			source, err := w.Capture(t.Context(), a)
			if err != nil {
				t.Fatal("literal CRLF change was not retained", err)
			}
			patch, err := disk.Read("artifacts/"+source.PatchID, a.Limits.MaxArtifact)
			if err != nil || !bytes.Contains(patch, []byte("+one\r\n")) || !bytes.Contains(patch, []byte("+two\r\n")) || bytes.Contains(patch, []byte("large.bin")) || bytes.Contains(patch, []byte(".gitattributes")) {
				t.Fatal("capture normalized changed bytes or touched unchanged source", string(patch), err)
			}
			r, err = w.load(a.RunID)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := w.retainedCandidate(t.Context(), r, true); err != nil {
				t.Fatal("literal candidate did not reconstruct its exact source", err)
			}
			if err := os.WriteFile(filepath.Join(r.WorkDir, "text.txt"), []byte("one\ntwo\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, _, err := w.retainedCandidate(t.Context(), r, true); err == nil {
				t.Fatal("retained candidate accepted normalized but different worktree bytes")
			}
			if err := os.WriteFile(filepath.Join(r.WorkDir, "text.txt"), literal, 0600); err != nil {
				t.Fatal(err)
			}
			if err := w.releaseRun(t.Context(), a.RunID, true); err != nil {
				t.Fatal("exact retained attribute source could not release", err)
			}
			a.AttemptID = "attempt_restored_attributes"
			a.Inputs = map[string]runner.Value{"patch": {ArtifactID: source.PatchID}}
			r, err = w.Prepare(t.Context(), a)
			if err != nil {
				t.Fatal("attribute source could not restore", err)
			}
			if data, err := os.ReadFile(filepath.Join(r.WorkDir, "text.txt")); err != nil || !bytes.Equal(data, literal) {
				t.Fatal("restoration changed captured bytes", data, err)
			}
			if data, err := os.ReadFile(filepath.Join(r.WorkDir, ".gitattributes")); err != nil || string(data) != builtinAttributeFixture {
				t.Fatal("restoration changed immutable attribute patterns/comments", err)
			}
		})
	}
}

func TestWorkspaceAttributesRefuseDriftNestedRulesAndExternalOverrides(t *testing.T) {
	for _, kind := range []string{"changed root", "removed root", "root link", "nested", "ignored nested", "root mode", "oversized root", "info", "info link"} {
		t.Run(kind, func(t *testing.T) {
			repo, disk := attributeRepository(t, "sha1"), workspaceDisk(t)
			w := &Workspaces{disk: disk}
			a := sourceAssignment(disk, repo, "attempt_attribute_drift")
			r, err := w.Prepare(t.Context(), a)
			if err != nil {
				t.Fatal(err)
			}
			root := filepath.Join(r.WorkDir, ".gitattributes")
			switch kind {
			case "changed root":
				err = os.WriteFile(root, []byte("* filter=custom\n"), 0600)
			case "removed root":
				err = os.Remove(root)
			case "root link":
				if err = os.Remove(root); err == nil {
					err = os.Symlink(filepath.Join(repo, ".gitattributes"), root)
				}
			case "nested", "ignored nested":
				if err = os.Mkdir(filepath.Join(r.WorkDir, "nested"), 0700); err == nil {
					err = os.WriteFile(filepath.Join(r.WorkDir, "nested", ".gitattributes"), []byte("* text=auto\n"), 0600)
				}
				if err == nil && kind == "ignored nested" {
					err = os.WriteFile(filepath.Join(r.WorkDir, ".gitignore"), []byte("nested/\n"), 0600)
				}
			case "root mode":
				err = os.Chmod(root, 0700)
			case "oversized root":
				err = os.WriteFile(root, bytes.Repeat([]byte{'#'}, maxRootAttributes+1), 0600)
			case "info":
				if err = os.MkdirAll(filepath.Join(repo, ".git", "info"), 0700); err == nil {
					err = os.WriteFile(filepath.Join(repo, ".git", "info", "attributes"), []byte("* filter=custom\n"), 0600)
				}
			case "info link":
				if err = os.MkdirAll(filepath.Join(repo, ".git", "info"), 0700); err == nil {
					err = os.Symlink(filepath.Join(repo, ".gitattributes"), filepath.Join(repo, ".git", "info", "attributes"))
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			var blocked *runner.CleanupBlockedError
			if _, err := w.Capture(t.Context(), a); !errors.As(err, &blocked) {
				t.Fatal("attribute drift entered retained source or lost its parked integrity classification", kind, err)
			}
			if _, err := os.Stat(root); kind != "removed root" && err != nil {
				t.Fatal("unsupported attribute evidence was removed", err)
			}
		})
	}
}

func TestAttributeAdmissionRejectsDriversBeforeCheckoutAndIgnoresGlobalRules(t *testing.T) {
	for _, mode := range []string{"root driver", "nested driver", "info driver", "global driver", "tree driver"} {
		t.Run(mode, func(t *testing.T) {
			repo, disk := attributeRepository(t, "sha1"), workspaceDisk(t)
			base := strings.TrimSpace(string(fixtureGit(t, repo, "rev-parse", "HEAD")))
			marker := filepath.Join(t.TempDir(), "executed")
			if mode == "root driver" || mode == "nested driver" || mode == "tree driver" {
				path := ".gitattributes"
				if mode == "nested driver" {
					if err := os.Mkdir(filepath.Join(repo, "nested"), 0700); err != nil {
						t.Fatal(err)
					}
					path = "nested/.gitattributes"
				}
				if err := os.WriteFile(filepath.Join(repo, path), []byte("* filter=custom\n"), 0600); err != nil {
					t.Fatal(err)
				}
				fixtureGit(t, repo, "add", "--", path)
				fixtureGit(t, repo, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgSign=false", "commit", "--quiet", "-m", "unsupported attribute source")
				if mode == "tree driver" {
					alternate := strings.TrimSpace(string(fixtureGit(t, repo, "rev-parse", "HEAD")))
					fixtureGit(t, repo, "reset", "--hard", base)
					fixtureGit(t, repo, "config", "attr.tree", alternate)
				}
			}
			quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
			command := "printf executed > " + quote(marker) + "; cat"
			fixtureGit(t, repo, "config", "filter.custom.clean", command)
			fixtureGit(t, repo, "config", "filter.custom.smudge", command)
			if mode == "info driver" || mode == "global driver" {
				file := filepath.Join(repo, ".git", "info", "attributes")
				if mode == "global driver" {
					file = filepath.Join(t.TempDir(), "attributes")
					fixtureGit(t, repo, "config", "core.attributesFile", file)
				}
				if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(file, []byte("* filter=custom\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			w := &Workspaces{disk: disk}
			a := sourceAssignment(disk, repo, "attempt_no_driver")
			_, err := w.Prepare(t.Context(), a)
			if (err == nil) != (mode == "global driver") {
				t.Fatal("untrusted attributes were accepted or global neutralization failed", mode, err)
			}
			if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("external attribute driver executed", mode, err)
			}
			if mode != "global driver" {
				if _, err := os.Stat(filepath.Join(disk.Root, "work", a.RunID, "source")); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("unsupported source reached checkout", mode, err)
				}
			}
		})
	}
}

func TestChangedAttributesCannotExecuteDriverOrReplaceCapturedCandidate(t *testing.T) {
	repo, disk := attributeRepository(t, "sha1"), workspaceDisk(t)
	w := &Workspaces{disk: disk}
	a := sourceAssignment(disk, repo, "attempt_before_attribute_drift")
	r, err := w.Prepare(t.Context(), a)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.WorkDir, "text.txt"), []byte("accepted bytes\n"), 0600); err != nil {
		t.Fatal(err)
	}
	source, err := w.Capture(t.Context(), a)
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "executed")
	quote := "'" + strings.ReplaceAll(marker, "'", "'\\''") + "'"
	fixtureGit(t, repo, "config", "filter.custom.clean", "printf executed > "+quote+"; cat")
	if err := os.WriteFile(filepath.Join(r.WorkDir, ".gitattributes"), []byte("* filter=custom\n"), 0600); err != nil {
		t.Fatal(err)
	}
	a.AttemptID = "attempt_after_attribute_drift"
	if _, err := w.Capture(t.Context(), a); err == nil {
		t.Fatal("untrusted changed attributes were captured")
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("changed attributes executed a configured clean driver", err)
	}
	r, err = w.load(a.RunID)
	if err != nil || r.Candidate == nil || r.Candidate.PatchID != source.PatchID || r.Candidate.Tree != source.Tree {
		t.Fatal("attribute rejection replaced prior accepted source", err)
	}
}
