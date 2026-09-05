package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/engine"
	"github.com/cortexium-io/runner/internal/github"
)

func TestWriteProjectPlanItemsSeparatesCardsAndHighlightsNumbers(t *testing.T) {
	plan := engine.ProjectPlan{WorkItems: []github.PlannedItem{
		{Title: "Build the foundation", Summary: "Create the user-visible shell.", AcceptanceCriteria: []string{"The shell is usable."}, Verification: []string{"Open the app."}},
		{Title: "Complete the workflow", Summary: "Finish the main behavior.", AcceptanceCriteria: []string{"The workflow is complete."}, Verification: []string{"Exercise the complete flow."}},
	}}
	var output bytes.Buffer

	writeProjectPlanItems(&output, plan, true)

	want := "\x1b[1;36m1.\x1b[0m Build the foundation\n" +
		"   Create the user-visible shell.\n" +
		"   acceptance: The shell is usable.\n" +
		"   verification: Open the app.\n\n" +
		"\x1b[1;36m2.\x1b[0m Complete the workflow\n"
	if !strings.Contains(output.String(), want) {
		t.Fatalf("plan items were not clearly separated and numbered:\n%q", output.String())
	}
}

func TestWriteBatchApprovalPreviewShowsEveryExactAuthorization(t *testing.T) {
	batch := github.BatchApprovalPlan{
		Source: github.WorkItem{ID: "PVTI_plan", Title: "Plan the work"}, Destination: "Ready",
		Children: []github.BatchApprovalItem{
			{Item: github.WorkItem{
				ID: "PVTI_one", Title: "First", Body: "Exact first body", Repository: "owner/repo", Status: "Needs assessment",
				Result: "prior result", Phase: "ready", Activity: "Implementing", QAFailures: 2, Branch: "runner/one",
				PullRequest: "https://github.com/owner/repo/pull/1", QACommit: "abc123",
			}, Role: "implementer", Assertion: "v2:first"},
			{Item: github.WorkItem{ID: "PVTI_two", Title: "Second", Body: "Exact second body", Repository: "owner/repo"}, Role: "implementer", Assertion: "v2:second"},
		},
	}
	var output bytes.Buffer
	writeBatchApprovalPreview(&output, batch)
	for _, expected := range []string{
		"Plan the work (PVTI_plan)", "Destination: Ready", "Exact staged children: 2",
		"First (PVTI_one)", "Current status: Needs assessment", "Result: prior result", "Phase: ready", "Activity: Implementing", "QA failures: 2",
		"Branch: runner/one", "Pull request: https://github.com/owner/repo/pull/1", "QA commit: abc123",
		"Exact first body", "Authenticated assertion: v2:first",
		"Second (PVTI_two)", "Exact second body", "Authenticated assertion: v2:second",
	} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("complete batch preview omitted %q:\n%s", expected, output.String())
		}
	}
}

