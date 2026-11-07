package runner_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
	"github.com/cortexium-io/runner/internal/runner/sqlite"
)

type collectionRecoveryFixture struct {
	rig      *rig
	before   runner.Run
	children []runner.Run
	req      runner.RecoverCollectionRequest
	executor *correctionRecoveryExecutor
	dir      string
}

func collectionRecoveryRig(t *testing.T, change func(*runner.Run, *runner.RecoverCollectionRequest)) collectionRecoveryFixture {
	t.Helper()
	s := snapshot(policy())
	s.Instance.Storage.Database.MaxRecord = 40 << 10
	s.Instance.Storage.Database.MaxSize = 4 << 20
	root := filepath.Join("..", "..", "examples", "jacks-break")
	var process runner.Process
	data, err := os.ReadFile(filepath.Join(root, "planned-retained-investigation.json"))
	if err != nil || json.Unmarshal(data, &process) != nil {
		t.Fatal(err)
	}
	// Freeze the legacy collector that failed before it could return a report.
	for i := range process.Steps {
		if process.Steps[i].ID == "measure" {
			process.Steps[i].Role = "delivery-measure"
			delete(process.Steps[i].Routes, "changes_requested")
			delete(process.Steps[i].Routes, "blocked")
		}
	}
	roles := map[string]runner.Role{}
	var load func([]runner.Step)
	load = func(steps []runner.Step) {
		for _, step := range steps {
			if step.ForEach != nil {
				load(step.ForEach.Steps)
				continue
			}
			var role runner.Role
			data, err := os.ReadFile(filepath.Join(root, "roles", step.Role+".json"))
			if err != nil || json.Unmarshal(data, &role) != nil {
				t.Fatal(step.Role, err)
			}
			roles[role.ID] = role
		}
	}
	load(process.Steps)
	index := func(id string) int {
		t.Helper()
		for i, step := range process.Steps {
			if step.ID == id {
				return i
			}
		}
		t.Fatal("missing fixture step", id)
		return -1
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	base, tree := strings.Repeat("a", 40), strings.Repeat("b", 40)
	source := &runner.SourceResult{
		AttemptID:  "attempt_join",
		PatchID:    "attempt_join/changes.patch",
		Repository: filepath.Join(dir, "repository"),
		WorkDir:    filepath.Join(dir, "checkout"),
		Base:       base,
		Tree:       tree,
	}
	returned := func(outputs map[string]runner.Value) *runner.ExecutionEnd {
		return &runner.ExecutionEnd{Kind: "returned", Result: &runner.StepResult{Outcome: "ready", Outputs: outputs}}
	}
	r := runner.Run{
		RecordFormat:      3,
		ID:                "run_collection",
		ProjectID:         "demo",
		Repository:        source.Repository,
		Source:            &runner.SourceSettings{Ref: base},
		Process:           process,
		Roles:             roles,
		Profiles:          map[string]string{},
		Step:              index("measure"),
		Visit:             9,
		Status:            "Failed",
		Reason:            "command exited 1: measurement output exceeds 256KiB\n",
		ResourcesReleased: true,
		Created:           time.Now().UTC(),
		Inputs: map[string]runner.Value{
			"request":     runner.Text("Complete the original scoped investigation without losing the five accepted cards."),
			"package":     runner.Text("..."),
			"tests":       runner.Text("^TestFixture$"),
			"benchmark":   runner.Text("^BenchmarkFixture$"),
			"publication": {Inline: []byte(`{"key":"accepted-publication"}`)},
			"patch":       {ArtifactID: "attempt_adopt/changes.patch"},
		},
		StepInputs: map[string]runner.Value{
			"patch":     {ArtifactID: source.PatchID},
			"package":   runner.Text("..."),
			"benchmark": runner.Text("^BenchmarkFixture$"),
			"previous":  runner.Text(""),
		},
		RouteUses: map[string]int{"check/changes_requested": 1},
	}
	for _, id := range []string{"baseline", "source-adopt", "admit", "plan", "cards", "join", "check", "measure"} {
		a := runner.Attempt{ID: "attempt_" + strings.ReplaceAll(id, "source-adopt", "adopt"), Step: index(id), State: runner.Finished, Released: true, End: returned(map[string]runner.Value{})}
		switch id {
		case "baseline":
			a.End.Result.Outputs = map[string]runner.Value{"artifact": {ArtifactID: "attempt_baseline/report"}}
		case "join":
			a.Source = source
			a.End.Result.Outputs = map[string]runner.Value{"patch": {ArtifactID: source.PatchID}, "manifest": {ArtifactID: "attempt_join/manifest"}, "feedback": runner.Text("")}
		case "check":
			a.Source = &runner.SourceResult{AttemptID: a.ID, InputPatchID: source.PatchID, Repository: source.Repository, Base: base, WorkDir: source.WorkDir}
			a.End.Result.Outputs = map[string]runner.Value{"check": runner.Text("scoped fixture tests passed"), "artifact": {ArtifactID: "attempt_check/report"}}
		case "measure":
			a.Visit = r.Visit
			a.Source = &runner.SourceResult{AttemptID: a.ID, InputPatchID: source.PatchID, Repository: source.Repository, Base: base, WorkDir: source.WorkDir}
			a.End = &runner.ExecutionEnd{Kind: "failed", Reason: r.Reason}
		}
		r.Attempts = append(r.Attempts, a)
	}
	group := &runner.TaskGroupState{
		StepID:     "cards",
		AttemptID:  "attempt_cards",
		Repository: r.Repository,
		Base:       base,
		Started:    r.Created,
		Done:       true,
	}
	var children []runner.Run
	for i := range 5 {
		item, id := fmt.Sprintf("card%d", i), fmt.Sprintf("child%d", i)
		identity := runner.TaskIdentity{RootRunID: r.ID, GroupStep: group.StepID, ItemID: item}
		candidate := runner.TaskCandidate{
			Identity:   identity,
			RunID:      id,
			Repository: r.Repository,
			Base:       base,
			Commit:     fmt.Sprintf("%040x", 100+i),
			Tree:       fmt.Sprintf("%040x", 200+i),
			PatchID:    fmt.Sprintf("attempt_%s_owner/changes.patch", item),
		}
		raw, _ := json.Marshal(candidate)
		child := runner.Run{
			RecordFormat:      3,
			ID:                id,
			ProjectID:         r.ProjectID,
			Repository:        r.Repository,
			Source:            &runner.SourceSettings{Ref: base},
			Task:              &runner.TaskRunLink{Identity: identity},
			Status:            "Completed",
			ResourcesReleased: true,
			Created:           r.Created,
			Process: runner.Process{
				ID:                 "isolated-card",
				MaxAttempts:        64,
				MaxAttemptsPerStep: 1,
				AttemptTimeout:     "20m",
				Steps: []runner.Step{
					{ID: "seed", Role: "task-source-seed"},
					{ID: "owner", Role: "planned-card-owner"},
					{ID: "pin", Role: "task-source-pin"},
				},
			},
			Roles:    map[string]runner.Role{},
			Profiles: map[string]string{},
			Outputs:  map[string]runner.Value{"result": {Inline: raw}},
		}
		for j, step := range child.Process.Steps {
			child.Roles[step.Role] = roles[step.Role]
			child.Attempts = append(child.Attempts, runner.Attempt{ID: fmt.Sprintf("attempt_%s_%s", item, step.ID), Step: j, State: runner.Finished, Released: true, End: returned(map[string]runner.Value{})})
		}
		group.Items = append(group.Items, runner.TaskItem{ID: item, Inputs: map[string]json.RawMessage{"task": []byte(`"accepted fixture card"`), "paths": []byte(fmt.Sprintf(`["fixture%d.go"]`, i))}})
		group.Children = append(group.Children, runner.TaskChild{ItemID: item, RunID: id, Candidate: &candidate})
		children = append(children, child)
	}
	r.TaskGroups = map[string]*runner.TaskGroupState{group.StepID: group}
	req := runner.RecoverCollectionRequest{RequestID: "owner-approved-collection", RunID: r.ID, AttemptID: "attempt_measure"}
	if change != nil {
		change(&r, &req)
	}
	store, err := sqlite.Open(dir, s.Instance.Storage)
	if err != nil {
		t.Fatal(err)
	}
	disk, err := runner.OpenDisk(dir, s.Instance.Storage)
	if err != nil {
		t.Fatal(err)
	}
	var artifacts []runner.Artifact
	for id, text := range map[string]string{
		source.PatchID:                "captured joined source",
		"attempt_adopt/changes.patch": "accepted publication patch",
		"attempt_baseline/report":     `{"baseline":true,"controls":6}`,
		"attempt_join/manifest":       `{"accepted_cards":5}`,
		"attempt_check/report":        `{"executed":7,"failed":0}`,
	} {
		if err := disk.Write("artifacts/"+id, []byte(text), disk.Policy.MaxArtifact); err != nil {
			t.Fatal(err)
		}
		artifacts = append(artifacts, runner.Artifact{ID: id, RunID: r.ID, ProjectID: r.ProjectID, Bytes: int64(len(text))})
	}
	initial := runner.Clone(r)
	initial.TaskGroups = nil
	if err := store.Save(t.Context(), initial, artifacts, nil); err != nil {
		t.Fatal("initial root", err)
	}
	for i, child := range children {
		patch := group.Children[i].Candidate.PatchID
		if err := disk.Write("artifacts/"+patch, []byte("accepted card source"), disk.Policy.MaxArtifact); err != nil {
			t.Fatal(err)
		}
		if err := store.Save(t.Context(), child, []runner.Artifact{{ID: patch, RunID: child.ID, ProjectID: child.ProjectID, Bytes: int64(len("accepted card source"))}}, nil); err != nil {
			t.Fatal("child", err)
		}
	}
	if err := store.Save(t.Context(), r, nil, nil); err != nil {
		t.Fatal("accepted root group", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	executor := &correctionRecoveryExecutor{started: make(chan runner.Assignment, 4)}
	for _, a := range r.Attempts {
		if !a.Released {
			executor.cleanupEntered = make(chan string, len(r.Attempts))
			break
		}
	}
	rig := openRig(t, dir, s, func(runner.Executor) runner.Executor { return executor })
	before, err := rig.core.Get(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	for i, child := range children {
		children[i], err = rig.core.Get(child.ID)
		if err != nil {
			t.Fatal(err)
		}
	}
	return collectionRecoveryFixture{rig, before, children, req, executor, dir}
}

func TestCollectionRecoveryPreservesFrozenRunAndFiveAcceptedCards(t *testing.T) {
	f := collectionRecoveryRig(t, nil)
	defer f.rig.close()
	_, definition, _ := runner.MarshalRunRecord(f.before)
	recovered, err := f.rig.core.RecoverCollection(f.req)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Status != "Queued" || recovered.Step != 7 || recovered.Visit != f.before.Visit+1 || recovered.VisitExtraAttempts != 0 || recovered.ResourcesReleased || len(recovered.Attempts) != 8 || recovered.Process.MaxAttempts != 64 || recovered.Process.MaxAttemptsPerStep != 1 || recovered.RouteUses["check/changes_requested"] != 1 || recovered.RouteUses["measure/collection_failure"] != 1 {
		t.Fatal("collection grant changed execution, original allowance or routing evidence", recovered.Status, recovered.Step, recovered.Reason)
	}
	_, afterDefinition, _ := runner.MarshalRunRecord(recovered)
	if !bytes.Equal(definition, afterDefinition) || !reflect.DeepEqual(recovered.TaskGroups, f.before.TaskGroups) || recovered.StepInputs["baseline"].ArtifactID != "attempt_baseline/report" || recovered.StepInputs["manifest"].ArtifactID != "attempt_join/manifest" || recovered.StepInputs["checks"].ArtifactID != "attempt_check/report" || !reflect.DeepEqual(recovered.StepInputs["request"], f.before.Inputs["request"]) {
		t.Fatal("frozen definitions, accepted cards or original evidence changed")
	}
	for i, old := range f.before.Attempts {
		got := recovered.Attempts[i]
		if old.ID != got.ID || !reflect.DeepEqual(old.End, got.End) || !reflect.DeepEqual(old.Source, got.Source) || got.Superseded != (old.Step >= recovered.Step) {
			t.Fatal("collection grant rewrote failed/prior evidence instead of normal invalidation", old.ID)
		}
	}
	for _, old := range f.children {
		current, err := f.rig.core.Get(old.ID)
		if err != nil || !reflect.DeepEqual(current.Attempts, old.Attempts) || !reflect.DeepEqual(current.Outputs, old.Outputs) {
			t.Fatal("collection recovery reran or changed an accepted child", old.ID, err)
		}
	}
	audit := recovered.CollectionRecovery
	if audit == nil || audit.RequestID != f.req.RequestID || audit.AttemptID != f.req.AttemptID || audit.Owner != "owner" || audit.PatchID != "attempt_join/changes.patch" || recovered.StepInputs["measurement"].ArtifactID != audit.Artifact {
		t.Fatal("collection grant lacks its exact incomplete-evidence audit", audit)
	}
	data, err := f.rig.disk.Read("artifacts/"+audit.Artifact, f.rig.disk.Policy.MaxArtifact)
	var diagnostic map[string]any
	if err != nil || json.Unmarshal(data, &diagnostic) != nil || diagnostic["status"] != "collection_failure" || diagnostic["evidence_status"] != "incomplete" || diagnostic["incomplete"] != true || diagnostic["failed_attempt"] != f.req.AttemptID || diagnostic["samples"] != nil || diagnostic["seconds"] != nil {
		t.Fatal("diagnostic invented measurement results or omitted collection failure", string(data), err)
	}
	receipt, err := f.rig.store.FindReceipt(t.Context(), f.req.RequestID)
	args, _ := json.Marshal(f.req)
	if err != nil || receipt.Kind != "recover_collection" || receipt.RunID != f.before.ID || !bytes.Equal(receipt.Args, args) {
		t.Fatal("owner grant has no durable exact-request receipt", receipt, err)
	}
	stored, err := f.rig.store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, saved := range stored {
		if saved.ID == recovered.ID {
			found = true
			if !reflect.DeepEqual(saved.CollectionRecovery, audit) {
				t.Fatal("collection audit did not survive SQLite serialization", saved.CollectionRecovery)
			}
		}
	}
	if !found {
		t.Fatal("recovered run disappeared from retained history")
	}
	select {
	case a := <-f.executor.started:
		if a.Task != recovered.Roles[recovered.Process.Steps[recovered.Step].Role].Purpose || a.Inputs["measurement"].ArtifactID != audit.Artifact || a.Setup.Profile.Access != "workspace-write" {
			t.Fatal("normal Core dispatch lost frozen owner/evidence", a)
		}
	case <-time.After(time.Second):
		t.Fatal("normal Core did not dispatch the configured owner")
	}
	if _, err := f.rig.core.RecoverCollection(f.req); err != nil {
		t.Fatal("same request did not replay idempotently", err)
	}
	duplicate := f.req
	duplicate.RequestID = "second-grant-same-failed-attempt"
	if _, err := f.rig.core.RecoverCollection(duplicate); err == nil {
		t.Fatal("the same failed measurement was granted again")
	}
	select {
	case a := <-f.executor.started:
		t.Fatal("receipt replay dispatched additional execution", a)
	default:
	}
}

func TestCollectionRecoveryRejectsUnsettledUnboundOrUnbudgetedFailures(t *testing.T) {
	for name, change := range map[string]func(*runner.Run, *runner.RecoverCollectionRequest){
		"wrong attempt":   func(_ *runner.Run, req *runner.RecoverCollectionRequest) { req.AttemptID = "other" },
		"unknown effects": func(r *runner.Run, _ *runner.RecoverCollectionRequest) { r.Attempts[7].State = runner.Unknown },
		"cancelled":       func(r *runner.Run, _ *runner.RecoverCollectionRequest) { r.CancelRequested = true },
		"pending approval": func(r *runner.Run, _ *runner.RecoverCollectionRequest) {
			r.Decision = &runner.Decision{ID: "protected"}
		},
		"cleanup pending": func(r *runner.Run, _ *runner.RecoverCollectionRequest) {
			r.ResourcesReleased = false
			r.CleanupBlocked = true
			r.CleanupError = "preserve source"
		},
		"attempt cleanup": func(r *runner.Run, _ *runner.RecoverCollectionRequest) { r.Attempts[7].Released = false },
		"deadline":        func(r *runner.Run, _ *runner.RecoverCollectionRequest) { r.Attempts[7].TimedOut = true },
		"different failure": func(r *runner.Run, _ *runner.RecoverCollectionRequest) {
			r.Attempts[7].End.Reason = "transport unavailable"
			r.Reason = r.Attempts[7].End.Reason
		},
		"retryable": func(r *runner.Run, _ *runner.RecoverCollectionRequest) { r.Attempts[7].End.Retryable = true },
		"network": func(r *runner.Run, _ *runner.RecoverCollectionRequest) {
			role := r.Roles["delivery-measure"]
			role.Execution.Command.Network = true
			r.Roles[role.ID] = role
		},
		"arbitrary command": func(r *runner.Run, _ *runner.RecoverCollectionRequest) {
			role := r.Roles["delivery-measure"]
			role.Execution.Command.Args[0] = "/bin/sh"
			r.Roles[role.ID] = role
		},
		"wrong artifact": func(r *runner.Run, _ *runner.RecoverCollectionRequest) {
			role := r.Roles["delivery-measure"]
			role.Execution.Command.Artifact = "other.json"
			r.Roles[role.ID] = role
		},
		"foreign candidate": func(r *runner.Run, _ *runner.RecoverCollectionRequest) {
			r.Attempts[7].Source.InputPatchID = "foreign/changes.patch"
		},
		"missing source": func(r *runner.Run, _ *runner.RecoverCollectionRequest) { r.Attempts[5].Source = nil },
		"wrong owner edge": func(r *runner.Run, _ *runner.RecoverCollectionRequest) {
			r.Process.Steps[8].Routes["changes_requested"] = runner.Route{Next: "plan", MaxTimes: 3}
		},
		"prior check failed": func(r *runner.Run, _ *runner.RecoverCollectionRequest) {
			r.Attempts[6].End.Result.Outcome = "changes_requested"
		},
		"budget":             func(r *runner.Run, _ *runner.RecoverCollectionRequest) { r.Process.MaxAttempts = 23 },
		"completion reserve": func(r *runner.Run, _ *runner.RecoverCollectionRequest) { r.Process.MaxAttempts = 24 },
		"already granted": func(r *runner.Run, _ *runner.RecoverCollectionRequest) {
			r.CollectionRecovery = &runner.CollectionRecovery{AttemptID: "attempt_measure"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := collectionRecoveryRig(t, change)
			defer f.rig.close()
			if _, err := f.rig.core.RecoverCollection(f.req); err == nil {
				t.Fatal("unsafe collection recovery accepted", name)
			}
		})
	}
}

func TestCollectionRecoveryPreservesDifferentDiagnosticAndValidatorFailure(t *testing.T) {
	for _, mode := range []string{"diagnostic drift", "source validator"} {
		t.Run(mode, func(t *testing.T) {
			f := collectionRecoveryRig(t, nil)
			defer f.rig.close()
			name := "artifacts/" + f.req.AttemptID + "/collection-failure.json"
			if mode == "diagnostic drift" {
				if err := f.rig.disk.Write(name, []byte("preserve unknown diagnostic"), f.rig.disk.Policy.MaxArtifact); err != nil {
					t.Fatal(err)
				}
			} else {
				f.executor.validate = func(context.Context, runner.Run, runner.SourceResult) error {
					return errors.New("retained capture hash differs")
				}
			}
			if _, err := f.rig.core.RecoverCollection(f.req); err == nil {
				t.Fatal("untrusted evidence became an owner grant")
			}
			if mode == "diagnostic drift" {
				data, err := f.rig.disk.Read(name, f.rig.disk.Policy.MaxArtifact)
				if err != nil || string(data) != "preserve unknown diagnostic" {
					t.Fatal("recovery overwrote differing retained evidence", err)
				}
			}
		})
	}
}
