package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestConfiguredVitestSamplesConfirmedFiveSecondTimeouts(t *testing.T) {
	node, modules := os.Getenv("RUNNER_FRONTEND_NODE"), os.Getenv("RUNNER_FRONTEND_MODULES")
	if node == "" || modules == "" {
		t.Skip("set RUNNER_FRONTEND_NODE and RUNNER_FRONTEND_MODULES to qualify Node24/Vitest CPU sampling")
	}
	source, scratch, _ := frontendFixture(t, t.TempDir())
	frontend := filepath.Join(source, "frontend")
	test := `import { expect, test } from 'vitest';
function controlledHotspot(ms: number) {
  const end = performance.now() + ms;
  while (performance.now() < end) { Math.sqrt(performance.now()); }
}
async function hotspotUntilTimeout(signal: AbortSignal) {
  while (!signal.aborted) {
    controlledHotspot(20);
    await new Promise<void>(resolve => setImmediate(resolve));
  }
}
let sharedWork = false;
test('complete workload sentinel', () => { sharedWork = true; expect(true).toBe(true); });
test('reproduces [5s]', async ({ signal }) => {
  await hotspotUntilTimeout(signal);
});
test('passes in isolation [5s]', async ({ signal }) => {
  if (sharedWork) await hotspotUntilTimeout(signal);
  else controlledHotspot(200);
});
`
	for name, data := range map[string]string{
		"src/timeout.test.ts": test,
		"vitest.config.ts":    "import { defineConfig } from 'vitest/config'; export default defineConfig({ test: { testTimeout: 5000 } });\n",
	} {
		if err := os.WriteFile(filepath.Join(frontend, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	o := options{node: node, modules: modules, source: source, scratch: scratch, tests: `["src/timeout.test.ts"]`, diagnoseTimeouts: true}
	if err := runTimeoutFixture(t, o); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(scratch, "frontend-check.json"))
	var p proof
	if err != nil || json.Unmarshal(data, &p) != nil {
		t.Fatal("frontend diagnostic proof unavailable", err)
	}
	d := p.TimeoutDiagnostics
	if p.Status != "failed" || p.Executed != 3 || p.Failed != 2 || p.Skipped != 0 || d == nil || d.Schema != 1 || d.Status != "complete" || d.Method != timeoutDiagnosticMethod || len(d.Cases) != 2 {
		if p.Typecheck != nil && p.Typecheck.Status != "passed" {
			t.Logf("fixture compiler output: %s", clip(p.Typecheck.Output, 1200))
		}
		t.Fatal("isolated evidence changed full-check authority or failed to settle", p.Reason, d)
	}
	for _, c := range d.Cases {
		expected := map[string]string{"reproduces [5s]": "timeout_reproduced", "passes in isolation [5s]": "isolated_pass"}[c.FullName]
		if c.File != "src/timeout.test.ts" || c.Status != expected || c.TimeoutMS != 5000 || c.OriginalDurationMS < 4900 || c.DiagnosticDurationMS <= 0 || c.Status == "timeout_reproduced" && c.DiagnosticDurationMS < 4900 {
			t.Fatal("case identity, deadline or execution receipt differs", c)
		}
		if !slices.ContainsFunc(c.Profiles, func(profile timeoutCPUProfile) bool {
			return profile.Process == "worker" && profile.ProjectSamples > 0 && slices.ContainsFunc(profile.Stacks, func(stack timeoutSampledStack) bool {
				return slices.ContainsFunc(stack.Frames, func(frame timeoutStackFrame) bool {
					return frame.Kind == "project" && frame.File == "frontend/src/timeout.test.ts" && frame.Function == "controlledHotspot" && frame.Line > 0 && frame.Column > 0
				})
			})
		}) {
			t.Fatal("profile did not localize the known project hotspot", c)
		}
		t.Logf("case %q %s original=%.3fms isolated=%.3fms profiles=%d matched=frontend/src/timeout.test.ts:controlledHotspot", c.FullName, c.Status, c.OriginalDurationMS, c.DiagnosticDurationMS, len(c.Profiles))
	}
	primary, err := os.ReadFile(filepath.Join(scratch, "vitest.json"))
	if err != nil || p.ReportBytes != len(primary) || p.ReportSHA256 != digest(primary) || !sameJSON(p.Vitest, primary) {
		t.Fatal("diagnostics replaced the original full-check report", err)
	}
	var domain map[string]string
	data, err = os.ReadFile(filepath.Join(scratch, "domain.json"))
	if err != nil || json.Unmarshal(data, &domain) != nil || domain["outcome"] != "changes_requested" || !strings.Contains(domain["reason"], "Test timed out in 5000ms.") {
		t.Fatal("isolated pass waived a full-check timeout", domain, err)
	}
	if remaining, err := filepath.Glob(filepath.Join(scratch, "timeout-diagnostic-*")); err != nil || len(remaining) != 0 {
		t.Fatal("settled diagnostic scratch was not removed", remaining, err)
	}
}

// The optional pinned sandbox invocation runs the same disposable assertion
// fixture with an isolated Codex configuration and no native generation.
func runTimeoutFixture(t *testing.T, o options) error {
	t.Helper()
	helper := os.Getenv("RUNNER_FRONTEND_CHECK_BINARY")
	if helper == "" {
		return run(o)
	}
	codex := os.Getenv("RUNNER_PREFLIGHT_CODEX")
	openssl := os.Getenv("RUNNER_FRONTEND_OPENSSL_CONF")
	if runtime.GOOS != "darwin" || !filepath.IsAbs(codex) || !filepath.IsAbs(helper) || !filepath.IsAbs(openssl) {
		t.Fatal("native timeout profiling requires pinned macOS Codex, the configured OpenSSL file and a locally built checker")
	}
	if err := os.Mkdir(o.scratch, 0700); err != nil {
		t.Fatal(err)
	}
	var config strings.Builder
	config.WriteString("default_permissions=\"runner-command\"\n[permissions.runner-command.filesystem]\n\":minimal\"=\"read\"\n")
	for _, path := range []string{o.source, filepath.Dir(filepath.Dir(o.node)), o.modules, helper, openssl} {
		fmt.Fprintf(&config, "%s=\"read\"\n", strconv.Quote(path))
	}
	fmt.Fprintf(&config, "%s=\"write\"\n[permissions.runner-command.network]\nenabled=false\n", strconv.Quote(o.scratch))
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(config.String()), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), codex, "sandbox", "-P", "runner-command", "-C", o.source, "--",
		"/usr/bin/env", "-i", "PATH="+filepath.Dir(o.node)+":/usr/bin:/bin", "TMPDIR="+o.scratch, "OPENSSL_CONF="+openssl,
		helper, "--node", o.node, "--modules", o.modules, "--source", o.source, "--scratch", o.scratch, "--tests", o.tests, "--diagnose-timeouts")
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + os.Getenv("HOME"), "CODEX_HOME=" + home}
	data, err := cmd.CombinedOutput()
	t.Logf("Pinned native sandbox result: %s", clip(string(data), 1200))
	return err
}

