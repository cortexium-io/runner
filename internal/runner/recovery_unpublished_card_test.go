package runner

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type cardRecoveryStore struct {
	frontendRecheckStore
	fail bool
	runs map[string]Run
}

func (s *cardRecoveryStore) Load(context.Context) ([]Run, error) {
	var out []Run
	for _, r := range s.runs {
		out = append(out, Clone(r))
	}
	return out, nil
}
func (s *cardRecoveryStore) Save(ctx context.Context, r Run, artifacts []Artifact, receipt *Receipt) error {
	if s.runs == nil {
		s.runs = map[string]Run{}
	}
	s.runs[r.ID] = Clone(r)
	for _, a := range artifacts {
		s.artifacts[a.ID] = a
	}
	return s.frontendRecheckStore.Save(ctx, r, artifacts, receipt)
}

func (s *cardRecoveryStore) SaveUnpublishedCardRecovery(ctx context.Context, root, child Run, receipt *Receipt) error {
	if s.fail {
		return errors.New("fixture atomic write failed")
	}
	if s.runs == nil {
		s.runs = map[string]Run{}
	}
	s.runs[child.ID] = Clone(child)
	return s.Save(ctx, root, nil, receipt)
}

type cardRecoveryExecutor struct {
	Executor
	calls    atomic.Int32
	starts   atomic.Int32
	validate func(Run, UnpublishedCardAssessment) error
}

func (e *cardRecoveryExecutor) Start(context.Context, Assignment) (Observation, error) {
	e.starts.Add(1)
	return Observation{}, errors.New("unexpected owner dispatch")
}

