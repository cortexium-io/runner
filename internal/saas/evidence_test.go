package saas

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"strings"
	"testing"
)

func validEvidence(t *testing.T) (Evidence, map[string][]byte) {
	t.Helper()
	h := strings.Repeat("a", 64)
	e := Evidence{Version: 1, Kind: "cortexium-local", Status: "passed", Base: strings.Repeat("b", 40), PatchSHA256: h, AuthoritySHA256: h, ServerSHA256: h, FrontendSHA256: h, ChromeSHA256: h, DriverSHA256: h, GoVersion: "go1.27.3", ModuleSHA256: h, LockSHA256: h, GoTests: 2, VueTests: 2, GoVet: true, GoBuild: true, VueTypecheck: true, VueBuild: true, GeneratedOwnership: true, RealWorker: "unverified"}
	for _, name := range RequiredCases {
		e.Cases = append(e.Cases, Case{Name: name, Passed: true, Actions: 2, Witnesses: []string{"normal UI state observed", "durable result verified"}})
	}
	var b bytes.Buffer
	im := image.NewRGBA(image.Rect(0, 0, 390, 844))
	im.Set(0, 0, color.White)
	if err := png.Encode(&b, im); err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{}
	for _, name := range RequiredImages {
		e.Images = append(e.Images, Image{Name: name, SHA256: Digest(b.Bytes()), Width: 390, Height: 844})
		files[name] = b.Bytes()
	}
	for _, name := range append(append([]string{}, RequiredReports...), "reports/browser.json") {
		b := []byte(`{"fixture":"complete declared report"}`)
		files[name] = b
		e.Reports = append(e.Reports, Report{Name: name, SHA256: Digest(b), Bytes: len(b), Complete: true})
	}
	return e, files
}

func TestLocalEvidenceRequiresEveryBoundJourneyAndActualViewport(t *testing.T) {
	e, files := validEvidence(t)
	b, err := Pack(e, files)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) > MaxBundle {
		t.Fatal("fixture exceeded unchanged artifact bound")
	}
	if _, got, err := Decode(b, e.Base, e.PatchSHA256, e.AuthoritySHA256); err != nil || len(got) != 17 {
		t.Fatal(len(got), err)
	}
	for _, tc := range []struct {
		name   string
		change func(*Evidence, map[string][]byte)
	}{
		{"missing case", func(e *Evidence, _ map[string][]byte) { e.Cases = e.Cases[:8] }},
		{"duplicate case", func(e *Evidence, _ map[string][]byte) { e.Cases[1] = e.Cases[0] }},
		{"failed case", func(e *Evidence, _ map[string][]byte) { e.Cases[2].Passed = false }},
		{"no witnesses", func(e *Evidence, _ map[string][]byte) { e.Cases[3].Witnesses = nil }},
		{"wrong dimensions", func(e *Evidence, _ map[string][]byte) { e.Images[0].Width = 1440 }},
		{"wrong screenshot", func(e *Evidence, f map[string][]byte) { f[e.Images[0].Name] = []byte("not a viewport") }},
		{"foreign candidate", func(e *Evidence, _ map[string][]byte) { e.PatchSHA256 = strings.Repeat("c", 64) }},
		{"foreign authority", func(e *Evidence, _ map[string][]byte) { e.AuthoritySHA256 = strings.Repeat("c", 64) }},
		{"missing build", func(e *Evidence, _ map[string][]byte) { e.ServerSHA256 = "" }},
		{"worker overclaim", func(e *Evidence, _ map[string][]byte) { e.RealWorker = "verified" }},
		{"skipped checks", func(e *Evidence, _ map[string][]byte) { e.GoTests = 0 }},
		{"missing report", func(e *Evidence, _ map[string][]byte) { e.Reports = e.Reports[1:] }},
		{"truncated report", func(e *Evidence, _ map[string][]byte) { e.Reports[0].Truncated = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed, f := validEvidence(t)
			tc.change(&changed, f)
			b, err := Pack(changed, f)
			if err == nil {
				_, _, err = Decode(b, e.Base, e.PatchSHA256, e.AuthoritySHA256)
			}
			if err == nil {
				t.Fatal("incomplete/foreign evidence accepted")
			}
		})
	}
	if _, _, err := Decode(b[:len(b)-5], e.Base, e.PatchSHA256, e.AuthoritySHA256); err == nil {
		t.Fatal("truncated archive accepted")
	}
}

func TestSourceChecksAreDistinctFromBrowserAndRealWorkerProof(t *testing.T) {
	e, files := validEvidence(t)
	e.Status = "checked"
	e.Cases = nil
	e.Images = nil
	e.Reports = e.Reports[:len(e.Reports)-1]
	b, _ := Pack(e, files)
	if _, _, err := DecodeCheckBundle(b, e.Base, e.PatchSHA256, e.AuthoritySHA256); err != nil {
		t.Fatal(err)
	}
	e.VueTypecheck = false
	b, _ = Pack(e, files)
	if _, _, err := DecodeCheckBundle(b, e.Base, e.PatchSHA256, e.AuthoritySHA256); err == nil {
		t.Fatal("missing checks accepted")
	}
}

func TestAuthorityUsesTheActualSelectedManifestAndNoLocalReplaces(t *testing.T) {
	h := strings.Repeat("a", 64)
	a := Authority{Version: 1, BriefCommit: BriefCommit, BriefSHA256: BriefSHA256, GennyCommit: GennyCommit, Base: strings.Repeat("b", 40), AppDirectory: "platform", Module: "example.invalid/fixture", GoMod: "module example.invalid/fixture\n\ngo 1.27.0\n\ntoolchain go1.27.3\n", GoSum: "known dependency sum\n", Package: `{"private":true,"dependencies":{"vue":"3.5.45"},"devDependencies":{"vite":"8.3.6","vitest":"5.1.0"}}`, Lock: `{"lockfileVersion":3}`, GoSHA256: h, GennySHA256: h, NodeSHA256: h}
	brief, err := os.ReadFile("../../testdata/saas/first-flow-brief.md")
	if err != nil {
		t.Fatal(err)
	}
	a.Brief, a.Contract, a.ProjectName = string(brief), Contract, "RunnerFixture"
	a.GoCheckSHA256 = h
	a.OperatorPolicySHA256 = h
	a.Runtime = ContainerRuntime{Image: "sha256:" + h, DriverSHA256: h, ChromeSHA256: h, Node: "26.13.1", Playwright: "1.65.0"}
	b, _ := json.Marshal(a)
	if _, err := DecodeAuthority(b, a.Base); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		change func(*Authority)
	}{
		{"wrong base", func(a *Authority) { a.Base = strings.Repeat("c", 40) }},
		{"old brief", func(a *Authority) { a.BriefCommit = strings.Repeat("c", 40) }},
		{"different module", func(a *Authority) { a.Module = "another.invalid/module" }},
		{"local replace", func(a *Authority) { a.GoMod += "\nreplace example.invalid/module => /private/source\n" }},
		{"old frontend", func(a *Authority) { a.Package = strings.ReplaceAll(a.Package, "3.5.45", "3.4.0") }},
		{"traversal", func(a *Authority) { a.AppDirectory = "../app" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := a
			tc.change(&v)
			b, _ := json.Marshal(v)
			if _, err := DecodeAuthority(b, a.Base); err == nil {
				t.Fatal("changed authority accepted")
			}
		})
	}
}
