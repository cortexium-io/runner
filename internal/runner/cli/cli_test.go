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

	"github.com/cortexium-io/runner/internal/runner/backup"
	"github.com/cortexium-io/runner/internal/runner/config"
	"github.com/cortexium-io/runner/internal/runner/core"
	"github.com/cortexium-io/runner/internal/runner/engine"
	"github.com/cortexium-io/runner/internal/runner/execution"
	"github.com/cortexium-io/runner/internal/runner/store"
)

type commandHarness struct {
	auditStarted, auditRelease                      chan struct{}
	interruptFirstAudit, planQuestion, dependencies bool
	auditCalls, planCalls                           int
}

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
	if err := os.MkdirAll(r.Attempt.EvidenceDir, 0700); err != nil {
		return execution.Completion{Stopped: true, Error: err.Error()}
	}
	if r.Attempt.Role == "plan" {
		h.planCalls++
		if h.planQuestion && h.planCalls == 1 {
			return execution.Completion{Stopped: true, Result: &core.Result{Outcome: "needs_input", Summary: "Greeting choice", Planning: &core.PlanningResult{Questions: []string{"Which greeting should be used?"}}}}
		}
		cards := []core.PlannedCard{{Key: "greeting", Body: commandBrief}}
		if h.dependencies {
			cards = append(cards, core.PlannedCard{Key: "followup", Body: commandBrief, Dependencies: []string{"greeting"}})
		}
		return execution.Completion{Stopped: true, Result: &core.Result{Outcome: "succeeded", Summary: "Proposed greeting", Planning: &core.PlanningResult{Cards: cards}}}
	}
	if r.Attempt.Role == "implementer" {
		if e := os.WriteFile(filepath.Join(r.Dir, "greeting.txt"), []byte("hello\n"), 0600); e != nil {
			return execution.Completion{Stopped: true, Error: e.Error()}
		}
	} else {
		h.auditCalls++
		if h.auditCalls == 1 {
			close(h.auditStarted)
		}
		select {
		case <-h.auditRelease:
		case <-ctx.Done():
			return execution.Completion{Stopped: true, Error: ctx.Err().Error()}
		}
		if h.interruptFirstAudit && h.auditCalls == 1 {
			return execution.Completion{Stopped: true, Error: "controlled review interruption"}
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
	root, cfgPath, cfg := commandFixture(t)
	repo := cfg.Repository.Path
	var b []byte
	var err error
	h := &commandHarness{auditStarted: make(chan struct{}), auditRelease: make(chan struct{}), interruptFirstAudit: true, planQuestion: true, dependencies: true}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
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
	// Retired entry-point commands must fail before opening writable authority.
	for _, command := range []string{"doctor", "update", "approve", "unknown"} {
		err = (CLI{Out: io.Discard, Harness: h}).Run(ctx, []string{"--config", cfgPath, command})
		if err == nil || !strings.Contains(err.Error(), "unknown command") {
			t.Fatalf("%s: %v", command, err)
		}
		if _, err = os.Lstat(cfg.StateDir); !os.IsNotExist(err) {
			t.Fatalf("%s created authority", command)
		}
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

	// The editor previews and saves while the audit keeps its admitted profile.
	edits := []string{"config", "edit", "--set", "parallelism=2", "--set", "planner=build", "--set", "profile.review=controlled medium 1m", "--set", "profile.careful=controlled high 2m", "--set", "repairs=1", "--set", "transient-retries=1", "--set", "invocations=8", "--set", "escalation=careful"}
	preview := previewValue(t, run(edits...), "Save preview")
	run(append(edits, "--save", "--preview", preview)...)
	if advice := run("status"); !strings.Contains(advice, "unapplied edits") || !strings.Contains(advice, "config explain") {
		t.Fatal("saved policy has no visible apply step", advice)
	}
	var change engine.PolicyChange
	if err = json.Unmarshal([]byte(run("config", "explain", "--json")), &change); err != nil {
		t.Fatal(err)
	}
	if len(change.Active) != 1 || change.Active[0].Profile.Reasoning != "high" {
		t.Fatal("preview lost active profile")
	}
	if err = (CLI{Out: io.Discard, Harness: h}).Run(ctx, []string{"--config", cfgPath, "config", "apply", "--preview", "stale"}); err == nil {
		t.Fatal("stale policy preview accepted")
	}
	run("config", "apply", "--preview", change.Preview)
	var policy engine.PolicyChange
	if err = json.Unmarshal([]byte(run("config", "explain", "--json")), &policy); err != nil {
		t.Fatal(err)
	}
	if policy.Proposed.Policy.MaxParallelism != 2 || policy.Proposed.Policy.MaxTransientRetries != 1 || policy.Proposed.Policy.MaxInvocationsPerTask == nil || *policy.Proposed.Policy.MaxInvocationsPerTask != 8 {
		t.Fatal("configured admission limits lost", policy)
	}
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
	// An interrupted audit must retain the candidate and repeat only audit.
	view = engine.View{}
	if err = json.Unmarshal([]byte(run("task", "show", id, "--json")), &view); err != nil {
		t.Fatal(err)
	}
	if view.Task.Attention == nil || view.Recovery == nil || view.Recovery.Role != "audit" || view.Task.Candidate == nil {
		t.Fatal("review recovery missing", view)
	}
	candidate := *view.Task.Candidate
	attempt := view.Task.Attention.Attempt
	advice := run("task", "show", id)
	if !strings.Contains(advice, "Recovery role: audit") || !strings.Contains(advice, "task resume "+id+" --attempt "+attempt) || !strings.Contains(advice, "--allow-new-invocation") || !strings.Contains(advice, candidate.Commit) {
		t.Fatal("interrupted review does not explain retained work and repeat permission", advice)
	}
	t.Log("Interrupted audit: retained candidate, exact resume command and explicit repeat permission visible.")
	if err = (CLI{Out: io.Discard, Harness: h}).Run(ctx, []string{"--config", cfgPath, "task", "resume", id, "--attempt", attempt}); err == nil {
		t.Fatal("review repeated without explicit permission")
	}
	run("task", "resume", id, "--attempt", attempt, "--allow-new-invocation")
	go func() { done <- (CLI{Out: io.Discard, Harness: h}).Run(ctx, []string{"--config", cfgPath, "run"}) }()
	waitView := func(taskID string, ready func(engine.View) bool) engine.View {
		t.Helper()
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			var next engine.View
			if err := json.Unmarshal([]byte(run("task", "show", taskID, "--json")), &next); err != nil {
				t.Fatal(err)
			}
			if ready(next) {
				return next
			}
			select {
			case <-ticker.C:
			case <-ctx.Done():
				t.Fatal("operator journey did not progress", next)
			}
		}
	}
	view = waitView(id, func(v engine.View) bool { return v.Task.Stage == core.AwaitingDelivery })
	run("drain")
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	if *view.Task.Candidate != candidate || len(view.Attempts) != 3 || view.Attempts[2].Role != "audit" || view.Attempts[2].Profile.Reasoning != "high" {
		t.Fatal("recovery lost candidate or pinned reviewer", view)
	}
	if advice := run("task", "show", id); !strings.Contains(advice, "integration is still pending") || view.Task.Usage.Invocations != 3 {
		t.Fatal("local acceptance claims completion or loses consumed calls", advice, view.Task.Usage)
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
	if view.Task.Stage != core.AwaitingDelivery || view.Task.Candidate == nil || len(view.Attempts) != 3 {
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
	view = waitView(id, func(v engine.View) bool { return v.Task.Attention != nil })
	run("drain")
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	advice = run("task", "show", id)
	if view.Task.Attention.Kind != core.Decision || !strings.Contains(advice, "Which greeting") || !strings.Contains(advice, "task replace "+id+" --version 1") {
		t.Fatal("question or versioned answer path not visible", advice)
	}
	t.Log("Plan question: full question and versioned replacement path visible; clarified request requires fresh approval.")
	if err = os.WriteFile(taskFile, []byte("Use hello as the greeting; then add the followup."), 0600); err != nil {
		t.Fatal(err)
	}
	run("task", "replace", id, "--version", "1", "--file", taskFile)
	view = engine.View{}
	if err = json.Unmarshal([]byte(run("task", "show", id, "--json")), &view); err != nil {
		t.Fatal(err)
	}
	run("task", "approve", id, "--version", "2", "--preview", view.Preview)
	go func() { done <- (CLI{Out: io.Discard, Harness: h}).Run(ctx, []string{"--config", cfgPath, "run"}) }()
	view = waitView(id, func(v engine.View) bool { return v.Task.Stage == core.AwaitingApproval })
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
	run("task", "release", id, "--version", "2", "--preview", view.ReleasePreview)
	view = engine.View{}
	if err = json.Unmarshal([]byte(run("task", "show", id, "--json")), &view); err != nil {
		t.Fatal(err)
	}
	if view.Task.Release == nil || len(view.Task.Release.Children) != 2 {
		t.Fatal("release missing")
	}
	var dependent engine.View
	if err = json.Unmarshal([]byte(run("task", "show", view.Task.Release.Children[1], "--json")), &dependent); err != nil {
		t.Fatal(err)
	}
	if dependent.Group != "Waiting" || dependent.Eligibility.WaitKind != "dependency" {
		t.Fatal("dependency not explained", dependent)
	}
	advice = run("task", "show", dependent.Task.ID)
	if !strings.Contains(advice, view.Task.Release.Children[0]) || !strings.Contains(advice, "exact completed version") {
		t.Fatal("dependency wait hides prerequisite or release condition", advice)
	}
	t.Log("Released Plan: dependent names its prerequisite and waits for exact-version completion.")
	human := run("status")
	var structured engine.Status
	if err = json.Unmarshal([]byte(run("status", "--json")), &structured); err != nil {
		t.Fatal(err)
	}
	for _, row := range structured.Tasks {
		if !strings.Contains(human, row.Summary) || !strings.Contains(human, row.NextAction) {
			t.Fatal("human/JSON advice differs")
		}
	}
	if strings.Index(human, "Needs you") > strings.Index(human, "Waiting") || strings.Index(human, "Waiting") > strings.Index(human, "Retained") {
		t.Fatal("status is not attention first", human)
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

	// Recover this same journey rather than duplicating Plan, Ready and quality
	// setup in another suite. All recorded inputs and historical bindings survive.
	bundle := filepath.Join(root, "backup")
	var snapshot backup.Report
	if err = json.Unmarshal([]byte(run("backup", "--to", bundle)), &snapshot); err != nil {
		t.Fatal(err)
	}
	original, err := store.ReadImage(ctx, cfg.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	run("status")
	for _, path := range snapshot.Destinations {
		if err = os.Rename(path, path+".original"); err != nil {
			t.Fatal(err)
		}
	}
	var restored backup.Report
	if err = json.Unmarshal([]byte(run("restore", "--from", bundle)), &restored); err != nil {
		t.Fatal(err)
	}
	if restored.Applied || restored.InspectionOnly {
		t.Fatal(restored)
	}
	run("restore", "--from", bundle, "--apply")
	if err = json.Unmarshal([]byte(run("status", "--json")), &structured); err != nil {
		t.Fatal(err)
	}
	if !structured.Drained || structured.Restoration == nil || structured.Restoration.InspectionOnly {
		t.Fatal("restore status lost disposition")
	}
	afterRestore, err := store.ReadImage(ctx, cfg.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	afterRestore.Settings.Restoration = nil
	if core.Digest(original) != core.Digest(afterRestore) {
		t.Fatal("restore changed journey history")
	}
	// The first restore consumes continuity. Restoring that snapshot again must
	// present history without executable advice or a configuration-save bypass.
	for _, path := range snapshot.Destinations {
		if err = os.Rename(path, path+".restored"); err != nil {
			t.Fatal(err)
		}
	}
	run("restore", "--from", bundle, "--apply")
	if text := run("status"); !strings.Contains(text, "inspection-only") || strings.Contains(text, "run to") {
		t.Fatal("held status gives executable advice", text)
	}
	view = engine.View{}
	if err = json.Unmarshal([]byte(run("task", "show", readyID, "--json")), &view); err != nil {
		t.Fatal(err)
	}
	if view.Preview != "" || view.QualityAdoptionPreview != "" || view.Restoration == nil || !view.Restoration.InspectionOnly {
		t.Fatal("held task grants approval")
	}
	for _, args := range [][]string{{"run"}, {"drain"}, {"task", "stop", readyID}, {"task", "resume", readyID, "--attempt", "saved", "--allow-new-invocation"}, {"config", "apply", "--preview", "any"}} {
		err = (CLI{Out: io.Discard, Harness: h}).Run(ctx, append([]string{"--config", cfgPath}, args...))
		if err == nil || !strings.Contains(err.Error(), "inspection-only") {
			t.Fatal("held command admitted", args, err)
		}
	}
	edits = []string{"config", "edit", "--set", "parallelism=3"}
	preview = previewValue(t, run(edits...), "Save preview")
	err = (CLI{Out: io.Discard, Harness: h}).Run(ctx, append([]string{"--config", cfgPath}, append(edits, "--save", "--preview", preview)...))
	if err == nil || !strings.Contains(err.Error(), "inspection-only") {
		t.Fatal("held config save admitted", err)
	}

}

func commandFixture(t *testing.T) (string, string, config.Config) {
	t.Helper()
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
	return root, cfgPath, cfg
}
