package config

import (
	"errors"
	"path/filepath"
	"reflect"

	"github.com/cortexium-io/runner/internal/runner"
)

const measuredInvestigationID = "deliver-measured-investigation"

// This one closed intake needs thirteen references to preserve all four reports,
// original evidence and publication authority. Individual roles still have at
// most eight inputs; all resource and protected-release limits are unchanged.
func measuredInvestigationOwner(p runner.Process, project runner.Project, publication int) (int, error) {
	bad := errors.New("measured investigation requires authenticated retained source, complete ordered history, independent initial review and the unchanged protected release suffix within eighteen attempts")
	if p.ID != measuredInvestigationID || publication != 11 || len(p.Steps) <= publication || p.MaxAttempts != 18 || p.MaxAttemptsPerStep != 1 || p.InfrastructureRetries != 1 || p.AttemptTimeout != "20m" || !reflect.DeepEqual(p.Inputs, map[string]string{
		"request": "text", "package": "text", "tests": "text", "benchmark": "text", "patch": "artifact",
		"original_baseline": "artifact", "manifest": "artifact", "publication": "json", "checks": "artifact",
		"measurement1": "artifact", "measurement2": "artifact", "measurement3": "artifact", "measurement4": "artifact",
	}) {
		return 0, bad
	}
	ids := []string{"publication-reference", "initial-history", "admit", "initial-review", "initial-integrate", "owner", "check", "measure", "measurement-history", "review", "integrate"}
	for i, id := range ids {
		s := p.Steps[i]
		if s.ID != id || s.ForEach != nil || s.Reuse || s.Escalation != nil || s.AttemptTimeout != "" {
			return 0, bad
		}
	}
	in := func(name string) runner.Binding { return runner.Binding{FromInput: name} }
	out := func(step, name string) runner.Binding { return runner.Binding{FromStep: step, Output: name} }
	prior := func(step, name, first string) runner.Binding {
		return runner.Binding{FromStep: step, Output: name, Previous: true, Fallback: &runner.Binding{FromStep: first, Output: name}}
	}
	empty := runner.Text("")
	none := runner.Value{Inline: []byte(`{}`)}
	defaulted := func(step, name string, value *runner.Value) runner.Binding {
		return runner.Binding{FromStep: step, Output: name, Previous: true, Default: value}
	}
	candidate := runner.Binding{FromCandidate: true}
	inputs := []map[string]runner.Binding{
		{"receipt": in("publication")},
		{"patch": in("patch"), "checks": in("checks"), "measurement1": in("measurement1"), "measurement2": in("measurement2"), "measurement3": in("measurement3"), "measurement4": in("measurement4")},
		{},
		{"request": in("request"), "patch": in("patch"), "summary": defaulted("owner", "summary", &empty), "measurements": out("initial-history", "artifact"), "baseline_measurements": in("original_baseline"), "manifest": in("manifest"), "checks": in("checks"), "before_measurements": in("measurement1")},
		{"patch": in("patch"), "verdict": out("initial-review", "verdict"), "review": out("initial-review", "summary")},
		{"request": in("request"), "review": prior("review", "summary", "initial-review"), "measurement": prior("measure", "artifact", "initial-history"), "measurement_history": prior("measurement-history", "artifact", "initial-history"), "qualification": defaulted("qualify", "receipt", &none), "baseline": in("original_baseline"), "manifest": in("manifest"), "checks": prior("check", "artifact", "initial-history")},
		{"patch": candidate, "package": in("package"), "tests": in("tests")},
		{"patch": candidate, "package": in("package"), "benchmark": in("benchmark"), "previous": defaulted("measure", "check", &empty)},
		{"previous": prior("measurement-history", "artifact", "initial-history"), "measurement": out("measure", "artifact")},
		{"request": in("request"), "patch": candidate, "summary": out("owner", "summary"), "measurements": out("measurement-history", "artifact"), "baseline_measurements": in("original_baseline"), "manifest": in("manifest"), "checks": out("check", "artifact"), "before_measurements": in("measurement1")},
		{"patch": candidate, "verdict": out("review", "verdict"), "review": out("review", "summary")},
	}
	routes := []map[string]runner.Route{
		{"ready": {Next: "initial-history"}, "blocked": {Next: "$fail"}},
		{"ready": {Next: "admit"}, "blocked": {Next: "$fail"}},
		{"ready": {Next: "initial-review"}, "blocked": {Next: "$fail"}},
		{"ready": {Next: "initial-integrate"}, "changes_requested": {Next: "owner", MaxTimes: 3}, "blocked": {Next: "$fail"}},
		{"ready": {Next: "publish"}},
		{"blocked": {Next: "$fail"}},
		{"ready": {Next: "measure"}, "changes_requested": {Next: "owner", MaxTimes: 3}, "blocked": {Next: "$fail"}},
		{"ready": {Next: "measurement-history"}, "continue": {Next: "measurement-history", MaxTimes: 1}, "changes_requested": {Next: "owner", MaxTimes: 3}, "blocked": {Next: "$fail"}},
		{"ready": {Next: "review"}, "blocked": {Next: "$fail"}},
		{"ready": {Next: "integrate"}, "changes_requested": {Next: "owner", MaxTimes: 3}, "blocked": {Next: "$fail"}},
		nil,
	}
	for i := range ids {
		if !reflect.DeepEqual(p.Steps[i].Inputs, inputs[i]) || !reflect.DeepEqual(p.Steps[i].Routes, routes[i]) {
			return 0, bad
		}
	}
	if !measurementHistoryProfile(project, p.Steps[1], true) || !historyAppendStep(p, project, 8, "initial-history", "measure", "review") {
		return 0, bad
	}
	for i, mode := range map[int]string{0: "publication-reference", 2: "readiness"} {
		if !reflect.DeepEqual(taskProfile(project, p.Steps[i]), runner.Profile{Harness: "release", Mode: mode, Access: "input-only"}) {
			return 0, bad
		}
	}
	for _, i := range []int{3, 5, 9} {
		q := taskProfile(project, p.Steps[i])
		access := "workspace-read"
		if i == 5 {
			access = "workspace-write"
		}
		if q.Harness != "codex" || q.Access != access || q.MaxInput != 8<<10 || q.MaxOutput != 512 || q.Model != "gpt-6.1-sol" || q.Reasoning != "xhigh" {
			return 0, bad
		}
	}
	for _, i := range []int{4, 10} {
		if !reflect.DeepEqual(taskProfile(project, p.Steps[i]), runner.Profile{Harness: "git-candidate", Access: "workspace-read"}) {
			return 0, bad
		}
	}
	if !measuredGoCommand(taskProfile(project, p.Steps[6]), false) || !measuredGoCommand(taskProfile(project, p.Steps[7]), true) {
		return 0, bad
	}
	return 5, nil
}

