package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner/config"
	"github.com/cortexium-io/runner/internal/runner/core"
	"github.com/cortexium-io/runner/internal/runner/engine"
	"github.com/cortexium-io/runner/internal/runner/execution"
)

type commandHarness struct{ auditStarted, auditRelease chan struct{} }

const commandBrief = "## Outcome\nGreeting\n## Scope\ngreeting.txt\n## Acceptance\nhello\n## Verification\nRead greeting.txt; expect hello. No new suite needed for this file boundary.\n"

func (h *commandHarness) CheckCapabilities(context.Context, config.Config) error { return nil }
func (h *commandHarness) Check(_ context.Context, dir string, c config.QualityCheck, evidence string, _ func(core.Process) error) core.CheckObservation {
	r := core.CheckObservation{CheckID: c.ID, Status: "passed", Stopped: true, OutputDigest: "passed", Evidence: evidence}
	if c.ID == "legacy" {
		r.Status = "failed"
		r.ExitCode = 1
		r.OutputDigest = "known legacy failure"
	}
	b, err := os.ReadFile(filepath.Join(dir, "greeting.txt"))
	if err != nil || (string(b) != "before\n" && string(b) != "hello\n") {
		r.Status = "unavailable"
		r.Detail = "unexpected source"
		return r
	}
	if err = os.WriteFile(evidence, []byte("approved check passed\n"), 0600); err != nil {
		r.Status = "unavailable"
		r.Detail = err.Error()
	}
	return r
}
func (h *commandHarness) Execute(ctx context.Context, r execution.Request, _ func(core.Process) error) execution.Completion {
	if r.Attempt.Role == "plan" {
		return execution.Completion{Stopped: true, Result: &core.Result{Outcome: "succeeded", Summary: "Proposed greeting", Planning: &core.PlanningResult{Cards: []core.PlannedCard{{Key: "greeting", Body: commandBrief}}}}}
	}
	if r.Attempt.Role == "implementer" {
		if e := os.WriteFile(filepath.Join(r.Dir, "greeting.txt"), []byte("hello\n"), 0600); e != nil {
			return execution.Completion{Stopped: true, Error: e.Error()}
		}
	} else {
		close(h.auditStarted)
		select {
		case <-h.auditRelease:
		case <-ctx.Done():
			return execution.Completion{Stopped: true, Error: ctx.Err().Error()}
		}
	}
	checks := []core.Check{}
	for _, p := range r.Proofs {
		checks = append(checks, core.Check{ID: p.ID, Status: "pass", Evidence: "greeting.txt inspected; exact expected bytes"})
	}
	return execution.Completion{Stopped: true, Result: &core.Result{Outcome: "succeeded", Summary: "Approved greeting verified", Checks: checks}}
}

type drainSignal chan struct{}