const knownCPUProfile = `{
  "startTime": 123, "endTime": 99999,
  "nodes": [
    {"id":1,"callFrame":{"functionName":"(root)"},"children":[2,3]},
    {"id":2,"callFrame":{"functionName":"(idle)"}},
    {"id":3,"callFrame":{"functionName":"hotspot","url":"file:///source/frontend/src/view.ts","lineNumber":9,"columnNumber":4}}
  ],
  "samples":[2,3,3],"timeDeltas":[10000,10000,10000]
}`

func TestTimeoutProfileBoundsAndSourceLocalization(t *testing.T) {
	profile, err := summarizeTimeoutProfile([]byte(knownCPUProfile), "worker", "/source", "/deps")
	if err != nil || profile.Samples != 3 || profile.IdleSamples != 1 || profile.ProjectSamples != 2 || len(profile.Stacks) != 1 || profile.Stacks[0].Samples != 2 || !slices.Equal(profile.Stacks[0].Frames, []timeoutStackFrame{
		{Kind: "project", File: "frontend/src/view.ts", Function: "hotspot", Line: 10, Column: 5},
	}) {
		t.Fatal("CPU samples did not localize to the project stack", profile, err)
	}
	for _, tc := range []struct{ name, data string }{
		{"unknown sample node", strings.Replace(knownCPUProfile, "2,3,3", "2,3,99", 1)},
		{"negative sample delta", strings.Replace(knownCPUProfile, "10000,10000,10000", "-1,10000,10000", 1)},
		{"empty samples", strings.Replace(knownCPUProfile, "2,3,3", "", 1)},
		{"invalid clock bounds", strings.Replace(knownCPUProfile, "99999", "10", 1)},
		{"ambiguous parent", strings.Replace(knownCPUProfile, "2,3]", "2,3,3]", 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := summarizeTimeoutProfile([]byte(tc.data), "worker", "/source", "/deps"); err == nil {
				t.Fatal("incomplete CPU evidence accepted")
			}
		})
	}
}

