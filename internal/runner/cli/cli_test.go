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

func (h *commandHarness) CheckCapabilities(context.Context, config.Config) error { return nil }
func (h *commandHarness) Baseline(_ context.Context, dir, _ string, log *os.File) error {
	b, e := os.ReadFile(filepath.Join(dir, "greeting.txt"))
	if e != nil {
		return e
	}
	if string(b) != "before\n" {
		return os.ErrInvalid
	}
	_, e = log.WriteString("approved baseline passed in committed-base copy\n")
	return e
}
func (h *commandHarness) Execute(ctx context.Context, r execution.Request, _ func(core.Process) error) execution.Completion {
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
	if err = os.WriteFile(taskFile, []byte("## Outcome\nGreeting\n## Scope\ngreeting.txt\n## Acceptance\nhello\n## Verification\nRead greeting.txt; expect hello. No new suite needed for this file boundary.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	id := strings.Fields(run("task", "add", "--file", taskFile))[0]
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
}
