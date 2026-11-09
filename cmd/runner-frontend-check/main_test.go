package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

const fixtureVitestOutput = "for arg do case \"$arg\" in --outputFile.json=*) target=${arg#*=};; esac; done\n"

func TestSelectionsRefuseFlagsPatternsTraversalAndDuplicates(t *testing.T) {
	for _, raw := range []string{`[]`, `["--passWithNoTests"]`, `["../outside.test.ts"]`, `["src/**/*.test.ts"]`, `["src/a.test.ts","src/a.test.ts"]`, `["/tmp/a.test.ts"]`, `["src/a.ts"]`, `"src/a.test.ts"`} {
		if _, err := selections(raw); err == nil {
			t.Fatal("unsafe selection accepted", raw)
		}
	}
	if x, err := selections(`["src/pages/board.test.ts","scripts/assets.test.js"]`); err != nil || len(x) != 2 {
		t.Fatal(x, err)
	}
}

func TestCheckerRequiresExecutedUnskippedAssertions(t *testing.T) {
	for _, scenario := range []string{"passed", "no tests", "skipped", "failed", "long assertion failure", "failed and skipped", "wrong file", "outside same basename", "omitted file", "empty file", "duplicate alias", "invalid counts", "invalid status", "missing field", "malformed", "missing report", "infrastructure", "unexpected exit with report"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			source, scratch, modules := frontendFixture(t, root)
			if err := os.MkdirAll(filepath.Join(modules, "vitest"), 0700); err != nil {
				t.Fatal(err)
			}
			result := vitestReport{
				Success: true,
				Total:   1,
				Passed:  1,
				Results: []vitestFile{{
					Name:       filepath.Join(scratch, "source/frontend/src/board.test.ts"),
					Status:     "passed",
					Assertions: []vitestAssertion{{Status: "passed", FullName: "board fixture asserts expected state"}},
				}},
			}
			code, outcome := 0, "blocked"
			tests := `["src/board.test.ts"]`
			switch scenario {
			case "passed":
				outcome = "ready"
			case "no tests":
				result.Total, result.Passed, result.Results = 0, 0, []vitestFile{}
			case "skipped":
				result.Passed, result.Pending = 0, 1
				result.Results[0].Assertions[0].Status = "pending"
			case "failed", "long assertion failure", "failed and skipped":
				result.Success, result.Passed, result.Failed = false, 0, 1
				result.Results[0].Status = "failed"
				result.Results[0].Assertions[0] = vitestAssertion{Status: "failed", FullName: "board fixture asserts expected state", FailureMessages: []string{"expected visible rectangle; received empty board"}}
				code, outcome = 1, "changes_requested"
				if scenario == "long assertion failure" {
					result.Results[0].Assertions[0].FailureMessages[0] = strings.Repeat("long assertion 😀 <>& evidence ", 200)
				}
				if scenario == "failed and skipped" {
					result.Total, result.Pending = 2, 1
					result.Results[0].Assertions = append(result.Results[0].Assertions, vitestAssertion{Status: "pending", FullName: "required skipped fixture"})
					outcome = "blocked"
				}
			case "wrong file":
				result.Results[0].Name = filepath.Join(scratch, "source/frontend/src/other.test.ts")
			case "outside same basename":
				path := filepath.Join(root, "board.test.ts")
				if err := os.WriteFile(path, []byte("export {}\n"), 0600); err != nil {
					t.Fatal(err)
				}
				result.Results[0].Name = path
			case "omitted file", "empty file":
				tests = `["src/board.test.ts","src/other.test.ts"]`
				if scenario == "empty file" {
					result.Results = append(result.Results, vitestFile{Name: filepath.Join(scratch, "source/frontend/src/other.test.ts"), Status: "passed"})
				}
			case "duplicate alias":
				alias := filepath.Join(root, "scratch-alias")
				if err := os.Symlink(scratch, alias); err != nil {
					t.Fatal(err)
				}
				duplicate := result.Results[0]
				duplicate.Name = filepath.Join(alias, "source/frontend/src/board.test.ts")
				result.Results = append(result.Results, duplicate)
				result.Total, result.Passed = 2, 2
			case "invalid counts":
				result.Total, result.Passed = 2, 2
			case "invalid status":
				result.Results[0].Assertions[0].Status = "unknown"
			case "unexpected exit with report":
				code = 2
			}
			encoded, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "missing field" {
				var fields map[string]json.RawMessage
				if err = json.Unmarshal(encoded, &fields); err != nil {
					t.Fatal(err)
				}
				delete(fields, "numTodoTests")
				encoded, err = json.Marshal(fields)
				if err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "malformed" {
				encoded = []byte(`{"success":`)
			}
			node := filepath.Join(root, "node")
			script := "#!/bin/sh\ncase \"$1\" in */vue-tsc/bin/vue-tsc.js) exit 0;; esac\n" + fixtureVitestOutput + "cat > \"$target\" <<'RUNNER_REPORT'\n" + string(encoded) + "\nRUNNER_REPORT\n"
			if scenario == "missing report" {
				script = "#!/bin/sh\nexit 0\n"
			}
			if code != 0 {
				if code == 1 {
					script += "exit 1\n"
				} else {
					script += "exit 2\n"
				}
			}
			if scenario != "infrastructure" {
				if err := os.WriteFile(node, []byte(script), 0700); err != nil {
					t.Fatal(err)
				}
			}
			err = run(options{node: node, modules: modules, source: source, scratch: scratch, tests: tests})
			if err != nil {
				t.Fatal(err)
			}
			var domain map[string]string
			data, err := os.ReadFile(filepath.Join(scratch, "domain.json"))
			if err != nil || json.Unmarshal(data, &domain) != nil {
				t.Fatal("missing domain result", err)
			}
			if domain["outcome"] != outcome {
				t.Fatal("incorrect evidence classification", scenario, domain)
			}
			if len(domain["reason"]) > 480 || !utf8.ValidString(domain["reason"]) {
				t.Fatal("domain reason violates bounded result contract", len(domain["reason"]), domain)
			}
			var retained proof
			data, err = os.ReadFile(filepath.Join(scratch, "frontend-check.json"))
			if err != nil || json.Unmarshal(data, &retained) != nil {
				t.Fatal("missing proof", err)
			}
			if scenario != "missing report" && scenario != "infrastructure" && scenario != "malformed" {
				actual, err := os.ReadFile(filepath.Join(scratch, "vitest.json"))
				if err != nil || retained.ReportSHA256 != fmtDigest(actual) || !sameJSON(retained.Vitest, actual) {
					t.Fatal("complete raw report was not retained", err, retained.ReportSHA256)
				}
			}
			if scenario == "failed" && (!strings.Contains(retained.Reason, "src/board.test.ts") || !strings.Contains(retained.Reason, "expected visible rectangle")) {
				t.Fatal("source correction lacks actionable assertion evidence", retained.Reason)
			}
			if scenario == "long assertion failure" && (len(retained.Reason) <= len(domain["reason"]) || !utf8.ValidString(retained.Reason)) {
				t.Fatal("artifact diagnostic was reduced to the domain summary", len(retained.Reason), len(domain["reason"]))
			}
			data, _ = os.ReadFile(filepath.Join(source, "frontend/src/board.test.ts"))
			if !strings.Contains(string(data), "unchanged") {
				t.Fatal("source changed")
			}
			if _, err = os.Stat(filepath.Join(modules, ".vite")); !os.IsNotExist(err) {
				t.Fatal("dependency cache mutated")
			}
		})
	}
}

