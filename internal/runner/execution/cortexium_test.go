//go:build darwin || linux

package execution

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/release"
	"github.com/cortexium-io/runner/internal/runner"
	"github.com/cortexium-io/runner/internal/saas"
)

func cortexiumFixture(t *testing.T) (*Releases, runner.Assignment, string) {
	t.Helper()
	disk := workspaceDisk(t)
	h := strings.Repeat("a", 64)
	base := strings.Repeat("b", 40)
	brief, err := os.ReadFile("../../../testdata/saas/first-flow-brief.md")
	if err != nil {
		t.Fatal(err)
	}
	a := saas.Authority{
		Version:              1,
		OperatorPolicySHA256: h,
		Runtime: saas.ContainerRuntime{
			Image:        "sha256:" + h,
			DriverSHA256: h,
			ChromeSHA256: h,
			Node:         "26.13.1",
			Playwright:   "1.65.0",
		},
		BriefCommit:   saas.BriefCommit,
		BriefSHA256:   saas.BriefSHA256,
		GennyCommit:   saas.GennyCommit,
		Base:          base,
		AppDirectory:  "platform",
		Module:        "fixture.invalid/local",
		ProjectName:   "RunnerFixture",
		Brief:         string(brief),
		Contract:      saas.Contract,
		GoMod:         "module fixture.invalid/local\n\ngo 1.27.0\n\ntoolchain go1.27.3\n",
		GoSum:         "synthetic locked sum\n",
		Package:       `{"private":true,"dependencies":{"vue":"3.5.45"},"devDependencies":{"vite":"8.3.6","vitest":"5.1.0"}}`,
		Lock:          `{"lockfileVersion":3}`,
		GoSHA256:      h,
		GoCheckSHA256: h,
		GennySHA256:   h,
		NodeSHA256:    h,
	}
	ab, _ := json.Marshal(a)
	patch := []byte("simulated candidate patch")
	e := saas.Evidence{
		Version:            1,
		Kind:               "cortexium-local",
		Status:             "checked",
		Base:               base,
		PatchSHA256:        saas.Digest(patch),
		AuthoritySHA256:    saas.Digest(ab),
		ChromeSHA256:       h,
		DriverSHA256:       h,
		ModuleSHA256:       saas.Digest([]byte(a.GoMod)),
		LockSHA256:         saas.Digest([]byte(a.Lock)),
		GoVersion:          "go1.27.3",
		GoTests:            1,
		VueTests:           1,
		GoVet:              true,
		GoBuild:            true,
		VueTypecheck:       true,
		VueBuild:           true,
		GeneratedOwnership: true,
		RealWorker:         "unverified",
	}
	b := saasSyntheticBuild(t, &e, a)
	reports := map[string][]byte{}
	for _, name := range saas.RequiredReports {
		p := []byte(`{"synthetic":true}`)
		reports[name] = p
		e.Reports = append(e.Reports, saas.Report{Name: name, SHA256: saas.Digest(p), Bytes: len(p), Complete: true})
	}
	data, err := saas.PackCheckedBuild(e, b, reports)
	if err != nil {
		t.Fatal(err)
	}
	for name, p := range map[string][]byte{"authority": ab, "patch": patch, "checks": data} {
		if err = disk.Write("artifacts/fixture/"+name, p, 16<<20); err != nil {
			t.Fatal(err)
		}
	}
	assignment := runner.Assignment{
		ProjectID: "cortexium-local",
		RunID:     "run_fixture",
		AttemptID: "attempt_browser",
		Deadline:  time.Now().Add(20 * time.Minute),
		Setup:     runner.ExecutionSetup{Profile: runner.Profile{Harness: "release", Mode: "cortexium-browser", Access: "input-only"}},
		Inputs: map[string]runner.Value{
			"patch":     {ArtifactID: "fixture/patch"},
			"authority": {ArtifactID: "fixture/authority"},
			"checks":    {ArtifactID: "fixture/checks"},
			"verdict":   runner.Text("APPROVE"),
		},
		Limits: runner.ExecutionLimits{MaxArtifact: 16 << 20, MaxRecord: 128 << 10},
	}
	w := &Workspaces{disk: disk}
	if err = w.save(workspaceRecord{
		RunID:      assignment.RunID,
		Base:       base,
		Repository: t.TempDir(),
		WorkDir:    filepath.Join(disk.Root, "work", assignment.RunID, "source"),
		Candidate:  &runner.SourceResult{Base: base, PatchID: "fixture/patch"},
	}); err != nil {
		t.Fatal(err)
	}
	op := evidenceBinary(t, "./testdata/saas/proof-operator")
	return &Releases{
		disk:       disk,
		workspaces: w,
		cfg:        &runner.ReleaseSettings{Command: []string{op, "--operator"}},
	}, assignment, op
}

