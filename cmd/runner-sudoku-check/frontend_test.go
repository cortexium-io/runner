package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func fixtureGit(t *testing.T, source string, args ...string) string {
	t.Helper()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("Git is required for source identity fixtures")
	}
	flags := []string{"-c", "user.name=Runner fixture", "-c", "user.email=fixture@runner.invalid", "-c", "commit.gpgSign=false", "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-C", source}
	cmd := exec.Command(git, append(flags, args...)...)
	data, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("fixture Git: %v %s", err, data)
	}
	return strings.TrimSpace(string(data))
}

func TestFrontendReuseNeedsExactProjectionCleanSourceAndAuthenticatedProof(t *testing.T) {
	source := fixtureSource(t)
	fixtureFile(t, source, "docs/current.md", "initial agreement")
	fixtureFile(t, source, "docs/sudoku-interface.md", "initial design record")
	fixtureGit(t, source, "init", "-q")
	fixtureGit(t, source, "add", ".")
	fixtureGit(t, source, "commit", "-qm", "fixture")
	commit := fixtureGit(t, source, "rev-parse", "HEAD")
	tree := fixtureGit(t, source, "rev-parse", "HEAD^{tree}")
	prior := frontendReport{Schema: 2, Status: "passed", Tests: []string{"src/example.test.ts"}, Workers: 1, Timeout: 5000, Executed: 393}
	zero := 0
	prior.Typecheck.Status, prior.Typecheck.Complete, prior.Typecheck.ExitCode = "passed", true, &zero
	data, _ := json.Marshal(prior)
	priorPath := filepath.Join(t.TempDir(), "proof.json")
	if err := os.WriteFile(priorPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	h := sourceHandoff{Identity: strings.Repeat("a", 64), Phase: "ready", MaterializedTree: tree, InitialCommit: commit}
	h.Source.Base, h.Source.Tree, h.Source.CheckSHA256, h.Source.Destination, h.Source.Exclude = strings.Repeat("b", 40), strings.Repeat("c", 40), digest(data), source, historicalExclusions
	handoff := filepath.Join(t.TempDir(), "handoff.json")
	if err := writeJSON(handoff, h); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RUNNER_SUDOKU_INITIAL_TREE", h.Source.Tree)
	t.Setenv("RUNNER_SUDOKU_PRIOR_CHECK_SHA256", digest(data))
	t.Setenv("RUNNER_SUDOKU_TESTS_JSON", "[\"src/example.test.ts\"]")
	t.Setenv("RUNNER_SUDOKU_FRONTEND_HELPER", "/usr/bin/false")
	t.Setenv("RUNNER_SUDOKU_EXPECTED_INCLUDE", "")
	o := options{source: source, handoff: handoff, priorCheck: priorPath, base: h.Source.Base}
	for _, marker := range []string{"", "mutable workspace marker is not authority"} {
		if marker != "" {
			fixtureFile(t, source, ".runner-source-handoff.json", marker)
		}
		proof, err := frontendGate(t.Context(), o, t.TempDir(), t.TempDir())
		if err != nil || proof.Mode != "reused" || proof.Executed != 393 {
			t.Fatalf("unchanged protected evidence was not reused: %+v %v", proof, err)
		}
	}
	fixtureFile(t, source, "docs/sudoku-interface.md", "updated non-runtime design record")
	proof, err := frontendGate(t.Context(), o, t.TempDir(), t.TempDir())
	if err != nil || proof.Mode != "reused" || len(proof.ReuseExclusions) != 1 {
		t.Fatalf("non-runtime design-only update repeated passing unit proof: %+v %v", proof, err)
	}
	_, before, err := candidateInventory(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	fixtureFile(t, source, "frontend/src/main.ts", "changed frontend")
	fixtureGit(t, source, "update-index", "--assume-unchanged", "frontend/src/main.ts")
	_, after, err := candidateInventory(context.Background(), source)
	if err != nil || before == after {
		t.Fatal("changed source was missing from candidate identity")
	}
	if _, err = frontendGate(t.Context(), o, t.TempDir(), t.TempDir()); err == nil || !strings.Contains(err.Error(), "fresh frontend proof") {
		t.Fatalf("changed source reused original proof: %v", err)
	}
	fixtureFile(t, source, "frontend/package.json", "changed dependency metadata")
	if _, err = frontendGate(t.Context(), o, t.TempDir(), t.TempDir()); err == nil || !strings.Contains(err.Error(), "protected/generated/dependency") {
		t.Fatalf("dependency metadata change was admitted: %v", err)
	}
	if err = os.Remove(filepath.Join(source, "frontend/package.json")); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(priorPath, []byte("changed proof"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = frontendGate(t.Context(), o, t.TempDir(), t.TempDir()); err == nil || !strings.Contains(err.Error(), "digest") {
		t.Fatalf("changed prior evidence was admitted: %v", err)
	}
}

func TestChangedScopeMatchesTheFrozenDeveloperClaims(t *testing.T) {
	for _, name := range []string{"frontend/src/pages/home/HomePage.vue", "frontend/src/components/example.test.ts", "docs/sudoku-interface.md"} {
		if err := validateChangedScope([]string{name}); err != nil {
			t.Fatalf("allowed scoped edit %s: %v", name, err)
		}
	}
	for _, name := range []string{"frontend/private/example.test.ts", "frontend/scripts/check.mjs", "frontend/public/icon.svg", "docs/other.md", "README.md", "frontend/src/app/routes.gny.ts", "frontend/src/AGENTS.md", "frontend/package.json", "go.mod"} {
		if err := validateChangedScope([]string{name}); err == nil {
			t.Fatalf("out-of-contract edit was admitted: %s", name)
		}
	}
}

func TestIgnoredBuildInputsCannotEscapeCandidateIdentity(t *testing.T) {
	source := fixtureSource(t)
	fixtureFile(t, source, ".gitignore", "frontend/src/ignored.ts\n")
	fixtureGit(t, source, "init", "-q")
	fixtureGit(t, source, "add", ".")
	fixtureGit(t, source, "commit", "-qm", "fixture")
	if _, _, err := candidateInventory(t.Context(), source); err != nil {
		t.Fatal(err)
	}
	for _, contents := range []string{"ignored initial source", "changed ignored source"} {
		fixtureFile(t, source, "frontend/src/ignored.ts", contents)
		if _, _, err := candidateInventory(t.Context(), source); err == nil || !strings.Contains(err.Error(), "ignored build input") {
			t.Fatalf("ignored consumed input was not rejected: %v", err)
		}
	}
}

func TestFrontendEvidenceRejectsSkippedIncompleteOrMissingSelectedAssertions(t *testing.T) {
	zero := 0
	valid := frontendReport{Schema: 2, Status: "passed", Tests: []string{"src/example.test.ts"}, Workers: 1, Timeout: 5000, Executed: 393}
	valid.Typecheck.Status, valid.Typecheck.Complete, valid.Typecheck.ExitCode = "passed", true, &zero
	for _, mutate := range []func(*frontendReport){
		func(p *frontendReport) { p.Skipped = 1 },
		func(p *frontendReport) { p.Incomplete = true },
		func(p *frontendReport) { p.Workers = 2 },
		func(p *frontendReport) { p.Timeout = 10000 },
		func(p *frontendReport) { p.Tests = []string{"other.test.ts"} },
		func(p *frontendReport) { p.Typecheck.Complete = false },
	} {
		p := valid
		mutate(&p)
		data, _ := json.Marshal(p)
		if _, err := parseFrontend(data, []string{"src/example.test.ts"}); err == nil {
			t.Fatalf("incomplete evidence admitted: %+v", p)
		}
	}
}
