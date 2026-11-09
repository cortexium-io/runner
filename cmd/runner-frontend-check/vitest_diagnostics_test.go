package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVitestPlaceholderRequiresMatchingBoundedJUnitEvidence(t *testing.T) {
	const file = "src/fixture.test.ts"
	const xml = `<testsuites><testsuite name="src/fixture.test.ts">
<testcase classname="src/fixture.test.ts" name="fixture &gt; held timer" time="0.0260000001">
<failure message="Test timed out in 25ms." type="Error">rendered stack</failure>
</testcase></testsuite></testsuites>`
	for _, scenario := range []struct {
		name   string
		xml    string
		change func(*vitestReport)
		limit  int
		link   bool
		pass   bool
	}{
		{name: "matching reports", xml: xml, pass: true},
		{name: "missing JUnit"},
		{name: "malformed JUnit", xml: xml[:len(xml)-1]},
		{name: "trailing content", xml: xml + "<unexpected/>"},
		{name: "over remaining capacity", xml: xml, limit: len(xml) - 1},
		{name: "symlink", xml: xml, link: true},
		{name: "wrong file", xml: strings.ReplaceAll(xml, file, "src/other.test.ts")},
		{name: "wrong classname", xml: strings.Replace(xml, `classname="src/fixture.test.ts"`, `classname="src/other.test.ts"`, 1)},
		{name: "wrong test", xml: strings.Replace(xml, "held timer", "other timer", 1)},
		{name: "wrong duration", xml: strings.Replace(xml, "0.0260000001", "0.0260001", 1)},
		{name: "not a failure", xml: strings.Replace(xml, `<failure message="Test timed out in 25ms." type="Error">rendered stack</failure>`, "<skipped/>", 1)},
		{name: "unresolved message", xml: strings.Replace(xml, "Test timed out in 25ms.", "STACK_TRACE_ERROR", 1)},
		{name: "duplicate JUnit assertion", xml: strings.Replace(xml, "</testsuite>", xml[strings.Index(xml, "<testcase"):strings.Index(xml, "</testsuite>")]+"</testsuite>", 1)},
		{
			name: "duplicate JSON assertion",
			xml:  xml,
			change: func(report *vitestReport) {
				report.Results[0].Assertions = append(report.Results[0].Assertions, report.Results[0].Assertions[0])
				report.Total, report.Failed = 2, 2
			},
		},
		{
			name: "missing JSON duration",
			xml:  xml,
			change: func(report *vitestReport) {
				report.Results[0].Assertions[0].Duration = nil
			},
		},
		{
			name: "unmatched second error",
			xml:  xml,
			change: func(report *vitestReport) {
				a := &report.Results[0].Assertions[0]
				a.FailureMessages = append(a.FailureMessages, "Error: STACK_TRACE_ERROR")
			},
		},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			dir := t.TempDir()
			frontend, err := filepath.EvalSymlinks(dir)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(frontend, file)
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("owned test"), 0600); err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			duration := 26.00000008
			report := vitestReport{
				Total:  1,
				Failed: 1,
				Results: []vitestFile{{
					Name:   path,
					Status: "failed",
					Assertions: []vitestAssertion{{
						Status:          "failed",
						FullName:        "fixture held timer",
						Title:           "held timer",
						AncestorTitles:  []string{"fixture"},
						Duration:        &duration,
						FailureMessages: []string{"Error: STACK_TRACE_ERROR\n  at owned fixture"},
					}},
				}},
			}
			if scenario.change != nil {
				scenario.change(&report)
			}
			data, err := json.Marshal(report)
			if err != nil {
				t.Fatal(err)
			}
			p := proof{
				Tests:             []string{file},
				CanonicalFrontend: frontend,
				Files:             []fileProof{{Selected: file, Canonical: path, identity: info}},
				Vitest:            bytes.Clone(data),
			}
			supplement := filepath.Join(dir, "vitest-junit.xml")
			if scenario.xml != "" {
				write := supplement
				if scenario.link {
					write += ".target"
				}
				if err := os.WriteFile(write, []byte(scenario.xml), 0600); err != nil {
					t.Fatal(err)
				}
				if scenario.link {
					if err := os.Symlink(write, supplement); err != nil {
						t.Fatal(err)
					}
				}
			}
			limit := evidenceLimit - len(data)
			if scenario.limit != 0 {
				limit = scenario.limit
			}
			outcome, reason := evaluate(&p, 1, supplement, limit)
			if !bytes.Equal(p.Vitest, data) {
				t.Fatal("diagnostic correlation rewrote the raw JSON report")
			}
			if !scenario.pass {
				if outcome != "blocked" || !strings.Contains(reason, "unresolved Vitest failure") || p.VitestDiagnostics != nil {
					t.Fatal("unverified placeholder became source feedback", outcome, reason, p.VitestDiagnostics)
				}
				return
			}
			d := p.VitestDiagnostics
			if outcome != "changes_requested" || d == nil || d.ReportSHA256 != digest([]byte(xml)) || d.ReportBytes != len(xml) || len(d.Failures) != 1 || d.Failures[0].DurationMS != duration || !strings.Contains(reason, "Test timed out in 25ms.") || strings.Contains(reason, "STACK_TRACE_ERROR") {
				t.Fatal("matched official reporter message was not used faithfully", outcome, reason, d)
			}
		})
	}
}
