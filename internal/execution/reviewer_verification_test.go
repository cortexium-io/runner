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
	for _, name := range []string{".git", "node_modules", "dist"} {
		if _, err := os.Lstat(filepath.Join(launch.VerificationRoot, name)); !os.IsNotExist(err) {
			t.Fatalf("unexpected artifact %s: %v", name, err)
		}
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

func TestReviewerVerificationPackageNetworkIsFocusedAndOptIn(t *testing.T) {
	profile, _ := ProfileForRole(RoleReviewer)
	for _, focused := range []bool{false, true} {
		for _, safe := range []bool{false, true} {
			launch := profileWorkspace{Dir: "/neutral", ReadRoot: "/candidate"}
			if focused {
				launch.VerificationRoot = "/neutral/verification"
			}
			for _, policy := range []string{
				strings.Join(codexProfileArgs(profile, launch, safe), " "),
				claudeSandboxSettings(profile, launch, safe),
			} {
				if strings.Contains(policy, "registry.npmjs.org") != (focused && safe) {
					t.Fatalf("unexpected package network: focused=%v safe=%v %s", focused, safe, policy)
				}
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
		"app.js": `import http from 'node:http'; import assert from 'node:assert/strict'; import proof from 'qa-proof';
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
