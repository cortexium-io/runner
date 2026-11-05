package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	for _, scenario := range []string{"passed", "no tests", "skipped", "failed", "missing report", "infrastructure"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			source, scratch, modules := filepath.Join(root, "source"), filepath.Join(root, "scratch"), filepath.Join(root, "modules")
			for _, name := range []string{"frontend/src", "internal", "server", "cmd", "migrations", ".genny"} {
				if err := os.MkdirAll(filepath.Join(source, name), 0700); err != nil {
					t.Fatal(err)
				}
			}
			for name, data := range map[string]string{"go.mod": "module fixture\n", "go.sum": "", "frontend/src/board.test.ts": "export const source = 'unchanged'\n"} {
				if err := os.WriteFile(filepath.Join(source, name), []byte(data), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.MkdirAll(filepath.Join(modules, "vitest"), 0700); err != nil {
				t.Fatal(err)
			}
			report := `{"success":true,"numTotalTests":1,"numPassedTests":1,"numFailedTests":0,"numPendingTests":0,"testResults":[{"assertionResults":[{"status":"passed"}]}]}`
			switch scenario {
			case "no tests":
				report = `{"success":true,"numTotalTests":0,"numPassedTests":0,"testResults":[]}`
			case "skipped":
				report = `{"success":true,"numTotalTests":1,"numPassedTests":0,"numPendingTests":1,"testResults":[{"assertionResults":[{"status":"pending"}]}]}`
			case "failed":
				report = `{"success":false,"numTotalTests":1,"numPassedTests":0,"numFailedTests":1,"testResults":[{"assertionResults":[{"status":"failed"}]}]}`
			}
			node := filepath.Join(root, "node")
			script := "#!/bin/sh\nwhile [ $# -gt 0 ]; do if [ \"$1\" = --outputFile ]; then shift; target=$1; fi; shift; done\nprintf '%s' '" + report + "' > \"$target\"\n"
			if scenario == "missing report" {
				script = "#!/bin/sh\nexit 0\n"
			}
			if scenario == "failed" {
				script += "exit 1\n"
			}
			if scenario != "infrastructure" {
				if err := os.WriteFile(node, []byte(script), 0700); err != nil {
					t.Fatal(err)
				}
			}
			err := run(options{node: node, modules: modules, source: source, scratch: scratch, tests: `["src/board.test.ts"]`})
			if scenario == "infrastructure" || scenario == "missing report" {
				if err == nil {
					t.Fatal("missing runtime treated as source feedback")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var domain map[string]string
			data, err := os.ReadFile(filepath.Join(scratch, "domain.json"))
			if err != nil || json.Unmarshal(data, &domain) != nil {
				t.Fatal("missing domain result", err)
			}
			if (domain["outcome"] == "ready") != (scenario == "passed") {
				t.Fatal("false passing proof", scenario, domain)
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

func TestSourceCopyRefusesLinksAndCopiesOnlyImportedDocs(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(filepath.Join(source, "frontend/src"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "frontend/src/test.ts"), []byte(`import fixture from '../../docs/used.json'`), 0600); err != nil {
		t.Fatal(err)
	}
	names, err := fixtureImports(source)
	if err != nil || len(names) != 1 || filepath.ToSlash(names[0]) != "docs/used.json" {
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
