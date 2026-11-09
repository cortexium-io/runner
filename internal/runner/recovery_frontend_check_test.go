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
	"testing"
	"time"
)

type frontendRecheckStore struct {
	Store
	artifacts map[string]Artifact
	receipts  map[string]Receipt
	saves     int
}

func (s *frontendRecheckStore) Artifact(_ context.Context, id string) (Artifact, error) {
	a, ok := s.artifacts[id]
	if !ok {
		return Artifact{}, ErrNotFound
	}
	return a, nil
}

func (s *frontendRecheckStore) FindReceipt(_ context.Context, id string) (Receipt, error) {
	r, ok := s.receipts[id]
	if !ok {
		return Receipt{}, ErrNotFound
	}
	return r, nil
}

func (s *frontendRecheckStore) Save(_ context.Context, _ Run, _ []Artifact, receipt *Receipt) error {
	s.saves++
	if receipt != nil {
		s.receipts[receipt.Key] = Clone(*receipt)
	}
	return nil
}

type frontendRecheckExecutor struct {
	Executor
	calls    int
	validate func(Run, SourceResult, Profile, string, string) error
}

func (e *frontendRecheckExecutor) ValidateFrontendCheckRecovery(_ context.Context, r Run, source SourceResult, profile Profile, helper, head string) error {
	e.calls++
	if e.validate != nil {
		return e.validate(r, source, profile, helper, head)
	}
	return nil
}