func TestApprovalPreviewsEscapeUntrustedTerminalControls(t *testing.T) {
	unsafe := func(marker string) string {
		return marker + "\x1b[2J\x1b]0;spoof\a\r\b\t\u202e"
	}
	item := github.WorkItem{
		ID: unsafe("item-id"), Title: unsafe("title"), Body: unsafe("body") + "\n" + unsafe("body-line"),
		URL: unsafe("url"), Repository: unsafe("repository"), Dependencies: []string{unsafe("dependency")},
		Status: unsafe("status"), Result: unsafe("result"), Phase: unsafe("phase"), Activity: unsafe("activity"), QAFailures: 2,
		Branch: unsafe("branch"), PullRequest: unsafe("pull-request"), QACommit: unsafe("qa-commit"),
	}
	var output bytes.Buffer
	writeItemApprovalPreview(&output, github.ApprovalPlan{
		Item: item, Role: unsafe("role"), Assertion: unsafe("assertion"), RemoveIntakeLabel: true,
	}, unsafe("intake-label"))
	writeStagedProjectPlanSummary(&output, engine.ProjectPlanApproval{
		BatchFingerprint: unsafe("fingerprint"), Destination: unsafe("destination"), Children: []github.WorkItem{item},
	})
	writeStagedProjectPlanReceipt(&output, engine.ProjectPlanApproval{
		BatchFingerprint: unsafe("receipt-fingerprint"), Destination: unsafe("receipt-destination"), Children: []github.WorkItem{item},
	}, unsafe("config-path"))
	writeBatchApprovalPreview(&output, github.BatchApprovalPlan{
		Source: github.WorkItem{ID: unsafe("source-id"), Title: unsafe("source-title"), Approval: unsafe("staging-provenance")}, Destination: unsafe("batch-destination"),
		Children: []github.BatchApprovalItem{{Item: item, Role: unsafe("batch-role"), Assertion: unsafe("batch-assertion")}},
	})
	if _, err := confirmStagedPlanApproval(newInitPrompter(strings.NewReader("2\n"), &output), engine.ProjectPlanApproval{
		Destination: unsafe("confirmation-destination"), Children: []github.WorkItem{{ID: "one"}},
	}); err != nil {
		t.Fatalf("render staged approval confirmation: %v", err)
	}
	if _, err := confirmBatchApproval(newInitPrompter(strings.NewReader("2\n"), &output), github.BatchApprovalPlan{
		Destination: unsafe("batch-confirmation-destination"), Children: []github.BatchApprovalItem{{Item: github.WorkItem{ID: "one"}}},
	}); err != nil {
		t.Fatalf("render source-backed batch confirmation: %v", err)
	}

	rendered := output.String()
	for _, control := range []string{"\x1b", "\r", "\b", "\t", "\a", "\u202e"} {
		if strings.Contains(rendered, control) {
			t.Fatalf("approval preview emitted terminal control %q:\n%q", control, rendered)
		}
	}
	for _, marker := range []string{
		"item-id", "title", "body", "body-line", "url", "repository", "dependency", "status", "result", "phase",
		"branch", "pull-request", "qa-commit", "role", "assertion", "intake-label", "fingerprint",
		"destination", "receipt-fingerprint", "receipt-destination", "config-path", "source-id", "source-title", "staging-provenance", "batch-destination", "batch-role", "batch-assertion", "confirmation-destination", "batch-confirmation-destination",
	} {
		if !strings.Contains(rendered, marker+`\x1b[2J\x1b]0;spoof\a\r\b\t\u202e`) {
			t.Fatalf("approval preview did not visibly escape %q:\n%q", marker, rendered)
		}
	}
	if got := terminalSafeApprovalText("first\nsecond"); got != `first\nsecond` {
		t.Fatalf("embedded newline was not escaped: %q", got)
	}
	if got := terminalSafeApprovalText(`literal\x1b`); got != `literal\\x1b` {
		t.Fatalf("literal escape text was ambiguous with an escaped control: %q", got)
	}
}

func TestConfirmSourceBackedBatchApprovalDefaultsToNo(t *testing.T) {
	batch := github.BatchApprovalPlan{
		Destination: "Ready",
		Children:    []github.BatchApprovalItem{{Item: github.WorkItem{ID: "one"}}, {Item: github.WorkItem{ID: "two"}}},
	}
	for _, test := range []struct {
		name  string
		input string
		want  bool
	}{
		{name: "explicit yes", input: "1\n", want: true},
		{name: "default no", input: "\n", want: false},
		{name: "explicit no", input: "2\n", want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			accepted, err := confirmBatchApproval(newInitPrompter(strings.NewReader(test.input), &output), batch)
			if err != nil {
				t.Fatal(err)
			}
			if accepted != test.want {
				t.Fatalf("accepted = %t, want %t", accepted, test.want)
			}
		})
	}
}

func TestResolvePlanIdeaReadsInteractiveMultilineInput(t *testing.T) {
	input := strings.NewReader("Create an RTS game.\n\nUse one HTML file.\n")
	var output bytes.Buffer

	idea, err := resolvePlanIdea("", "", input, &output, true)
	if err != nil {
		t.Fatalf("resolve interactive plan idea: %v", err)
	}
	if idea != "Create an RTS game.\n\nUse one HTML file." {
		t.Fatalf("multiline idea = %q", idea)
	}
	for _, expected := range []string{"Project idea", "press Ctrl-D at the empty prompt", "> "} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("interactive prompt missing %q: %s", expected, output.String())
		}
	}
}

