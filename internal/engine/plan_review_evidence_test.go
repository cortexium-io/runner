package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/execution"
	"github.com/cortexium-io/runner/internal/github"
	"github.com/cortexium-io/runner/internal/metrics"
	"github.com/cortexium-io/runner/internal/subprocess"
	"github.com/cortexium-io/runner/internal/workspace"
)

type observedPlanEvidence struct {
	manifest workspace.ReviewEvidenceManifest
	files    map[string]string
}

// Only model/GitHub transport is substituted. Receipts are produced in the
// implementer's real worktree and observed through the actual reviewer grant.
type planEvidenceRunner struct {
	*deliveryMilestoneRunner
	produceEvidence                            bool
	memberRoots                                map[string]string
	cardEvidence                               map[string]observedPlanEvidence
	planEvidence                               []map[string]observedPlanEvidence
	gateLog                                    string
	interruptIntegration, interruptPublication bool
	interruptBeforeComment                     bool
	planReviewSummary                          string
	interrupted, offline                       bool
}

func (r *planEvidenceRunner) RunBoundedHeadTailInput(ctx context.Context, command string, args []string, dir string, timeout time.Duration, input io.Reader, _ int, _ string) (subprocess.Result, error) {
	return runEngineTestWithInput(ctx, command, args, dir, timeout, input, r.Run)
}

func (r *planEvidenceRunner) Run(ctx context.Context, command string, args []string, dir string, timeout time.Duration) (subprocess.Result, error) {
	if r.offline {
		return subprocess.Result{}, context.Canceled
	}
	if r.interruptBeforeComment && !r.interrupted && command == "gh" && len(args) > 1 && args[0] == "issue" && args[1] == "comment" {
		r.interrupted, r.offline = true, true
		return subprocess.Result{}, context.Canceled
	}
	planReview := false
	if command == "codex" {
		data, err := os.ReadFile(argumentValue(args, "--output-schema"))
		if err != nil {
			return subprocess.Result{}, err
		}
		var schema struct{ Properties map[string]any }
		if err := json.Unmarshal(data, &schema); err != nil {
			return subprocess.Result{}, err
		}
		if schema.Properties["checks"] != nil || schema.Properties["criteria"] != nil {
			planReview = strings.Contains(strings.Join(args, " "), `"review_scope":"plan"`)
			root := ""
			const marker = "Runner-captured read-only review evidence root: "
			for _, line := range strings.Split(args[len(args)-1], "\n") {
				if strings.HasPrefix(line, marker) {
					root = strings.TrimPrefix(line, marker)
				}
			}
			if root != "" {
				if planReview {
					members, err := filepath.Glob(filepath.Join(root, "members", "*", "manifest.json"))
					if err != nil {
						return subprocess.Result{}, err
					}
					observed := map[string]observedPlanEvidence{}
					for _, manifest := range members {
						evidence, err := readPlanEvidence(filepath.Dir(manifest))
						if err != nil || evidence.manifest.Provenance == nil {
							return subprocess.Result{}, fmt.Errorf("read member evidence: %v", err)
						}
						id := evidence.manifest.Provenance.MemberID
						if _, duplicate := observed[id]; duplicate {
							return subprocess.Result{}, fmt.Errorf("duplicate member evidence %s", id)
						}
						observed[id] = evidence
					}
					r.planEvidence = append(r.planEvidence, observed)
				} else {
					evidence, err := readPlanEvidence(root)
					if err != nil || evidence.manifest.Provenance == nil {
						return subprocess.Result{}, fmt.Errorf("read card evidence: %v", err)
					}
					r.cardEvidence[evidence.manifest.Provenance.MemberID] = evidence
				}
			}
		} else {
			root := profileReadRoot(args, dir)
			id := "PVTI_created_2"
			if strings.Contains(root, "created_3") {
				id = "PVTI_created_3"
			}
			r.memberRoots[id] = root
			// This tracked ignore rule is part of each real candidate. The
			// identically named receipts themselves never enter the plan branch.
			if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("reports/\n"), 0600); err != nil {
				return subprocess.Result{}, err
			}
			if r.produceEvidence {
				if err := writePlanReceipts(root, id); err != nil {
					return subprocess.Result{}, err
				}
			}
		}
	}
	result, err := r.deliveryMilestoneRunner.Run(ctx, command, args, dir, timeout)
	if err == nil && planReview && r.planReviewSummary != "" {
		path := argumentValue(args, "--output-last-message")
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return result, readErr
		}
		var assessment map[string]any
		if err := json.Unmarshal(data, &assessment); err != nil {
			return result, err
		}
		assessment["summary"] = r.planReviewSummary
		data, err = json.Marshal(assessment)
		if err != nil {
			return result, err
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			return result, err
		}
	}
	// A lost successful response also prevents failure handlers writing state,
	// matching a coordinator interruption at the actual irreversible boundary.
	integration := r.interruptIntegration && command == "git" && containsArgument(args, "push") && r.planPushes == 2
	publication := r.interruptPublication && command == "gh" && len(args) > 1 && args[0] == "pr" && args[1] == "create"
	if !r.interrupted && err == nil && (integration || publication) {
		r.interrupted, r.offline = true, true
		return result, context.Canceled
	}
	return result, err
}

