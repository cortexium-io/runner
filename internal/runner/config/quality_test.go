package config

import (
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
