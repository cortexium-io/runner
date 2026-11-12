package execution

import (
	"bytes"
	"encoding/json"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/runner"
	"github.com/cortexium-io/runner/internal/saas"
	"github.com/cortexium-io/runner/testdata/saas/proof"
)

func TestLocalImagesUseSavedCandidateAndFrozenAuthority(t *testing.T) {
	disk := workspaceDisk(t)
	base, h := strings.Repeat("a", 40), strings.Repeat("b", 64)
	authority := saas.Authority{Version: 1, BriefCommit: saas.BriefCommit, BriefSHA256: saas.BriefSHA256, GennyCommit: saas.GennyCommit, Base: base, AppDirectory: "platform", Module: "example.invalid/fixture", GoMod: "module example.invalid/fixture\n\ngo 1.27.0\n\ntoolchain go1.27.3\n", GoSum: "pinned\n", Package: `{"private":true,"dependencies":{"vue":"3.5.45"},"devDependencies":{"vite":"8.3.6","vitest":"5.1.0"}}`, Lock: `{"lockfileVersion":3}`, GoSHA256: h, GennySHA256: h, NodeSHA256: h}
	brief, err := os.ReadFile("../../../testdata/saas/first-flow-brief.md")
	if err != nil {
		t.Fatal(err)
	}
	authority.Brief, authority.Contract, authority.ProjectName = string(brief), saas.Contract, "RunnerFixture"
	authority.GoCheckSHA256 = h
	authority.OperatorPolicySHA256 = h
	authority.Runtime = saas.ContainerRuntime{Image: "sha256:" + h, DriverSHA256: h, ChromeSHA256: h, Node: "26.13.1", Playwright: "1.65.0"}
	ab, _ := json.Marshal(authority)
	patch := []byte("authenticated fixture patch")
	e := saas.Evidence{Version: 1, Kind: "cortexium-local", Status: "passed", Base: base, PatchSHA256: saas.Digest(patch), AuthoritySHA256: saas.Digest(ab), ServerSHA256: h, FrontendSHA256: h, ChromeSHA256: h, DriverSHA256: h, ModuleSHA256: h, LockSHA256: h, GoVersion: "go1.27.3", GoTests: 1, VueTests: 1, GoVet: true, GoBuild: true, VueTypecheck: true, VueBuild: true, GeneratedOwnership: true, RealWorker: "unverified"}
	e.ModuleSHA256, e.LockSHA256 = saas.Digest([]byte(authority.GoMod)), saas.Digest([]byte(authority.Lock))
	server := saasSyntheticBuild(t, &e, authority)
	for _, name := range saas.RequiredCases {
		e.Cases = append(e.Cases, saas.Case{Name: name, Passed: true, Actions: 2, Witnesses: []string{"observed input", "observed result"}})
	}
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 390, 844))); err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{}
	for _, name := range saas.RequiredImages {
		files[name] = b.Bytes()
		e.Images = append(e.Images, saas.Image{Name: name, SHA256: saas.Digest(b.Bytes()), Width: 390, Height: 844})
	}
	for _, name := range append(append([]string{}, saas.RequiredReports...), "reports/browser.json") {
		data := []byte(`{"fixture":"complete report"}`)
		files[name] = data
		e.Reports = append(e.Reports, saas.Report{Name: name, SHA256: saas.Digest(data), Bytes: len(data), Complete: true})
	}
	data, err := saas.Pack(e, files)
	if err != nil {
		t.Fatal(err)
	}
	for name, b := range map[string][]byte{"authority": ab, "patch": patch, "evidence": data} {
		if err = disk.Write("artifacts/saas/"+name, b, 16<<20); err != nil {
			t.Fatal(err)
		}
	}
	a := runner.Assignment{RunID: "local_fixture", AttemptID: "local_review", Task: "Independently inspect the authenticated local candidate proof", Setup: runner.ExecutionSetup{Profile: runner.Profile{Harness: "codex", Mode: "saas-review", Access: "workspace-read", Model: "gpt-6.1-sol", Reasoning: "xhigh", MaxInput: 8 << 10, MaxOutput: 512}}, Inputs: map[string]runner.Value{"authority": {ArtifactID: "saas/authority"}, "patch": {ArtifactID: "saas/patch"}, "evidence": {ArtifactID: "saas/evidence"}}, Limits: runner.ExecutionLimits{MaxArtifact: 16 << 20, MaxRecord: 128 << 10}}
	w := &Workspaces{disk: disk}
	r := workspaceRecord{RunID: a.RunID, Repository: t.TempDir(), Base: base, WorkDir: filepath.Join(disk.Root, "work", a.RunID, "source"), Candidate: &runner.SourceResult{Base: base, PatchID: "saas/patch"}}
	if err = w.save(r); err != nil {
		t.Fatal(err)
	}
	a.OutputTypes = map[string]string{"verdict": "text", "summary": "text"}
	a.Outcomes = []string{"ready", "changes_requested", "blocked"}
	if err = os.MkdirAll(r.WorkDir, 0700); err != nil {
		t.Fatal(err)
	}
	staged, err := stagedInputData(disk, a)
	if err != nil {
		t.Fatal(err)
	}
	inputs, err := experienceImageInputs(disk, nativeJob{Assignment: a, Workspace: &r})
	if err != nil || len(inputs) != 9 {
		t.Fatal(len(inputs), err)
	}
	for _, input := range inputs[1:] {
		v := input.(map[string]any)
		path := v["path"].(string)
		if staged[filepath.Base(path)] == nil || !strings.HasPrefix(filepath.Base(path), "saas-chrome-") {
			t.Fatal("foreign image namespace", path)
		}
	}
	r.Candidate.PatchID = "foreign/patch"
	if err = w.save(r); err != nil {
		t.Fatal(err)
	}
	if _, err = stagedInputData(disk, a); err == nil {
		t.Fatal("stale evidence reached image review after source correction")
	}
	r.Candidate.PatchID = "saas/patch"
	if err = w.save(r); err != nil {
		t.Fatal(err)
	}
	a.Setup.Profile.Mode = "saas-source-review"
	e.Status, e.Images, e.Cases = "checked", nil, nil
	e.Reports = e.Reports[:len(e.Reports)-1]
	checked, err := saas.PackCheckedBuild(e, server, files)
	if err != nil {
		t.Fatal(err)
	}
	if err = disk.Write("artifacts/saas/evidence", checked, 16<<20); err != nil {
		t.Fatal(err)
	}
	if staged, err = stagedInputData(disk, a); err != nil || staged["saas-saas.json"] == nil || staged["saas-reports-go.json"] == nil {
		t.Fatal("checked build did not reach independent source review", err)
	}
	if inputs, err = experienceImageInputs(disk, nativeJob{Assignment: a, Workspace: &r}); err != nil || len(inputs) != 0 {
		t.Fatal("source checks became browser proof", err)
	}
}

// Non-executable simulated ELF is solely codec/control proof. The separately
// opted-in container fixture supplies a genuine generated Linux executable.
func saasSyntheticBuild(t *testing.T, e *saas.Evidence, a saas.Authority) []byte {
	t.Helper()
	b, err := proof.Build(e, a)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
