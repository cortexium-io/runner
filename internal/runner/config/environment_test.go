package config

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestEnvironmentAndOutputAuthority(t *testing.T) {
	base := QualitySpec{Definition: "done", Preparation: &CheckEnvironmentSpec{Go: "1.26.9", Node: "24.21.0", NPMPackages: []string{"web"}}, Outputs: []CheckOutput{{Path: "web/node_modules"}, {Path: "build", Placeholders: []string{"build/index.html"}}}, Checks: []QualityCheck{{ID: "build", Command: "npm test", Directory: "web", Prerequisites: []string{"npm"}, Timeout: "1m", Artifacts: []string{"build/report.json"}, AutoCPUs: true}}}
	for _, tc := range []struct {
		name  string
		edit  func(*QualitySpec)
		valid bool
	}{
		{"valid", func(*QualitySpec) {}, true},
		{"version", func(s *QualitySpec) { s.Preparation.Node = "24" }, false},
		{"mixed image", func(s *QualitySpec) { s.CheckImage = "sha256:" + strings.Repeat("a", 64) }, false},
		{"npm mount missing", func(s *QualitySpec) { s.Outputs = s.Outputs[1:] }, false},
		{"overlap", func(s *QualitySpec) { s.Outputs = append(s.Outputs, CheckOutput{Path: "build/sub"}) }, false},
		{"root", func(s *QualitySpec) { s.Outputs = append(s.Outputs, CheckOutput{Path: "."}) }, false},
		{"control", func(s *QualitySpec) { s.Outputs = append(s.Outputs, CheckOutput{Path: ".git/hooks"}) }, false},
		{"escape", func(s *QualitySpec) { s.Outputs[1].Path = "../build" }, false},
		{"glob", func(s *QualitySpec) { s.Outputs[1].Path = "build/*" }, false},
		{"manifest", func(s *QualitySpec) { s.Outputs = append(s.Outputs, CheckOutput{Path: "web"}) }, false},
		{"source", func(s *QualitySpec) { s.Sources = []string{"build/index.html"} }, false},
		{"artifact source", func(s *QualitySpec) { s.Checks[0].Artifacts = []string{"web/package.json"} }, false},
		{"artifact alias overlap", func(s *QualitySpec) {
			s.Checks[0].Artifacts = []string{"build", ".runner-output/outputs/build/report.json"}
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, _ := json.Marshal(base)
			var s QualitySpec
			_ = json.Unmarshal(data, &s)
			tc.edit(&s)
			if err := s.Validate(); (err == nil) != tc.valid {
				t.Fatal(err)
			}
		})
	}
	q := Quality{Definition: "done", Outputs: base.Outputs, Preparation: &PreparedEnvironment{Recipe: "v1", Spec: *base.Preparation, Image: "first", Inputs: map[string]string{"web/package-lock.json": "one"}}}
	before := q.Revision()
	q.Preparation.Inputs["web/package-lock.json"] = "two"
	if before == q.Revision() {
		t.Fatal("dependency inputs did not change authority")
	}
	before = q.Revision()
	q.Outputs[1].Placeholders = nil
	if before == q.Revision() {
		t.Fatal("placeholder permission did not change authority")
	}
}
