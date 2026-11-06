package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/runner"
)

func plannedFixture(t *testing.T, example, file string) (runner.Process, runner.Project) {
	t.Helper()
	root := filepath.Join("..", "..", "..", "examples", example)
	var process runner.Process
	read := func(name string, value any) {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		if err = decode(data, value); err != nil {
			t.Fatal(name, err)
		}
	}
	read(file, &process)
	project := runner.Project{Source: &runner.SourceSettings{Ref: strings.Repeat("1", 40)}, Roles: map[string]runner.Role{}}
	var roles func([]runner.Step)
	roles = func(steps []runner.Step) {
		for _, step := range steps {
			if step.ForEach != nil {
				roles(step.ForEach.Steps)
				continue
			}
			if project.Roles[step.Role].ID != "" {
				continue
			}
			var role runner.Role
			read(filepath.Join("roles", step.Role+".json"), &role)
			if err := errors.Join(types(role.Inputs), types(role.Outputs), ready(*role.Execution)); err != nil {
				t.Fatal(step.Role, err)
			}
			if role.Execution.Harness == "task-source" {
				if err := taskSourceContract(role, role.Execution.Mode); err != nil {
					t.Fatal(err)
				}
			}
			project.Roles[role.ID] = role
		}
	}
	roles(process.Steps)
	return process, project
}

func plannedStep(t *testing.T, p *runner.Process, id string) *runner.Step {
	t.Helper()
	for i := range p.Steps {
		if p.Steps[i].ID == id {
			return &p.Steps[i]
		}
	}
	t.Fatal("missing planned step", id)
	return nil
}

func TestPlannedExamplesKeepCombinedProofAndProtectedDelivery(t *testing.T) {
	for _, tc := range []struct{ example, file string }{
		{"reviewed-go", "processes/develop-planned.json"},
		{"jacks-break", "planned-frontend.json"},
		{"jacks-break", "planned-investigation.json"},
		{"jacks-break", "planned-retained-frontend.json"},
		{"jacks-break", "planned-retained-investigation.json"},
	} {
		t.Run(tc.file, func(t *testing.T) {
			p, project := plannedFixture(t, tc.example, tc.file)
			if err := errors.Join(validateProcess(p, project), validateReleaseProcess(p, project)); err != nil {
				t.Fatal(err)
			}
			g := plannedStep(t, &p, "cards").ForEach
			if p.MaxAttempts != 64 || p.MaxAttemptsPerStep != 1 || p.InfrastructureRetries != 1 || g.Mode != "isolated" || g.MaxParallel != 2 || g.ScopeInput != "paths" {
				t.Fatal("planned process lost its one bounded root envelope")
			}
			if g.Steps[1].Inputs["request"] != (runner.Binding{FromInput: "request"}) || g.Output != (runner.Binding{FromStep: "pin", Output: "candidate"}) || g.Steps[2].Routes["changes_requested"] != (runner.Route{Next: "owner", MaxTimes: 3}) {
				t.Fatal("card lost original task, unreviewed pin or scope correction")
			}
			for _, id := range []string{"owner", "review"} {
				step := plannedStep(t, &p, id)
				if step.Inputs["manifest"] != (runner.Binding{FromStep: "join", Output: "manifest"}) || project.Roles[step.Role].Inputs["manifest"] != "artifact" {
					t.Fatal("combined plan evidence was not an explicit artifact", id)
				}
				profile := project.Roles[step.Role].Execution
				if profile.MaxInput != 8<<10 || profile.MaxOutput != 512 || profile.Model != "gpt-6.1-sol" || profile.Reasoning != "xhigh" {
					t.Fatal("coding/review model limits changed", id)
				}
			}
			if project.Roles["planned-plan"].Execution.MaxOutput != 8<<10 || plannedStep(t, &p, "join").Routes["ready"].Next != "check" {
				t.Fatal("initial joined work repeated combined correction or lacked bounded planner output")
			}
			if tc.example == "jacks-break" && (plannedStep(t, &p, "qualify").Routes["changes_requested"].Next != "owner" || !plannedStep(t, &p, "prepare").Routes["ready"].Approval || !plannedStep(t, &p, "backup").Routes["ready"].Approval) {
				t.Fatal("source correction or protected gates were bypassed")
			}
		})
	}
}