func (e *cardRecoveryExecutor) ValidateUnpublishedCardRecovery(_ context.Context, r Run, a UnpublishedCardAssessment) error {
	e.calls.Add(1)
	if e.validate != nil {
		return e.validate(r, a)
	}
	return nil
}
func cardRecoveryFixture(t *testing.T) (*Core, *cardRecoveryStore, *cardRecoveryExecutor, RecoverUnpublishedCardRequest) {
	t.Helper()
	load := func(name string, out any) {
		raw, e := os.ReadFile(filepath.Join("..", "..", "examples", "jacks-break", name))
		if e != nil {
			t.Fatal(e)
		}
		if e = json.Unmarshal(raw, out); e != nil {
			t.Fatal(e)
		}
	}
	var process Process
	load("planned-frontend.json", &process)
	roles := map[string]Role{}
	var visit func([]Step)
	visit = func(steps []Step) {
		for _, s := range steps {
			if s.ForEach != nil {
				visit(s.ForEach.Steps)
			} else if roles[s.Role].ID == "" {
				var r Role
				load(filepath.Join("roles", s.Role+".json"), &r)
				roles[s.Role] = r
			}
		}
	}
	visit(process.Steps)
	policy := DefaultStorage()
	policy.MinFree = 0
	policy.Database.MaxRecord = 40 << 10
	disk, e := OpenDisk(t.TempDir(), policy)
	if e != nil {
		t.Fatal(e)
	}
	store := &cardRecoveryStore{frontendRecheckStore: frontendRecheckStore{artifacts: map[string]Artifact{}, receipts: map[string]Receipt{}}}
	executor := &cardRecoveryExecutor{}
	c := &Core{ctx: t.Context(), store: store, executor: executor, disk: disk, runs: map[string]Run{}, inFlight: map[string]bool{}, wake: make(chan struct{}, 1), changed: make(chan struct{}), snapshot: Snapshot{Instance: Instance{TaskConcurrency: 2}}}
	root := Run{RecordFormat: 3, ID: "run_card_root", ProjectID: "demo", Revision: 164, Repository: filepath.Join(disk.Root, "repo"), Source: &SourceSettings{Ref: strings.Repeat("a", 40)}, Process: process, Roles: roles, Profiles: map[string]string{}, Step: 3, Visit: 3, Status: "Failed", Reason: "task rendered_matrix: attempt deadline reached", ResourcesReleased: true, Inputs: map[string]Value{"request": Text("original approved repairs"), "package": Text("."), "tests": Text("[]"), "benchmark": Text(""), "review_evidence": {ArtifactID: "attempt_reference/result"}}}
	add := func(run, id, value string) Value {
		if e := disk.Write("artifacts/"+id, []byte(value), policy.MaxArtifact); e != nil {
			t.Fatal(e)
		}
		store.artifacts[id] = Artifact{ID: id, RunID: run, ProjectID: root.ProjectID, Bytes: int64(len(value))}
		return Value{ArtifactID: id}
	}
	baseline := add(root.ID, "attempt_baseline/result", "preserved failing baseline")
	group := &TaskGroupState{StepID: "cards", AttemptID: "attempt_group", Base: root.Source.Ref, Repository: root.Repository, Done: true, Failure: root.Reason, Evidence: map[string]Value{"baseline": baseline}}
	for i := range 8 {
		id := fmt.Sprintf("card%d", i)
		if i == 7 {
			id = "rendered_matrix"
		}
		group.Items = append(group.Items, TaskItem{ID: id, Inputs: map[string]json.RawMessage{"task": json.RawMessage(`"same approved card"`), "paths": json.RawMessage(fmt.Sprintf(`["card%d.txt"]`, i))}})
		group.Children = append(group.Children, TaskChild{ItemID: id, RunID: "run_" + id})
	}
	group.Items[7].DependsOn = []string{"card0", "card1", "card2", "card3", "card4"}
	root.TaskGroups = map[string]*TaskGroupState{"cards": group}
	for range 5 {
		root.Attempts = append(root.Attempts, Attempt{ID: fmt.Sprintf("attempt_root_%d", len(root.Attempts)), Step: 2, State: Finished, Released: true})
	}
	for i := range 7 {
		child := childForItem(root, group, group.Items[i], group.Children[i].RunID)
		child.Revision = []int64{61, 58, 66, 61, 47, 61, 59}[i]
		child.Status = "Completed"
		child.ResourcesReleased = true
		child.Step = 3
		child.Visit = 2
		for step := range 3 {
			child.Attempts = append(child.Attempts, Attempt{ID: fmt.Sprintf("attempt_%d_%d", i, step), Step: step, State: Finished, Released: true})
		}
		patch := add(child.ID, child.Attempts[1].ID+"/changes.patch", fmt.Sprintf("pin %d", i))
		candidate := TaskCandidate{Identity: child.Task.Identity, RunID: child.ID, Repository: root.Repository, Base: group.Base, Commit: fmt.Sprintf("%040x", i+1), Tree: fmt.Sprintf("%040x", i+10), PatchID: patch.ArtifactID}
		raw, _ := json.Marshal(candidate)
		child.Outputs = map[string]Value{"result": {Inline: raw}}
		group.Children[i].Candidate = &candidate
		c.runs[child.ID] = child
	}
	c.runs[root.ID] = root
	child := childForItem(root, group, group.Items[7], group.Children[7].RunID)
	child.Status = "Failed"
	child.Reason = "attempt deadline reached"
	child.Revision = 81
	child.ResourcesReleased = true
	child.Step = 1
	child.Visit = 1
	authority, e := c.taskAssignment(child, child.Process.Steps[1])
	if e != nil {
		t.Fatal(e)
	}
	deps, _ := json.Marshal(authority.Dependencies)
	child.Inputs["_task_dependencies"] = Value{Inline: deps}
	seed := Attempt{ID: "attempt_seed", Step: 0, Visit: 0, State: Finished, Released: true, End: &ExecutionEnd{Kind: "returned", Result: &StepResult{Outcome: "ready", Outputs: map[string]Value{"manifest": add(child.ID, "attempt_seed/manifest", "authenticated dependency manifest")}}}}
	input := add(child.ID, "attempt_seed/changes.patch", "original dependency source")
	failed := Attempt{ID: "attempt_failed", Step: 1, Visit: 1, State: Finished, Released: true, TimedOut: true, StopRequested: true, Started: time.Now().UTC().Add(-time.Hour)}
	failed.Deadline = failed.Started.Add(20 * time.Minute)
	patch := add(child.ID, failed.ID+"/changes.patch", "captured unreviewed source")
	failed.Source = &SourceResult{AttemptID: failed.ID, PatchID: patch.ArtifactID, InputPatchID: input.ArtifactID, Base: group.Base, Tree: strings.Repeat("b", 40), Repository: root.Repository, WorkDir: filepath.Join(disk.Root, "work", child.ID, "source")}
	failed.End = &ExecutionEnd{Kind: "returned", Result: &StepResult{Outcome: "ready", Outputs: map[string]Value{"patch": patch, "summary": Text("authored; checks pending")}}}
	child.Attempts = []Attempt{seed, failed}
	child.StepInputs, e = bindings(child, child.Process.Steps[1].Inputs)
	if e != nil {
		t.Fatal(e)
	}
	c.runs[child.ID] = child
	req := RecoverUnpublishedCardRequest{RequestID: "approved-once", RunID: root.ID, Revision: c.outputRun(root).Revision, GroupAttemptID: group.AttemptID, ChildRunID: child.ID, ChildRevision: child.Revision, AttemptID: failed.ID, PatchSHA256: recoveryDigestRaw([]byte("captured unreviewed source"))}
	return c, store, executor, req
}
func recoveryDigestRaw(raw []byte) string { // match artifact bytes, not their JSON representation
	return fmt.Sprintf("%x", sha256.Sum256(raw))
}
func TestUnpublishedCardRecoveryPreservesFrozenFamilyAndOneUse(t *testing.T) {
	c, store, exec, req := cardRecoveryFixture(t)
	before := Clone(c.runs)
	assessment, e := c.CheckUnpublishedCardRecovery(req)
	if e != nil {
		t.Fatal(e)
	}
	if store.saves != 0 || len(c.wake) != 0 || !reflect.DeepEqual(before, c.runs) || assessment.Spent != 28 || assessment.Ceiling != 64 || assessment.ReservedCompletion != 23 || len(assessment.Pins) != 7 {
		t.Fatal("preflight changed state or reserve", assessment)
	}
	req.BindingsSHA256 = assessment.BindingsSHA256
	root, e := c.RecoverUnpublishedCard(req)
	if e != nil {
		t.Fatal(e)
	}
	child := c.runs[req.ChildRunID]
	old := before[child.ID]
	if root.RecordFormat != 4 || root.Status != "Running" || root.TaskGroups["cards"].Done || root.TaskGroups["cards"].Failure != "" || root.UnpublishedCardRecovery.OriginalGroupFailure != before[root.ID].Reason || child.Status != "Queued" || child.Visit != old.Visit+1 || child.Step != 1 || child.ResourcesReleased || child.Current() != nil || c.familyAttempts(root) != 28 {
		t.Fatal("wrong recovery transition", root.Status, child.Status)
	}
	if !reflect.DeepEqual(child.Attempts, old.Attempts) || !reflect.DeepEqual(child.Process, old.Process) || !reflect.DeepEqual(child.StepInputs, old.StepInputs) || !reflect.DeepEqual(root.Process, before[root.ID].Process) || !reflect.DeepEqual(root.Roles, before[root.ID].Roles) || !reflect.DeepEqual(root.Inputs, before[root.ID].Inputs) {
		t.Fatal("frozen source/history or permissions changed")
	}
	for _, pin := range assessment.Pins {
		if !reflect.DeepEqual(c.runs[pin.RunID], before[pin.RunID]) {
			t.Fatal("accepted card changed", pin.Identity.ItemID)
		}
	}
	a, e := c.assignment(child, Attempt{ID: "attempt_recovery_owner", Step: 1, Visit: child.Visit, Deadline: time.Now().Add(20 * time.Minute)})
	if e != nil || a.UnpublishedCardRecovery == nil || a.UnpublishedCardRecovery.Source != *old.Current().Source || !reflect.DeepEqual(a.Inputs["manifest"], old.StepInputs["manifest"]) || a.Setup.Profile.Access != "workspace-write" {
		t.Fatal("owner did not receive exact normal restoration", e)
	}
	calls := exec.calls.Load()
	again, e := c.RecoverUnpublishedCard(req)
	if e != nil || again.ID != root.ID || store.saves != 1 || exec.calls.Load() != calls {
		t.Fatal("idempotent replay repeated admission", e)
	}
	req.RequestID = "another-grant"
	req.Revision = root.Revision
	if _, e = c.RecoverUnpublishedCard(req); e == nil {
		t.Fatal("one-use grant renewed")
	}
	for _, id := range []string{root.ID, child.ID} {
		r := c.runs[id]
		r.Status = "Failed"
		c.runs[id] = r
		if _, e = c.Retry(RetryRequest{RequestID: "retry-" + id, RunID: id, ExtraAttempts: 1}); e == nil {
			t.Fatal("retry bypassed the one-use guard", id)
		}
	}
}
func TestUnpublishedCardRecoveryRejectsDriftAndUnsettledOrThinWork(t *testing.T) {
	cases := map[string]func(*Core, *RecoverUnpublishedCardRequest){
		"revision":       func(c *Core, q *RecoverUnpublishedCardRequest) { q.Revision++ },
		"child revision": func(c *Core, q *RecoverUnpublishedCardRequest) { q.ChildRevision++ },
		"group":          func(c *Core, q *RecoverUnpublishedCardRequest) { q.GroupAttemptID = "foreign" },
		"attempt":        func(c *Core, q *RecoverUnpublishedCardRequest) { q.AttemptID = "foreign" },
		"patch hash":     func(c *Core, q *RecoverUnpublishedCardRequest) { q.PatchSHA256 = strings.Repeat("a", 64) },
		"artifact owner": func(c *Core, q *RecoverUnpublishedCardRequest) {
			s := c.store.(*cardRecoveryStore)
			a := s.artifacts["attempt_failed/changes.patch"]
			a.RunID = "foreign"
			s.artifacts[a.ID] = a
		},
		"missing patch": func(c *Core, q *RecoverUnpublishedCardRequest) {
			_ = c.disk.Remove("artifacts/attempt_failed/changes.patch")
		},
		"changed pin bytes": func(c *Core, q *RecoverUnpublishedCardRequest) {
			_ = c.disk.Write("artifacts/attempt_0_1/changes.patch", []byte("pin X"), c.disk.Policy.MaxArtifact)
		},
		"child active": func(c *Core, q *RecoverUnpublishedCardRequest) {
			r := c.runs[q.ChildRunID]
			r.Attempts[1].State = Unknown
			c.runs[r.ID] = r
		},
		"writer cleanup": func(c *Core, q *RecoverUnpublishedCardRequest) {
			r := c.runs[q.ChildRunID]
			r.ResourcesReleased = false
			c.runs[r.ID] = r
		},
		"pin drift": func(c *Core, q *RecoverUnpublishedCardRequest) {
			r := c.runs[q.RunID]
			r.TaskGroups["cards"].Children[0].Candidate.Commit = strings.Repeat("c", 40)
			c.runs[r.ID] = r
		},
		"dependency drift": func(c *Core, q *RecoverUnpublishedCardRequest) {
			r := c.runs[q.ChildRunID]
			r.Inputs["_task_dependencies"] = Value{Inline: json.RawMessage(`[]`)}
			c.runs[r.ID] = r
		},
		"source scope": func(c *Core, q *RecoverUnpublishedCardRequest) {
			r := c.runs[q.ChildRunID]
			r.Inputs["paths"] = Value{Inline: json.RawMessage(`["outside.txt"]`)}
			c.runs[r.ID] = r
		},
		"wrong failure": func(c *Core, q *RecoverUnpublishedCardRequest) {
			r := c.runs[q.ChildRunID]
			r.Current().TimedOut = false
			c.runs[r.ID] = r
		},
		"cleanup in flight": func(c *Core, q *RecoverUnpublishedCardRequest) { c.inFlight["attempt_0_1"] = true },
		"ceiling": func(c *Core, q *RecoverUnpublishedCardRequest) {
			r := c.runs[q.RunID]
			r.Process.MaxAttempts = 30
			c.runs[r.ID] = r
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			c, s, _, q := cardRecoveryFixture(t)
			a, e := c.CheckUnpublishedCardRecovery(q)
			if e != nil {
				t.Fatal(e)
			}
			q.BindingsSHA256 = a.BindingsSHA256
			mutate(c, &q)
			before := Clone(c.runs)
			if _, e = c.RecoverUnpublishedCard(q); e == nil || s.saves != 0 || len(c.wake) != 0 || !reflect.DeepEqual(before, c.runs) {
				t.Fatal("invalid recovery mutated family", e)
			}
		})
	}
}
func TestUnpublishedCardRecoveryExactReserveAtomicFailureAndConcurrentReplay(t *testing.T) {
	for _, extra := range []int{12, 13} {
		t.Run(fmt.Sprint("spent-extra-", extra), func(t *testing.T) {
			c, s, _, q := cardRecoveryFixture(t)
			r := c.runs[q.RunID]
			for range extra {
				r.Attempts = append(r.Attempts, Attempt{ID: fmt.Sprintf("spent_%d", len(r.Attempts)), State: Finished, Released: true})
			}
			c.runs[r.ID] = r
			a, e := c.CheckUnpublishedCardRecovery(q)
			if extra == 13 {
				if e == nil {
					t.Fatal("reserved completion consumed")
				}
				return
			}
			if e != nil {
				t.Fatal(e)
			}
			q.BindingsSHA256 = a.BindingsSHA256
			s.fail = true
			before := Clone(c.runs)
			if _, e = c.RecoverUnpublishedCard(q); e == nil || !reflect.DeepEqual(before, c.runs) {
				t.Fatal("failed atomic persistence changed state", e)
			}
			s.fail = false
			if _, e = c.RecoverUnpublishedCard(q); e != nil {
				t.Fatal("exact full reserve rejected", e)
			}
		})
	}
	c, s, _, q := cardRecoveryFixture(t)
	a, e := c.CheckUnpublishedCardRecovery(q)
	if e != nil {
		t.Fatal(e)
	}
	q.BindingsSHA256 = a.BindingsSHA256
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); _, e := c.RecoverUnpublishedCard(q); errs <- e }()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal("concurrent same-request replay failed", e)
		}
	}
	if s.saves != 1 {
		t.Fatal("concurrent duplicate grant", s.saves)
	}
}
func TestUnpublishedCardRecoveryCannotRouteOrRetryAnotherOwner(t *testing.T) {
	c, _, _, q := cardRecoveryFixture(t)
	a, e := c.CheckUnpublishedCardRecovery(q)
	if e != nil {
		t.Fatal(e)
	}
	q.BindingsSHA256 = a.BindingsSHA256
	if _, e = c.RecoverUnpublishedCard(q); e != nil {
		t.Fatal(e)
	}
	child := c.runs[q.ChildRunID]
	original := Clone(child.Attempts)
	child.Attempts = append(child.Attempts, Attempt{ID: "recovered-owner", Step: 1, Visit: child.Visit, State: Finished, Released: true, End: &ExecutionEnd{Kind: "failed", Retryable: true, Reason: "owned visit failed"}})
	c.finish(&child, len(child.Attempts)-1)
	if child.Status != "Failed" || !strings.Contains(child.Reason, "one-use") || !reflect.DeepEqual(child.Attempts[:2], original) {
		t.Fatal("failed recovery retried or changed old failure", child.Status, child.Reason)
	}
	child.Step = 2
	child.Visit++
	child.Attempts = append(child.Attempts, Attempt{ID: "pin-reject", Step: 2, Visit: child.Visit, State: Finished, Released: true, End: &ExecutionEnd{Kind: "returned", Result: &StepResult{Outcome: "changes_requested"}}})
	c.route(&child, child.Current())
	if child.Status != "Failed" || child.Step != 2 {
		t.Fatal("pin rejection granted another owner")
	}
}