func frontendFixture(t *testing.T, root string) (source, scratch, modules string) {
	t.Helper()
	source, scratch, modules = filepath.Join(root, "source"), filepath.Join(root, "scratch"), filepath.Join(root, "modules")
	for _, name := range []string{"frontend/src", "internal", "server", "cmd", "migrations", ".genny"} {
		if err := os.MkdirAll(filepath.Join(source, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for name, data := range map[string]string{"go.mod": "module fixture\n", "go.sum": "", "frontend/src/board.test.ts": "export const source = 'unchanged'\n", "frontend/src/other.test.ts": "export {}\n"} {
		if err := os.WriteFile(filepath.Join(source, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(source, "frontend", "tsconfig.json"), []byte(`{"compilerOptions":{"target":"ES2022","module":"ESNext","moduleResolution":"Bundler","strict":true,"resolveJsonModule":true,"skipLibCheck":true,"types":["node"]},"include":["src/**/*.ts","src/**/*.vue"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{
		"vue-tsc/package.json":         `{"name":"vue-tsc","version":"3.3.11"}`,
		"vue-tsc/bin/vue-tsc.js":       "// fixture compiler entry\n",
		"typescript/package.json":      `{"name":"typescript","version":"6.0.3"}`,
		"typescript/lib/typescript.js": "// fixture TypeScript entry\n",
	} {
		path := filepath.Join(modules, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return source, scratch, modules
}

func fmtDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func sameJSON(a, b []byte) bool {
	var x, y bytes.Buffer
	return json.Compact(&x, a) == nil && json.Compact(&y, b) == nil && bytes.Equal(x.Bytes(), y.Bytes())
}

func TestConfiguredVitestRetainsRealReportAndCanonicalIdentity(t *testing.T) {
	node, modules := os.Getenv("RUNNER_FRONTEND_NODE"), os.Getenv("RUNNER_FRONTEND_MODULES")
	if node == "" || modules == "" {
		t.Skip("set RUNNER_FRONTEND_NODE and RUNNER_FRONTEND_MODULES to the configured Node24/Vitest runtime for isolated qualification")
	}
	version, err := exec.Command(node, "--version").Output()
	if err != nil || !strings.HasPrefix(string(version), "v24.") {
		t.Fatal("qualification requires configured Node24", string(version), err)
	}
	packageData, err := os.ReadFile(filepath.Join(modules, "vitest", "package.json"))
	var pkg struct{ Version string }
	if err != nil || json.Unmarshal(packageData, &pkg) != nil || pkg.Version == "" {
		t.Fatal("configured Vitest identity is unavailable", err)
	}
	t.Logf("Configured runtime Node=%s Vitest=%s", strings.TrimSpace(string(version)), pkg.Version)
	for _, scenario := range []struct {
		name, tests, outcome string
	}{
		{name: "canonical alias", tests: `["src/pass.test.ts"]`, outcome: "ready"},
		{name: "assertion failure", tests: `["src/fail.test.ts"]`, outcome: "changes_requested"},
		{name: "assertion and timeout", tests: `["src/timeout.test.ts"]`, outcome: "changes_requested"},
		{name: "skipped assertion", tests: `["src/skip.test.ts"]`, outcome: "blocked"},
		{name: "omitted selected file", tests: `["src/pass.test.ts","src/omitted.test.ts"]`, outcome: "blocked"},
		{name: "unexpected substring match", tests: `["src/collision.test.ts"]`, outcome: "blocked"},
		{name: "imported docs and testdata", tests: `["src/nested/fixture-import.test.ts"]`, outcome: "ready"},
		{name: "runtime passes but TypeScript fails", tests: `["src/pass.test.ts"]`, outcome: "changes_requested"},
		{name: "Vue source type failure", tests: `["src/pass.test.ts"]`, outcome: "changes_requested"},
		{name: "combined compiler and assertion failure", tests: `["src/fail.test.ts"]`, outcome: "changes_requested"},
		{name: "compiler syntax prevents suite loading", tests: `["src/pass.test.ts"]`, outcome: "changes_requested"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			root := t.TempDir()
			if runtime.GOOS == "darwin" {
				// /var is the supported system alias that Node/Vite resolve to
				// /private/var. Exercise that real difference, not basename recovery.
				root, err = os.MkdirTemp("/var/tmp", "runner-frontend-check-")
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.RemoveAll(root) })
			}
			source, scratch, _ := frontendFixture(t, root)
			if err := copySource(filepath.Join("..", "..", "testdata", "frontend-check", "frontend"), filepath.Join(source, "frontend")); err != nil {
				t.Fatal(err)
			}
			fixtureBefore := map[string][]byte{}
			for _, name := range []string{"docs", "testdata"} {
				if err := copySource(filepath.Join("..", "..", "testdata", "frontend-check", name), filepath.Join(source, name)); err != nil {
					t.Fatal(err)
				}
				fixtureBefore[name], err = os.ReadFile(filepath.Join(source, name, "imported.json"))
				if err != nil {
					t.Fatal(err)
				}
			}
			if scenario.name == "runtime passes but TypeScript fails" || scenario.name == "combined compiler and assertion failure" {
				path := filepath.Join(source, "frontend", "src", "board.test.ts")
				if err := os.WriteFile(path, []byte("export const source: string = 42\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if scenario.name == "Vue source type failure" {
				path := filepath.Join(source, "frontend", "src", "TypeFixture.vue")
				if err := os.WriteFile(path, []byte("<script setup lang=\"ts\">\nconst label: string = 42\n</script>\n<template>{{ label }}</template>\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if scenario.name == "compiler syntax prevents suite loading" {
				path := filepath.Join(source, "frontend", "src", "pass.test.ts")
				if err := os.WriteFile(path, []byte("import { expect, test } from 'vitest'\nconst value: = 1\ntest('syntax prevents execution', () => expect(value).toBe(1))\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if scenario.name == "assertion and timeout" {
				if pkg.Version != "4.1.11" || strings.TrimSpace(string(version)) != "v24.21.0" {
					t.Fatal("timeout qualification requires configured Node 24.21.0 and Vitest 4.1.11")
				}
				path := filepath.Join(source, "frontend", "src", "timeout.test.ts")
				const fixture = `import { describe, expect, test } from 'vitest'
describe('outer fixture & identity', () => {
  test('ordinary assertion', () => expect('actual', 'ordinary fixture evidence').toBe('expected'))
  test('held timer <expected>', async () => { await new Promise<void>(() => {}) }, 25)
})
`
				if err := os.WriteFile(path, []byte(fixture), 0600); err != nil {
					t.Fatal(err)
				}
			}
			compilerBefore, err := os.ReadFile(filepath.Join(modules, "vue-tsc", "bin", "vue-tsc.js"))
			if err != nil {
				t.Fatal(err)
			}
			projectBefore, err := os.ReadFile(filepath.Join(source, "frontend", "tsconfig.json"))
			if err != nil {
				t.Fatal(err)
			}
			if err := run(options{node: node, modules: modules, source: source, scratch: scratch, tests: scenario.tests}); err != nil {
				t.Fatal(err)
			}
			var domain map[string]string
			data, err := os.ReadFile(filepath.Join(scratch, "domain.json"))
			if err != nil || json.Unmarshal(data, &domain) != nil || domain["outcome"] != scenario.outcome {
				t.Fatal("real Vitest outcome", scenario.name, domain, err)
			}
			if len(domain["reason"]) > 480 || !utf8.ValidString(domain["reason"]) {
				t.Fatal("real assertion diagnostic exceeds the domain contract", len(domain["reason"]))
			}
			var retained proof
			data, err = os.ReadFile(filepath.Join(scratch, "frontend-check.json"))
			if err != nil || json.Unmarshal(data, &retained) != nil {
				t.Fatal("missing real report artifact", err)
			}
			raw, err := os.ReadFile(filepath.Join(scratch, "vitest.json"))
			if err != nil || !sameJSON(retained.Vitest, raw) || retained.ReportSHA256 != fmtDigest(raw) || retained.ReportBytes != len(raw) {
				t.Fatal("raw Vitest report was not completely retained", err)
			}
			if scenario.outcome == "ready" && (len(retained.Files) != 1 || retained.Files[0].Executed != 1) {
				t.Fatal("selected-file execution was not proven", retained.Files)
			}
			if scenario.name == "canonical alias" && runtime.GOOS == "darwin" {
				if retained.Frontend == retained.CanonicalFrontend || retained.Files[0].Canonical != retained.Files[0].Reported[0] {
					t.Fatal("fixture did not prove actual Mac canonical path alias", retained)
				}
			}
			if scenario.name == "imported docs and testdata" {
				for _, scope := range []string{"docs", "testdata"} {
					original := filepath.Join(source, scope, "imported.json")
					copied := filepath.Join(scratch, "source", scope, "imported.json")
					before := fixtureBefore[scope]
					sourceAfter, err := os.ReadFile(original)
					if err != nil || !bytes.Equal(before, sourceAfter) {
						t.Fatal("frontend execution changed source fixture", scope, err)
					}
					got, err := os.ReadFile(copied)
					if err != nil || !bytes.Equal(before, got) {
						t.Fatal("literal imported fixture was not copied", scope, err)
					}
					originalInfo, err := os.Stat(original)
					if err != nil {
						t.Fatal(err)
					}
					copyInfo, err := os.Stat(copied)
					if err != nil || os.SameFile(originalInfo, copyInfo) {
						t.Fatal("fixture copy aliases source", scope, err)
					}
					if _, err = os.Lstat(filepath.Join(scratch, "source", scope, "unrelated.json")); !os.IsNotExist(err) {
						t.Fatal("unrelated fixture was copied", scope, err)
					}
					if err = os.WriteFile(copied, []byte(`{"scratch":"changed"}`), 0600); err != nil {
						t.Fatal(err)
					}
					after, err := os.ReadFile(original)
					if err != nil || !bytes.Equal(before, after) {
						t.Fatal("scratch fixture mutation changed the source", scope, err)
					}
				}
			}
			if scenario.name == "assertion failure" && (!strings.Contains(retained.Reason, "src/fail.test.ts") || !strings.Contains(retained.Reason, "retained fixture assertion")) {
				t.Fatal("real assertion diagnostic was lost", retained.Reason)
			}
			if scenario.name == "assertion failure" {
				var report vitestReport
				if json.Unmarshal(retained.Vitest, &report) != nil || len(report.Results) != 1 || len(report.Results[0].Assertions) != 1 {
					t.Fatal("real assertion report was lost")
				}
				messages := report.Results[0].Assertions[0].FailureMessages
				if len(messages) != 1 || !strings.Contains(messages[0], strings.Repeat("😀", 400)) || len(retained.Reason) <= len(domain["reason"]) {
					t.Fatal("long real assertion message was truncated or replaced by its summary")
				}
			}
			if scenario.name == "assertion and timeout" {
				var report vitestReport
				if json.Unmarshal(retained.Vitest, &report) != nil || len(report.Results) != 1 || len(report.Results[0].Assertions) != 2 {
					t.Fatal("missing real mixed failure report")
				}
				ordinary, timeout := report.Results[0].Assertions[0], report.Results[0].Assertions[1]
				if len(ordinary.FailureMessages) != 1 || !strings.Contains(ordinary.FailureMessages[0], "ordinary fixture evidence") || !hasVitestPlaceholder(timeout.FailureMessages) || timeout.Duration == nil {
					t.Fatal("fixture did not reproduce both exact reporter paths", ordinary, timeout)
				}
				junit, err := os.ReadFile(filepath.Join(scratch, "vitest-junit.xml"))
				diagnostics := retained.VitestDiagnostics
				if err != nil || diagnostics == nil || diagnostics.ReportBytes != len(junit) || diagnostics.ReportSHA256 != fmtDigest(junit) || len(diagnostics.Failures) != 1 {
					t.Fatal("verified supplemental report identity missing", err, diagnostics)
				}
				failure := diagnostics.Failures[0]
				if failure.File != "src/timeout.test.ts" || failure.FullName != timeout.FullName || failure.DurationMS != *timeout.Duration || failure.ErrorIndex != 0 || !strings.HasPrefix(failure.Message, "Test timed out in 25ms.") || !strings.Contains(failure.Message, `"testTimeout"`) || !strings.Contains(domain["reason"], "Test timed out in 25ms.") || retained.Failed != 2 || retained.Executed != 2 {
					t.Fatal("timeout message/duration was lost or joined to the wrong assertion", failure, domain)
				}
			}
			if retained.Typecheck == nil || !retained.Typecheck.Complete || retained.Typecheck.Version != "3.3.11" || retained.Typecheck.TypeScriptVersion != "6.0.3" {
				t.Fatal("pinned compiler receipt is absent or incomplete", retained.Typecheck)
			}
			if scenario.name == "compiler syntax prevents suite loading" {
				if retained.Typecheck.Status != "failed" || !strings.Contains(retained.Typecheck.Output, "TS1110") || !retained.VitestIncomplete || retained.Executed != 0 || retained.Failed != 0 {
					t.Fatal("compiler failure invented assertion execution", retained)
				}
			} else if strings.Contains(scenario.name, "TypeScript fails") || scenario.name == "Vue source type failure" || scenario.name == "combined compiler and assertion failure" {
				if retained.Typecheck.Status != "failed" || !strings.Contains(retained.Typecheck.Output, "TS2322") || len(retained.Typecheck.Diagnostics) == 0 {
					t.Fatal("conclusive TS2322 was not retained", retained.Typecheck)
				}
				if scenario.name == "combined compiler and assertion failure" && retained.Failed != 1 || scenario.name != "combined compiler and assertion failure" && retained.Failed != 0 {
					t.Fatal("both independent compiler and Vitest results were not retained", retained.Failed)
				}
			} else if retained.Typecheck.Status != "passed" {
				t.Fatal("valid project did not pass Vue/TypeScript", retained.Typecheck)
			}
			compilerAfter, compilerErr := os.ReadFile(filepath.Join(modules, "vue-tsc", "bin", "vue-tsc.js"))
			projectAfter, projectErr := os.ReadFile(filepath.Join(source, "frontend", "tsconfig.json"))
			if compilerErr != nil || projectErr != nil || !bytes.Equal(compilerBefore, compilerAfter) || !bytes.Equal(projectBefore, projectAfter) {
				t.Fatal("typecheck changed source configuration or immutable runtime", compilerErr, projectErr)
			}
			t.Logf("Node=%s scenario=%s outcome=%s raw_bytes=%d raw_sha256=%s frontend=%s canonical=%s", strings.TrimSpace(string(version)), scenario.name, domain["outcome"], len(raw), retained.ReportSHA256, retained.Frontend, retained.CanonicalFrontend)
		})
	}
}

func TestCompilerFailureDoesNotBypassVitestIdentityOrRuntimeEvidence(t *testing.T) {
	for _, scenario := range []string{"transform", "runtime", "duplicate", "unexpected", "omitted", "counts", "skipped", "compiler passed"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			source, scratch, modules := frontendFixture(t, root)
			frontend := filepath.Join(scratch, "source", "frontend")
			load := vitestFile{Name: filepath.Join(frontend, "src", "board.test.ts"), Status: "failed", Message: "Transform failed with 1 error: source syntax"}
			other := vitestFile{Name: filepath.Join(frontend, "src", "other.test.ts"), Status: "passed", Assertions: []vitestAssertion{{Status: "passed", FullName: "other source still executes"}}}
			report := vitestReport{Total: 1, Passed: 1, Results: []vitestFile{load, other}}
			want := "blocked"
			switch scenario {
			case "transform":
				want = "changes_requested"
			case "runtime":
				report.Results[0].Message = "Error: verifier runtime dependency unavailable"
			case "duplicate":
				report.Results = append(report.Results, load)
			case "unexpected":
				report.Results[0].Name = filepath.Join(root, "board.test.ts")
				if err := os.WriteFile(report.Results[0].Name, []byte("export {}"), 0600); err != nil {
					t.Fatal(err)
				}
			case "omitted":
				report.Results = report.Results[:1]
				report.Total, report.Passed = 0, 0
			case "counts":
				report.Total = 2
			case "skipped":
				report.Passed, report.Pending = 0, 1
				report.Results[1].Assertions[0].Status = "pending"
			}
			encoded, err := json.Marshal(report)
			if err != nil {
				t.Fatal(err)
			}
			compiler := "printf '%s\\n' 'src/board.test.ts(1,14): error TS1110: Type expected.'; exit 2"
			if scenario == "compiler passed" {
				compiler = "exit 0"
			}
			script := "#!/bin/sh\ncase \"$1\" in */vue-tsc/bin/vue-tsc.js) " + compiler + ";; esac\n" + fixtureVitestOutput + "cat > \"$target\" <<'RUNNER_REPORT'\n" + string(encoded) + "\nRUNNER_REPORT\nexit 1\n"
			node := filepath.Join(root, "node")
			if err := os.WriteFile(node, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			if err := run(options{node: node, modules: modules, source: source, scratch: scratch, tests: `["src/board.test.ts","src/other.test.ts"]`}); err != nil {
				t.Fatal(err)
			}
			var domain map[string]string
			data, err := os.ReadFile(filepath.Join(scratch, "domain.json"))
			if err != nil || json.Unmarshal(data, &domain) != nil || domain["outcome"] != want {
				t.Fatal("compiler failure bypassed verifier identity/runtime contract", domain, err)
			}
			if want == "changes_requested" {
				var p proof
				data, err = os.ReadFile(filepath.Join(scratch, "frontend-check.json"))
				if err != nil || json.Unmarshal(data, &p) != nil || !p.VitestIncomplete || p.Executed != 1 || p.Failed != 0 {
					t.Fatal("compiler feedback invented or concealed assertion execution", p, err)
				}
			}
		})
	}
}

func TestClipIncludesEllipsisInByteLimit(t *testing.T) {
	for _, limit := range []int{0, 1, 2, 3, 4, 479, 480, 512} {
		clipped := clip(strings.Repeat("😀 evidence ", 100), limit)
		if len(clipped) > limit || !utf8.ValidString(clipped) {
			t.Fatal("invalid bounded UTF-8 summary", limit, len(clipped), clipped)
		}
	}
}

func TestReportEncodingRetainsHTMLWithoutExceedingExistingArtifactLimit(t *testing.T) {
	// HTML-heavy assertion messages used to inflate sixfold under json.Marshal.
	raw := []byte(`{"message":"` + strings.Repeat("<>&", (8<<20)/3-10) + `"}`)
	if len(raw) > 8<<20 || !json.Valid(raw) {
		t.Fatal("invalid bound fixture", len(raw))
	}
	path := filepath.Join(t.TempDir(), "frontend-check.json")
	if err := writeJSON(path, proof{Schema: 2, Status: "blocked", Vitest: raw}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	var retained proof
	if err != nil || len(data) > 16<<20 || json.Unmarshal(data, &retained) != nil || !sameJSON(retained.Vitest, raw) {
		t.Fatal("raw report inflated or lost", len(data), err)
	}
	if err = writeJSON(path, map[string]string{"message": strings.Repeat("x", 16<<20)}); err != errArtifactLimit {
		t.Fatal("encoded artifact limit was not enforced", err)
	}
}

func TestSourceCopyRefusesLinksAndFindsOnlyImportedFixtures(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(filepath.Join(source, "frontend/src"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, scope := range []string{"docs", "testdata"} {
		if err := os.MkdirAll(filepath.Join(source, scope), 0700); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"used.json", "unrelated.json"} {
			if err := os.WriteFile(filepath.Join(source, scope, name), []byte(`{"fixture":true}`), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	imports := "import fixture from '../../docs/used.json'\nimport data from '../../testdata/used.json'\nconst again = import('../../docs/used.json')\n"
	if err := os.WriteFile(filepath.Join(source, "frontend/src/test.ts"), []byte(imports), 0600); err != nil {
		t.Fatal(err)
	}
	names, err := fixtureImports(source)
	if err != nil || len(names) != 2 || filepath.ToSlash(names[0]) != "docs/used.json" || filepath.ToSlash(names[1]) != "testdata/used.json" {
		t.Fatal(names, err)
	}
	private := filepath.Join(root, "private")
	if err := os.WriteFile(private, []byte("sentinel"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(source, "frontend/src/link.test.ts")
	if err := os.Symlink(private, link); err != nil {
		t.Fatal(err)
	}
	if err := copySource(filepath.Join(source, "frontend"), filepath.Join(root, "copy")); err == nil {
		t.Fatal("source copy followed symlink")
	}
}

func TestFixturePreparationBlocksUnsafeOrMissingImportsBeforeExecution(t *testing.T) {
	for _, scenario := range []string{"missing", "absolute", "escape", "parent symlink", "nested parent symlink", "leaf symlink", "directory leaf"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			source, scratch, modules := frontendFixture(t, root)
			private := filepath.Join(root, "outside")
			if err := os.Mkdir(private, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(private, "used.json"), []byte(`{"private":"sentinel"}`), 0600); err != nil {
				t.Fatal(err)
			}
			ref := "../../testdata/used.json"
			path := filepath.Join(source, "testdata")
			if scenario == "parent symlink" {
				if err := os.Symlink(private, path); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Mkdir(path, 0700); err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "absolute":
				ref = filepath.Join(private, "used.json")
			case "escape":
				ref = "../../../outside/used.json"
			case "nested parent symlink":
				ref = "../../testdata/nested/used.json"
				if err := os.Symlink(private, filepath.Join(path, "nested")); err != nil {
					t.Fatal(err)
				}
			case "leaf symlink":
				if err := os.Symlink(filepath.Join(private, "used.json"), filepath.Join(path, "used.json")); err != nil {
					t.Fatal(err)
				}
			case "directory leaf":
				if err := os.Mkdir(filepath.Join(path, "used.json"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(source, "frontend/src/board.test.ts"), []byte("import fixture from '"+ref+"'\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := run(options{node: filepath.Join(root, "unavailable-node"), modules: modules, source: source, scratch: scratch, tests: `["src/board.test.ts"]`}); err != nil {
				t.Fatal(err)
			}
			var domain map[string]string
			data, err := os.ReadFile(filepath.Join(scratch, "domain.json"))
			if err != nil || json.Unmarshal(data, &domain) != nil || domain["outcome"] != "blocked" || !strings.HasPrefix(domain["reason"], "frontend fixture preparation:") {
				t.Fatal("unsafe or missing fixture reached execution", scenario, domain, err)
			}
			if _, err := os.Lstat(filepath.Join(scratch, "source", "frontend", "node_modules")); !os.IsNotExist(err) {
				t.Fatal("fixture failure reached runtime preparation", err)
			}
			if data, err = os.ReadFile(filepath.Join(private, "used.json")); err != nil || string(data) != `{"private":"sentinel"}` {
				t.Fatal("outside fixture changed", err)
			}
		})
	}
}

func TestTypecheckPreservesConclusiveDiagnosticsAndBlocksIncompleteExecution(t *testing.T) {
	for _, scenario := range []string{"passed", "source failure", "runtime failure", "missing dependency diagnostic", "global configuration diagnostic", "outside diagnostic", "missing tool", "missing configuration", "missing metadata", "missing TypeScript", "launch failure", "cancelled", "deadline"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			source, _, modules := frontendFixture(t, root)
			frontend := filepath.Join(source, "frontend")
			canonical, err := filepath.EvalSymlinks(frontend)
			if err != nil {
				t.Fatal(err)
			}
			node := filepath.Join(root, "node")
			message, exitCode, expected := "", "0", "blocked"
			switch scenario {
			case "passed":
				expected = "passed"
			case "source failure":
				message = "src/board.test.ts(1,14): error TS2322: Type 'number' is not assignable to type 'string'.\n  Complete continuation evidence\n"
				exitCode, expected = "2", "failed"
			case "runtime failure":
				message, exitCode = "Error: Cannot find module '@volar/typescript'\n", "1"
			case "missing dependency diagnostic":
				message, exitCode = "src/board.test.ts(1,1): error TS2307: Cannot find module 'missing-input'.\n", "2"
			case "global configuration diagnostic":
				message, exitCode = "error TS18003: No inputs were found in config file.\n", "2"
			case "outside diagnostic":
				message, exitCode = "../outside.ts(1,1): error TS2322: Outside source error.\n", "2"
			case "missing tool":
				err = os.Remove(filepath.Join(modules, "vue-tsc", "bin", "vue-tsc.js"))
			case "missing configuration":
				err = os.Remove(filepath.Join(frontend, "tsconfig.json"))
			case "missing metadata":
				err = os.Remove(filepath.Join(modules, "typescript", "package.json"))
			case "missing TypeScript":
				err = os.Remove(filepath.Join(modules, "typescript", "lib", "typescript.js"))
			}
			if err != nil {
				t.Fatal(err)
			}
			if scenario != "launch failure" {
				script := "#!/bin/sh\n[ \"$#\" = 6 ] && [ \"$2\" = --noEmit ] && [ \"$3\" = --pretty ] && [ \"$4\" = false ] && [ \"$5\" = --project ] && [ \"$6\" = tsconfig.json ] || exit 99\ncat <<'RUNNER_DIAGNOSTIC'\n" + message + "RUNNER_DIAGNOSTIC\nexit " + exitCode + "\n"
				if err = os.WriteFile(node, []byte(script), 0700); err != nil {
					t.Fatal(err)
				}
			}
			ctx := t.Context()
			if scenario == "cancelled" {
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
			}
			if scenario == "deadline" {
				expired, cancel := context.WithDeadline(ctx, time.Now().Add(-time.Second))
				defer cancel()
				ctx = expired
			}
			p := checkTypes(ctx, options{node: node, modules: modules}, frontend, canonical)
			if p.Status != expected {
				t.Fatal("incorrect typecheck classification", scenario, p)
			}
			if scenario == "source failure" && (!p.Complete || p.Output != message || len(p.Diagnostics) != 1 || p.Diagnostics[0].Code != 2322 || p.Diagnostics[0].File != filepath.Join(canonical, "src", "board.test.ts")) {
				t.Fatal("complete source diagnostic was not retained", p)
			}
			if (scenario == "cancelled" || scenario == "deadline" || scenario == "launch failure") && p.Complete {
				t.Fatal("unestablished compiler execution was marked complete", p)
			}
		})
	}
}

func TestTypecheckAndVitestShareDeadlineAndRawEvidenceBounds(t *testing.T) {
	for _, scenario := range []string{"expired shared deadline", "compiler overflow", "shared raw budget"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			source, scratch, modules := frontendFixture(t, root)
			node := filepath.Join(root, "node")
			script := "#!/bin/sh\nexec /usr/bin/head -c 8388609 /dev/zero\n"
			if scenario == "shared raw budget" {
				script = "#!/bin/sh\ncase \"$1\" in */vue-tsc/bin/vue-tsc.js) printf '%s' \"src/board.test.ts(1,14): error TS2322: \"; /usr/bin/head -c 1048576 /dev/zero | /usr/bin/tr '\\000' x; printf '\\n'; exit 2;; esac\n" + fixtureVitestOutput + "{ printf '%s' '{\"padding\":\"'; /usr/bin/head -c 7340032 /dev/zero | /usr/bin/tr '\\000' x; printf '%s' '\"}'; } > \"$target\"\n"
			}
			if err := os.WriteFile(node, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			ctx := t.Context()
			if scenario == "expired shared deadline" {
				expired, cancel := context.WithDeadline(ctx, time.Now().Add(-time.Second))
				defer cancel()
				ctx = expired
			}
			if err := runContext(ctx, options{node: node, modules: modules, source: source, scratch: scratch, tests: `["src/board.test.ts"]`}); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(scratch, "frontend-check.json"))
			var p proof
			if err != nil || len(data) > 16<<20 || json.Unmarshal(data, &p) != nil || p.Status != "blocked" || p.Typecheck == nil {
				t.Fatal("incomplete compiler evidence did not remain bounded and blocked", len(data), err)
			}
			if scenario != "shared raw budget" && p.Typecheck.Complete {
				t.Fatal("incomplete compiler execution was marked complete")
			}
			if len(p.Vitest) != 0 || p.Executed != 0 {
				t.Fatal("Vitest ran after an unestablished compiler execution")
			}
			if scenario == "compiler overflow" && p.Typecheck.OutputBytes != evidenceLimit {
				t.Fatal("raw compiler cap was bypassed", p.Typecheck.OutputBytes)
			}
			if scenario == "shared raw budget" && (!p.Typecheck.Complete || !strings.Contains(p.Reason, "oversized") || p.ReportBytes != 0) {
				t.Fatal("compiler and Vitest gained separate raw evidence allowances", p.Typecheck, p.Reason)
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	output := &completeOutput{limit: 8, cancel: cancel}
	if _, err := output.ReadFrom(strings.NewReader("0123456789")); err == nil || output.Len() != 8 || !output.overflow || ctx.Err() == nil {
		t.Fatal("ReaderFrom bypassed complete evidence capture limit")
	}
}
