package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/cortexium-io/runner/internal/runner"
)

func productDeliveryFixture(t *testing.T, file string) (runner.Process, runner.Project) {
	t.Helper()
	_, project := deliveryFixture(t)
	root := filepath.Join("..", "..", "..", "examples", "jacks-break")
	var p runner.Process
	data, err := os.ReadFile(filepath.Join(root, file))
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, &p); err != nil {
		t.Fatal(err)
	}
	for _, step := range p.Steps {
		if _, ok := project.Roles[step.Role]; ok {
			continue
		}
		var role runner.Role
		data, err = os.ReadFile(filepath.Join(root, "roles", step.Role+".json"))
		if err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(data, &role); err != nil {
			t.Fatal(err)
		}
		if err = errors.Join(types(role.Inputs), types(role.Outputs), ready(*role.Execution)); err != nil {
			t.Fatal(step.ID, err)
		}
		project.Roles[step.Role] = role
	}
	return p, project
}

// The combined route must carry both check scopes and actual baseline evidence
// through correction before publishing one cumulative candidate.
func TestCombinedProductDeliveryKeepsBothScopesAndProtectedSuffix(t *testing.T) {
	p, project := productDeliveryFixture(t, "product.json")
	var err error
	if err = errors.Join(validateProcess(p, project), validateReleaseProcess(p, project)); err != nil {
		t.Fatal(err)
	}
	if p.ID != "deliver-product" || p.MaxAttempts != 35 || p.MaxAttemptsPerStep != 1 || p.AttemptTimeout != "20m" {
		t.Fatal("combined delivery changed existing execution ceilings")
	}
	if !reflect.DeepEqual(p.Inputs, map[string]string{
		"request":        "text",
		"package":        "text",
		"tests":          "text",
		"frontend_tests": "text",
		"benchmark":      "text",
	}) {
		t.Fatal("combined intake must keep frontend file paths separate from Go selectors")
	}
	steps := map[string]runner.Step{}
	for _, step := range p.Steps {
		steps[step.ID] = step
	}
	for i, id := range []string{"baseline", "admit", "owner", "frontend-check", "check", "measure", "review", "integrate", "publish"} {
		if p.Steps[i].ID != id {
			t.Fatal("combined stages lost their dependency order", id)
		}
	}
	if steps["owner"].Role != "product-owner" || steps["review"].Role != "product-review" ||
		steps["owner"].Inputs["frontend_tests"] != (runner.Binding{FromInput: "frontend_tests"}) ||
		steps["frontend-check"].Inputs["tests"] != (runner.Binding{FromInput: "frontend_tests"}) ||
		steps["check"].Inputs["tests"] != (runner.Binding{FromInput: "tests"}) ||
		steps["owner"].Inputs["baseline"] != (runner.Binding{FromStep: "baseline", Output: "artifact"}) ||
		steps["review"].Inputs["frontend_checks"] != (runner.Binding{FromStep: "frontend-check", Output: "artifact"}) ||
		steps["review"].Inputs["baseline_measurements"] != (runner.Binding{FromStep: "baseline", Output: "artifact"}) {
		t.Fatal("combined owner/reviewer lost a domain or its actual proof")
	}
	measurement := steps["owner"].Inputs["measurement"]
	if measurement.FromStep != "measure" || measurement.Output != "artifact" || !measurement.Previous || measurement.Fallback == nil ||
		*measurement.Fallback != (runner.Binding{FromStep: "baseline", Output: "artifact"}) {
		t.Fatal("first and correction visits must receive the full applicable evidence")
	}
	for _, id := range []string{"frontend-check", "check", "measure", "review", "integrate"} {
		if steps[id].Inputs["patch"] != (runner.Binding{FromCandidate: true}) {
			t.Fatal("stage must consume the current cumulative candidate", id)
		}
	}
	for _, id := range []string{"frontend-check", "check", "measure", "review", "qualify"} {
		outcome := "changes_requested"
		if id == "measure" {
			outcome = "continue"
		}
		if steps[id].Routes[outcome] != (runner.Route{Next: "owner", MaxTimes: 1}) {
			t.Fatal("correction must retain both scopes in the same bounded owner", id)
		}
	}
}