func writePlanReceipts(root, id string) error {
	if err := os.MkdirAll(filepath.Join(root, "reports"), 0700); err != nil {
		return err
	}
	for _, name := range []string{"baseline.json", "final.json"} {
		if err := os.WriteFile(filepath.Join(root, "reports", name), []byte(id+":"+name+"\n"), 0600); err != nil {
			return err
		}
	}
	return nil
}

func readPlanEvidence(root string) (observedPlanEvidence, error) {
	observed := observedPlanEvidence{files: map[string]string{}}
	data, err := os.ReadFile(filepath.Join(root, "manifest.json"))
	if err != nil {
		return observed, err
	}
	if err := json.Unmarshal(data, &observed.manifest); err != nil {
		return observed, err
	}
	for _, file := range observed.manifest.Files {
		data, err := os.ReadFile(filepath.Join(root, "files", filepath.FromSlash(file.Path)))
		if err != nil {
			return observed, err
		}
		observed.files[file.Path] = string(data)
	}
	return observed, nil
}

func attachPlanEvidenceRunner(t *testing.T, f *deliveryRunFixture) *planEvidenceRunner {
	t.Helper()
	r := &planEvidenceRunner{deliveryMilestoneRunner: f.runner, memberRoots: map[string]string{}, cardEvidence: map[string]observedPlanEvidence{}}
	restartPlanEvidenceEngine(t, f, r)
	return r
}

// The shared constructor releases a plan without evidence paths. Configure
// this variant before approval so evidence selection matches the approved
// profile throughout delivery, including historical acceptance recovery.
func newProductionEvidenceDelivery(t *testing.T) (*deliveryRunFixture, *planEvidenceRunner) {
	t.Helper()
	repo, remote := createPublicationRepository(t)
	runner := &deliveryMilestoneRunner{project: &fakeGitHubProjectRunner{itemsJSON: `{"items":[]}`}}
	gateLog := filepath.Join(t.TempDir(), "complete-gate.log")
	cfg := completeEngineTestConfig(config.Config{ProjectDir: repo, ReviewEvidencePaths: []string{"reports"},
		PlanDelivery: &config.PlanDeliveryConfig{Enabled: true, CompleteVerification: "complete"},
		Verification: map[string]config.VerificationEntrypoint{"complete": {
			Command: "/bin/sh", ToolchainCommands: []string{"/bin/sh"}, TimeoutSeconds: 30,
			Args: []string{"-c", "printf 'gate\\n' >> \"$1\"; test \"$(cat member-1.txt)\" = ready && test \"$(cat member-2.txt)\" = ready", "complete-gate", gateLog}, InputPaths: []string{"member-1.txt", "member-2.txt"},
		}},
	})
	profile := cfg.Roles["reviewer"]
	profile.Access = config.RoleAccessHost
	cfg.Roles["reviewer"] = profile
	f := &deliveryRunFixture{cfg: cfg, runner: runner, repo: repo, remote: remote}
	r := attachPlanEvidenceRunner(t, f)
	r.gateLog = gateLog
	children, err := f.service.ApplyProjectPlan(t.Context(), sourcedDirectProjectPlanFixture(t, repo))
	if err != nil {
		t.Fatal(err)
	}
	preview, err := f.service.PlanStagedProjectPlanApproval(t.Context(), children[0].PlanningBatchFingerprint)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.ApplyProjectPlanApproval(t.Context(), preview); err != nil {
		t.Fatal(err)
	}
	f.parentID = children[0].PlanningSourceID
	r.produceEvidence = true
	return f, r
}

func restartPlanEvidenceEngine(t *testing.T, f *deliveryRunFixture, r *planEvidenceRunner) {
	t.Helper()
	var err error
	f.service, err = New(f.cfg, r)
	if err != nil {
		t.Fatal(err)
	}
}