func TestPlannedDeliveryRejectsAuthorityAndProofBypasses(t *testing.T) {
	base, project := plannedFixture(t, "jacks-break", "planned-frontend.json")
	for name, change := range map[string]func(*runner.Process, *runner.Project){
		"implicit parallel":  func(p *runner.Process, _ *runner.Project) { plannedStep(t, p, "cards").ForEach.Mode = "" },
		"unknown mode":       func(p *runner.Process, _ *runner.Project) { plannedStep(t, p, "cards").ForEach.Mode = "dynamic" },
		"unbounded parallel": func(p *runner.Process, _ *runner.Project) { plannedStep(t, p, "cards").ForEach.MaxParallel = 9 },
		"missing scope":      func(p *runner.Process, _ *runner.Project) { plannedStep(t, p, "cards").ForEach.ScopeInput = "unknown" },
		"no source":          func(_ *runner.Process, p *runner.Project) { p.Source = nil },
		"dependency intake":  func(p *runner.Process, _ *runner.Project) { p.Inputs["_task_dependencies"] = "json" },
		"original task lost": func(p *runner.Process, _ *runner.Project) {
			plannedStep(t, p, "cards").ForEach.Steps[1].Inputs["request"] = runner.Binding{FromItem: "task"}
		},
		"forged task verdict": func(p *runner.Process, _ *runner.Project) {
			plannedStep(t, p, "cards").ForEach.Steps[2].Role = "delivery-integrate"
		},
		"child release": func(p *runner.Process, _ *runner.Project) {
			plannedStep(t, p, "cards").ForEach.Steps[2].Role = "delivery-publish"
		},
		"last patch masquerades as join": func(p *runner.Process, _ *runner.Project) {
			plannedStep(t, p, "join").Inputs["results"] = runner.Binding{FromStep: "cards", Output: "last"}
		},
		"rerun completed cards": func(p *runner.Process, _ *runner.Project) {
			plannedStep(t, p, "check").Routes["changes_requested"] = runner.Route{Next: "cards", MaxTimes: 1}
		},
		"unbounded plan repair": func(p *runner.Process, _ *runner.Project) {
			plannedStep(t, p, "cards").Routes["invalid_plan"] = runner.Route{Next: "plan"}
		},
		"plan repair after source": func(p *runner.Process, _ *runner.Project) {
			plannedStep(t, p, "check").Routes["invalid_plan"] = runner.Route{Next: "plan", MaxTimes: 2}
		},
		"skip combined checks": func(p *runner.Process, _ *runner.Project) {
			plannedStep(t, p, "join").Routes["ready"] = runner.Route{Next: "integrate"}
		},
		"review missing plan": func(p *runner.Process, _ *runner.Project) { delete(plannedStep(t, p, "review").Inputs, "manifest") },
		"repair missing plan": func(p *runner.Process, _ *runner.Project) { delete(plannedStep(t, p, "owner").Inputs, "manifest") },
		"qualification skips repair": func(p *runner.Process, _ *runner.Project) {
			plannedStep(t, p, "qualify").Routes["changes_requested"] = runner.Route{Next: "integrate", MaxTimes: 1}
		},
		"merge approval waived": func(p *runner.Process, _ *runner.Project) {
			plannedStep(t, p, "prepare").Routes["ready"] = runner.Route{Next: "merge"}
		},
		"production approval waived": func(p *runner.Process, _ *runner.Project) {
			plannedStep(t, p, "backup").Routes["ready"] = runner.Route{Next: "stop"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			p, proj := runner.Clone(base), runner.Clone(project)
			change(&p, &proj)
			if errors.Join(validateProcess(p, proj), validateReleaseProcess(p, proj)) == nil {
				t.Fatal("unsafe planned definition accepted")
			}
		})
	}
}

func TestTaskSourceProfilesAndCapacityAreExplicit(t *testing.T) {
	profile := runner.Profile{Harness: "task-source", Mode: "seed", Access: "workspace-read"}
	if err := ready(profile); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*runner.Profile){
		func(p *runner.Profile) { p.Mode = "approve" },
		func(p *runner.Profile) { p.Access = "workspace-write" },
		func(p *runner.Profile) { p.Model = "unconfigured" },
		func(p *runner.Profile) { p.Command = &runner.Command{} },
	} {
		p := profile
		change(&p)
		if ready(p) == nil {
			t.Fatal("task source acquired an unconfigured capability")
		}
	}
	l, _ := fixture(t)
	if _, err := l.Load(); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"0", "1", "2", "8", "-1", "9"} {
		_, err := l.Set("instance.yaml", "task_concurrency", json.RawMessage(n))
		if (err != nil) != (n == "-1" || n == "9") {
			t.Fatal("unexpected task actor capacity validation", n, err)
		}
	}
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "examples", "reviewed-go", "instance.json"))
	var instance runner.Instance
	if err != nil || json.Unmarshal(data, &instance) != nil {
		t.Fatal("planned instance fixture unavailable", err)
	}
	project, err := filepath.Abs(filepath.Join("..", "..", "..", "examples", "reviewed-go", "project.json"))
	if err != nil {
		t.Fatal(err)
	}
	instance.Projects = []string{project}
	file := filepath.Join(t.TempDir(), "instance.json")
	for _, n := range []int{0, 1, 2} {
		instance.TaskConcurrency = n
		encoded, err := json.Marshal(instance)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(file, encoded, 0600); err != nil {
			t.Fatal(err)
		}
		loader, _ := New(file)
		if _, err = loader.Load(); (err != nil) != (n < 2) {
			t.Fatal("planned group exceeded explicit instance task capacity", n, err)
		}
	}
}