func frontendRecheckFixture(t *testing.T) (*Core, *frontendRecheckStore, *frontendRecheckExecutor, RecoverFrontendCheckRequest) {
	t.Helper()
	examples := filepath.Join("..", "..", "examples", "jacks-break")
	load := func(name string, value any) {
		data, err := os.ReadFile(filepath.Join(examples, name))
		if err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(data, value); err != nil {
			t.Fatal(err)
		}
	}
	var process Process
	load("planned-retained-frontend.json", &process)
	roles := map[string]Role{}
	for _, step := range process.Steps {
		if step.ForEach == nil {
			var role Role
			load(filepath.Join("roles", step.Role+".json"), &role)
			roles[step.Role] = role
		}
	}
	// The installed profile adds only these absolute local path settings.
	roles["frontend-check"].Execution.Command.Env = map[string]string{
		"OPENSSL_CONF": "/dev/null", "PATH": "/usr/bin:/bin",
		"RUNNER_GO_MODULES": "/owned/go-modules", "RUNNER_NODE_MODULES": "/owned/node-modules",
	}
	process.MaxAttempts = 37 // 20 already used plus the exact 17-attempt protected tail.
	policy := DefaultStorage()
	policy.MinFree = 0
	policy.Database.MaxRecord = 40 << 10
	disk, err := OpenDisk(t.TempDir(), policy)
	if err != nil {
		t.Fatal(err)
	}
	store := &frontendRecheckStore{artifacts: map[string]Artifact{}, receipts: map[string]Receipt{}}
	exec := &frontendRecheckExecutor{}
	c := &Core{ctx: t.Context(), store: store, disk: disk, executor: exec, runs: map[string]Run{}, inFlight: map[string]bool{}, changed: make(chan struct{}), wake: make(chan struct{}, 1)}
	artifact := func(run Run, id, content string) Value {
		if err := disk.Write("artifacts/"+id, []byte(content), policy.MaxArtifact); err != nil {
			t.Fatal(err)
		}
		store.artifacts[id] = Artifact{ID: id, RunID: run.ID, ProjectID: run.ProjectID, Bytes: int64(len(content))}
		return Value{ArtifactID: id}
	}
	const reason = "fixed owner cannot establish fresh scoped execution"
	const patch = "retained candidate patch"
	base, tree := strings.Repeat("a", 40), strings.Repeat("b", 40)
	r := Run{
		ID: "run_frontend", ProjectID: "demo", Repository: filepath.Join(t.TempDir(), "repo"), Source: &SourceSettings{Ref: base},
		RecordFormat: 3, Process: process, Roles: roles, Step: taskStepIndex(process, "owner"), Visit: 7, Status: "Failed", Reason: reason,
		Inputs:    map[string]Value{"tests": Text(`["src/board.test.ts"]`), "request": Text("Retain the scoped correction.")},
		RouteUses: map[string]int{"check/changes_requested": 1, "owner/blocked": 1}, ResourcesReleased: true, Revision: 17,
	}
	workDir := filepath.Join(disk.Root, "work", r.ID, "source")
	group := &TaskGroupState{StepID: "cards", AttemptID: "attempt_cards", Repository: r.Repository, Base: base, Done: true}
	r.TaskGroups = map[string]*TaskGroupState{"cards": group}
	for i := range 4 {
		id := fmt.Sprintf("card%d", i)
		child := Run{ID: "run_" + id, ProjectID: r.ProjectID, Repository: r.Repository, Source: Clone(r.Source), Status: "Completed", ResourcesReleased: true, Revision: int64(i + 1)}
		identity := TaskIdentity{RootRunID: r.ID, GroupStep: "cards", ItemID: id}
		child.Task = &TaskRunLink{Identity: identity}
		for _, step := range []string{"seed", "owner", "pin"} {
			child.Attempts = append(child.Attempts, Attempt{ID: "attempt_" + id + "_" + step, State: Finished, Released: true})
		}
		candidate := TaskCandidate{Identity: identity, RunID: child.ID, Repository: r.Repository, Base: base, Commit: strings.Repeat("c", 40), Tree: tree, PatchID: "attempt_" + id + "_owner/changes.patch"}
		data, _ := json.Marshal(candidate)
		child.Outputs = map[string]Value{"result": {Inline: data}}
		artifact(child, candidate.PatchID, id+" patch")
		group.Items = append(group.Items, TaskItem{ID: id})
		group.Children = append(group.Children, TaskChild{ItemID: id, RunID: child.ID, Candidate: &candidate})
		c.runs[child.ID] = child
	}
	for i, name := range []string{"source-adopt", "admit", "baseline", "plan", "cards", "join", "check", "owner"} {
		a := Attempt{ID: "attempt_" + name, Step: taskStepIndex(process, name), Visit: i, State: Finished, Released: true, End: &ExecutionEnd{Kind: "returned", Result: &StepResult{Outcome: "ready"}}}
		switch name {
		case "join":
			a.Source = &SourceResult{AttemptID: a.ID, PatchID: a.ID + "/changes.patch", Repository: r.Repository, WorkDir: workDir, Base: base, Tree: tree}
			a.End.Result.Outputs = map[string]Value{"patch": artifact(r, a.Source.PatchID, "joined patch"), "manifest": artifact(r, a.ID+"/task-manifest.json", "accepted join manifest")}
		case "check":
			a.Superseded = true
			a.Source = &SourceResult{AttemptID: a.ID, InputPatchID: "attempt_join/changes.patch", Repository: r.Repository, WorkDir: workDir, Base: base}
			a.End.Result = &StepResult{Outcome: "changes_requested", Outputs: map[string]Value{"artifact": artifact(r, a.ID+"/result", "prior scoped check"), "check": Text("previous failure")}}
		case "owner":
			a.Source = &SourceResult{AttemptID: a.ID, InputPatchID: "attempt_join/changes.patch", PatchID: a.ID + "/changes.patch", Repository: r.Repository, WorkDir: workDir, Base: base, Tree: tree}
			a.End.Result = &StepResult{Outcome: "blocked", Reason: reason, Outputs: map[string]Value{"summary": Text(reason), "patch": artifact(r, a.Source.PatchID, patch)}}
		}
		r.Attempts = append(r.Attempts, a)
	}
	r.StepInputs = map[string]Value{"checks": {ArtifactID: "attempt_check/result"}, "manifest": {ArtifactID: "attempt_join/task-manifest.json"}}
	c.runs[r.ID] = r
	req := RecoverFrontendCheckRequest{RequestID: "owner-grant", RunID: r.ID, AttemptID: r.Current().ID, Revision: 27, PatchSHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(patch))), HelperSHA256: strings.Repeat("c", 64), PublicationHead: strings.Repeat("d", 40)}
	return c, store, exec, req
}

