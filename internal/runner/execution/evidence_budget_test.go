//go:build darwin || linux

package execution

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
)

// Representative private argv/env have the installed pilot's path lengths and
// helper layout. Tests never read its config, token or runtime state.
func privateEvidenceRoles(t *testing.T, file string) (runner.Process, map[string]runner.Role) {
	t.Helper()
	root := filepath.Join("..", "..", "..", "examples", "jacks-break")
	data, err := os.ReadFile(filepath.Join(root, file))
	var p runner.Process
	if err != nil || json.Unmarshal(data, &p) != nil {
		t.Fatal(err)
	}
	roles := map[string]runner.Role{}
	private := "/Users/fixture/.codex/jbg31"
	node := "/Users/fixture/.codex/runner-pilots/jacks-break-20261029.IGAntz/verify/npm-cache/_npx/538786c08bcb9442/node_modules/node/bin/node"
	modules := "/Users/fixture/.codex/runner-pilots/jacks-break-20261029.IGAntz/verify/frontend/node_modules"
	goBinary := "/Users/fixture/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.26.7.darwin-arm64/bin/go"
	for _, step := range p.Steps {
		var role runner.Role
		data, err = os.ReadFile(filepath.Join(root, "roles", step.Role+".json"))
		if err != nil || json.Unmarshal(data, &role) != nil {
			t.Fatal(step.Role, err)
		}
		if command := role.Execution.Command; command != nil {
			replace := strings.NewReplacer("/absolute/path/to/runner-frontend-check", private+"/coordinator/runner-frontend-check", "/absolute/path/to/runner-measure", private+"/coordinator/runner-measure", "/absolute/path/to/runner-go-check", private+"/coordinator/runner-go-check", "/absolute/path/to/node", node, "/absolute/path/to/frontend-node-modules", modules, "/absolute/path/to/go", goBinary, "/absolute/path/to/module-cache", private+"/runtime/modcache")
			for i, arg := range command.Args {
				command.Args[i] = replace.Replace(arg)
			}
			for key, value := range command.Env {
				command.Env[key] = replace.Replace(value)
			}
			if step.Role == "frontend-check" || step.Role == "frontend-source-check" {
				command.Env = map[string]string{"PATH": filepath.Dir(node) + ":/usr/bin:/bin", "OPENSSL_CONF": "/opt/homebrew/etc/openssl@3/openssl.cnf", "RUNNER_NODE_MODULES": modules, "RUNNER_GO_MODULES": private + "/runtime/modcache"}
			}
		}
		roles[role.ID] = role
	}
	return p, roles
}

func evidenceBudgetInputs(file string) map[string]runner.Value {
	// Both real prepared requests are shorter than this bound (1,078/1,466B).
	request := runner.Text(strings.Repeat("bounded evidence request; ", 80))
	if file == "research-nonogram.json" || file == "retained-research-nonogram.json" {
		values := map[string]runner.Value{"request": request, "package": runner.Text("..."), "tests": runner.Text("^Test(RunnerNonogram25Hard.*|Next.*|V2(VariationReplayAndAuthority|AggregateBoundsAndCancellation|LateAndWarmCancellation|PlayerPublicationContract))$"), "baseline_benchmark": runner.Text("^BenchmarkRunnerNonogram25Hard$"), "benchmark": runner.Text("^BenchmarkRunnerNonogram25HardResearch$")}
		if file == "retained-research-nonogram.json" {
			values["prior_patch"] = runner.Value{ArtifactID: "attempt_" + strings.Repeat("A", 26) + "/changes.patch"}
		}
		return values
	}
	paths := `["src/components/player-shell.test.ts","src/pages/home/setup-dialog.test.ts","src/pages/shikaku/ShikakuWorkspace.test.ts","src/pages/nonogram/NonogramWorkspace.test.ts","src/pages/kakuro/KakuroWorkspace.test.ts","src/pages/hashi/HashiWorkspace.test.ts","src/pages/hashi/v3-workspace.test.ts","src/pages/binary/BinaryWorkspace.test.ts","src/pages/calcudoku/CalcudokuWorkspace.test.ts","src/pages/futoshiki/FutoshikiWorkspace.test.ts"]`
	values := map[string]runner.Value{"request": request, "package": runner.Text("..."), "tests": runner.Text(paths), "benchmark": runner.Text("none")}
	if file == "retained-frontend.json" {
		values["patch"] = runner.Value{ArtifactID: "attempt_" + strings.Repeat("A", 26) + "/changes.patch"}
	}
	return values
}

