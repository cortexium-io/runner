//go:build darwin || linux

package execution

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
)

type imageQualificationPeer struct {
	codexPeer
	root  string
	calls *atomic.Int32
}

func (p *imageQualificationPeer) Call(ctx context.Context, method string, args, out any) error {
	if method == "turn/start" {
		if !p.calls.CompareAndSwap(0, 1) {
			return errors.New("bounded image qualification allows one native turn")
		}
		f, err := os.OpenFile(filepath.Join(p.root, "turn-requested.json"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		data, _ := json.Marshal(map[string]any{"model": "gpt-6.1-sol", "reasoning": "xhigh", "images": 36, "requested": time.Now().UTC()})
		_, err = f.Write(data)
		if err == nil {
			err = f.Sync()
		}
		err = errors.Join(err, f.Close())
		if err != nil {
			return err
		}
	}
	return p.codexPeer.Call(ctx, method, args, out)
}

func TestNativeExperienceImageReview(t *testing.T) {
	if os.Getenv("RUNNER_EXPERIENCE_NATIVE") != "1" {
		t.Skip("explicit isolated image qualification: one Sol/xhigh read-only turn, no Safari/Simulator/host automation")
	}
	root := os.Getenv("RUNNER_EXPERIENCE_ROOT")
	if !filepath.IsAbs(root) {
		t.Fatal("fresh private evidence directory required")
	}
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal("qualification must not silently repeat", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	work, err := os.MkdirTemp(filepath.Join(home, ".codex"), "rie-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(work)
	cfg := workspaceSnapshot(t, sourceRepositoryAt(t, filepath.Join(work, "repository")))
	cfg.Instance.Codex.Binary = os.Getenv("RUNNER_PREFLIGHT_CODEX")
	cfg.Instance.Codex.AuthFile = filepath.Join(home, ".codex", "auth.json")
	cfg.Instance.Storage.MaxArtifact = 16 << 20
	cfg.Instance.Storage.MinFree = 1 << 30
	cfg.Instance.Storage.Database.MaxRecord = 40 << 10
	project := cfg.Projects["demo"]
	role := runner.Role{
		ID:      "image-review",
		Purpose: "Independently inspect all attached actual viewport screenshots. The source checks passed; determine whether a person can reach every editing control. Identify any covered controls and the affected puzzle. This is a synthetic image transport fixture, with no claim of product or native Safari acceptance.",
		Inputs:  map[string]string{"receipt": "json", "evidence": "artifact"},
		Outputs: map[string]string{"verdict": "text", "summary": "text"},
		Execution: &runner.Profile{
			Harness:      "codex",
			Mode:         "experience-review",
			Access:       "workspace-read",
			Model:        "gpt-6.1-sol",
			Reasoning:    "xhigh",
			MaxInput:     8 << 10,
			MaxOutput:    512,
			Instructions: "Use product-experience-review principles: personally inspect every attached viewport at readable scale; assess visual usability independently of passing checks. Return changes_requested and verdict BLOCKED if any editing control is covered. State the affected puzzle and the visual problem in at most 200 characters. Return ready/APPROVE only if every editing control is visibly reachable. Do not use tools, edit files, run tests, launch browsers or infer a visual result from the manifest. Native Safari is outside this disposable image transport fixture; do not claim it passed."},
	}
	fixture, err := filepath.Abs("../../../testdata/experience/fixture.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Instance.Codex.CommandReadPaths = append(cfg.Instance.Codex.CommandReadPaths, fixture)
	stage := runner.Role{
		ID:      "stage",
		Purpose: "Stage the immutable disposable image fixture through the declared command artifact boundary.",
		Inputs:  map[string]string{},
		Outputs: map[string]string{"check": "text", "artifact": "artifact"},
		Execution: &runner.Profile{
			Harness: "command",
			Access:  "workspace-read",
			Command: &runner.Command{
				Args:     []string{"/bin/cp", fixture, "{scratch}/fixture.tar.gz"},
				Artifact: "fixture.tar.gz",
			},
		},
	}
	project.Roles = map[string]runner.Role{role.ID: role, stage.ID: stage}
	project.DefaultProcess = "image-fixture"
	project.Processes = map[string]runner.Process{"image-fixture": {
		ID:     "image-fixture",
		Inputs: map[string]string{"receipt": "json"},
		Steps: []runner.Step{
			{ID: "stage", Role: "stage", Inputs: map[string]runner.Binding{}},
			{
				ID:     role.ID,
				Role:   role.ID,
				Inputs: map[string]runner.Binding{"receipt": {FromInput: "receipt"}, "evidence": {FromStep: "stage", Output: "artifact"}},
				Routes: map[string]runner.Route{"ready": {Next: "$complete"}, "changes_requested": {Next: "$fail"}, "blocked": {Next: "$fail"}},
			},
		},
		MaxAttempts:        2,
		MaxAttemptsPerStep: 1,
		AttemptTimeout:     "5m",
	}}
	cfg.Projects["demo"] = project
	var calls atomic.Int32
	rig := openNativeRig(t, filepath.Join(work, "state"), cfg, nil, func(s *Service) {
		s.commands.launchWorker = func(file string, finished func()) error {
			command := exec.Command(os.Args[0], "-test.run=^TestCommandWorkerProcess$", "-test.timeout=6m")
			command.Env = append(os.Environ(), "RUNNER_COMMAND_WORKER_SPEC="+file)
			if err := command.Start(); err != nil {
				return err
			}
			go func() { _ = command.Wait(); finished() }()
			return nil
		}
		s.Codex.connect = func(ctx context.Context, socket string, notify func(rpcMessage)) (codexPeer, error) {
			peer, err := dialCodex(ctx, socket, notify)
			if err != nil {
				return nil, err
			}
			return &imageQualificationPeer{peer, root, &calls}, nil
		}
	})
	defer rig.close()
	a := experienceAssignment(t, rig.disk)
	run, err := rig.core.Start(runner.StartRequest{RequestID: "one-image-review", Project: "demo", Inputs: map[string]runner.Value{"receipt": a.Inputs["receipt"]}})
	if err != nil {
		t.Fatal(err)
	}
	done := waitSourceRun(t, rig.core, run.ID, func(r runner.Run) bool { return r.Terminal() && r.ResourcesReleased }, 5*time.Minute)
	data, _ := json.MarshalIndent(done, "", "  ")
	if err := os.WriteFile(filepath.Join(root, "result.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if len(done.Attempts) != 2 || done.Attempts[1].End == nil || done.Attempts[1].End.Result == nil {
		t.Fatal("read-only image review did not return a judgment", done.Reason)
	}
	result := done.Attempts[1].End.Result
	var summary, verdict string
	_ = json.Unmarshal(result.Outputs["summary"].Inline, &summary)
	_ = json.Unmarshal(result.Outputs["verdict"].Inline, &verdict)
	if calls.Load() != 1 || result.Outcome != "changes_requested" || verdict != "BLOCKED" || !strings.Contains(strings.ToLower(summary), "shikaku") || !strings.Contains(strings.ToLower(summary), "control") {
		t.Fatal("model did not independently identify the visual obstruction", result.Outcome, verdict, summary)
	}
	t.Log("One Sol/xhigh read-only image judgment rejected covered Shikaku controls despite passing source checks; resources released; no Safari, Simulator or product work")
}