func planEvidenceChildren(t *testing.T, f *deliveryRunFixture) []github.WorkItem {
	t.Helper()
	items, err := f.service.source.LifecycleItems(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var children []github.WorkItem
	for _, item := range items {
		if item.PlanningSourceID == f.parentID {
			children = append(children, item)
		}
	}
	if len(children) != 2 {
		t.Fatalf("expected exact two-member fixture, got %d", len(children))
	}
	return children
}

func planMemberEvidenceAcceptance(t *testing.T, f *deliveryRunFixture, child github.WorkItem) (workspace.Metadata, workspace.PublicationRecord) {
	t.Helper()
	provider := workspace.NewGitProvider(f.service.run)
	metadata, err := provider.InspectRetainedReview(t.Context(), workspace.Request{
		WorkingDir: f.repo, WorktreeRoot: f.cfg.Harnesses[0].WorkspaceWriteRoot, WorkID: "assignment_" + safeRefComponent(child.ID),
		ItemID: child.ID, Repository: child.Repository, DelegatedContentDigest: github.DelegatedContentFor(child).Digest,
		BranchName: child.Branch, BaseRef: "origin/" + f.parent(t).Branch,
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := workspace.CaptureCheckoutSnapshotStateWithLimits(t.Context(), f.service.run, metadata.WorktreePath, 30*time.Second, workspace.DefaultSnapshotLimits())
	if err != nil {
		t.Fatal(err)
	}
	record, found, err := provider.LoadPublicationAcceptance(t.Context(), metadata, snapshot, workspace.PublicationEvidence{PlanRevision: github.PlanRevision(f.parent(t).Body)})
	if err != nil || !found {
		t.Fatalf("missing exact member acceptance: found=%t err=%v", found, err)
	}
	return metadata, record
}

// Model the on-disk state of the earlier engine, which accepted this exact
// candidate but did not retain evidence. Keep approved config, Project
// authority, Git objects and every other acceptance field intact.
func modelLegacyPlanAcceptance(t *testing.T, f *deliveryRunFixture, child github.WorkItem) workspace.PublicationRecord {
	t.Helper()
	metadata, record := planMemberEvidenceAcceptance(t, f, child)
	if record.ReviewEvidenceDigest == "" {
		t.Fatal("fixture did not create the acceptance to model as historical")
	}
	root := filepath.Join(filepath.Dir(metadata.WorktreePath), ".runner-state", "publications")
	paths, err := filepath.Glob(filepath.Join(root, "v3", record.CommitOID+"*.json"))
	if err != nil {
		t.Fatal(err)
	}
	legacy := record
	legacy.ReviewEvidenceDigest = ""
	matches := 0
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var stored workspace.PublicationRecord
		if err := json.Unmarshal(data, &stored); err != nil {
			t.Fatal(err)
		}
		if stored != record {
			continue
		}
		data, err = json.Marshal(legacy)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		matches++
	}
	if matches != 1 {
		t.Fatalf("expected one exact historical acceptance for %s, got %d", child.ID, matches)
	}
	if err := os.RemoveAll(filepath.Join(root, "review-evidence", record.ReviewEvidenceDigest)); err != nil {
		t.Fatal(err)
	}
	_, reloaded := planMemberEvidenceAcceptance(t, f, child)
	if reloaded != legacy {
		t.Fatal("historical acceptance did not retain its exact validated authority and candidate")
	}
	return reloaded
}

func runPlanEvidenceCycle(t *testing.T, f *deliveryRunFixture) []RunResult {
	t.Helper()
	results, err := f.service.RunCycle(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, result := range results {
		if result.Error != "" || result.Outcome == execution.OutcomeBlocked {
			t.Fatalf("production cycle: %s: %s", result.Summary, result.Error)
		}
	}
	return results
}

func assertWholePlanEvidence(t *testing.T, f *deliveryRunFixture, r *planEvidenceRunner, kind string) {
	t.Helper()
	if len(r.planEvidence) != 1 || len(r.planEvidence[0]) != 2 {
		t.Fatalf("whole-plan reviewer did not receive exactly two separate member bundles: %#v", r.planEvidence)
	}
	for _, child := range planEvidenceChildren(t, f) {
		got, ok := r.planEvidence[0][child.ID]
		if !ok || got.manifest.Provenance == nil {
			t.Fatalf("missing member %s", child.ID)
		}
		p := got.manifest.Provenance
		if p.Kind != kind || p.ParentID != f.parentID || p.MemberID != child.ID || p.ContentDigest != github.DelegatedContentFor(child).Digest || p.PlanRevision != github.PlanRevision(f.parent(t).Body) || got.manifest.CandidateCommit != child.QACommit || got.manifest.SourceRoot != r.memberRoots[child.ID] {
			t.Fatalf("lost original member/candidate provenance: %+v", got.manifest)
		}
		want := map[string]string{"reports/baseline.json": child.ID + ":baseline.json\n", "reports/final.json": child.ID + ":final.json\n"}
		if !reflect.DeepEqual(got.files, want) || len(got.manifest.MissingPaths) != 0 {
			t.Fatalf("same-name receipts overwritten or recaptured for %s: %#v", child.ID, got)
		}
	}
}

func TestProductionPlanReviewPreservesMemberReceipts(t *testing.T) {
	f, r := newProductionEvidenceDelivery(t)
	historyStore := metrics.NewStore(filepath.Join(t.TempDir(), "metrics", "metrics.jsonl"))
	f.service.SetMetricsObserver(func(event metrics.Event) error {
		err := historyStore.Append(event)
		if err != nil {
			t.Errorf("record delivery metrics: %v", err)
		}
		return err
	})
	f.integrateMembers(t)
	if len(r.cardEvidence) != 2 {
		t.Fatalf("card reviewers did not observe both member captures: %d", len(r.cardEvidence))
	}
	for id, root := range r.memberRoots {
		if err := os.WriteFile(filepath.Join(root, "reports", "final.json"), []byte("later unreviewed output for "+id), 0600); err != nil {
			t.Fatal(err)
		}
	}
	runPlanEvidenceCycle(t, f)
	assertWholePlanEvidence(t, f, r, workspace.EvidenceAccepted)
	for id, observed := range r.planEvidence[0] {
		if !reflect.DeepEqual(observed, r.cardEvidence[id]) {
			t.Fatalf("whole-plan reviewer did not receive the exact earlier card snapshot for %s", id)
		}
	}
	if f.parent(t).Status != "PR Ready" || r.implementations != 2 || r.reviews != 3 || r.creates != 1 {
		t.Fatalf("unexpected delivery/model counts: parent=%s implementations=%d reviews=%d PRs=%d", f.parent(t).Status, r.implementations, r.reviews, r.creates)
	}
	history, err := historyStore.Read()
	if err != nil || history.MalformedRecords != 0 {
		t.Fatalf("read delivery metrics: malformed=%d err=%v", history.MalformedRecords, err)
	}
	summary := metrics.Summarize(history.Attempts)
	if summary.CompletedAttempts != 5 || summary.UnfinishedAttempts != 0 || summary.StageCoveredAttempts != 5 {
		t.Fatalf("delivery metrics lost implementation/review attempts: %+v", summary)
	}
	for _, name := range []string{metrics.StageAuthorityValidation, metrics.StageEvidenceCapture, metrics.StageSnapshotValidation, metrics.StagePlanIntegration} {
		observed := false
		for _, stage := range summary.Stages {
			if stage.Name == name {
				observed = stage.Completed > 0 && stage.Completed == stage.Runs && stage.Failed == 0 && stage.Blocked == 0
			}
		}
		if !observed {
			t.Errorf("delivery did not successfully observe completed %s stages: %+v", name, summary.Stages)
		}
	}
	var totalDuration int64
	for _, attempt := range history.Attempts {
		if attempt.IsRunnerObservation() {
			continue
		}
		totalDuration += attempt.DurationMilliseconds
		uncovered := metrics.Summarize([]metrics.Attempt{attempt}).UncoveredDurationMilliseconds
		if uncovered < 0 || uncovered > attempt.DurationMilliseconds {
			t.Errorf("attempt %s uncovered duration %d exceeds [0, %d]", attempt.AttemptID, uncovered, attempt.DurationMilliseconds)
		}
		// Overlapping observations must contribute their interval union, not
		// add duration twice. Reuse this run's intervals without another delivery.
		attempt.Stages = append(append([]metrics.Stage(nil), attempt.Stages...), attempt.Stages...)
		if doubled := metrics.Summarize([]metrics.Attempt{attempt}).UncoveredDurationMilliseconds; doubled != uncovered {
			t.Errorf("attempt %s counted overlapping intervals twice: uncovered=%d duplicated=%d", attempt.AttemptID, uncovered, doubled)
		}
	}
	t.Logf("delivery metrics: four required stages observed; uncovered=%dms of %dms", summary.UncoveredDurationMilliseconds, totalDuration)
}

func TestProductionPlanReviewRecoversLegacyEvidenceByParentRetry(t *testing.T) {
	f, r := newProductionEvidenceDelivery(t)
	r.produceEvidence = false
	f.integrateMembers(t)
	for _, item := range append(planEvidenceChildren(t, f), f.parent(t)) {
		action, err := f.service.source.Authorize(t.Context(), item)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.service.source.TransitionRejection(t.Context(), action, item.Status, item.Phase, "Retained rejection history", 2); err != nil {
			t.Fatal(err)
		}
	}
	children := planEvidenceChildren(t, f)
	accepted := map[string]workspace.PublicationRecord{}
	for _, child := range children {
		accepted[child.ID] = modelLegacyPlanAcceptance(t, f, child)
	}
	for id, root := range r.memberRoots {
		if err := writePlanReceipts(root, id); err != nil {
			t.Fatal(err)
		}
	}
	restartPlanEvidenceEngine(t, f, r)
	results, err := f.service.RunCycle(t.Context())
	if err != nil || len(results) != 1 || !strings.Contains(results[0].Error, "predates durable evidence capture") || f.parent(t).Status != "Blocked" || r.reviews != 2 || r.implementations != 2 {
		_, _, authorityErr := f.service.source.DeliveryForItem(t.Context(), f.parent(t))
		t.Fatalf("legacy parent did not refuse missing accepted evidence before model: results=%#v err=%v parent=%s reviews=%d authority=%v", results, err, f.parent(t).Status, r.reviews, authorityErr)
	}
	blocked := f.parent(t)
	preview, err := f.service.PlanProjectItemRetry(t.Context(), f.parentID)
	if err != nil {
		t.Fatal(err)
	}
	if preview.TargetStatus != "Agent QA" || preview.EvidenceRecovery == nil || len(preview.EvidenceRecovery.Members) != 2 {
		t.Fatalf("retry preview omitted exact evidence recovery: %+v", preview)
	}
	for _, member := range preview.EvidenceRecovery.Members {
		p := member.Evidence.Manifest.Provenance
		if !member.RecoveryRequired || p == nil || p.Kind != workspace.EvidenceRecovered || p.AcceptanceDigest != member.AcceptanceDigest || len(member.Evidence.Manifest.Files) != 2 {
			t.Fatalf("recovered bytes impersonated prior acceptance: %+v", member)
		}
	}
	if !reflect.DeepEqual(blocked, f.parent(t)) || !reflect.DeepEqual(children, planEvidenceChildren(t, f)) {
		t.Fatal("preview mutated parent or exact children")
	}
	if len(preservedPlanEvidence(t, f)) != 0 {
		t.Fatal("preview persisted unconfirmed recovery evidence")
	}
	// Reject both altered caller input and changed retained bytes before saving
	// recovery or moving the parent; neither refusal may requeue implementation.
	preview.EvidenceRecovery.Members[0].Evidence.Digest = strings.Repeat("f", 64)
	if _, err := f.service.ApplyProjectItemRetry(t.Context(), preview); err == nil {
		t.Fatal("modified preview accepted")
	}
	preview, err = f.service.PlanProjectItemRetry(t.Context(), f.parentID)
	if err != nil {
		t.Fatal(err)
	}
	member := preview.EvidenceRecovery.Members[0]
	if err := os.WriteFile(filepath.Join(member.Workspace, "reports", "final.json"), []byte("changed since preview"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.ApplyProjectItemRetry(t.Context(), preview); err == nil {
		t.Fatal("changed retained evidence accepted against old preview")
	}
	if !reflect.DeepEqual(blocked, f.parent(t)) || !reflect.DeepEqual(children, planEvidenceChildren(t, f)) || r.reviews != 2 || r.implementations != 2 {
		t.Fatal("refused retry mutated lifecycle/counters or invoked a model")
	}
	if len(preservedPlanEvidence(t, f)) != 0 {
		t.Fatal("refused retry persisted evidence from an altered preview")
	}
	if err := writePlanReceipts(member.Workspace, member.ID); err != nil {
		t.Fatal(err)
	}
	preview, err = f.service.PlanProjectItemRetry(t.Context(), f.parentID)
	if err != nil {
		t.Fatal(err)
	}
	retried, err := f.service.ApplyProjectItemRetry(t.Context(), preview)
	if err != nil {
		t.Fatal(err)
	}
	if retried.ID != f.parentID || retried.Status != "Agent QA" || retried.QAFailures != 2 || !reflect.DeepEqual(children, planEvidenceChildren(t, f)) || r.implementations != 2 || r.reviews != 2 {
		t.Fatal("parent retry changed exact integrated children, reset counters or repeated model work")
	}
	for _, child := range children {
		metadata, record := planMemberEvidenceAcceptance(t, f, child)
		if record != accepted[child.ID] || record.ReviewEvidenceDigest != "" {
			t.Fatal("recovery rewrote the historical acceptance or claimed the original reviewer saw recovered bytes")
		}
		evidence, found, err := workspace.LoadAcceptedEvidence(t.Context(), metadata, record, f.parentID, f.cfg.ReviewEvidencePaths, workspace.DefaultSnapshotLimits())
		if err != nil || !found || evidence.Manifest.Provenance == nil || evidence.Manifest.Provenance.Kind != workspace.EvidenceRecovered || evidence.Manifest.Provenance.AcceptanceDigest != workspace.PublicationAcceptanceDigest(record) {
			t.Fatalf("recovered evidence lost exact historical provenance: found=%t err=%v manifest=%+v", found, err, evidence.Manifest)
		}
	}
	restartPlanEvidenceEngine(t, f, r)
	runPlanEvidenceCycle(t, f)
	assertWholePlanEvidence(t, f, r, workspace.EvidenceRecovered)
	if f.parent(t).Status != "PR Ready" || f.parent(t).QAFailures != 2 || r.implementations != 2 || r.reviews != 3 || r.creates != 1 || !reflect.DeepEqual(children, planEvidenceChildren(t, f)) {
		t.Fatal("recovered parent did not publish through QA with original members/counters")
	}
}

type preservedPlanEvidenceFile struct {
	bytes string
	info  fs.FileInfo
}

func preservedPlanEvidence(t *testing.T, f *deliveryRunFixture) map[string]preservedPlanEvidenceFile {
	t.Helper()
	root := filepath.Join(f.cfg.Harnesses[0].WorkspaceWriteRoot, ".runner-state", "publications", "review-evidence")
	files := map[string]preservedPlanEvidenceFile{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		files[path] = preservedPlanEvidenceFile{bytes: string(data), info: info}
		return err
	})
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return files
}

func TestProductionPlanReviewEvidenceSurvivesInterruptedPublication(t *testing.T) {
	for _, boundary := range []string{"member integration", "final publication"} {
		t.Run(boundary, func(t *testing.T) {
			f, r := newProductionEvidenceDelivery(t)
			r.interruptIntegration, r.interruptPublication = boundary == "member integration", boundary == "final publication"
			for cycle := 0; cycle < 8 && !r.interrupted; cycle++ {
				results, err := f.service.RunCycle(t.Context())
				if !r.interrupted {
					if err != nil {
						t.Fatal(err)
					}
					for _, result := range results {
						if result.Error != "" {
							t.Fatalf("before interruption: %s", result.Error)
						}
					}
				}
			}
			if !r.interrupted {
				t.Fatal("production coordinator never reached the requested interruption")
			}
			before := preservedPlanEvidence(t, f)
			if len(before) == 0 {
				t.Fatal("acceptance did not durably preserve reviewed receipts before publication")
			}
			for id, root := range r.memberRoots {
				if err := os.WriteFile(filepath.Join(root, "reports", "final.json"), []byte("later unreviewed output for "+id), 0600); err != nil {
					t.Fatal(err)
				}
			}
			r.offline = false
			restartPlanEvidenceEngine(t, f, r)
			for cycle := 0; cycle < 8 && f.parent(t).Status != "PR Ready"; cycle++ {
				runPlanEvidenceCycle(t, f)
			}
			assertWholePlanEvidence(t, f, r, workspace.EvidenceAccepted)
			if f.parent(t).Status != "PR Ready" || r.implementations != 2 || r.reviews != 3 || r.creates != 1 || r.planPushes != 3 {
				t.Fatalf("recovery repeated work: parent=%s implementation=%d review=%d PR=%d pushes=%d", f.parent(t).Status, r.implementations, r.reviews, r.creates, r.planPushes)
			}
			after := preservedPlanEvidence(t, f)
			if len(after) != 6 { // Exactly one manifest and two original receipts per child.
				t.Fatalf("duplicate or missing durable captures: %d files", len(after))
			}
			for path, old := range before {
				current, ok := after[path]
				if !ok || old.bytes != current.bytes || !os.SameFile(old.info, current.info) || !old.info.ModTime().Equal(current.info.ModTime()) {
					t.Fatalf("recovery rewrote or recaptured accepted evidence: %s", path)
				}
			}
		})
	}
}

func TestProductionPlanReviewProgressRechecksMemberEvidence(t *testing.T) {
	f, r := newProductionEvidenceDelivery(t)
	f.integrateMembers(t)
	integrated := f.parent(t)
	if _, err := f.service.workspaceForItem(t.Context(), integrated, github.DelegatedContentFor(integrated).Digest, f.repo); err != nil {
		t.Fatal(err)
	}
	advanceRemoteBase(t, f.repo, "base-addition.txt", "new destination behavior\n")
	results, err := f.service.RunCycle(t.Context())
	if err != nil || len(results) != 1 || results[0].Outcome != "warning" || r.reviews != 2 {
		t.Fatalf("destination refresh before parent QA: results=%+v err=%v", results, err)
	}
	r.interruptPublication = true
	_, _ = f.service.RunCycle(t.Context())
	if !r.interrupted || r.reviews != 3 || r.creates != 1 {
		t.Fatal("did not retain accepted whole-plan progress before publication response loss")
	}
	r.offline = false
	progress := retainedPlanProgress(t, f)
	if progress.Publication == nil || progress.Accepted.ReviewAssessment == nil || progress.EvidenceCollectionDigest == "" {
		t.Fatal("fixture lacks exact parent acceptance and its reviewed evidence collection")
	}
	gateRuns, err := os.ReadFile(r.gateLog)
	if err != nil || string(gateRuns) != "gate\n" {
		t.Fatalf("expected one actual complete gate: %q err=%v", gateRuns, err)
	}
	parent := f.parent(t)
	if progress.Candidate.Head == integrated.QACommit || parent.QACommit != integrated.QACommit || r.planPushes != 4 {
		t.Fatal("fixture must retain integrated P in Project after publication pushed refreshed R")
	}
	if head := strings.TrimSpace(runGitTest(t, "", "--git-dir", f.remote, "rev-parse", "refs/heads/"+parent.Branch)); head != progress.Candidate.Head {
		t.Fatal("lost publication did not leave the exact refreshed candidate on the remote")
	}
	children := planEvidenceChildren(t, f)
	// Exercise both older progress and a different valid collection digest at
	// the resume boundary. Reuse this checkpoint, without another delivery.
	for _, digest := range []string{"", strings.Repeat("f", 64)} {
		stale := *progress
		stale.EvidenceCollectionDigest = digest
		if err := f.service.savePlanVerification(parent, github.DelegatedContentFor(parent), &stale); err != nil {
			t.Fatal(err)
		}
		action, err := f.service.source.Authorize(t.Context(), parent)
		if err != nil {
			t.Fatal(err)
		}
		_, lane := f.service.laneForItem(parent)
		result, resumed := f.service.resumeAcceptedPlanPublication(t.Context(), action, lane, RunResult{Item: parent}, f.repo, "stale-evidence-checkpoint")
		if resumed || result.Error != "" || r.reviews != 3 || r.implementations != 2 || r.creates != 1 {
			t.Fatalf("stale evidence progress did not yield to fresh QA: resumed=%t result=%+v", resumed, result)
		}
		if after, err := os.ReadFile(r.gateLog); err != nil || string(after) != string(gateRuns) {
			t.Fatalf("stale progress ran a gate before renewed QA: %q err=%v", after, err)
		}
	}
	if err := f.service.savePlanVerification(parent, github.DelegatedContentFor(parent), progress); err != nil {
		t.Fatal(err)
	}
	for _, child := range children {
		modelLegacyPlanAcceptance(t, f, child)
	}
	restartPlanEvidenceEngine(t, f, r)
	results, err = f.service.RunCycle(t.Context())
	if err != nil || len(results) != 1 || results[0].FailureClass != string(execution.FailureIntegrityViolation) || !strings.Contains(results[0].Error, "no durable evidence") {
		t.Fatalf("retained parent progress bypassed member evidence preflight: results=%#v err=%v", results, err)
	}
	if f.parent(t).Status != "Blocked" || f.parent(t).PullRequest != "" || !reflect.DeepEqual(children, planEvidenceChildren(t, f)) || r.implementations != 2 || r.reviews != 3 || r.creates != 1 || r.planPushes != 4 {
		t.Fatal("missing evidence resumed publication, invoked a model/classifier or requeued integrated work")
	}
	if after, err := os.ReadFile(r.gateLog); err != nil || string(after) != string(gateRuns) {
		t.Fatalf("missing evidence ran a complete gate before refusing resume: %q err=%v", after, err)
	}
	if !reflect.DeepEqual(retainedPlanProgress(t, f), progress) {
		t.Fatal("missing evidence refusal discarded existing verification progress")
	}
	preview, err := f.service.PlanProjectItemRetry(t.Context(), f.parentID)
	if err != nil {
		t.Fatal(err)
	}
	if preview.EvidenceRecovery == nil || len(preview.EvidenceRecovery.Members) != 2 {
		t.Fatal("saved parent progress hid required member recovery")
	}
	for _, member := range preview.EvidenceRecovery.Members {
		if !member.RecoveryRequired {
			t.Fatal("legacy member was not marked for historical recovery")
		}
	}
	if _, err := f.service.ApplyProjectItemRetry(t.Context(), preview); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(retainedPlanProgress(t, f), progress) {
		t.Fatal("retry discarded the old progress instead of requiring its evidence freshness check")
	}
	// A historical publication only permits its exact R, never an unrelated
	// remote head. Reuse this delivery and restore only the disposable Git ref.
	foreign := strings.TrimSpace(runGitTest(t, progress.Metadata.WorktreePath, "commit-tree", progress.Candidate.Tree, "-p", progress.Candidate.Head, "-m", "Unapproved external plan update"))
	planRef := "refs/heads/" + parent.Branch
	runGitTest(t, progress.Metadata.WorktreePath, "push", f.remote, foreign+":"+planRef)
	restartPlanEvidenceEngine(t, f, r)
	results, err = f.service.RunCycle(t.Context())
	if err != nil || len(results) != 1 || results[0].Outcome != execution.OutcomeBlocked || !strings.Contains(results[0].Error, "unexpected remote identity") {
		t.Fatalf("unexpected remote head was not refused: results=%+v err=%v", results, err)
	}
	if r.reviews != 3 || r.implementations != 2 || r.creates != 1 || r.planPushes != 4 || !reflect.DeepEqual(retainedPlanProgress(t, f), progress) {
		t.Fatal("unexpected head ran work, published, or replaced retained acceptance")
	}
	if after, err := os.ReadFile(r.gateLog); err != nil || string(after) != string(gateRuns) {
		t.Fatalf("unexpected head ran complete verification: %q err=%v", after, err)
	}
	if head := strings.TrimSpace(runGitTest(t, "", "--git-dir", f.remote, "rev-parse", planRef)); head != foreign {
		t.Fatal("unexpected remote head was overwritten")
	}
	runGitTest(t, "", "--git-dir", f.remote, "update-ref", planRef, progress.Candidate.Head, foreign)
	preview, err = f.service.PlanProjectItemRetry(t.Context(), f.parentID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.ApplyProjectItemRetry(t.Context(), preview); err != nil {
		t.Fatal(err)
	}
	restartPlanEvidenceEngine(t, f, r)
	results = runPlanEvidenceCycle(t, f)
	if len(results) != 1 || results[0].ResumedCheckpoint || r.reviews != 4 || r.implementations != 2 || r.creates != 1 || f.parent(t).Status != "PR Ready" || !reflect.DeepEqual(children, planEvidenceChildren(t, f)) {
		t.Fatalf("newly recovered evidence reused old QA or repeated implementation/publication: results=%+v reviews=%d PRs=%d", results, r.reviews, r.creates)
	}
	if len(r.planEvidence) != 2 || len(r.planEvidence[1]) != 2 {
		t.Fatal("recovered evidence was not presented to a fresh whole-plan reviewer")
	}
	for _, child := range children {
		observed := r.planEvidence[1][child.ID]
		if observed.manifest.Provenance == nil || observed.manifest.Provenance.Kind != workspace.EvidenceRecovered || !reflect.DeepEqual(observed.files, r.cardEvidence[child.ID].files) {
			t.Fatalf("fresh QA did not receive honest recovered member evidence: %+v", observed)
		}
	}
	if renewed := retainedPlanProgress(t, f); renewed.AttemptID == progress.AttemptID || renewed.EvidenceCollectionDigest == "" || renewed.EvidenceCollectionDigest == progress.EvidenceCollectionDigest {
		t.Fatal("fresh QA did not replace the stale evidence collection binding")
	} else if renewed.Publication == nil || *renewed.Publication != *progress.Publication || renewed.Candidate.Head != progress.Candidate.Head || f.parent(t).QACommit != progress.Candidate.Head {
		t.Fatal("fresh QA lost the original publication record or changed the accepted refreshed candidate")
	}
}

func TestProductionPlanReviewRecoveryBeforePublicationComment(t *testing.T) {
	f, r := newProductionEvidenceDelivery(t)
	f.integrateMembers(t)
	children := planEvidenceChildren(t, f)
	commentsBefore := append([]github.ItemComment(nil), r.project.issueComments...)
	postedBefore := len(r.project.postedComments)
	r.interruptBeforeComment = true
	_, _ = f.service.RunCycle(t.Context())
	if !r.interrupted || r.reviews != 3 || r.creates != 0 || len(r.project.postedComments) != postedBefore || !reflect.DeepEqual(r.project.issueComments, commentsBefore) {
		t.Fatal("fixture did not stop after acceptance and before posting its publication comment")
	}
	r.offline = false
	progress := retainedPlanProgress(t, f)
	if progress.Publication == nil || progress.EvidenceCollectionDigest == "" {
		t.Fatal("interrupted publication lacks its immutable acceptance and evidence binding")
	}
	publicationComment := qaCommentMarker(f.parentID, progress.Candidate.Head, progress.Publication.AcceptanceComment) + "\n\n" + progress.Publication.AcceptanceComment
	for _, comment := range commentsBefore {
		if comment.Body == publicationComment {
			t.Fatal("original publication comment was already visible to QA")
		}
	}
	// New selected evidence changes only review context, not the accepted Git
	// candidate. A renewed model response must not replace the immutable comment.
	if err := writePlanReceipts(progress.Metadata.WorktreePath, f.parentID); err != nil {
		t.Fatal(err)
	}
	r.planReviewSummary = "Fresh QA accepted the additional parent evidence."
	restartPlanEvidenceEngine(t, f, r)
	results := runPlanEvidenceCycle(t, f)
	if len(results) != 1 || results[0].ResumedCheckpoint || r.reviews != 4 || r.implementations != 2 || r.creates != 1 || f.parent(t).Status != "PR Ready" || !reflect.DeepEqual(children, planEvidenceChildren(t, f)) {
		t.Fatalf("renewed QA could not finish interrupted publication: results=%+v reviews=%d PRs=%d", results, r.reviews, r.creates)
	}
	renewed := retainedPlanProgress(t, f)
	if renewed.AttemptID == progress.AttemptID || renewed.EvidenceCollectionDigest == progress.EvidenceCollectionDigest || renewed.Comment == progress.Comment || renewed.Accepted.ReviewAssessment.Summary != r.planReviewSummary {
		t.Fatal("fixture did not bind new evidence to a distinct renewed review comment")
	}
	if renewed.Publication == nil || *renewed.Publication != *progress.Publication || len(r.project.postedComments) != postedBefore+1 || r.project.postedComments[postedBefore] != publicationComment {
		t.Fatal("recovery rewrote immutable acceptance or failed to post its exact original comment once")
	}
	if !slices.Equal(renewed.Assignment.Spec.ReviewCommentContext, humanCommentContext(commentsBefore)) {
		t.Fatal("renewed QA was credited with seeing the later publication comment")
	}
	if gateRuns, err := os.ReadFile(r.gateLog); err != nil || string(gateRuns) != "gate\n" {
		t.Fatalf("unchanged executable proof was repeated: %q err=%v", gateRuns, err)
	}
	// The original comment may be added after QA; an independent addition or
	// alteration still invalidates review context, even with its exact marker.
	action, err := f.service.source.Authorize(t.Context(), f.parent(t))
	if err != nil {
		t.Fatal(err)
	}
	commentsAfter := append([]github.ItemComment(nil), r.project.issueComments...)
	for _, change := range []string{"additional operator comment", "altered publication comment"} {
		t.Run(change, func(t *testing.T) {
			r.project.issueComments = append([]github.ItemComment(nil), commentsAfter...)
			defer func() { r.project.issueComments = append([]github.ItemComment(nil), commentsAfter...) }()
			if change == "additional operator comment" {
				r.project.issueComments = append(r.project.issueComments, github.ItemComment{Author: "dan", Body: "Please reassess the newly identified edge case."})
			} else {
				for i := range r.project.issueComments {
					if r.project.issueComments[i].Body == publicationComment {
						r.project.issueComments[i].Body += "\nOperator follow-up: additional proof is required."
					}
				}
			}
			if _, err := f.service.revalidatePlanProgress(t.Context(), action, renewed); err == nil || !strings.Contains(err.Error(), "parent comment context changed") {
				t.Fatalf("unexpected comment context was accepted: %v", err)
			}
		})
	}
	if r.reviews != 4 || r.implementations != 2 || r.creates != 1 || !reflect.DeepEqual(retainedPlanProgress(t, f), renewed) {
		t.Fatal("comment refusal invoked work or changed retained progress")
	}
}