func TestMultilinePlanIdeaInstructionHighlightsCtrlD(t *testing.T) {
	colored := multilinePlanIdeaInstruction(true)
	if !strings.Contains(colored, "press \x1b[1;36mCtrl-D\x1b[0m at the empty prompt") {
		t.Fatalf("Ctrl-D was not highlighted with the question color: %q", colored)
	}
	plain := multilinePlanIdeaInstruction(false)
	if plain != "Enter as many lines as needed. When finished, press Ctrl-D at the empty prompt." {
		t.Fatalf("plain instruction changed: %q", plain)
	}
}

func TestResolvePlanIdeaAcceptsPipedInputUntilEOF(t *testing.T) {
	idea, err := resolvePlanIdea("", "", strings.NewReader("First line\nSecond line\n"), &bytes.Buffer{}, false)
	if err != nil {
		t.Fatalf("resolve piped plan idea: %v", err)
	}
	if idea != "First line\nSecond line" {
		t.Fatalf("piped idea = %q", idea)
	}
}

func TestResolvePlanIdeaPreservesExplicitInputs(t *testing.T) {
	idea, err := resolvePlanIdea("  direct idea  ", "", strings.NewReader("ignored"), &bytes.Buffer{}, false)
	if err != nil || idea != "direct idea" {
		t.Fatalf("direct idea = %q, error=%v", idea, err)
	}

	path := filepath.Join(t.TempDir(), "idea.md")
	if err := os.WriteFile(path, []byte("File idea\nwith detail\n"), 0o644); err != nil {
		t.Fatalf("write idea file: %v", err)
	}
	idea, err = resolvePlanIdea("", path, strings.NewReader("ignored"), &bytes.Buffer{}, false)
	if err != nil || idea != "File idea\nwith detail" {
		t.Fatalf("file idea = %q, error=%v", idea, err)
	}

	if _, err := resolvePlanIdea("direct", path, strings.NewReader(""), &bytes.Buffer{}, false); err == nil {
		t.Fatal("expected direct and file inputs to conflict")
	}
}

func TestResolvePlanIdeaRejectsEmptyInput(t *testing.T) {
	_, err := resolvePlanIdea("", "", strings.NewReader("\n"), &bytes.Buffer{}, false)
	if err == nil || !strings.Contains(err.Error(), "project idea is empty") {
		t.Fatalf("empty idea error = %v", err)
	}
}

