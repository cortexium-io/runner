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

func TestArtifactSelectionsAreExplicitAndPinned(t *testing.T) {
	spec := QualitySpec{Definition: "done", Checks: []QualityCheck{{ID: "browser", Command: "true", Directory: ".", Prerequisites: []string{"sh"}, Timeout: "1m"}}}
	for _, paths := range [][]string{nil, {".runner-output/reports"}, {".runner-output/a.json", ".runner-output/images"}} {
		spec.Checks[0].Artifacts = paths
		if err := spec.Validate(); err != nil {
			t.Fatal(paths, err)
		}
	}
	for _, paths := range [][]string{{"."}, {".runner-output"}, {"/tmp/report"}, {".runner-output/../secret"}, {".runner-output/*.png"}, {".runner-output/link\\escape"}, {".runner-output/a", ".runner-output/a"}, {".runner-output/a", ".runner-output/a-other", ".runner-output/a/child"}} {
		spec.Checks[0].Artifacts = paths
		if spec.Validate() == nil {
			t.Fatal("unsafe selection accepted", paths)
		}
	}
	q := Quality{Definition: "done", Checks: []QualityCheck{{ID: "browser"}}}
	before := q.Revision()
	q.Checks[0].Artifacts = []string{".runner-output/reports"}
	if q.Revision() == before {
		t.Fatal("artifact policy omitted from approval")
	}
	if q.ValidateResults([]core.CheckObservation{{CheckID: "browser", Status: "passed", Stopped: true}}, nil) == nil {
		t.Fatal("required artifacts silently omitted")
	}
	if err := q.ValidateResults([]core.CheckObservation{{CheckID: "browser", Status: "unavailable", Stopped: true, Detail: "prepared image unavailable"}}, nil); err == nil || !strings.Contains(err.Error(), "prepared image unavailable") {
		t.Fatal("missing artifact report hid the actual unavailable capability", err)
	}
}

func TestCheckCPUBudgetIsValidatedAndPinned(t *testing.T) {
	spec := QualitySpec{CheckImage: "sha256:" + strings.Repeat("a", 64), Definition: "done", Checks: []QualityCheck{{ID: "test", Command: "true", Directory: ".", Prerequisites: []string{"sh"}, Timeout: "1m"}}}
	for _, tc := range []struct {
		name      string
		cpus      *int
		auto      bool
		container bool
		valid     bool
	}{
		{"default", nil, false, true, true}, {"one", new(1), false, true, true}, {"four", new(4), false, true, true},
		{"automatic", nil, true, true, true}, {"zero", new(0), false, true, false}, {"negative", new(-1), false, true, false},
		{"too many", new(5), false, true, false}, {"conflicting", new(2), true, true, false},
		{"host default", nil, false, false, true}, {"host explicit", new(2), false, false, false}, {"host automatic", nil, true, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := spec
			s.Checks = []QualityCheck{spec.Checks[0]}
			s.Checks[0].CPUs, s.Checks[0].AutoCPUs = tc.cpus, tc.auto
			if !tc.container {
				s.CheckImage = ""
			}
			if err := s.Validate(); (err == nil) != tc.valid {
				t.Fatal("CPU validation", tc, err)
			}
		})
	}
	q := Quality{Definition: "done", CheckImage: spec.CheckImage, Checks: spec.Checks}
	before := q.Revision()
	q.Checks[0].CPUs = new(4)
	if q.Revision() == before {
		t.Fatal("CPU quota omitted from agreement identity")
	}
}
