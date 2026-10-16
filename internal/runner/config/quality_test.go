package config

import (
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/runner/core"
)

func TestBaselineExceptionIsExactAndCannotCoverUnavailableProof(t *testing.T) {
	failed := core.CheckObservation{CheckID: "legacy", Status: "failed", ExitCode: 1, OutputDigest: "known-output", Stopped: true}
	report := QualityReport{Quality: Quality{Definition: "done", Checks: []QualityCheck{{ID: "legacy"}}, Baseline: []core.CheckObservation{failed}}, Files: map[string]string{"legacy_test.go": "original"}}
	request := []ExceptionRequest{{CheckID: "legacy", Rationale: "Known unrelated defect", Scope: []string{"legacy_test.go"}}}
	q, err := report.ResolveExceptions(request)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"same", "changed output", "changed status", "changed source", "missing source", "missing result"} {
		t.Run(kind, func(t *testing.T) {
			got := failed
			files := map[string]string{"legacy_test.go": "original"}
			results := []core.CheckObservation{got}
			switch kind {
			case "changed output":
				results[0].OutputDigest = "new failure"
			case "changed status":
				results[0].Status = "unavailable"
			case "changed source":
				files["legacy_test.go"] = "weakened"
			case "missing source":
				delete(files, "legacy_test.go")
			case "missing result":
				results = nil
			}
			if err := q.ValidateResults(results, files); (err == nil) != (kind == "same") {
				t.Fatal(kind, err)
			}
		})
	}
	for _, status := range []string{"passed", "unavailable"} {
		report.Quality.Baseline[0].Status = status
		if _, err = report.ResolveExceptions(request); err == nil {
			t.Fatal("exception accepted for", status)
		}
	}
	report.Quality.Baseline[0] = failed
	for _, scope := range [][]string{nil, {"missing"}, {"../outside"}, {"*.go"}, {"legacy_test.go", "legacy_test.go"}} {
		request[0].Scope = scope
		if _, err = report.ResolveExceptions(request); err == nil {
			t.Fatal("invalid exception scope", scope)
		}
	}
}

func TestRequiredCIIsExplicitAndPartOfTheQualityRevision(t *testing.T) {
	spec := QualitySpec{Definition: "done", Checks: []QualityCheck{{ID: "test", Command: "true", Directory: ".", Prerequisites: []string{"sh"}, Timeout: "1m"}}}
	req := core.CIRequirement{Kind: "check", Name: "test", AppID: 15368}
	for _, tc := range []struct {
		name     string
		required []core.CIRequirement
		valid    bool
	}{
		{"unconfigured", nil, true}, {"check", []core.CIRequirement{req}, true}, {"status", []core.CIRequirement{{Kind: "status", Name: "legacy"}}, true},
		{"missing producer", []core.CIRequirement{{Kind: "check", Name: "test"}}, false},
		{"status producer", []core.CIRequirement{{Kind: "status", Name: "test", AppID: 1}}, false},
		{"duplicate", []core.CIRequirement{req, req}, false}, {"unknown kind", []core.CIRequirement{{Kind: "workflow", Name: "test"}}, false},
		{"control character", []core.CIRequirement{{Kind: "status", Name: "test\n"}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec.RequiredCI = tc.required
			if (spec.Validate() == nil) != tc.valid {
				t.Fatal("wrong requirement validation", tc)
			}
		})
	}
	old := Quality{Definition: "done"}
	next := old
	next.RequiredCI = []core.CIRequirement{req}
	if old.Revision() == next.Revision() {
		t.Fatal("CI requirements omitted from pinned revision")
	}
}

func TestCheckImagePinsPreparedContentInTheAgreement(t *testing.T) {
	spec := QualitySpec{Definition: "done", Checks: []QualityCheck{{ID: "test", Command: "true", Directory: ".", Prerequisites: []string{"sh"}, Timeout: "1m"}}}
	for _, image := range []string{"", "sha256:" + strings.Repeat("a", 64), "ubuntu:24.04", "repo@sha256:" + strings.Repeat("a", 64), "sha256:short"} {
		spec.CheckImage = image
		valid := image == "" || image == "sha256:"+strings.Repeat("a", 64)
		if (spec.Validate() == nil) != valid {
			t.Fatal("image selection must be immutable local content", image)
		}
	}
	native := Quality{Definition: "done"}
	container := native
	container.CheckImage = "sha256:" + strings.Repeat("a", 64)
	if native.Revision() == container.Revision() {
		t.Fatal("check environment omitted from agreement identity")
	}
}

func TestQualityTimingKeepsReadyCompleteAndPlanChecksScoped(t *testing.T) {
	q := Quality{Checks: []QualityCheck{{ID: "task", At: "task"}, {ID: "completion", At: "completion"}, {ID: "both"}}}
	for _, tc := range []struct {
		task  core.Task
		final bool
		want  string
	}{
		{core.Task{}, false, "task completion both"},
		{core.Task{ParentID: "plan"}, false, "task both"},
		{core.Task{PlanRequest: true}, true, "completion both"},
		{core.Task{PlanRequest: true}, false, "task completion both"},
	} {
		names := []string{}
		for _, c := range q.For(tc.task, tc.final).Checks {
			names = append(names, c.ID)
		}
		if strings.Join(names, " ") != tc.want {
			t.Fatal(names, tc.want)
		}
	}
	c := Config{}
	c.Delivery.Mode = "local"
	q.RequiredCI = []core.CIRequirement{{Kind: "check", Name: "test", AppID: 1}}
	if c.ValidateQuality(q) == nil {
		t.Fatal("hosted obligation silently accepted locally")
	}
}