func TestResolveWorkItemBodyRequiresOneNonEmptySource(t *testing.T) {
	path := filepath.Join(t.TempDir(), "work.md")
	if err := os.WriteFile(path, []byte("  Detailed work request.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := resolveWorkItemBody("  Inline request. ", ""); err != nil || got != "Inline request." {
		t.Fatalf("inline body = %q, error=%v", got, err)
	}
	if got, err := resolveWorkItemBody("", path); err != nil || got != "Detailed work request." {
		t.Fatalf("file body = %q, error=%v", got, err)
	}
	if _, err := resolveWorkItemBody("inline", path); err == nil || !strings.Contains(err.Error(), "either --body or --body-file") {
		t.Fatalf("two body sources error = %v", err)
	}
	if _, err := resolveWorkItemBody("", ""); err == nil || !strings.Contains(err.Error(), "is required") {
		t.Fatalf("missing body error = %v", err)
	}
}

func TestHumanWorkDestinationUsesConfiguredPlanAndReadyLanes(t *testing.T) {
	resolved, err := completeCLITestConfig(t.TempDir()).Resolve()
	if err != nil {
		t.Fatal(err)
	}
	plan, planAction, err := humanWorkDestination(resolved, "plan")
	if err != nil || plan != "Plan" || !strings.Contains(planAction, "planner") {
		t.Fatalf("plan destination = %q, action=%q, error=%v", plan, planAction, err)
	}
	ready, readyAction, err := humanWorkDestination(resolved, "ready")
	if err != nil || ready != "Ready" || !strings.Contains(readyAction, "dependencies") {
		t.Fatalf("ready destination = %q, action=%q, error=%v", ready, readyAction, err)
	}
	if _, _, err := humanWorkDestination(resolved, "other"); err == nil {
		t.Fatal("unknown destination was accepted")
	}
}

func TestApplyPlanTaskGranularityOverridesBothDownstreamRoles(t *testing.T) {
	cfg := completeCLITestConfig(t.TempDir())
	cfg.Roles["mechanical"] = config.RoleConfig{Extends: config.WorkRoleImplementer, TaskGranularity: config.TaskGranularityStandard, Description: "Mechanical"}
	cfg.PlannerImplementers = []string{"mechanical"}
	if err := applyPlanTaskGranularity(&cfg, config.TaskGranularitySmall); err != nil {
		t.Fatalf("apply task granularity: %v", err)
	}
	for _, contract := range []string{config.WorkRoleImplementer, config.WorkRoleReviewer} {
		roleID := cfg.RoleIDForContract(contract)
		profile, ok := cfg.RoleProfile(roleID)
		if !ok || profile.TaskGranularity != config.TaskGranularitySmall {
			t.Fatalf("%s task granularity = %#v, found=%t", contract, profile, ok)
		}
	}
	if profile, _ := cfg.RoleProfile("mechanical"); profile.TaskGranularity != config.TaskGranularitySmall {
		t.Fatal("small-tasks override omitted planner-selectable profile")
	}

	plannerID := cfg.RoleIDForContract(config.WorkRolePlanner)
	if profile, ok := cfg.RoleProfile(plannerID); !ok || profile.TaskGranularity != "" {
		t.Fatalf("planner profile was changed: %#v, found=%t", profile, ok)
	}
	if err := applyPlanTaskGranularity(&cfg, "automatic"); err == nil || !strings.Contains(err.Error(), "standard or small") {
		t.Fatalf("invalid task granularity error = %v", err)
	}
}

func TestPlanRejectsSmallTasksWhenApprovingStagedBatch(t *testing.T) {
	err := runPlan(t.Context(), []string{"--approve-staged", "v1:batch", "--small-tasks"}, strings.NewReader(""), io.Discard)
	if err == nil || !strings.Contains(err.Error(), "cannot be combined") {
		t.Fatalf("small tasks was accepted while approving staged batch: %v", err)
	}
}

func TestPlanApplyModeApprovesByDefaultWhenCreating(t *testing.T) {
	for _, test := range []struct {
		name                string
		create              bool
		stageOnly           bool
		interactiveAccepted bool
		wantStage           bool
		wantRelease         bool
	}{
		{name: "preview", wantStage: false, wantRelease: false},
		{name: "create", create: true, wantStage: true, wantRelease: true},
		{name: "explicit stage only", stageOnly: true, wantStage: true, wantRelease: false},
		{name: "interactive acceptance", interactiveAccepted: true, wantStage: true, wantRelease: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			gotStage, gotRelease := planApplyMode(test.create, test.stageOnly, test.interactiveAccepted)
			if gotStage != test.wantStage || gotRelease != test.wantRelease {
				t.Fatalf("planApplyMode() = (%t, %t), want (%t, %t)", gotStage, gotRelease, test.wantStage, test.wantRelease)
			}
		})
	}
}

func TestPlanRejectsCreateWithStageOnly(t *testing.T) {
	err := runPlan(t.Context(), []string{"--create", "--stage-only"}, strings.NewReader(""), io.Discard)
	if err == nil || !strings.Contains(err.Error(), "either --create") {
		t.Fatalf("create and stage-only were accepted together: %v", err)
	}
}

