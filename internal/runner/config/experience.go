package config

import (
	"errors"
	"reflect"

	"github.com/cortexium-io/runner/internal/runner"
)

func validateExperienceSuffix(p runner.Process, project runner.Project, publication, owner int) error {
	bad := errors.New("experience delivery requires authenticated export, independent image review, the existing shared correction allowance and a mandatory native evidence gate before protected preparation")
	if p.ID != "deliver-planned-frontend" || p.Inputs["review_evidence"] != "artifact" || publication+5 >= len(p.Steps) {
		return bad
	}
	qualify, export, review, gate := p.Steps[publication+1], p.Steps[publication+2], p.Steps[publication+3], p.Steps[publication+4]
	profile := taskProfile(project, review)
	if profile.Harness != "codex" || profile.Mode != "experience-review" || profile.Access != "workspace-read" || profile.Model != "gpt-6.1-sol" || profile.Reasoning != "xhigh" || profile.MaxInput != 8<<10 || profile.MaxOutput != 512 || review.Reuse || review.ForEach != nil || review.Escalation != nil ||
		!reflect.DeepEqual(project.Roles[review.Role].Outputs, map[string]string{"verdict": "text", "summary": "text"}) ||
		!reflect.DeepEqual(review.Inputs, map[string]runner.Binding{
			"request":         {FromInput: "request"},
			"receipt":         {FromStep: qualify.ID, Output: "receipt"},
			"evidence":        {FromStep: export.ID, Output: "artifact"},
			"review_evidence": {FromInput: "review_evidence"},
			"checks":          {FromStep: "check", Output: "artifact"},
			"manifest":        {FromStep: "join", Output: "manifest"},
		}) || !reflect.DeepEqual(gate.Inputs, map[string]runner.Binding{
		"receipt":  {FromStep: qualify.ID, Output: "receipt"},
		"evidence": {FromStep: export.ID, Output: "artifact"},
		"verdict":  {FromStep: review.ID, Output: "verdict"},
	}) {
		return bad
	}
	feedbackIndex := publication - 2
	if feedbackIndex <= owner {
		return bad
	}
	feedback := p.Steps[feedbackIndex]
	empty := runner.Text("")
	if taskProfile(project, feedback) != (runner.Profile{Harness: "release", Mode: "experience-feedback", Access: "input-only"}) ||
		!reflect.DeepEqual(feedback.Inputs, map[string]runner.Binding{
			"source_review":     {FromStep: "review", Output: "summary", Previous: true, Default: &empty},
			"experience_review": {FromStep: review.ID, Output: "summary", Previous: true, Default: &empty},
		}) || !reflect.DeepEqual(feedback.Routes, map[string]runner.Route{"ready": {Next: p.Steps[owner].ID, MaxTimes: 6}, "blocked": {Next: "$fail"}}) ||
		p.Steps[feedbackIndex-1].Routes["changes_requested"] != (runner.Route{Next: feedback.ID, MaxTimes: 3}) ||
		!reflect.DeepEqual(p.Steps[owner].Inputs["review"], runner.Binding{FromStep: feedback.ID, Output: "summary", Previous: true, Default: &empty}) {
		return bad
	}
	if !reflect.DeepEqual(review.Routes, map[string]runner.Route{
		"ready":             {Next: gate.ID},
		"changes_requested": {Next: feedback.ID, MaxTimes: qualify.Routes["changes_requested"].MaxTimes},
		"blocked":           {Next: "$fail"},
	}) {
		return bad
	}
	return nil
}