func (s drainSignal) Write(b []byte) (int, error) {
	select {
	case s <- struct{}{}:
	default:
	}
	return len(b), nil
}
func TestLocalCommandJourneyDrainAndReadOnlyReopen(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(root, "repo")
	if err = os.Mkdir(repo, 0700); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = repo
		if b, e := c.CombinedOutput(); e != nil {
			t.Fatalf("git: %s %v", b, e)
		}
	}
	git("init", "-b", "main")
	git("config", "user.name", "Test")
	git("config", "user.email", "test@example.invalid")
	if err = os.WriteFile(filepath.Join(repo, "greeting.txt"), []byte("before\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(repo, "legacy.txt"), []byte("known failure\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-m", "baseline")
	// The baseline must read the committed version, not this user's local edit.
	if err = os.WriteFile(filepath.Join(repo, "greeting.txt"), []byte("local unrelated edit\n"), 0600); err != nil {
		t.Fatal(err)
	}
	p := core.Profile{Harness: "codex", Model: "controlled", Reasoning: "high", Timeout: "1m"}
	cfg := config.Config{Format: "runner-local-v1", Repository: config.Repository{Path: repo, BaseRef: "refs/heads/main", BaseFreshness: "recorded"}, StateDir: filepath.Join(root, "state"), WorktreeDir: filepath.Join(root, "worktrees"), Profiles: map[string]core.Profile{"build": p, "review": p}, Roles: map[string]string{"implementer": "build", "reviewer": "review"}, Policy: config.Policy{MaxParallelism: 1}}
	cfg.Delivery.Mode = "local"
	b, _ := json.Marshal(cfg)
	cfgPath := filepath.Join(root, "config.json")
	if err = os.WriteFile(cfgPath, b, 0600); err != nil {
		t.Fatal(err)
	}
	h := &commandHarness{auditStarted: make(chan struct{}), auditRelease: make(chan struct{})}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	run := func(args ...string) string {
		t.Helper()
		var out bytes.Buffer
		err := (CLI{Out: &out, Harness: h}).Run(ctx, append([]string{"--config", cfgPath}, args...))
		if err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out.String())
		}
		return out.String()
	}
	if err = (CLI{Out: io.Discard, Harness: h}).Run(ctx, []string{"--config", cfgPath, "status"}); err == nil {
		t.Fatal("missing authority looked initialized")
	}
	if _, err = os.Lstat(cfg.StateDir); !os.IsNotExist(err) {
		t.Fatal("status created authority")
	}
	run("init", "--check", "test -f greeting.txt")
	if _, err = os.Lstat(cfg.StateDir); !os.IsNotExist(err) {
		t.Fatal("preview wrote state")
	}
	run("init", "--check", "test -f greeting.txt", "--apply")
	taskFile := filepath.Join(root, "task.md")
	if err = os.WriteFile(taskFile, []byte(commandBrief), 0600); err != nil {
		t.Fatal(err)
	}
	id := strings.Fields(run("task", "add", "--file", taskFile))[0]
	readyID := id
	var view engine.View
	if err = json.Unmarshal([]byte(run("task", "show", id, "--json")), &view); err != nil {
		t.Fatal(err)
	}
	run("task", "approve", id, "--version", "1", "--preview", view.Preview)
	done := make(chan error, 1)
	go func() { done <- (CLI{Out: io.Discard, Harness: h}).Run(ctx, []string{"--config", cfgPath, "run"}) }()
	select {
	case <-h.auditStarted:
	case <-ctx.Done():
		t.Fatal("audit not reached")
	}
	if !strings.Contains(run("status", "--json"), `"stage": "Reviewing"`) {
		t.Fatal("status did not observe active audit")
	}

	// Preview and apply live policy without changing the already running audit.
	cfg.Policy.MaxParallelism = 2
	cfg.Roles["planner"] = "build"
	profile := cfg.Profiles["review"]
	profile.Reasoning = "medium"
	cfg.Profiles["review"] = profile
	b, _ = json.Marshal(cfg)
	if err = os.WriteFile(cfgPath, b, 0600); err != nil {
		t.Fatal(err)
	}
	var change engine.PolicyChange
	if err = json.Unmarshal([]byte(run("config", "explain")), &change); err != nil {
		t.Fatal(err)
	}
	if len(change.Active) != 1 || change.Active[0].Profile.Reasoning != "high" {
		t.Fatal("preview lost active profile")
	}
	if err = (CLI{Out: io.Discard, Harness: h}).Run(ctx, []string{"--config", cfgPath, "config", "apply", "--preview", "stale"}); err == nil {
		t.Fatal("stale policy preview accepted")
	}
	run("config", "apply", "--preview", change.Preview)
	if err = json.Unmarshal([]byte(run("task", "show", id, "--json")), &view); err != nil {
		t.Fatal(err)
	}
	if view.Attempts[len(view.Attempts)-1].Profile.Reasoning != "high" {
		t.Fatal("active attempt changed with policy")
	}
	signal := make(drainSignal, 1)
	drained := make(chan error, 1)
	go func() { drained <- (CLI{Out: signal, Harness: h}).Run(ctx, []string{"--config", cfgPath, "drain"}) }()
	select {
	case <-signal:
	case <-ctx.Done():
		t.Fatal("drain did not record admission stop")
	}
	close(h.auditRelease)
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("coordinator did not settle")
	}
	select {
	case err = <-drained:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("drain did not return")
	}
	dbPath := filepath.Join(cfg.StateDir, "runner.db")
	before, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal([]byte(run("task", "show", id, "--json")), &view); err != nil {
		t.Fatal(err)
	}
	run("status", "--json")
	run("config", "explain")
	after, err := os.ReadFile(dbPath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("inspection wrote authority")
	}
	if view.Task.Stage != core.AwaitingDelivery || view.Task.Candidate == nil || len(view.Attempts) != 2 {
		t.Fatalf("unexpected retained journey: %+v", view)
	}
	if b, err = os.ReadFile(filepath.Join(repo, "greeting.txt")); err != nil || string(b) != "local unrelated edit\n" {
		t.Fatal("original checkout modified")
	}

	// The Plan command has its own approval boundary before any child is created.
	if err = os.WriteFile(taskFile, []byte("Change the greeting to hello."), 0600); err != nil {
		t.Fatal(err)
	}
	id = strings.Fields(run("task", "add", "--plan", "--file", taskFile))[0]
	if err = json.Unmarshal([]byte(run("task", "show", id, "--json")), &view); err != nil {
		t.Fatal(err)
	}
	run("task", "approve", id, "--version", "1", "--preview", view.Preview)
	go func() { done <- (CLI{Out: io.Discard, Harness: h}).Run(ctx, []string{"--config", cfgPath, "run"}) }()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		view = engine.View{}
		if err = json.Unmarshal([]byte(run("task", "show", id, "--json")), &view); err != nil {
			t.Fatal(err)
		}
		if view.Task.Stage == core.AwaitingApproval {
			break
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatal("proposal not ready")
		}
	}
	run("drain")
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	before, err = os.ReadFile(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	shown := run("task", "show", id)
	if !strings.Contains(shown, commandBrief) || !strings.Contains(shown, "Card greeting") || !strings.Contains(shown, view.ReleasePreview) {
		t.Fatal("proposal not reviewable", shown)
	}
	after, err = os.ReadFile(dbPath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("proposal inspection wrote authority")
	}
	run("task", "release", id, "--version", "1", "--preview", view.ReleasePreview)
	view = engine.View{}
	if err = json.Unmarshal([]byte(run("task", "show", id, "--json")), &view); err != nil {
		t.Fatal(err)
	}
	if view.Task.Release == nil || len(view.Task.Release.Children) != 1 {
		t.Fatal("release missing")
	}
	run("task", "stop", id)
	// Quality changes apply only to new work until explicit task adoption.
	spec := config.QualitySpec{Definition: "Updated agreement with an identified legacy exception", Checks: []config.QualityCheck{{ID: "legacy", Command: "false", Directory: ".", Prerequisites: []string{"sh"}, Timeout: "1m"}}}
	qualityFile := filepath.Join(root, "quality.json")
	b, _ = json.Marshal(spec)
	if err = os.WriteFile(qualityFile, b, 0600); err != nil {
		t.Fatal(err)
	}
	var qp engine.QualityPreview
	if err = json.Unmarshal([]byte(run("quality", "preview", "--file", qualityFile)), &qp); err != nil {
		t.Fatal(err)
	}
	var report config.QualityReport
	if err = json.Unmarshal([]byte(run("quality", "check", "--file", qualityFile, "--preview", qp.Preview)), &report); err != nil {
		t.Fatal(err)
	}
	exceptionFile := filepath.Join(root, "exceptions.json")
	b, _ = json.Marshal([]config.ExceptionRequest{{CheckID: "legacy", Rationale: "Existing unrelated failure", Scope: []string{"legacy.txt"}}})
	if err = os.WriteFile(exceptionFile, b, 0600); err != nil {
		t.Fatal(err)
	}
	before, err = os.ReadFile(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	oldQuality := view.Task.Current().Quality
	run("quality", "show", "--revision", oldQuality)
	qp = engine.QualityPreview{}
	if err = json.Unmarshal([]byte(run("quality", "preview", "--report", report.ID, "--exceptions", exceptionFile)), &qp); err != nil {
		t.Fatal(err)
	}
	after, err = os.ReadFile(dbPath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("quality preview wrote authority")
	}
	run("quality", "apply", "--report", report.ID, "--exceptions", exceptionFile, "--preview", qp.Preview)
	run("quality", "show")
	view = engine.View{}
	if err = json.Unmarshal([]byte(run("task", "show", readyID, "--json")), &view); err != nil {
		t.Fatal(err)
	}
	if view.Task.Current().Quality != oldQuality || view.QualityAdoptionPreview == "" {
		t.Fatal("default silently changed approved work")
	}
	run("task", "adopt-quality", readyID, "--version", "1", "--preview", view.QualityAdoptionPreview)
	view = engine.View{}
	if err = json.Unmarshal([]byte(run("task", "show", readyID, "--json")), &view); err != nil {
		t.Fatal(err)
	}
	if view.Task.Current().Number != 2 || view.Task.Stage != core.Draft || view.Task.Approval != nil || view.Task.Candidate != nil {
		t.Fatal("adoption retained old acceptance")
	}
}