func TestPlanSerializesOtherStandalonePlanning(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	cfg := completeCLITestConfig(t.TempDir())
	configPath := filepath.Join(t.TempDir(), "runner.config.json")
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	projectLock, err := github.AcquirePlanningLock(*cfg.GitHubProject)
	if err != nil {
		t.Fatal(err)
	}
	defer projectLock.Release()

	err = runPlan(t.Context(), []string{"--config", configPath, "--idea", "Plan this safely."}, strings.NewReader(""), io.Discard)
	if err == nil || !strings.Contains(err.Error(), "planning is already active") {
		t.Fatalf("planning did not honor the standalone planning lock: %v", err)
	}
}

func TestConfirmPlanCreationOffersYesAndNo(t *testing.T) {
	cfg := completeCLITestConfig(t.TempDir())
	cfg.GitHubProject.Owner = "morphar"
	cfg.GitHubProject.Number = 8

	for _, test := range []struct {
		name  string
		input string
		want  bool
	}{
		{name: "yes", input: "1\n", want: true},
		{name: "no", input: "2\n", want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			confirmed, err := confirmPlanCreation(newInitPrompter(strings.NewReader(test.input), &output), cfg, 3)
			if err != nil {
				t.Fatalf("confirm plan creation: %v", err)
			}
			if confirmed != test.want {
				t.Fatalf("confirmed = %t, want %t", confirmed, test.want)
			}
			for _, expected := range []string{
				"Create and approve these 3 proposed cards in GitHub Project #8 (morphar)?",
				"Yes — Stage the complete batch, revalidate it, then release it to its configured work lane.",
				"No — Keep the preview",
			} {
				if !strings.Contains(output.String(), expected) {
					t.Fatalf("confirmation missing %q: %s", expected, output.String())
				}
			}
		})
	}
}

func TestConfirmPlanCreationUsesSingularCardLabel(t *testing.T) {
	cfg := completeCLITestConfig(t.TempDir())
	var output bytes.Buffer
	if _, err := confirmPlanCreation(newInitPrompter(strings.NewReader("2\n"), &output), cfg, 1); err != nil {
		t.Fatalf("confirm one card: %v", err)
	}
	if !strings.Contains(output.String(), "Create and approve this proposed card") {
		t.Fatalf("singular confirmation = %s", output.String())
	}
}

func TestWriteStagedProjectPlanSummaryIsCompact(t *testing.T) {
	approval := engine.ProjectPlanApproval{
		BatchFingerprint: "v1:batch",
		Destination:      "Ready",
		Children: []github.WorkItem{{
			ID: "PVTI_child", Title: "Exact child", Body: "Exact body", Repository: "owner/repo", Status: "Needs assessment",
		}},
	}
	var output bytes.Buffer

	writeStagedProjectPlanSummary(&output, approval)

	for _, expected := range []string{
		"Batch fingerprint: v1:batch", "Destination after approval: Ready", "Exact staged children: 1",
		"Exact child (PVTI_child)", "Current status: Needs assessment",
	} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("staged approval preview omitted %q:\n%s", expected, output.String())
		}
	}
	for _, repeatedDetail := range []string{"Exact body", "Result:", "Phase:", "Activity:", "QA failures:", "Exact source body:"} {
		if strings.Contains(output.String(), repeatedDetail) {
			t.Fatalf("staged plan summary repeated approval detail %q:\n%s", repeatedDetail, output.String())
		}
	}
}

func TestWriteStagedProjectPlanReceiptIsCompact(t *testing.T) {
	approval := engine.ProjectPlanApproval{
		BatchFingerprint: "v1:batch",
		Destination:      "Ready",
		Children: []github.WorkItem{
			{ID: "PVTI_one", Title: "First card", Body: "large exact body that belongs in the approval preview", Status: "Needs assessment"},
			{ID: "PVTI_two", Title: "Second card", Body: "another large exact body", Status: "Needs assessment"},
		},
	}
	var output bytes.Buffer

	writeStagedProjectPlanReceipt(&output, approval, "/project/.cortexium/runner.json")

	for _, expected := range []string{
		"Runner staged project plan",
		"Batch fingerprint: v1:batch",
		"Destination after approval: Ready",
		"Exact staged children: 2",
		"1. First card (PVTI_one)",
		"2. Second card (PVTI_two)",
		`cortexium-runner plan --config "/project/.cortexium/runner.json" --approve-staged v1:batch`,
	} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("staging receipt omitted %q:\n%s", expected, output.String())
		}
	}
	for _, repeatedDetail := range []string{"large exact body", "Activity:", "QA failures:", "Exact source body:"} {
		if strings.Contains(output.String(), repeatedDetail) {
			t.Fatalf("staging receipt repeated approval detail %q:\n%s", repeatedDetail, output.String())
		}
	}
}

