package verification

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/execution"
	"github.com/cortexium-io/runner/internal/subprocess"
)

func writeFixture(t *testing.T, root, name, body string) {
	t.Helper()
	file := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, output)
	}
	return string(output)
}

func fixture(t *testing.T) (string, config.VerificationEntrypoint) {
	t.Helper()
	root := t.TempDir()
	git(t, root, "init", "-b", "main")
	git(t, root, "config", "user.name", "Fixture")
	git(t, root, "config", "user.email", "fixture@example.invalid")
	writeFixture(t, root, "src/source.txt", "source")
	writeFixture(t, root, "docs/proof.txt", "old evidence")
	writeFixture(t, root, ".gitignore", "deps/\n")
	writeFixture(t, root, "deps/lib/index.js", "dependency")
	git(t, root, "add", ".")
	git(t, root, "commit", "-m", "baseline")
	return root, config.VerificationEntrypoint{Command: "/bin/sh", Args: []string{"-c", "exit 0"}, TimeoutSeconds: 10, InputPaths: []string{"src"}, DependencyPaths: []string{"deps"}, ToolchainCommands: []string{"/bin/sh"}}
}

func TestContentInputsTrackChangesWithoutCopyIdentity(t *testing.T) {
	left, right := t.TempDir(), t.TempDir()
	for _, root := range []string{left, right} {
		writeFixture(t, root, "src/a", "one")
		writeFixture(t, root, "docs/proof", "old")
	}
	observe := func(root string) string {
		t.Helper()
		digest, err := collectInputs(t.Context(), root, []string{"src", "missing"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		return digest
	}
	initial := observe(left)
	if initial != observe(right) {
		t.Fatal("identical copied files lost applicability")
	}
	writeFixture(t, left, "docs/proof", "new evidence")
	if initial != observe(left) {
		t.Fatal("unselected evidence invalidated executable check")
	}
	for _, change := range []struct {
		name  string
		apply func()
	}{
		{"edit", func() { writeFixture(t, left, "src/a", "two") }},
		{"add", func() { writeFixture(t, left, "src/b", "new") }},
		{"delete", func() {
			if err := os.Remove(filepath.Join(left, "src/a")); err != nil {
				t.Fatal(err)
			}
		}},
		{"missing appears", func() { writeFixture(t, left, "missing", "present") }},
	} {
		t.Run(change.name, func(t *testing.T) {
			before := observe(left)
			change.apply()
			if before == observe(left) {
				t.Fatal("change not observed")
			}
		})
	}
}

func TestInputRootsRejectEscapesAndUnselectedLinkTargets(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "src/a", "safe")
	writeFixture(t, root, "outside", "not selected")
	for _, name := range []string{"../outside", "/tmp", ".git/config", "src/../outside", "."} {
		if _, err := collectInputs(t.Context(), root, []string{name}, nil); err == nil {
			t.Fatalf("accepted %q", name)
		}
	}
	if err := os.Symlink("../outside", filepath.Join(root, "src/link")); err != nil {
		t.Fatal(err)
	}
	if _, err := collectInputs(t.Context(), root, []string{"src"}, nil); err == nil {
		t.Fatal("outside dependency accepted")
	}
	if err := os.Remove(filepath.Join(root, "src/link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("a", filepath.Join(root, "src/link")); err != nil {
		t.Fatal(err)
	}
	if _, err := collectInputs(t.Context(), root, []string{"src"}, nil); err != nil {
		t.Fatal(err)
	}
}

func TestObserveSeparatesCandidateIntegrityAndApplicability(t *testing.T) {
	root, entry := fixture(t)
	base := strings.TrimSpace(git(t, root, "rev-parse", "HEAD"))
	observe := func() Observation {
		t.Helper()
		o, err := ObserveCandidate(context.Background(), root, base, "approved behavior", entry)
		if err != nil {
			t.Fatal(err)
		}
		return o
	}
	before := observe()
	writeFixture(t, root, "docs/proof.txt", "renewed evidence")
	git(t, root, "add", "docs/proof.txt")
	git(t, root, "commit", "-m", "evidence only")
	after := observe()
	if before.CommitOID == after.CommitOID || before.Integrity == after.Integrity {
		t.Fatal("full candidate integrity lost")
	}
	if !reflect.DeepEqual(before.Inputs, after.Inputs) {
		t.Fatal("evidence-only change invalidated executable applicability")
	}
	writeFixture(t, root, "deps/lib/index.js", "changed dependency")
	changed := observe()
	if changed.Inputs.Dependencies == after.Inputs.Dependencies {
		t.Fatal("actual installed dependency change ignored")
	}
	writeFixture(t, root, "src/source.txt", "dirty")
	if _, err := ObserveCandidate(t.Context(), root, base, "approved behavior", entry); err == nil {
		t.Fatal("dirty candidate accepted")
	}
}

func TestInputSymlinkCannotTraverseUnobservedIntermediateHop(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "src/a", "one")
	writeFixture(t, root, "src/b", "two")
	if err := os.MkdirAll(filepath.Join(root, "deps"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../src/a", filepath.Join(root, "deps/current")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../deps/current", filepath.Join(root, "src/link")); err != nil {
		t.Fatal(err)
	}
	if _, err := collectInputs(t.Context(), root, []string{"src"}, nil); err == nil {
		t.Fatal("unobserved intermediate target accepted")
	}
}

func TestConfiguredRuntimeReplacementInvalidatesActualReceipt(t *testing.T) {
	root, entry := fixture(t)
	tool := filepath.Join(t.TempDir(), "runtime")
	writeTool := func(body string) {
		t.Helper()
		if err := os.WriteFile(tool, []byte("#!/bin/sh\nprintf "+body), 0700); err != nil {
			t.Fatal(err)
		}
	}
	writeTool("one")
	entry.ToolchainCommands = []string{"/bin/sh", tool}
	entry.Args = []string{"-c", "\"$1\"", "fixture", tool}
	base := strings.TrimSpace(git(t, root, "rev-parse", "HEAD"))
	observe := func(ctx context.Context) (Observation, error) {
		return ObserveCandidate(ctx, root, base, "approved", entry)
	}
	request := Request{Entrypoint: "complete", Entry: entry, Directory: root, Repository: "owner/repo", AttemptID: "observed", Boundary: execution.VerificationComplete, Observe: observe}
	result, err := run(t.Context(), request, ownedTestGrant)
	if err != nil {
		t.Fatal(err)
	}
	if result.Output.Stdout != "one" {
		t.Fatal("fixture did not execute configured runtime")
	}
	writeTool("two")
	after, err := observe(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	assessment, err := execution.AssessVerificationReceipt(result.Receipt, result.Digest, execution.VerificationTarget{Repository: request.Repository, CandidateOID: after.CommitOID, Entrypoint: request.Entrypoint, SettingsDigest: result.Receipt.SettingsDigest, Inputs: after.Inputs, Boundary: request.Boundary})
	if err != nil || assessment.Applicable || assessment.Reason != "environment changed" {
		t.Fatalf("runtime change reused proof: %+v %v", assessment, err)
	}
}

func TestEnvironmentRejectsRelativeExecutableAndIgnoresOnlyOwnershipMetadata(t *testing.T) {
	entry := config.VerificationEntrypoint{Command: "./scripts/check.sh", ToolchainCommands: []string{"/bin/sh"}}
	if _, err := observeEnvironment(t.Context(), entry); err == nil {
		t.Fatal("relative executable accepted")
	}
	entry.Command = "/bin/sh"
	before, err := observeEnvironment(t.Context(), entry)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(subprocess.OwnershipEnvironmentVariable, "operational-only")
	t.Setenv(subprocess.HeavyOwnershipEnvironmentVariable, "operational-only")
	after, err := observeEnvironment(t.Context(), entry)
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("ownership metadata invalidated applicability")
	}
	t.Setenv("NODE_ENV", "changed-verification-semantics")
	after, err = observeEnvironment(t.Context(), entry)
	if err != nil {
		t.Fatal(err)
	}
	if before == after {
		t.Fatal("semantic environment ignored")
	}
}