func TestEvidenceWorkflowDefinitionsFitPilotWithPrivateHelperArgv(t *testing.T) {
	for _, file := range []string{"frontend.json", "retained-frontend.json", "research-nonogram.json", "retained-research-nonogram.json"} {
		t.Run(file, func(t *testing.T) {
			p, roles := privateEvidenceRoles(t, file)
			profiles := map[string]string{}
			for name := range roles {
				profiles[name] = ""
			}
			r := runner.Run{ID: "run_" + strings.Repeat("A", 26), ProjectID: "jacks-break", Repository: "/Users/fixture/.codex/jbg31/source", Source: &runner.SourceSettings{Ref: strings.Repeat("f", 40)}, Process: p, Roles: roles, Profiles: profiles, Inputs: evidenceBudgetInputs(file), Status: "Queued", Created: time.Date(2026, 11, 6, 12, 0, 0, 123456789, time.UTC)}
			data, err := json.Marshal(r)
			if err != nil || len(data) > 20<<10 {
				t.Fatalf("definition/inputs/private argv need %d bytes within unchanged half-record %d: %v", len(data), 20<<10, err)
			}
			t.Logf("private definition+inputs %d/%d bytes", len(data), 20<<10)
		})
	}
}

func TestEvidenceOwnerAndReviewPayloadsFitUnchangedNativeLimit(t *testing.T) {
	feedback := runner.Text("command exited 1\n" + strings.Repeat("x", 480))
	// Exercise the complete 768-byte public diagnostic allowance. The producer's
	// own tests verify that escaped/structured diagnostics obey this bound.
	diagnostic := `{"status":"truncated","issue":""}`
	diagnostic = strings.Replace(diagnostic, `"issue":""`, `"issue":"`+strings.Repeat("x", 768-len(diagnostic))+`"`, 1)
	if len(diagnostic) != 768 || !json.Valid([]byte(diagnostic)) {
		t.Fatal("invalid maximum-size diagnostic fixture")
	}
	qualification, _ := json.Marshal(map[string]any{
		"key":             "attempt_" + strings.Repeat("a", 26),
		"run_id":          "run_" + strings.Repeat("b", 26),
		"action":          "qualify",
		"policy_sha256":   strings.Repeat("c", 64),
		"source_ref":      strings.Repeat("d", 40),
		"recovery":        "source_correction_required",
		"note":            strings.Repeat("f", 600),
		"browser_failure": json.RawMessage(diagnostic),
	})
	artifact := runner.Value{ArtifactID: "attempt_" + strings.Repeat("A", 26) + "/result"}
	for _, file := range []string{"frontend.json", "retained-frontend.json", "research-nonogram.json", "retained-research-nonogram.json"} {
		p, roles := privateEvidenceRoles(t, file)
		inputs := evidenceBudgetInputs(file)
		for _, name := range []string{"owner", "review"} {
			t.Run(file+"/"+name, func(t *testing.T) {
				var step runner.Step
				for _, s := range p.Steps {
					if s.ID == name {
						step = s
					}
				}
				role := roles[step.Role]
				values := map[string]runner.Value{}
				for key, kind := range role.Inputs {
					switch {
					case kind == "artifact":
						values[key] = artifact
					case key == "qualification":
						values[key] = runner.Value{Inline: qualification}
					case key == "request" || key == "tests" || key == "benchmark":
						values[key] = inputs[key]
					case key == "summary" || key == "review":
						values[key] = runner.Text(strings.Repeat("r", 240))
					default:
						values[key] = feedback
					}
				}
				if err := runner.ValidateValues(values, role.Inputs); err != nil {
					t.Fatal(err)
				}
				a := runner.Assignment{AttemptID: "attempt_" + strings.Repeat("B", 26), Task: role.Purpose, Setup: runner.ExecutionSetup{Profile: *role.Execution}, Inputs: values, OutputTypes: role.Outputs, Outcomes: []string{"blocked", "changes_requested"}, Deadline: time.Date(2026, 11, 6, 12, 20, 0, 123456789, time.UTC)}
				payload, schema, err := nativePayloadAt(a, a.Deadline.Add(-20*time.Minute))
				if err != nil {
					t.Fatal(err)
				}
				encoded, _ := json.Marshal(schema)
				base := workspaceBase
				if role.Execution.Access == "workspace-read" {
					base = reviewBase
				}
				size := len(payload) + len(encoded) + len(base) + len(role.Execution.Instructions)
				if role.Execution.MaxInput != 8<<10 || role.Execution.MaxOutput != 512 {
					t.Fatal("native ceilings changed")
				}
				t.Logf("native payload %d/%d bytes", size, 8<<10)
			})
		}
	}
}