func TestConfirmStagedPlanApprovalDefaultsToLeavingBatchStaged(t *testing.T) {
	approval := engine.ProjectPlanApproval{
		Destination: "Ready",
		Children:    []github.WorkItem{{ID: "PVTI_one"}, {ID: "PVTI_two"}},
	}
	for _, test := range []struct {
		name  string
		input string
		want  bool
	}{
		{name: "explicit yes", input: "1\n", want: true},
		{name: "default no", input: "\n", want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			accepted, err := confirmStagedPlanApproval(newInitPrompter(strings.NewReader(test.input), &output), approval)
			if err != nil {
				t.Fatal(err)
			}
			if accepted != test.want {
				t.Fatalf("accepted = %t, want %t", accepted, test.want)
			}
			for _, expected := range []string{
				"Approve all 2 exact staged cards and release them together to Ready?",
				"Yes — Authorize and release the complete displayed batch.",
				"No — Leave every card unapproved in staging.",
			} {
				if !strings.Contains(output.String(), expected) {
					t.Fatalf("confirmation omitted %q:\n%s", expected, output.String())
				}
			}
		})
	}
}

func TestInteractiveIdeaAndCreationConfirmationShareBufferedInput(t *testing.T) {
	cfg := completeCLITestConfig(t.TempDir())
	var output bytes.Buffer
	prompter := newInitPrompter(&eofBetweenReads{beforeEOF: []byte("Build the feature.\n"), afterEOF: []byte("2\n")}, &output)

	idea, err := readMultilinePlanIdea(prompter.input, &output, true)
	if err != nil {
		t.Fatalf("read plan idea: %v", err)
	}
	if idea != "Build the feature." {
		t.Fatalf("idea = %q", idea)
	}
	confirmed, err := confirmPlanCreation(prompter, cfg, 2)
	if err != nil {
		t.Fatalf("confirm plan creation: %v", err)
	}
	if confirmed {
		t.Fatal("No selection after Ctrl-D was lost")
	}
}

type eofBetweenReads struct {
	beforeEOF []byte
	afterEOF  []byte
	sentEOF   bool
}

func (r *eofBetweenReads) Read(destination []byte) (int, error) {
	if len(r.beforeEOF) > 0 {
		count := copy(destination, r.beforeEOF)
		r.beforeEOF = r.beforeEOF[count:]
		return count, nil
	}
	if !r.sentEOF {
		r.sentEOF = true
		return 0, io.EOF
	}
	if len(r.afterEOF) > 0 {
		count := copy(destination, r.afterEOF)
		r.afterEOF = r.afterEOF[count:]
		return count, nil
	}
	return 0, io.EOF
}

func TestPlanApprovalPreviewShowsSelectedExecutionProfile(t *testing.T) {
	var output bytes.Buffer
	writeProjectPlanItems(&output, engine.ProjectPlan{WorkItems: []github.PlannedItem{{Title: "Bounded task", ImplementationProfile: "mechanical", ProfileReason: "Existing pattern"}}}, false)
	if !strings.Contains(output.String(), "execution profile: mechanical — Existing pattern") {
		t.Fatal("plan preview hides profile choice")
	}
	output.Reset()
	writeStagedProjectPlanSummary(&output, engine.ProjectPlanApproval{Children: []github.WorkItem{{ID: "item", ImplementationProfile: "mechanical"}}})
	if !strings.Contains(output.String(), "Execution profile: mechanical") {
		t.Fatal("staged approval hides profile choice")
	}
}