func TestUnpublishedCardRecoveryNormalPinGroupAndDownstreamBindings(t *testing.T) {
	c, s, _, q := cardRecoveryFixture(t)
	a, e := c.CheckUnpublishedCardRecovery(q)
	if e != nil {
		t.Fatal(e)
	}
	q.BindingsSHA256 = a.BindingsSHA256
	if _, e = c.RecoverUnpublishedCard(q); e != nil {
		t.Fatal(e)
	}
	child := c.runs[q.ChildRunID]
	owner := Attempt{ID: "new-owner", Step: 1, Visit: child.Visit, State: Finished, Released: true, End: &ExecutionEnd{Kind: "returned", Result: &StepResult{Outcome: "ready", Outputs: map[string]Value{"patch": {ArtifactID: "new-owner/changes.patch"}, "summary": Text("fixture owner")}}}}
	child.Attempts = append(child.Attempts, owner)
	c.route(&child, child.Current())
	if child.Step != 2 || child.Status != "Queued" {
		t.Fatal("owner bypassed normal pin", child.Step, child.Status)
	}
	candidate := TaskCandidate{Identity: child.Task.Identity, RunID: child.ID, Repository: child.Repository, Base: child.Source.Ref, Commit: strings.Repeat("c", 40), Tree: strings.Repeat("d", 40), PatchID: "new-owner/changes.patch"}
	s.artifacts[candidate.PatchID] = Artifact{ID: candidate.PatchID, RunID: child.ID, ProjectID: child.ProjectID}
	raw, _ := json.Marshal(candidate)
	child.Attempts = append(child.Attempts, Attempt{ID: "new-pin", Step: 2, Visit: child.Visit, State: Finished, Released: true, End: &ExecutionEnd{Kind: "returned", Result: &StepResult{Outcome: "ready", Outputs: map[string]Value{"candidate": {Inline: raw}}}}})
	c.route(&child, child.Current())
	child.ResourcesReleased = true
	c.runs[child.ID] = child
	if child.Status != "Completed" {
		t.Fatal("normal pin did not complete", child.Status, child.Reason)
	}
	root := Clone(c.runs[q.RunID])
	original := Clone(root.Process)
	if !c.driveTaskGroup(root) {
		t.Fatal("group not rejoined")
	}
	root = c.runs[q.RunID]
	if !root.TaskGroups["cards"].Done || root.Process.Steps[root.Step].ID != "join" || root.Status != "Queued" || root.Decision != nil || !reflect.DeepEqual(root.Process, original) || c.familyAttempts(root) != 31 {
		t.Fatal("group skipped or changed downstream gates", root.Step, root.Status, c.familyAttempts(root))
	}
	assignment, e := c.assignment(root, Attempt{ID: "join-next", Step: root.Step, Visit: root.Visit})
	if e != nil || assignment.TaskSource == nil || len(assignment.TaskSource.Dependencies) != 8 || assignment.Inputs["results"].ArtifactID != q.GroupAttemptID+"/results" {
		t.Fatal("join did not bind eight ordinary pins", e)
	}
	if s.artifacts[q.GroupAttemptID+"/results"].RunID != root.ID {
		t.Fatal("manifest not owned")
	}
}

