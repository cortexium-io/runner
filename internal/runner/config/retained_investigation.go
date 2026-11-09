package config

import (
	"errors"
	"path/filepath"
	"reflect"

	"github.com/cortexium-io/runner/internal/runner"
)

// Retained work carries source/evidence and publication authority separately.
// This closed prefix seeds only after authentication, admits before models, and
// never copies an incomplete candidate measurement as pre-correction evidence.
func retainedInvestigationOwner(p runner.Process, project runner.Project, publication int) (int, error) {
	bad := errors.New("retained investigation requires authenticated publication, retained source/evidence, complete before measurement and bounded correction before protected delivery")
	if publication == 11 {
		if len(p.Steps) <= publication || p.Steps[8].ID != "measurement-history" || !historyAppendStep(p, project, 8, "retain-measurement", "measure", "review") ||
			p.Steps[7].Routes["ready"] != (runner.Route{Next: "measurement-history"}) ||
			!reflect.DeepEqual(p.Steps[5].Inputs["measurement_history"], runner.Binding{FromStep: "measurement-history", Output: "artifact", Previous: true, Fallback: &runner.Binding{FromStep: "retained-measure", Output: "artifact"}}) ||
			p.Steps[9].Inputs["measurements"] != (runner.Binding{FromStep: "measurement-history", Output: "artifact"}) {
			return 0, bad
		}
		p = runner.Clone(p)
		p.Steps = append(p.Steps[:8], p.Steps[9:]...)
		p.Steps[7].Routes["ready"] = runner.Route{Next: "review"}
		p.Steps[8].Inputs["measurements"] = runner.Binding{FromStep: "measure", Output: "artifact"}
		delete(p.Steps[5].Inputs, "measurement_history")
		return retainedInvestigationOwner(p, project, 10)
	}
	if publication != 10 || len(p.Steps) <= publication || p.MaxAttempts != 35 || p.MaxAttemptsPerStep != 1 || p.InfrastructureRetries != 1 || p.AttemptTimeout != "20m" ||
		!reflect.DeepEqual(p.Inputs, map[string]string{
			"request": "text", "package": "text", "tests": "text", "benchmark": "text",
			"patch": "artifact", "original_baseline": "artifact", "manifest": "artifact", "publication": "json",
		}) {
		return 0, bad
	}
	ids := []string{"publication-reference", "retained-check", "retained-measure", "retain-measurement", "admit", "owner", "check", "measure", "review", "integrate"}
	for i, id := range ids {
		s := p.Steps[i]
		if s.ID != id || s.ForEach != nil || s.Reuse || s.Escalation != nil || s.AttemptTimeout != "" {
			return 0, bad
		}
	}
	in := func(name string) runner.Binding { return runner.Binding{FromInput: name} }
	out := func(step, name string) runner.Binding { return runner.Binding{FromStep: step, Output: name} }
	prior := func(step, name, first string) runner.Binding {
		f := out(first, name)
		return runner.Binding{FromStep: step, Output: name, Previous: true, Fallback: &f}
	}
	empty := runner.Value{Inline: []byte(`""`)}
	none := runner.Value{Inline: []byte(`{}`)}
	defaulted := func(step, name string, value *runner.Value) runner.Binding {
		return runner.Binding{FromStep: step, Output: name, Previous: true, Default: value}
	}
	candidate := runner.Binding{FromCandidate: true}
	inputs := []map[string]runner.Binding{
		{"receipt": in("publication")},
		{"patch": in("patch"), "package": in("package"), "tests": in("tests")},
		{
			"patch": in("patch"), "package": in("package"), "benchmark": in("benchmark"),
			"previous": defaulted("retained-measure", "check", &empty),
		},
		{"measurement": prior("measure", "artifact", "retained-measure")},
		{},
		{
			"request":       in("request"),
			"baseline":      in("original_baseline"),
			"manifest":      in("manifest"),
			"checks":        prior("check", "artifact", "retained-check"),
			"measurement":   prior("measure", "artifact", "retained-measure"),
			"review":        defaulted("review", "summary", &empty),
			"qualification": defaulted("qualify", "receipt", &none),
		},
		{"patch": candidate, "package": in("package"), "tests": in("tests")},
		{
			"patch": candidate, "package": in("package"), "benchmark": in("benchmark"),
			"previous": prior("measure", "check", "retained-measure"),
		},
		{
			"request":               in("request"),
			"patch":                 candidate,
			"summary":               out("owner", "summary"),
			"checks":                out("check", "artifact"),
			"measurements":          out("measure", "artifact"),
			"before_measurements":   out("retain-measurement", "artifact"),
			"baseline_measurements": in("original_baseline"),
			"manifest":              in("manifest"),
		},
		{"patch": candidate, "verdict": out("review", "verdict"), "review": out("review", "summary")},
	}
	routes := []map[string]runner.Route{
		{"ready": {Next: "retained-check"}, "blocked": {Next: "$fail"}},
		{"ready": {Next: "retained-measure"}, "changes_requested": {Next: "$fail"}, "blocked": {Next: "$fail"}},
		{"ready": {Next: "retain-measurement"}, "continue": {Next: "retain-measurement"}, "changes_requested": {Next: "admit"}, "blocked": {Next: "$fail"}},
		{"ready": {Next: "admit"}, "blocked": {Next: "$fail"}},
		{"ready": {Next: "owner"}, "blocked": {Next: "$fail"}},
		{"blocked": {Next: "$fail"}},
		{"ready": {Next: "measure"}, "changes_requested": {Next: "owner", MaxTimes: 3}, "blocked": {Next: "$fail"}},
		{"ready": {Next: "review"}, "continue": {Next: "retain-measurement", MaxTimes: 1}, "changes_requested": {Next: "owner", MaxTimes: 3}, "blocked": {Next: "$fail"}},
		{"ready": {Next: "integrate"}, "changes_requested": {Next: "owner", MaxTimes: 3}, "blocked": {Next: "$fail"}},
		nil,
	}
	for i := range ids {
		if !reflect.DeepEqual(p.Steps[i].Inputs, inputs[i]) || !reflect.DeepEqual(p.Steps[i].Routes, routes[i]) {
			return 0, bad
		}
	}
	profiles := make([]runner.Profile, len(ids))
	for i := range profiles {
		profiles[i] = taskProfile(project, p.Steps[i])
	}
	if profiles[0].Harness != "release" || profiles[0].Mode != "publication-reference" || profiles[0].Access != "input-only" || profiles[4].Harness != "release" || profiles[4].Mode != "readiness" {
		return 0, bad
	}
	for _, i := range []int{1, 2, 3, 6, 7} {
		q := profiles[i]
		if q.Harness != "command" || q.Access != "workspace-read" || q.Command == nil || q.Command.Network {
			return 0, bad
		}
	}
	for _, i := range []int{2, 7} {
		q := profiles[i].Command
		if len(q.Args) != 16 || !filepath.IsAbs(q.Args[0]) || filepath.Base(q.Args[0]) != "runner-measure" || !filepath.IsAbs(q.Args[2]) || q.Result != "domain.json" || q.Artifact != "measurements.json" || q.Directory != "" || len(q.ExitOutcomes) != 0 || len(q.Env) != 1 || !filepath.IsAbs(q.Env["GOMODCACHE"]) {
			return 0, bad
		}
		if !reflect.DeepEqual(q.Args, []string{
			q.Args[0], "--go", q.Args[2], "--source", "{workspace}",
			"--scratch", "{scratch}", "--package", "{text.package}",
			"--benchmark", "{text.benchmark}", "--previous", "{text.previous}",
			"--repair-collection", "--bind-source", "--require-evidence",
		}) {
			return 0, bad
		}
	}
	if !reflect.DeepEqual(profiles[3], runner.Profile{
		Harness: "command",
		Access:  "workspace-read",
		Command: &runner.Command{
			Args:     []string{"/bin/cp", "{input.measurement}", "{scratch}/before-measurements.json"},
			Artifact: "before-measurements.json",
		},
	}) {
		return 0, bad
	}
	for _, i := range []int{1, 6} {
		if !measuredGoCommand(profiles[i], false) {
			return 0, bad
		}
	}
	for _, i := range []int{5, 8} {
		q := profiles[i]
		if q.Harness != "codex" || q.MaxInput != 8<<10 || q.MaxOutput != 512 || q.Model != "gpt-6.1-sol" || q.Reasoning != "xhigh" {
			return 0, bad
		}
	}
	if profiles[5].Access != "workspace-write" || profiles[8].Access != "workspace-read" || profiles[9].Harness != "git-candidate" {
		return 0, bad
	}
	return 5, nil
}
