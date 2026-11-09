package config

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/runner"
)

func TestPlannedRetainedResearchEndsWithEvidenceWithoutReleaseAuthority(t *testing.T) {
	p, project := plannedFixture(t, "jacks-break", "planned-retained-research-nonogram.json")
	if err := errors.Join(validateProcess(p, project), validateReleaseProcess(p, project)); err != nil {
		t.Fatal(err)
	}
	if p.ID != "research-planned-retained-nonogram" || p.MaxAttempts != 64 || p.MaxAttemptsPerStep != 1 || p.InfrastructureRetries != 1 || p.AttemptTimeout != "20m" || p.Inputs["prior_patch"] != "artifact" || p.Inputs["patch"] != "" {
		t.Fatal("research lost its shared envelope or treated reference bytes as a source seed")
	}
	order := []string{"baseline", "source-check", "reference", "plan", "cards", "join", "owner", "check", "measure", "review"}
	if len(p.Steps) != len(order) {
		t.Fatal("research acquired an additional execution stage")
	}
	for i, id := range order {
		if p.Steps[i].ID != id {
			t.Fatal("pristine evidence/reference must precede planning and implementation", id)
		}
	}
	baseline, reference := plannedStep(t, &p, "baseline"), plannedStep(t, &p, "reference")
	if baseline.Inputs["benchmark"] != (runner.Binding{FromInput: "baseline_benchmark"}) || baseline.Inputs["patch"] != (runner.Binding{}) ||
		plannedStep(t, &p, "measure").Inputs["benchmark"] != (runner.Binding{FromInput: "benchmark"}) {
		t.Fatal("pristine baseline and opt-in candidate workloads were conflated")
	}
	copy := project.Roles[reference.Role]
	if copy.Execution.Harness != "deterministic" || copy.Execution.Mode != "file" || copy.Execution.Input != "prior_patch" || copy.Execution.Output != "artifact" ||
		!reflect.DeepEqual(copy.Inputs, map[string]string{"prior_patch": "artifact"}) || !reflect.DeepEqual(copy.Outputs, map[string]string{"artifact": "artifact"}) ||
		reference.Inputs["prior_patch"] != (runner.Binding{FromInput: "prior_patch"}) {
		t.Fatal("reference materialization became source restoration or lost its owned artifact")
	}
	plan, group := plannedStep(t, &p, "plan"), plannedStep(t, &p, "cards")
	expected := map[string]runner.Binding{
		"baseline":    {FromStep: "baseline", Output: "artifact"},
		"prior_patch": {FromStep: "reference", Output: "artifact"},
		"checks":      {FromStep: "source-check", Output: "artifact"},
	}
	if !reflect.DeepEqual(group.ForEach.Evidence, expected) || group.ForEach.MaxParallel != 2 {
		t.Fatal("research cards lost immutable evidence or bounded parallelism")
	}
	for name, binding := range expected {
		if plan.Inputs[name] != binding || project.Roles[plan.Role].Inputs[name] != "artifact" ||
			group.ForEach.Steps[1].Inputs[name] != (runner.Binding{FromInput: "_task_evidence_" + name}) || project.Roles[group.ForEach.Steps[1].Role].Inputs[name] != "artifact" {
			t.Fatal("initial research implementation lost full evidence", name)
		}
	}
	if group.Routes["invalid_plan"] != (runner.Route{Next: "plan", MaxTimes: 2}) || group.ForEach.Steps[2].Routes["changes_requested"] != (runner.Route{Next: "owner", MaxTimes: 3}) {
		t.Fatal("planner/card corrections lost their separate finite limits")
	}
	for _, id := range []string{"join", "check", "review"} {
		if plannedStep(t, &p, id).Routes["changes_requested"] != (runner.Route{Next: "owner", MaxTimes: 3}) {
			t.Fatal("combined research correction is unbounded or replans completed cards", id)
		}
	}
	if plannedStep(t, &p, "measure").Routes["continue"] != (runner.Route{Next: "owner", MaxTimes: 1}) || plannedStep(t, &p, "join").Routes["ready"] != (runner.Route{Next: "check"}) {
		t.Fatal("research repeated initial repair or weakened experiment bound")
	}
	var check func([]runner.Step)
	check = func(steps []runner.Step) {
		for _, step := range steps {
			for _, route := range step.Routes {
				if route.Approval {
					t.Fatal("research introduced a protected delivery/approval route")
				}
			}
			if step.ForEach != nil {
				check(step.ForEach.Steps)
				continue
			}
			profile := project.Roles[step.Role].Execution
			if profile.Harness == "release" || profile.Harness == "git-candidate" || profile.Command != nil && profile.Command.Network || step.ID == "publish" || step.ID == "integrate" {
				t.Fatal("research acquired credential-bearing publication or source integration authority", step.ID)
			}
		}
	}
	check(p.Steps)
}

func TestPlannedResearchRetainsHonestNegativeAndFullCombinedProof(t *testing.T) {
	p, project := plannedFixture(t, "jacks-break", "planned-retained-research-nonogram.json")
	_, original := plannedFixture(t, "jacks-break", "retained-research-nonogram.json")
	review := plannedStep(t, &p, "review")
	role := project.Roles[review.Role]
	prior := original.Roles["nonogram-research-review"]
	if !reflect.DeepEqual(role.Outputs, prior.Outputs) || !strings.HasSuffix(role.Execution.Instructions, prior.Execution.Instructions) || role.Execution.Access != "workspace-read" ||
		review.Routes["ready"] != (runner.Route{Next: "$complete"}) || p.Outputs["conclusion"] != (runner.Binding{FromStep: "review", Output: "conclusion"}) {
		t.Fatal("planned research weakened independent evidence/negative-result acceptance")
	}
	negative := map[string]runner.Value{"conclusion": runner.Text("NOT_REPRODUCED"), "summary": runner.Text("Complete bounded evidence supports a negative result; original seed remains unknown.")}
	if err := runner.ValidateValues(negative, role.Outputs); err != nil || role.Outputs["verdict"] != "" || !strings.Contains(role.Execution.Instructions, "REPRODUCED or NOT_REPRODUCED") {
		t.Fatal("complete negative evidence was treated as a product-fix/release verdict", err)
	}
	for _, name := range []string{"baseline", "measurements", "checks", "manifest", "prior_patch"} {
		if role.Inputs[name] != "artifact" || p.Outputs[name].FromStep == "" {
			t.Fatal("research omitted a full retained proof/reference artifact", name)
		}
	}
	for _, id := range []string{"owner", "review"} {
		step := plannedStep(t, &p, id)
		profile := project.Roles[step.Role].Execution
		if step.Inputs["manifest"] != (runner.Binding{FromStep: "join", Output: "manifest"}) || profile.MaxInput != 8<<10 || profile.MaxOutput != 512 {
			t.Fatal("combined research lost accepted card evidence or changed model bounds", id)
		}
	}
	if p.Outputs["patch"] != (runner.Binding{FromCandidate: true}) || plannedStep(t, &p, "owner").Inputs["checks"].Fallback == nil ||
		*plannedStep(t, &p, "owner").Inputs["checks"].Fallback != (runner.Binding{FromStep: "source-check", Output: "artifact"}) {
		t.Fatal("research patch or full initial check evidence was lost")
	}
	bad := runner.Clone(p)
	plannedStep(t, &bad, "cards").ForEach.Evidence["prior_patch"] = runner.Binding{FromInput: "prior_patch"}
	if validateProcess(bad, project) == nil {
		t.Fatal("unowned intake reference bypassed configured artifact materialization")
	}
}
