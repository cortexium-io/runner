package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unicode/utf8"
)

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
			script := "#!/bin/sh\nwhile [ $# -gt 0 ]; do if [ \"$1\" = --outputFile ]; then shift; target=$1; fi; shift; done\ncat > \"$target\" <<'RUNNER_REPORT'\n" + string(encoded) + "\nRUNNER_REPORT\n"
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
		{name: "skipped assertion", tests: `["src/skip.test.ts"]`, outcome: "blocked"},
		{name: "omitted selected file", tests: `["src/pass.test.ts","src/omitted.test.ts"]`, outcome: "blocked"},
		{name: "unexpected substring match", tests: `["src/collision.test.ts"]`, outcome: "blocked"},
		{name: "imported docs and testdata", tests: `["src/nested/fixture-import.test.ts"]`, outcome: "ready"},
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
			if scenario.outcome == "changes_requested" && (!strings.Contains(retained.Reason, "src/fail.test.ts") || !strings.Contains(retained.Reason, "retained fixture assertion")) {
				t.Fatal("real assertion diagnostic was lost", retained.Reason)
			}
			if scenario.outcome == "changes_requested" {
				var report vitestReport
				if json.Unmarshal(retained.Vitest, &report) != nil || len(report.Results) != 1 || len(report.Results[0].Assertions) != 1 {
					t.Fatal("real assertion report was lost")
				}
				messages := report.Results[0].Assertions[0].FailureMessages
				if len(messages) != 1 || !strings.Contains(messages[0], strings.Repeat("😀", 400)) || len(retained.Reason) <= len(domain["reason"]) {
					t.Fatal("long real assertion message was truncated or replaced by its summary")
				}
			}
			t.Logf("Node=%s scenario=%s outcome=%s raw_bytes=%d raw_sha256=%s frontend=%s canonical=%s", strings.TrimSpace(string(version)), scenario.name, domain["outcome"], len(raw), retained.ReportSHA256, retained.Frontend, retained.CanonicalFrontend)
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