func measuredGoCommand(p runner.Profile, measurement bool) bool {
	if p.Harness != "command" || p.Access != "workspace-read" || p.Command == nil || len(p.Command.Args) < 3 || !filepath.IsAbs(p.Command.Args[0]) || !filepath.IsAbs(p.Command.Args[2]) || len(p.Command.Env) != 1 || !filepath.IsAbs(p.Command.Env["GOMODCACHE"]) {
		return false
	}
	c := p.Command
	want := runner.Command{Env: map[string]string{"GOMODCACHE": c.Env["GOMODCACHE"]}, Artifact: "proof.json", ExitOutcomes: map[int]string{1: "changes_requested", 2: "blocked"}}
	name := "runner-go-check"
	want.Args = []string{c.Args[0], "--go", c.Args[2], "--package", "{text.package}", "--run", "{text.tests}", "--timeout", "10m", "--scratch", "{scratch}", "--output", "{scratch}/proof.json"}
	if measurement {
		name, want.Result, want.Artifact, want.ExitOutcomes = "runner-measure", "domain.json", "measurements.json", nil
		want.Args = []string{c.Args[0], "--go", c.Args[2], "--source", "{workspace}", "--scratch", "{scratch}", "--package", "{text.package}", "--benchmark", "{text.benchmark}", "--previous", "{text.previous}", "--repair-collection", "--bind-source", "--require-evidence"}
	}
	return filepath.Base(c.Args[0]) == name && reflect.DeepEqual(*c, want)
}