func waitCortexium(t *testing.T, r *Releases, id string) runner.Observation {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	for {
		o, err := r.Observe(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if o.End != nil {
			return o
		}
		select {
		case <-ctx.Done():
			t.Fatal("Cortexium did not settle", o)
		case <-time.After(time.Millisecond):
		}
	}
}
func closeCortexium(t *testing.T, r *Releases) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := r.Close(ctx); err != nil {
		t.Error(err)
	}
}

func TestCortexiumTransferRejoinsShortObservationAndAllowsIndependentProgress(t *testing.T) {
	r, a, op := cortexiumFixture(t)
	defer closeCortexium(t, r)
	dir := t.TempDir()
	fifo, received := filepath.Join(dir, "barrier"), filepath.Join(dir, "received")
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	r.cfg.Command = []string{op, "--operator", "--barrier", fifo, "--received", received}
	wake := make(chan string, 4)
	r.SetWake(func(id string) { wake <- id })
	short, cancel := context.WithCancel(t.Context())
	first := make(chan runner.Observation, 1)
	go func() { o, _ := r.Start(short, a); first <- o }()
	waitFile(t, received)
	cancel()
	o := <-first
	if o.End != nil || !o.Reconciling {
		t.Fatal("short observation falsely settled transfer", o)
	}
	// A second attempt has an independent lease and reaches its simulated result
	// while the first exact transfer remains held behind a deterministic FIFO.
	second := runner.Clone(a)
	second.AttemptID = "attempt_independent"
	r.cfg.Command = []string{op, "--operator"}
	if _, err := r.Start(t.Context(), second); err != nil {
		t.Fatal(err)
	}
	if o = waitCortexium(t, r, second.AttemptID); o.End.Kind != "returned" {
		t.Fatal(o)
	}
	f, err := os.OpenFile(fifo, os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if o = waitCortexium(t, r, a.AttemptID); o.End.Kind != "returned" {
		t.Fatal(o)
	}
	select {
	case <-wake:
	case <-time.After(time.Second):
		t.Fatal("no completion wake")
	}
	j, err := r.load(a.AttemptID)
	if err != nil || j.CortexiumInput == nil || !j.OperatorDispatched || !j.CortexiumAcknowledged {
		t.Fatal("durable operation not retained", j, err)
	}
}

func TestCortexiumValidReboundInputStopsOriginalAfterRestart(t *testing.T) {
	r, a, op := cortexiumFixture(t)
	defer closeCortexium(t, r)
	p, _, checked, _, err := r.cortexiumInput(a)
	if err != nil {
		t.Fatal(err)
	}
	requests := filepath.Join(t.TempDir(), "requests.jsonl")
	j := releaseJob{
		Assignment:         a,
		Command:            []string{op, "--operator", "--requests", requests},
		OperatorDispatched: true,
		CortexiumInput:     &p,
		CortexiumBase:      checked.Base,
	}
	if err = r.save(j); err != nil {
		t.Fatal(err)
	}
	// Rebind a perfectly valid new authority and matching checked-build receipt.
	// The codec alone accepts it, but this dispatched key must retain its original.
	authority, _ := r.disk.Read("artifacts/fixture/authority", 16<<20)
	var bound saas.Authority
	if err = json.Unmarshal(authority, &bound); err != nil {
		t.Fatal(err)
	}
	bound.OperatorPolicySHA256 = strings.Repeat("c", 64)
	changed, _ := json.Marshal(bound)
	data, _ := r.disk.Read("artifacts/fixture/checks", 16<<20)
	e, files, err := saas.DecodeCheckedBuild(data, checked.Base, p.PatchSHA256, p.AuthoritySHA256)
	if err != nil {
		t.Fatal(err)
	}
	e.AuthoritySHA256 = saas.Digest(changed)
	data, err = saas.PackCheckedBuild(e, files["app/server"], files)
	if err != nil {
		t.Fatal(err)
	}
	if err = r.disk.Write("artifacts/fixture/authority", changed, 16<<20); err != nil {
		t.Fatal(err)
	}
	if err = r.disk.Write("artifacts/fixture/checks", data, 16<<20); err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err = r.cortexiumInput(a); err != nil {
		t.Fatal("rebound fixture must remain structurally valid", err)
	}
	resumed := &Releases{disk: r.disk, workspaces: r.workspaces, cfg: r.cfg}
	defer closeCortexium(t, resumed)
	o := waitCortexium(t, resumed, a.AttemptID)
	if o.End.Kind != "failed" || !strings.Contains(o.End.Reason, "original operation stopped") {
		t.Fatal(o)
	}
	body, err := os.ReadFile(requests)
	if err != nil {
		t.Fatal(err)
	}
	var q release.Request
	if err = json.Unmarshal(body, &q); err != nil {
		t.Fatal(err)
	}
	got := *q.Cortexium
	got.Transport = ""
	if got != p || q.Source != checked.Base || q.Cortexium.Transport != "stop" {
		t.Fatal("drift dispatched replacement bytes instead of stopping original", q)
	}
	saved, err := resumed.load(a.AttemptID)
	if err != nil || *saved.CortexiumInput != p || !saved.CortexiumIntegrityLost || saved.End.Kind != "failed" {
		t.Fatal(saved, err)
	}
}

func waitFile(t *testing.T, name string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(name); err == nil {
			return
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		select {
		case <-ctx.Done():
			t.Fatal("owned barrier was not reached", name)
		case <-ticker.C:
		}
	}
}

func TestCortexiumCancellationSettlesOriginalDispatchedOperation(t *testing.T) {
	for _, afterDispatch := range []bool{false, true} {
		t.Run(map[bool]string{false: "before dispatch", true: "after dispatch"}[afterDispatch], func(t *testing.T) {
			r, a, op := cortexiumFixture(t)
			defer closeCortexium(t, r)
			dir := t.TempDir()
			fifo, received, requests := filepath.Join(dir, "barrier"), filepath.Join(dir, "received"), filepath.Join(dir, "requests")
			if err := syscall.Mkfifo(fifo, 0600); err != nil {
				t.Fatal(err)
			}
			r.cfg.Command = []string{op, "--operator", "--barrier", fifo, "--received", received, "--requests", requests}
			if afterDispatch {
				short, cancel := context.WithCancel(t.Context())
				returned := make(chan struct{})
				go func() { defer close(returned); _, _ = r.Start(short, a) }()
				waitFile(t, received)
				cancel()
				<-returned
				if err := r.Stop(t.Context(), a.AttemptID); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := r.save(releaseJob{Assignment: a, Command: r.cfg.Command, Stopped: true}); err != nil {
					t.Fatal(err)
				}
			}
			o := waitCortexium(t, r, a.AttemptID)
			if o.End.Kind != "stopped" {
				t.Fatal(o)
			}
			if !afterDispatch {
				if _, err := os.Stat(requests); !os.IsNotExist(err) {
					t.Fatal("cancelled before dispatch still called operator", err)
				}
				return
			}
			lines, err := os.ReadFile(requests)
			if err != nil {
				t.Fatal(err)
			}
			var first *release.Request
			for _, line := range strings.Split(strings.TrimSpace(string(lines)), "\n") {
				var q release.Request
				if err = json.Unmarshal([]byte(line), &q); err != nil {
					t.Fatal(err)
				}
				if first == nil {
					copy := q
					first = &copy
					continue
				}
				p := *q.Cortexium
				original := *first.Cortexium
				p.Transport = ""
				original.Transport = ""
				if q.Cortexium.Transport != "stop" || p != original || q.Source != first.Source || q.Key != first.Key {
					t.Fatal("cancellation replaced original operation", q)
				}
			}
		})
	}
}

func TestCortexiumLostAcknowledgementRejoinsSameIdentityAfterOwnerRestart(t *testing.T) {
	r, a, op := cortexiumFixture(t)
	dir := t.TempDir()
	fifo, received, requests := filepath.Join(dir, "barrier"), filepath.Join(dir, "received"), filepath.Join(dir, "requests")
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	r.cfg.Command = []string{op, "--operator", "--barrier", fifo, "--received", received, "--requests", requests}
	short, cancel := context.WithCancel(t.Context())
	returned := make(chan struct{})
	go func() { defer close(returned); _, _ = r.Start(short, a) }()
	waitFile(t, received)
	cancel()
	<-returned
	// Closing only this fixture transport loses the response, preserving the
	// durable request. The simulated operator acknowledges the same bytes later.
	closeCortexium(t, r)
	resumed := &Releases{disk: r.disk, workspaces: r.workspaces, cfg: r.cfg}
	defer closeCortexium(t, resumed)
	o := waitCortexium(t, resumed, a.AttemptID)
	if o.End.Kind != "returned" {
		t.Fatal(o)
	}
	lines, err := os.ReadFile(requests)
	if err != nil {
		t.Fatal(err)
	}
	var identity []byte
	count := 0
	for _, line := range strings.Split(strings.TrimSpace(string(lines)), "\n") {
		var q release.Request
		if err = json.Unmarshal([]byte(line), &q); err != nil {
			t.Fatal(err)
		}
		if q.Cortexium.Transport != "payload" {
			t.Fatal("lost ack did not resend exact immutable payload")
		}
		canonical, _ := json.Marshal(q)
		if count > 0 && string(canonical) != string(identity) {
			t.Fatal("owner restart changed operation")
		}
		identity = canonical
		count++
	}
	if count != 2 {
		t.Fatal("unexpected transport dispatch count", count)
	}
}

func TestCortexiumCleanupFactsDistinguishActiveReconciliationAndOperatorAttention(t *testing.T) {
	for _, tc := range []struct {
		name        string
		state       string
		reconciling bool
		reason      string
	}{
		{
			name:        "held-owned-worker",
			state:       "running",
			reconciling: true,
			reason:      "same owned cleanup worker remains active",
		},
		{
			name:   "expired-budget",
			state:  "unknown",
			reason: "cleanup deadline exhausted; needs operator",
		},
		{
			name:   "claimed-dead-worker",
			state:  "unknown",
			reason: "cleanup visit consumed without settlement; needs operator",
		},
		{
			name:   "old-unknown-budget",
			state:  "unknown",
			reason: "old cleanup budget unknown; needs operator",
		},
		{
			name:        "unknown-cannot-claim-activity",
			state:       "unknown",
			reconciling: true,
			reason:      "writer state unknown; needs operator",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, a, _ := cortexiumFixture(t)
			defer closeCortexium(t, r)
			response := release.Response{
				Key:             a.AttemptID,
				PayloadRetained: true,
				State:           tc.state,
				Reconciling:     tc.reconciling,
				Reason:          tc.reason,
			}
			data, err := json.Marshal(response)
			if err != nil {
				t.Fatal(err)
			}
			program := filepath.Join(t.TempDir(), "fixed-observation")
			body := "#!/bin/sh\n/bin/cat > /dev/null\nprintf '%s\\n' '" + string(data) + "'\n"
			if err := os.WriteFile(program, []byte(body), 0700); err != nil {
				t.Fatal(err)
			}
			j := releaseJob{Assignment: a, Command: []string{program}}
			o, err := r.observeCortexium(t.Context(), j, runner.Observation{State: runner.Running})
			if err != nil || o.End != nil || o.Reconciling != (tc.state == "running" && tc.reconciling) {
				t.Fatal("cleanup facts were lost or falsely settled", o, err)
			}
			if tc.state == "unknown" && (o.State != runner.Unknown || !strings.Contains(o.Reason, "needs operator")) {
				t.Fatal("exhausted cleanup concealed attention", o)
			}
		})
	}
}