func TestPlannedRetainedDeliveryAuthenticatesSourceBeforeModels(t *testing.T) {
	for _, name := range []string{"frontend", "investigation"} {
		t.Run(name, func(t *testing.T) {
			p, project := plannedFixture(t, "jacks-break", "planned-retained-"+name+".json")
			adopt := plannedStep(t, &p, "source-adopt")
			if err := sourceAdoptionContract(project.Roles[adopt.Role]); err != nil {
				t.Fatal(err)
			}
			index := 0
			if name == "investigation" {
				index = 1
			}
			if p.Steps[index].ID != adopt.ID || p.Steps[index+1].ID != "admit" || adopt.Inputs["patch"] != (runner.Binding{FromInput: "patch"}) || adopt.Inputs["receipt"] != (runner.Binding{FromInput: "publication"}) {
				t.Fatal("retained source bypassed authentication or pristine-baseline order")
			}
			previous := plannedStep(t, &p, "publish").Inputs["previous"]
			if previous.Default != nil || previous.Fallback == nil || *previous.Fallback != (runner.Binding{FromStep: "source-adopt", Output: "receipt"}) {
				t.Fatal("publication lost its authenticated same-PR continuation identity")
			}
			for label, change := range map[string]func(*runner.Process){
				"patch not supplied": func(p *runner.Process) { delete(plannedStep(t, p, "source-adopt").Inputs, "patch") },
				"receipt from source": func(p *runner.Process) {
					plannedStep(t, p, "source-adopt").Inputs["receipt"] = runner.Binding{FromInput: "patch"}
				},
				"origin discarded": func(p *runner.Process) {
					empty := runner.Value{Inline: []byte(`{}`)}
					plannedStep(t, p, "publish").Inputs["previous"] = runner.Binding{FromStep: "publish", Output: "receipt", Previous: true, Default: &empty}
				},
				"skip source auth": func(p *runner.Process) {
					plannedStep(t, p, "source-adopt").Routes["ready"] = runner.Route{Next: "plan"}
				},
			} {
				t.Run(label, func(t *testing.T) {
					bad := runner.Clone(p)
					change(&bad)
					if errors.Join(validateProcess(bad, project), validateReleaseProcess(bad, project)) == nil {
						t.Fatal("unsafe retained delivery accepted")
					}
				})
			}
		})
	}
}

func TestPlannedEvidenceIsFrozenFromPriorProofAndReachesOwners(t *testing.T) {
	base, project := plannedFixture(t, "jacks-break", "planned-investigation.json")
	evidence := runner.Binding{FromStep: "baseline", Output: "artifact"}
	g := plannedStep(t, &base, "cards").ForEach
	if g.Evidence["baseline"] != evidence || plannedStep(t, &base, "plan").Inputs["baseline"] != evidence || g.Steps[1].Inputs["baseline"] != (runner.Binding{FromInput: "_task_evidence_baseline"}) {
		t.Fatal("initial planning/card implementation lost actual baseline evidence")
	}
	for _, id := range []string{"check", "review", "qualify"} {
		if plannedStep(t, &base, id).Routes["changes_requested"].MaxTimes != 3 {
			t.Fatal("new planned investigation lost routine correction policy", id)
		}
	}
	for name, change := range map[string]func(*runner.Process){
		"intake artifact": func(p *runner.Process) {
			plannedStep(t, p, "cards").ForEach.Evidence["baseline"] = runner.Binding{FromInput: "patch"}
		},
		"future artifact": func(p *runner.Process) {
			plannedStep(t, p, "cards").ForEach.Evidence["baseline"] = runner.Binding{FromStep: "measure", Output: "artifact"}
		},
		"current candidate": func(p *runner.Process) {
			plannedStep(t, p, "cards").ForEach.Evidence["baseline"] = runner.Binding{FromCandidate: true}
		},
		"text as proof": func(p *runner.Process) {
			plannedStep(t, p, "cards").ForEach.Evidence["baseline"] = runner.Binding{FromStep: "baseline", Output: "check"}
		},
		"owner misses proof":   func(p *runner.Process) { delete(plannedStep(t, p, "cards").ForEach.Steps[1].Inputs, "baseline") },
		"planner misses proof": func(p *runner.Process) { delete(plannedStep(t, p, "plan").Inputs, "baseline") },
		"reserved intake":      func(p *runner.Process) { p.Inputs["_task_evidence_baseline"] = "artifact" },
		"too much proof": func(p *runner.Process) {
			g := plannedStep(t, p, "cards").ForEach
			for _, name := range []string{"a", "b", "c"} {
				g.Evidence[name] = evidence
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			p := runner.Clone(base)
			change(&p)
			if errors.Join(validateProcess(p, project), validateReleaseProcess(p, project)) == nil {
				t.Fatal("unfrozen or unconsumed task evidence was accepted")
			}
		})
	}
}
