package execution

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/metrics"
	"github.com/cortexium-io/runner/internal/workspace"
)

func verificationFixture(t *testing.T) profileWorkspace {
	t.Helper()
	source := t.TempDir()
	for path, content := range map[string]string{
		"package.json":      `{"name":"candidate"}`,
		"package-lock.json": `{"lockfileVersion":3}`, "app.js": `process.stdout.write("candidate")`,
	} {
		if err := os.WriteFile(filepath.Join(source, path), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	dir := t.TempDir()
	return profileWorkspace{Dir: dir, ReadRoot: source, TempDir: filepath.Join(dir, "runtime")}
}

func TestReviewerVerificationKeepsSourceIsolatedAndAllowsGeneratedOutput(t *testing.T) {
	launch := verificationFixture(t)
	if err := os.WriteFile(filepath.Join(launch.ReadRoot, ".git"), []byte("gitdir: /never/expose/shared/git"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("app.js", filepath.Join(launch.ReadRoot, "app-link.js")); err != nil {
		t.Fatal(err)
	}
	copy, err := prepareReviewerVerification(t.Context(), &launch, workspace.DefaultSnapshotLimits())
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(filepath.Join(launch.VerificationRoot, ".git")); err != nil || !info.IsDir() {
		t.Fatalf("fresh standalone Git index missing: %v", err)
	}
	for _, name := range []string{"node_modules", "dist", ".git/commondir", ".git/objects/info/alternates"} {
		if _, err := os.Lstat(filepath.Join(launch.VerificationRoot, name)); !os.IsNotExist(err) {
			t.Fatalf("unexpected artifact %s: %v", name, err)
		}
	}
	// A Git status refresh may update index cache bytes, but not its inventory.
	status := exec.CommandContext(t.Context(), "git", "status", "--porcelain")
	status.Dir = launch.VerificationRoot
	if output, err := status.CombinedOutput(); err != nil {
		t.Fatalf("private index status: %v %s", err, output)
	}
	for _, name := range []string{"node_modules", "dist", "test-results"} {
		path := filepath.Join(launch.VerificationRoot, name)
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "output"), []byte("generated"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(launch.ReadRoot, name)); !os.IsNotExist(err) {
			t.Fatalf("setup changed canonical source: %s", name)
		}
	}
	if err := copy.verify(); err != nil {
		t.Fatalf("generated artifacts rejected: %v", err)
	}
	link, err := os.Readlink(filepath.Join(launch.VerificationRoot, "app-link.js"))
	if err != nil || link != "app.js" {
		t.Fatalf("internal symlink changed: %q %v", link, err)
	}
}

func TestReviewerVerificationRejectsChangedOrRedirectedSource(t *testing.T) {
	for _, mutation := range []string{"edit", "delete", "symlink", "parent-symlink", "lockfile"} {
		t.Run(mutation, func(t *testing.T) {
			launch := verificationFixture(t)
			copy, err := prepareReviewerVerification(t.Context(), &launch, workspace.DefaultSnapshotLimits())
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(launch.VerificationRoot, "app.js")
			switch mutation {
			case "edit":
				err = os.WriteFile(path, []byte("changed"), 0o600)
			case "lockfile":
				err = os.WriteFile(filepath.Join(launch.VerificationRoot, "package-lock.json"), []byte("changed"), 0o600)
			case "delete":
				err = os.Remove(path)
			case "symlink":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				err = os.Symlink(filepath.Join(launch.ReadRoot, "app.js"), path)
			case "parent-symlink":
				if err := os.Rename(launch.VerificationRoot, launch.VerificationRoot+"-moved"); err != nil {
					t.Fatal(err)
				}
				err = os.Symlink(launch.ReadRoot, launch.VerificationRoot)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := copy.verify(); err == nil {
				t.Fatal("changed verification source accepted")
			}
			original, err := os.ReadFile(filepath.Join(launch.ReadRoot, "app.js"))
			if err != nil || string(original) != `process.stdout.write("candidate")` {
				t.Fatal("canonical source changed")
			}
		})
	}
}

func TestReviewerVerificationRejectsChangedGitInventoryAndControls(t *testing.T) {
	for _, mutation := range []string{"remove-entry", "hide-entry", "skip-worktree", "intent-to-add", "config", "redirect-index", "redirect-git", "redirect-empty-directory", "commondir", "attributes", "alternates", "new-directory", "split-index"} {
		t.Run(mutation, func(t *testing.T) {
			launch := verificationFixture(t)
			if mutation == "intent-to-add" {
				if err := os.WriteFile(filepath.Join(launch.ReadRoot, "app.js"), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			copy, err := prepareReviewerVerification(t.Context(), &launch, workspace.DefaultSnapshotLimits())
			if err != nil {
				t.Fatal(err)
			}
			switch mutation {
			case "remove-entry", "hide-entry", "skip-worktree":
				args := []string{"update-index", "--force-remove", "app.js"}
				if mutation == "hide-entry" {
					args = []string{"update-index", "--assume-unchanged", "app.js"}
				}
				if mutation == "skip-worktree" {
					args = []string{"update-index", "--skip-worktree", "app.js"}
				}
				command := exec.CommandContext(t.Context(), "git", args...)
				command.Dir = launch.VerificationRoot
				if output, err := command.CombinedOutput(); err != nil {
					t.Fatalf("mutate private index: %v %s", err, output)
				}
			case "intent-to-add":
				for _, args := range [][]string{{"rm", "--cached", "app.js"}, {"add", "--intent-to-add", "app.js"}} {
					command := exec.CommandContext(t.Context(), "git", args...)
					command.Dir = launch.VerificationRoot
					if output, err := command.CombinedOutput(); err != nil {
						t.Fatalf("change intent to add: %v %s", err, output)
					}
				}
			case "config":
				err = os.WriteFile(filepath.Join(launch.VerificationRoot, ".git", "config"), []byte("[remote \"origin\"]\nurl = https://invalid.example/repo\n"), 0o600)
			case "commondir", "attributes", "alternates", "new-directory":
				paths := map[string]string{"commondir": "commondir", "attributes": "info/attributes", "alternates": "objects/info/alternates", "new-directory": "hooks"}
				path := filepath.Join(launch.VerificationRoot, ".git", paths[mutation])
				if mutation == "new-directory" {
					err = os.Mkdir(path, 0o700)
				} else {
					if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
						t.Fatal(err)
					}
					err = os.WriteFile(path, []byte(t.TempDir()+"\n"), 0o600)
				}
			case "split-index":
				command := exec.CommandContext(t.Context(), "git", "update-index", "--split-index")
				command.Dir = launch.VerificationRoot
				if output, err := command.CombinedOutput(); err != nil {
					t.Fatalf("split private index: %v %s", err, output)
				}
			case "redirect-index", "redirect-git", "redirect-empty-directory":
				path := filepath.Join(launch.VerificationRoot, ".git")
				if mutation == "redirect-index" {
					path = filepath.Join(path, "index")
				}
				if mutation == "redirect-empty-directory" {
					path = filepath.Join(path, "refs", "heads")
				}
				if err := os.Rename(path, path+"-original"); err != nil {
					t.Fatal(err)
				}
				err = os.Symlink(path+"-original", path)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := copy.verify(); err == nil {
				t.Fatal("changed or redirected Git inventory accepted")
			}
		})
	}
}

func TestReviewerVerificationRejectsExternalLinksAndBoundsCopies(t *testing.T) {
	for _, scenario := range []string{"external-link", "entries", "file-bytes", "total-bytes", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			launch := verificationFixture(t)
			limits := workspace.DefaultSnapshotLimits()
			ctx := t.Context()
			switch scenario {
			case "external-link":
				if err := os.Symlink("../../outside", filepath.Join(launch.ReadRoot, "escape")); err != nil {
					t.Fatal(err)
				}
			case "entries":
				limits.MaxEntries = 1
			case "file-bytes":
				limits.MaxFileBytes = 1
			case "total-bytes":
				limits.MaxFileBytes, limits.MaxTotalBytes = 40, 40
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			_, err := prepareReviewerVerification(ctx, &launch, limits)
			if err == nil {
				t.Fatal("unsafe or over-budget copy accepted")
			}
			if launch.VerificationRoot != "" {
				t.Fatal("failed copy exposed to harness")
			}
		})
	}
}

func TestReviewerVerificationRejectsSourceOmittedByGit(t *testing.T) {
	launch := verificationFixture(t)
	if err := os.Symlink("app.js", filepath.Join(launch.ReadRoot, ".gitmodules")); err != nil {
		t.Fatal(err)
	}
	if copy, err := prepareReviewerVerification(t.Context(), &launch, workspace.DefaultSnapshotLimits()); err == nil || copy != nil {
		t.Fatal("Git silently omitted a copied source path from the verification inventory")
	}
	if launch.VerificationRoot != "" {
		t.Fatal("incomplete source inventory exposed to reviewer")
	}
}

func TestReviewerVerificationPackageNetworkIsFocusedAndOptIn(t *testing.T) {
	profile, _ := ProfileForRole(RoleReviewer)
	for _, focused := range []bool{false, true} {
		for _, safe := range []bool{false, true} {
			launch := profileWorkspace{Dir: "/neutral", ReadRoot: "/candidate"}
			if focused {
				launch.VerificationRoot = "/neutral/verification"
			}
			codex := strings.Join(codexProfileArgs(profile, launch, safe), " ")
			claude := claudeSandboxSettings(profile, launch, safe)
			for _, domain := range packageDevelopmentDomains {
				if strings.Contains(codex, `"`+domain+`"="allow"`) != (focused && safe) {
					t.Fatalf("unexpected Codex package network domain %s: focused=%v safe=%v %s", domain, focused, safe, codex)
				}
			}
			expectedClaudeDomains := `"allowedDomains":["localhost","127.0.0.1"]`
			if focused && safe {
				expectedClaudeDomains = `"allowedDomains":["localhost","127.0.0.1","registry.npmjs.org","proxy.golang.org","sum.golang.org","storage.googleapis.com"]`
			}
			if !strings.Contains(claude, expectedClaudeDomains) {
				t.Fatalf("unexpected Claude package network: focused=%v safe=%v %s", focused, safe, claude)
			}
			for _, policy := range []string{codex, claude} {
				if strings.Contains(policy, `"/candidate"="write"`) || strings.Contains(policy, "danger-full-access") {
					t.Fatalf("candidate isolation widened: %s", policy)
				}
			}
		}
	}
	if err := ValidateHarnessProfile(config.HarnessPiCLI, RoleReviewer); err == nil {
		t.Fatal("Pi sandbox boundary changed")
	}
}

func TestReviewerVerificationStageRejectsTamperingAndCleansFailedLaunch(t *testing.T) {
	for _, failLaunch := range []bool{false, true} {
		t.Run(fmt.Sprint(failLaunch), func(t *testing.T) {
			fixture := verificationFixture(t)
			var neutral string
			run := &sharedReviewerHarnessRunner{response: failingReviewerContent()}
			run.onRun = func(dir string) error {
				neutral = dir
				if failLaunch {
					return errors.New("simulated harness failure")
				}
				return os.WriteFile(filepath.Join(dir, "verification", "app.js"), []byte("modified candidate"), 0o600)
			}
			cfg := config.ExecutionConfig{Harness: config.HarnessConfig{Kind: config.HarnessCodexCLI, Command: config.HarnessCodexCLI, WorkingDir: fixture.ReadRoot, TimeoutSeconds: 30}}
			schema, err := reviewerAuditSchema(2)
			if err != nil {
				t.Fatal(err)
			}
			result, err := runStructuredHarness(t.Context(), RoleReviewer, config.HarnessCodexCLI, cfg, fixture.ReadRoot, "Verify", schema, "require", metrics.StageReviewerVerify, run)
			if err == nil {
				t.Fatal("failed or tampered verification accepted")
			}
			if !failLaunch && (result.FailureClass != FailureIntegrityViolation || result.Message != "") {
				t.Fatalf("tampered model result survived: %#v", result)
			}
			if neutral == "" {
				t.Fatalf("harness did not run: %v", err)
			}
			if _, err := os.Stat(neutral); !os.IsNotExist(err) {
				t.Fatal("failed stage leaked its verification copy")
			}
		})
	}
}

func TestReviewerVerificationRunsGitDependentRepositoryCommand(t *testing.T) {
	makeCommand, err := exec.LookPath("make")
	if err != nil {
		t.Skip("make is required for the repository verification entrypoint check")
	}
	fixture := verificationFixture(t)
	makefile := ".PHONY: verify\nverify:\n\t@git ls-files --cached --others --exclude-standard | grep -qx app.js\n\t@test -f app.js\n\t@mkdir -p test-results\n\t@printf 'candidate verified\\n' > test-results/verification.log\n"
	if err := os.WriteFile(filepath.Join(fixture.ReadRoot, "Makefile"), []byte(makefile), 0o600); err != nil {
		t.Fatal(err)
	}
	schema, err := reviewerResolutionSchema([]reviewerUnresolvedCheck{{Key: "P1"}})
	if err != nil {
		t.Fatal(err)
	}
	run := &sharedReviewerHarnessRunner{response: `{"checks":{"P1":{"status":"passed","evidence":["make verify exited 0 using the disposable source index; test-results/verification.log: candidate verified."]}},"summary":"The required repository command passed."}`}
	var verificationDir string
	run.onRun = func(dir string) error {
		prompt := run.inputs[len(run.inputs)-1]
		for _, instruction := range []string{
			"fresh standalone Git index",
			"no commits, history, remotes, shared Git administration",
			"including complete validation",
			"When repository policy permits Runner-bound evidence",
			"Do not manufacture a standalone receipt",
			"Report the actual command, settings, exit status",
		} {
			if !strings.Contains(prompt, instruction) {
				t.Fatalf("verification guidance omitted %q", instruction)
			}
		}
		verificationDir = filepath.Join(dir, "verification")
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, makeCommand, "verify")
		command.Dir = verificationDir
		command.Env = []string{"PATH=" + os.Getenv("PATH")}
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("run Git-dependent repository command: %v %s", err, output)
		}
		log, err := os.ReadFile(filepath.Join(verificationDir, "test-results", "verification.log"))
		if err != nil || string(log) != "candidate verified\n" {
			t.Fatalf("verification evidence missing: %q %v", log, err)
		}
		return nil
	}
	cfg := config.ExecutionConfig{Harness: config.HarnessConfig{Kind: config.HarnessCodexCLI, Command: config.HarnessCodexCLI, WorkingDir: fixture.ReadRoot, TimeoutSeconds: 30}}
	result, err := runStructuredHarness(t.Context(), RoleReviewer, config.HarnessCodexCLI, cfg, fixture.ReadRoot, "Verify the candidate with make verify.", schema, "require", metrics.StageReviewerVerify, run)
	if err != nil {
		t.Fatalf("repository verification stage failed: %v", err)
	}
	resolved, err := decodeReviewerResolutionContent([]reviewerUnresolvedCheck{{Key: "P1"}}, result.Message)
	if err != nil || resolved.Checks["P1"].Status != "passed" || len(run.inputs) != 1 {
		t.Fatalf("verification did not retain the command outcome in one stage: %#v %v", resolved, err)
	}
	if verificationDir == "" {
		t.Fatal("verification command was not run")
	}
	if _, err := os.Stat(verificationDir); !os.IsNotExist(err) {
		t.Fatal("verification copy was not cleaned")
	}
	if _, err := os.Stat(filepath.Join(fixture.ReadRoot, "test-results")); !os.IsNotExist(err) {
		t.Fatal("verification wrote into the canonical candidate")
	}
}

// This tool-dependent proof stays out of the ordinary Go feedback loop. It
// uses only a local locked package and loopback; no registry or model calls.
func TestReviewerVerificationNodeSmoke(t *testing.T) {
	if os.Getenv("CORTEXIUM_RUNNER_TEST_QA_SETUP") != "1" {
		t.Skip("set CORTEXIUM_RUNNER_TEST_QA_SETUP=1 for the offline npm and local-app smoke")
	}
	launch := verificationFixture(t)
	files := map[string]string{
		"package.json":         `{"name":"qa-fixture","version":"1.0.0","type":"module","dependencies":{"qa-proof":"file:fixture"}}`,
		"package-lock.json":    `{"name":"qa-fixture","version":"1.0.0","lockfileVersion":3,"packages":{"":{"name":"qa-fixture","version":"1.0.0","dependencies":{"qa-proof":"file:fixture"}},"fixture":{"name":"qa-proof","version":"1.0.0"},"node_modules/qa-proof":{"resolved":"fixture","link":true}}}`,
		"fixture/package.json": `{"name":"qa-proof","version":"1.0.0","type":"module","main":"index.js"}`,
		"fixture/index.js":     `export default "exact candidate";`,
		"app.js": `import http from 'node:http'; import assert from 'node:assert/strict'; import proof from 'qa-proof'; import { execFileSync } from 'node:child_process';
const files = execFileSync('git', ['ls-files', '-z', '--cached', '--others', '--exclude-standard'], {encoding: 'utf8'}).split('\0');
assert(files.includes('app.js') && files.includes('package-lock.json'));
const server = http.createServer((req, res) => res.end(proof));
await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
try { const response = await fetch('http://127.0.0.1:' + server.address().port); assert.equal(await response.text(), 'exact candidate'); console.log('candidate app verified'); }
finally { await new Promise(resolve => server.close(resolve)); }`,
	}
	for path, content := range files {
		target := filepath.Join(launch.ReadRoot, path)
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	copy, err := prepareReviewerVerification(t.Context(), &launch, workspace.DefaultSnapshotLimits())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	probe := exec.CommandContext(ctx, "node", "app.js")
	probe.Dir = launch.VerificationRoot
	if output, err := probe.CombinedOutput(); err == nil || !strings.Contains(string(output), "ERR_MODULE_NOT_FOUND") {
		t.Fatalf("fixture did not reproduce missing dependencies: %v %s", err, output)
	}
	install := exec.CommandContext(ctx, "npm", "ci", "--offline", "--ignore-scripts", "--no-audit", "--no-fund", "--cache", filepath.Join(launch.Dir, "npm-cache"))
	install.Dir = launch.VerificationRoot
	if output, err := install.CombinedOutput(); err != nil {
		t.Fatalf("restore locked local dependency: %v %s", err, output)
	}
	probe = exec.CommandContext(ctx, "node", "app.js")
	probe.Dir = launch.VerificationRoot
	if output, err := probe.CombinedOutput(); err != nil || !strings.Contains(string(output), "candidate app verified") {
		t.Fatalf("candidate app failed after setup: %v %s", err, output)
	}
	if err := copy.verify(); err != nil {
		t.Fatalf("setup changed copied source: %v", err)
	}
	if _, err := os.Stat(filepath.Join(launch.ReadRoot, "node_modules")); !os.IsNotExist(err) {
		t.Fatal("setup changed canonical checkout")
	}
}
