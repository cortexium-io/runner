package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestConfiguredVitestSerialDiagnostic(t *testing.T) {
	node, modules := os.Getenv("RUNNER_FRONTEND_NODE"), os.Getenv("RUNNER_FRONTEND_MODULES")
	if node == "" || modules == "" {
		t.Skip("set RUNNER_FRONTEND_NODE and RUNNER_FRONTEND_MODULES to qualify serial diagnostics with Vitest")
	}
	const sourceTests = `import { expect, test } from 'vitest';
test('fits a board (one)', () => expect(1).toBe(1));
test('fits a board one', () => expect(1).toBe(2));
`
	for _, tc := range []struct {
		name, testName, status    string
		executed, failed, skipped int
	}{
		{"suite", "", "failed", 2, 1, 0},
		{"exact name", "fits a board (one)", "passed", 1, 0, 1},
		{"assertion fails", "fits a board one", "failed", 1, 1, 1},
		{"missing name", "other assertion", "blocked", 0, 0, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source, scratch, _ := frontendFixture(t, t.TempDir())
			if err := os.WriteFile(filepath.Join(source, "frontend/src/board.test.ts"), []byte(sourceTests), 0600); err != nil {
				t.Fatal(err)
			}
			o := options{node: node, modules: modules, source: source, scratch: scratch,
				tests: `["src/board.test.ts"]`, serial: true, testName: tc.testName}
			if err := run(o); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(scratch, "frontend-check.json"))
			if err != nil {
				t.Fatal(err)
			}
			var got proof
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatal(err)
			}
			if got.Status != tc.status || got.TestName != tc.testName || got.TestWorkers != 1 || got.TestTimeoutMS != 5000 ||
				got.Executed != tc.executed || got.Failed != tc.failed || got.Skipped != tc.skipped {
				t.Fatalf("unexpected diagnostic: status=%s name=%q workers=%d timeout=%d executed=%d failed=%d skipped=%d reason=%s",
					got.Status, got.TestName, got.TestWorkers, got.TestTimeoutMS, got.Executed, got.Failed, got.Skipped, got.Reason)
			}
		})
	}
}

func TestConfiguredVitestPrivateSelection(t *testing.T) {
	node, modules := os.Getenv("RUNNER_FRONTEND_NODE"), os.Getenv("RUNNER_FRONTEND_MODULES")
	if node == "" || modules == "" {
		t.Skip("set RUNNER_FRONTEND_NODE and RUNNER_FRONTEND_MODULES to qualify private test selections with Vitest")
	}
	source, scratch, _ := frontendFixture(t, t.TempDir())
	const selected = "private/games/sudoku/board.test.ts"
	const testSource = `import { expect, test } from 'vitest';
import { value } from '../../../src/board';
test('private test imports source', () => expect(value).toBe('unchanged'));
`
	for name, contents := range map[string]string{
		"frontend/" + selected:  testSource,
		"frontend/src/board.ts": "export const value = 'unchanged';\n",
	} {
		path := filepath.Join(source, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := run(options{
		node:    node,
		modules: modules,
		source:  source,
		scratch: scratch,
		tests:   `["` + selected + `"]`,
		serial:  true,
	}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(scratch, "frontend-check.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got proof
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status != "passed" || got.Executed != 1 || got.Failed != 0 || got.Skipped != 0 ||
		got.TestWorkers != 1 || got.TestTimeoutMS != 5000 || len(got.Files) != 1 || got.Files[0].Selected != selected {
		t.Fatalf("private selection did not produce complete serial proof: %+v", got)
	}
	data, err = os.ReadFile(filepath.Join(source, "frontend", selected))
	if err != nil || string(data) != testSource {
		t.Fatal("read-only private test source changed", err)
	}
}