func TestRecoverFrontendCheckQueuesOnlyFrozenCheckAtExactBudget(t *testing.T) {
	c, store, exec, req := frontendRecheckFixture(t)
	before := Clone(c.runs[req.RunID])
	exec.validate = func(r Run, source SourceResult, profile Profile, helper, head string) error {
		if r.ID != before.ID || source != *before.Current().Source || !frontendRecheckProfile(profile) || helper != req.HelperSHA256 || head != req.PublicationHead {
			return errors.New("wrong validation identity")
		}
		return nil
	}
	r, err := c.RecoverFrontendCheck(req)
	if err != nil {
		t.Fatal(err)
	}
	grant := r.FrontendCheckRecovery
	if r.Status != "Queued" || r.Process.Steps[r.Step].ID != "check" || r.Visit != before.Visit+1 || r.Decision != nil || r.ResourcesReleased || grant == nil || grant.CheckVisit != r.Visit || grant.PriorCheckID != "attempt_check/result" || grant.PublicationHead != req.PublicationHead || exec.calls != 1 || store.saves != 1 {
		t.Fatal("recovery did not persist exactly one queued check", r.Status, r.Step, grant, store.saves, exec.calls)
	}
	if !reflect.DeepEqual(r.Attempts, before.Attempts) || !reflect.DeepEqual(r.Process, before.Process) || !reflect.DeepEqual(r.TaskGroups, before.TaskGroups) || r.RouteUses["check/changes_requested"] != before.RouteUses["check/changes_requested"] || c.familyRemaining(r) != 17 {
		t.Fatal("recovery changed history, pins, workflow or allowances")
	}
	a, err := c.assignment(r, Attempt{ID: "attempt_recheck", Step: r.Step, Deadline: time.Now().Add(time.Minute)})
	if err != nil || a.Inputs["patch"].ArtifactID != grant.PatchID || !reflect.DeepEqual(a.Inputs["tests"], before.Inputs["tests"]) || a.FrontendCheckPins == nil || a.FrontendCheckPins.PatchSHA256 != req.PatchSHA256 || a.FrontendCheckPins.HelperSHA256 != req.HelperSHA256 {
		t.Fatal("next assignment lost the exact check inputs and pins", a, err)
	}
	if replay, err := c.RecoverFrontendCheck(req); err != nil || !reflect.DeepEqual(replay, r) || exec.calls != 1 || store.saves != 1 {
		t.Fatal("identical request did not replay its saved handoff", err, store.saves, exec.calls)
	}
	req.HelperSHA256 = strings.Repeat("e", 64)
	if _, err := c.RecoverFrontendCheck(req); err == nil || store.saves != 1 || exec.calls != 1 {
		t.Fatal("same request ID accepted a different helper identity", err)
	}
}

func TestRecoverFrontendCheckRejectsUnsafeSavedFactsWithoutMutation(t *testing.T) {
	for _, fault := range []string{"budget", "revision", "owner outcome", "prior receipt", "prior patch", "foreign patch", "digest", "active child lease", "child pin", "write check", "unrecognized check environment", "approval bypass", "qualified publication drift", "child changed during validation"} {
		t.Run(fault, func(t *testing.T) {
			c, store, exec, req := frontendRecheckFixture(t)
			r := Clone(c.runs[req.RunID])
			calls := 0
			switch fault {
			case "budget":
				r.Process.MaxAttempts--
			case "revision":
				req.Revision++
			case "owner outcome":
				r.Current().End.Result.Outcome = "ready"
			case "prior receipt":
				r.StepInputs["checks"] = Value{ArtifactID: "attempt_other/result"}
			case "prior patch":
				r.Attempts[len(r.Attempts)-2].Source.InputPatchID = "attempt_other/changes.patch"
			case "foreign patch":
				p := store.artifacts[r.Current().Source.PatchID]
				p.RunID = "run_foreign"
				store.artifacts[p.ID] = p
			case "digest":
				req.PatchSHA256 = strings.Repeat("f", 64)
			case "active child lease":
				c.inFlight["attempt_card0_owner"] = true
			case "child pin":
				r.TaskGroups["cards"].Children[0].Candidate.Commit = strings.Repeat("e", 40)
			case "write check":
				r.Roles["frontend-check"].Execution.Access = "workspace-write"
			case "unrecognized check environment":
				r.Roles["frontend-check"].Execution.Command.Env["NODE_OPTIONS"] = "--require=/foreign/script"
			case "approval bypass":
				r.Process.Steps[taskStepIndex(r.Process, "prepare")].Routes["ready"] = Route{Next: "merge"}
			case "qualified publication drift":
				calls = 1
				exec.validate = func(Run, SourceResult, Profile, string, string) error { return errors.New("draft publication changed") }
			case "child changed during validation":
				calls = 1
				exec.validate = func(Run, SourceResult, Profile, string, string) error {
					c.mu.Lock()
					defer c.mu.Unlock()
					child := c.runs["run_card0"]
					child.Revision++
					c.runs[child.ID] = child
					return nil
				}
			}
			c.runs[r.ID] = r
			if _, err := c.RecoverFrontendCheck(req); err == nil || store.saves != 0 || exec.calls != calls || !reflect.DeepEqual(c.runs[r.ID], r) || len(c.wake) != 0 {
				t.Fatal("unsafe recovery changed or dispatched work", err, store.saves, exec.calls)
			}
		})
	}
}