func TestTimeoutProfileCollectionRefusesUnboundedAndUnlocalizedEvidence(t *testing.T) {
	withWorkerNode := strings.Replace(knownCPUProfile, `"children":[2,3]`, `"children":[2,3,4]`, 1)
	withWorkerNode = strings.Replace(withWorkerNode, "\n  ],", `,{"id":4,"callFrame":{"functionName":"runTest","url":"file:///deps/vitest/dist/chunks/test.pinned.js","lineNumber":1,"columnNumber":1}}
  ],`, 1)
	for _, tc := range []struct {
		name  string
		data  string
		limit int
	}{
		{"missing", "", 1 << 20},
		{"outside source and dependencies", strings.ReplaceAll(knownCPUProfile, "/source/frontend", "/unowned"), 1 << 20},
		{"no qualified worker runtime", knownCPUProfile, 1 << 20},
		{"worker runtime never sampled", withWorkerNode, 1 << 20},
		{"oversized", knownCPUProfile, len(knownCPUProfile)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "CPU.20261109.120000.42.0.1.cpuprofile"), []byte(knownCPUProfile), 0600); err != nil {
				t.Fatal(err)
			}
			if tc.data != "" {
				if err := os.WriteFile(filepath.Join(dir, "CPU.20261109.120000.43.0.1.cpuprofile"), []byte(tc.data), 0600); err != nil {
					t.Fatal(err)
				}
			}
			owned, err := os.Lstat(dir)
			if err != nil {
				t.Fatal(err)
			}
			root, err := os.OpenRoot(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			entries, _, err := timeoutScratchEntries(root, owned)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := readTimeoutProfiles(root, entries, 42, "/source", "/deps", &tc.limit); err == nil {
				t.Fatal("missing, unlocalized or oversized worker profile accepted")
			}
		})
	}
}

func TestTimeoutScratchCleanupPreservesReplacedLeaf(t *testing.T) {
	parentDir := t.TempDir()
	dir := filepath.Join(parentDir, "timeout-diagnostic-fixture")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"vitest.json", "vitest-junit.xml"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	parent, err := os.OpenRoot(parentDir)
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()
	name := filepath.Base(dir)
	owned, err := parent.Lstat(name)
	if err != nil {
		t.Fatal(err)
	}
	root, err := parent.OpenRoot(name)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	entries, _, err := timeoutScratchEntries(root, owned)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(dir, "vitest.json"), filepath.Join(parentDir, "preserved.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "vitest.json"), []byte("replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := removeTimeoutScratch(parent, root, name, owned, entries); err == nil {
		t.Fatal("cleanup accepted an unowned replacement")
	}
	for _, name := range []string{"vitest.json", "vitest-junit.xml", "../preserved.json"} {
		if _, err := os.Lstat(filepath.Join(dir, name)); err != nil {
			t.Fatal("cleanup removed evidence before validating the complete inventory", err)
		}
	}
}