func TestUnpublishedCardRecoveryCancellationAndFailedRestartNeverRenewOwner(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(fmt.Sprint("cancelled-", cancelled), func(t *testing.T) {
			c, s, exec, q := cardRecoveryFixture(t)
			a, e := c.CheckUnpublishedCardRecovery(q)
			if e != nil {
				t.Fatal(e)
			}
			q.BindingsSHA256 = a.BindingsSHA256
			if _, e = c.RecoverUnpublishedCard(q); e != nil {
				t.Fatal(e)
			}
			if cancelled {
				if _, e = c.Cancel(q.RunID); e != nil {
					t.Fatal(e)
				}
			}
			child := c.runs[q.ChildRunID]
			old := Clone(child.Attempts)
			if child.CancelRequested != cancelled {
				t.Fatal("parent cancellation did not propagate")
			}
			end := &ExecutionEnd{Kind: "failed", Retryable: true, Reason: "bounded visit failed"}
			if cancelled {
				end = &ExecutionEnd{Kind: "stopped", Reason: "ordinary stop settled"}
			}
			child.Attempts = append(child.Attempts, Attempt{ID: "one-recovered-owner", Step: 1, Visit: child.Visit, State: Finished, Released: true, End: end})
			c.finish(&child, len(child.Attempts)-1)
			child.ResourcesReleased = true
			c.runs[child.ID] = child
			if !child.Terminal() || !reflect.DeepEqual(child.Attempts[:2], old) {
				t.Fatal("failure/cancellation lost history")
			}
			c.driveTaskGroup(Clone(c.runs[q.RunID]))
			root := c.runs[q.RunID]
			root.ResourcesReleased = true
			c.runs[root.ID] = root
			if !root.Terminal() || !root.TaskGroups["cards"].Done || c.familyAttempts(root) != 29 {
				t.Fatal("failed visit did not settle exactly once", root.Status)
			}
			s.runs = Clone(c.runs)
			restarted, e := New(t.Context(), s, exec, c.disk, c.snapshot)
			if e != nil {
				t.Fatal(e)
			}
			defer restarted.Close()
			for _, id := range []string{root.ID, child.ID} {
				if _, e = restarted.Retry(RetryRequest{RequestID: "after-restart-" + id, RunID: id, ExtraAttempts: 1}); e == nil {
					t.Fatal("restart granted another owner", id)
				}
			}
			replay, e := restarted.RecoverUnpublishedCard(q)
			if e != nil || replay.ID != root.ID {
				t.Fatal("restart lost one-use receipt", e)
			}
			restarted.mu.Lock()
			used := restarted.familyAttempts(restarted.runs[root.ID])
			restarted.mu.Unlock()
			if used != 29 || exec.starts.Load() != 0 {
				t.Fatal("restart replayed owner or changed usage", used, exec.starts.Load())
			}
		})
	}
}

