package config

import (
	"errors"
	"reflect"

	"github.com/cortexium-io/runner/internal/runner"
)

// The local Cortexium route ends at an independently reviewed local candidate.
// It is separate from the frozen Jack release suffix and its native-device gate.
func validateLocalSaaS(p runner.Process, project runner.Project) error {
	if p.ID != "develop-local" {
		return nil
	}
	bad := errors.New("develop-local requires fixed checks, independent source review, reviewed-build browser evidence and independent image review before local integration")
	ids := []string{"authority", "plan", "cards", "join", "owner", "check", "source-review", "browser", "local-review", "integrate"}
	if len(p.Steps) != len(ids) || p.MaxAttempts != 64 || p.MaxAttemptsPerStep != 1 || p.AttemptTimeout != "20m" {
		return bad
	}
	for i, s := range p.Steps {
		if s.ID != ids[i] || s.Reuse || s.Escalation != nil {
			return bad
		}
	}
	check, source, browser, review, integrate := p.Steps[5], p.Steps[6], p.Steps[7], p.Steps[8], p.Steps[9]
	group := p.Steps[2].ForEach
	if group == nil || len(group.Steps) != 3 || group.MaxParallel != 2 {
		return bad
	}
	cp, sp, bp, rp, ip := taskProfile(project, check), taskProfile(project, source), taskProfile(project, browser), taskProfile(project, review), taskProfile(project, integrate)
	for _, profile := range []runner.Profile{sp, rp, taskProfile(project, p.Steps[4]), taskProfile(project, group.Steps[1])} {
		if profile.Model != "gpt-6.1-sol" || profile.Reasoning != "xhigh" || profile.MaxInput != 8<<10 || profile.MaxOutput != 512 {
			return bad
		}
	}
	if taskProfile(project, p.Steps[1]).Mode != "saas-plan" || taskProfile(project, p.Steps[4]).Mode != "genny-source" || taskProfile(project, group.Steps[1]).Mode != "genny-source" {
		return bad
	}
	if cp.Harness != "command" || cp.Command == nil || cp.Command.Network || cp.Command.Artifact != "checks.tar.gz" || cp.Command.Result != "domain.json" || sp.Harness != "codex" || sp.Mode != "saas-source-review" || sp.Access != "workspace-read" || bp.Harness != "release" || bp.Mode != "cortexium-browser" || bp.Access != "input-only" || bp.Command != nil || rp.Harness != "codex" || rp.Mode != "saas-review" || rp.Access != "workspace-read" || ip.Harness != "git-candidate" {
		return bad
	}
	if !reflect.DeepEqual(browser.Inputs, map[string]runner.Binding{"patch": {FromCandidate: true}, "authority": {FromStep: "authority", Output: "artifact"}, "checks": {FromStep: "check", Output: "artifact"}, "verdict": {FromStep: "source-review", Output: "verdict"}}) ||
		!reflect.DeepEqual(check.Inputs, map[string]runner.Binding{"patch": {FromCandidate: true}, "authority": {FromStep: "authority", Output: "artifact"}}) ||
		source.Inputs["patch"] != (runner.Binding{FromCandidate: true}) || source.Inputs["evidence"] != (runner.Binding{FromStep: "check", Output: "artifact"}) || source.Inputs["authority"] != (runner.Binding{FromStep: "authority", Output: "artifact"}) ||
		review.Inputs["patch"] != (runner.Binding{FromCandidate: true}) || review.Inputs["evidence"] != (runner.Binding{FromStep: "browser", Output: "artifact"}) || review.Inputs["authority"] != (runner.Binding{FromStep: "authority", Output: "artifact"}) ||
		integrate.Inputs["patch"] != (runner.Binding{FromCandidate: true}) || integrate.Inputs["verdict"] != (runner.Binding{FromStep: "local-review", Output: "verdict"}) || integrate.Inputs["review"] != (runner.Binding{FromStep: "local-review", Output: "summary"}) {
		return bad
	}
	for _, s := range []runner.Step{check, source, browser, review} {
		next := map[string]string{"check": "source-review", "source-review": "browser", "browser": "local-review", "local-review": "integrate"}[s.ID]
		if !reflect.DeepEqual(s.Routes, map[string]runner.Route{"ready": {Next: next}, "changes_requested": {Next: "owner", MaxTimes: 3}, "blocked": {Next: "$fail"}}) {
			return bad
		}
	}
	for _, s := range p.Steps {
		profile := taskProfile(project, s)
		if s.ID != "browser" && profile.Command != nil && profile.Command.Network {
			return bad
		}
	}
	return nil
}