func TestUnpublishedCardRecoveryBindsObservedFamilyRevisionAndFullChildState(t *testing.T) {
	c, store, exec, q := cardRecoveryFixture(t)
	before := Clone(c.runs)
	observed, e := c.Get(q.RunID)
	if e != nil {
		t.Fatal(e)
	}
	var stored int64
	for _, run := range c.Status().Runs {
		if run.ID == q.RunID {
			stored = run.Revision
		}
	}
	if stored != 164 || observed.Revision != 658 || q.Revision != observed.Revision || q.ChildRevision != 81 {
		t.Fatal("fixture must distinguish status root164 from Get/inspect family658", stored, observed.Revision, q)
	}
	wrong := q
	wrong.Revision = stored
	if _, e = c.CheckUnpublishedCardRecovery(wrong); e == nil || !strings.Contains(e.Error(), "observed family revision") {
		t.Fatal("stored status revision accepted as public recovery revision", e)
	}
	if exec.calls.Load() != 0 || store.saves != 0 || !reflect.DeepEqual(before, c.runs) {
		t.Fatal("wrong revision changed state or reached source validation")
	}
	assessment, e := c.CheckUnpublishedCardRecovery(q)
	if e != nil {
		t.Fatal("unchanged observed family could not preflight", e)
	}
	if assessment.Revision != observed.Revision || assessment.StoredRevision != stored || len(assessment.Pins) != 7 {
		t.Fatal("preflight lost public/stored custody", assessment)
	}
	encoded, e := json.Marshal(assessment)
	var wire struct {
		Revision       int64 `json:"revision"`
		StoredRevision int64 `json:"stored_revision"`
	}
	if e != nil || json.Unmarshal(encoded, &wire) != nil || wire.Revision != 658 || wire.StoredRevision != 164 {
		t.Fatal("owner preflight does not expose both revision meanings", e)
	}
	q.BindingsSHA256 = assessment.BindingsSHA256
	first := observed.TaskGroups["cards"].Children[0].RunID
	child := c.runs[first]
	child.Revision++
	c.runs[first] = child
	if _, e = c.RecoverUnpublishedCard(q); e == nil {
		t.Fatal("child drift accepted stale family revision")
	}
	newer, e := c.Get(q.RunID)
	if e != nil {
		t.Fatal(e)
	}
	q.Revision = newer.Revision
	if _, e = c.RecoverUnpublishedCard(q); e == nil || !strings.Contains(e.Error(), "bindings changed") {
		t.Fatal("fresh revision reused stale full-family digest", e)
	}
	// A sum collision must not hide two changed child records.
	second := observed.TaskGroups["cards"].Children[1].RunID
	child = c.runs[second]
	child.Revision--
	c.runs[second] = child
	q.Revision = observed.Revision
	sameSum, e := c.Get(q.RunID)
	if e != nil || sameSum.Revision != observed.Revision {
		t.Fatal("fixture did not restore the public revision sum", e)
	}
	if _, e = c.RecoverUnpublishedCard(q); e == nil || !strings.Contains(e.Error(), "bindings changed") {
		t.Fatal("same aggregate revision bypassed immutable child bindings", e)
	}
	if store.saves != 0 || len(c.wake) != 0 || c.runs[q.RunID].Revision != stored {
		t.Fatal("stale binding refusal changed operational state")
	}
}
