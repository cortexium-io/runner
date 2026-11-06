package execution

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/cortexium-io/runner/internal/release"
	"github.com/cortexium-io/runner/internal/runner"
)

// Releases invokes one trusted operator action. It does not run product source,
// grant native roles credentials, approve a gate or schedule downstream work.
type Releases struct {
	disk           *runner.Disk
	cfg            *runner.ReleaseSettings
	workspaces     *Workspaces
	adoptionMu     sync.Mutex
	adoptions      map[string]*sourceAdoptionWork
	adoptionWake   func(string)
	adoptionClosed bool
}

type releaseJob struct {
	Assignment          runner.Assignment    `json:"assignment"`
	Command             []string             `json:"command"`
	Stopped             bool                 `json:"stopped,omitempty"`
	End                 *runner.ExecutionEnd `json:"end,omitempty"`
	GitHub              string               `json:"github,omitempty"`
	SourcePrepared      bool                 `json:"source_prepared,omitempty"`
	Candidate           string               `json:"candidate,omitempty"`
	SelectedBase        string               `json:"selected_base,omitempty"`
	CancelReceipt       json.RawMessage      `json:"cancel_receipt,omitempty"`
	PublicationPrevious json.RawMessage      `json:"publication_previous,omitempty"`
	Source              *runner.SourceResult `json:"source,omitempty"`
	AdoptionReceipt     json.RawMessage      `json:"adoption_receipt,omitempty"`
}

func releaseName(id string) string { return "jobs/" + id + ".release.json" }
func (r *Releases) Owns(id string) bool {
	_, err := os.Lstat(filepath.Join(r.disk.Root, releaseName(id)))
	return !errors.Is(err, os.ErrNotExist)
}
func (r *Releases) load(id string) (releaseJob, error) {
	var j releaseJob
	b, err := r.disk.Read(releaseName(id), r.disk.Policy.Database.MaxRecord)
	if err == nil {
		err = json.Unmarshal(b, &j)
	}
	if err == nil && j.Assignment.AttemptID != id {
		err = errors.New("release identity differs from saved intent")
	}
	return j, err
}
func (r *Releases) save(j releaseJob) error {
	r.adoptionMu.Lock()
	defer r.adoptionMu.Unlock()
	if j.Assignment.Setup.Profile.Mode == "source-adopt" {
		if old, err := r.load(j.Assignment.AttemptID); err == nil && old.Stopped {
			j.Stopped = true
		}
		if j.Stopped && j.End != nil {
			j.End = &runner.ExecutionEnd{Kind: "stopped", Reason: "source adoption settled after cancellation"}
		}
	}
	b, err := json.Marshal(j)
	if err != nil {
		return err
	}
	return r.disk.Write(releaseName(j.Assignment.AttemptID), b, r.disk.Policy.Database.MaxRecord)
}
func (r *Releases) Start(ctx context.Context, a runner.Assignment) (runner.Observation, error) {
	if _, err := r.load(a.AttemptID); errors.Is(err, os.ErrNotExist) {
		if r.cfg == nil || len(r.cfg.Command) == 0 {
			return runner.Observation{
				State: runner.Finished,
				End:   &runner.ExecutionEnd{Kind: "failed", Reason: "release operator is not configured"},
			}, nil
		}
		if err = r.save(releaseJob{Assignment: runner.Clone(a), Command: runner.Clone(r.cfg.Command), GitHub: r.cfg.GitHub}); err != nil {
			return runner.Observation{}, err
		}
	} else if err != nil {
		return runner.Observation{}, err
	}
	return r.Observe(ctx, a.AttemptID)
}
func (r *Releases) observeRelease(ctx context.Context, id string) (runner.Observation, error) {
	j, err := r.load(id)
	if err != nil {
		return runner.Observation{}, err
	}
	zero := int64(0)
	o := runner.Observation{
		State:     runner.Running,
		Usage:     runner.Usage{InputTokens: &zero, OutputTokens: &zero, CachedInputTokens: &zero},
		NextCheck: time.Now().Add(3 * time.Second),
	}
	if j.End == nil {
		if j.Assignment.Source == nil {
			return o, errors.New("release requires an explicit source")
		}
		request := release.Request{
			Project:  j.Assignment.ProjectID,
			RunID:    j.Assignment.RunID,
			Key:      id,
			Action:   j.Assignment.Setup.Profile.Mode,
			Source:   j.Assignment.Source.Ref,
			Previous: j.Assignment.Inputs["receipt"].Inline,
			Recovery: j.Assignment.Inputs["recovery"].Inline,
			Retry:    j.Assignment.Inputs["previous"].Inline,
		}
		sourceAdopt := request.Action == "source-adopt"
		if sourceAdopt {
			request.Action, request.SourceContinuation = "adopt", true
			if len(j.AdoptionReceipt) > 0 {
				return r.finishSourceAdoption(ctx, j, o, release.Response{Receipt: j.AdoptionReceipt}, "ready")
			}
		}
		if request.Action == "readiness" {
			if err := r.prepareReleaseSource(ctx, &j); err != nil {
				o.State = runner.Unknown
				var pending *sourceArchivePending
				o.Reconciling = errors.As(err, &pending) || errors.Is(ctx.Err(), context.DeadlineExceeded) && captureWasInterrupted(ctx, err)
				o.Reason = runner.Clip(err.Error())
				return o, nil
			}
		}
		request.Source = r.releaseSource(j)
		if len(j.CancelReceipt) > 0 {
			request.Key = id + "_cancel"
			request.Action = "recover-stop"
			request.Previous = j.CancelReceipt
		}
		if request.Action == "readiness" && r.workspaces != nil {
			if w, err := r.workspaces.load(j.Assignment.RunID); err == nil {
				o.Source = &runner.SourceResult{Repository: w.Repository, Base: w.Base, WorkDir: w.WorkDir, AttemptID: id}
			}
		}
		if request.Action == "publish" {
			candidate, err := r.publishCandidate(ctx, &j)
			if err != nil {
				o.State = runner.Unknown
				o.Reason = runner.Clip(err.Error())
				return o, nil
			}
			request.Candidate = candidate
			if len(j.PublicationPrevious) != 0 {
				request.Previous = j.PublicationPrevious
			}
		}
		input, _ := json.Marshal(request)
		call, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		cmd := exec.CommandContext(call, j.Command[0], j.Command[1:]...)
		cmd.Stdin = bytes.NewReader(input)
		output := &diagnosticTail{limit: 32 << 10}
		cmd.Stdout = output
		cmd.Stderr = &diagnosticTail{limit: 1024}
		if err = cmd.Run(); err != nil {
			o.State = runner.Unknown
			o.Reconciling = true
			o.Reason = "release operator disconnected; rejoin the same saved operation " + id
			return o, nil
		}
		var response release.Response
		if err = json.Unmarshal(output.bytes(), &response); err != nil || response.Key != request.Key {
			o.State = runner.Unknown
			o.Reason = "release operator response identity or format differs; retain the operation"
			return o, nil
		}
		o.Reason = runner.Clip(response.Reason)
		if response.State != "finished" {
			if response.State != "running" {
				o.State = runner.Unknown
			}
			return o, nil
		}
		if j.Stopped && request.Action == "stop" && !response.Failed {
			var receipt struct {
				Source  string `json:"source_ref"`
				DataSHA string `json:"data_sha256"`
			}
			if json.Unmarshal(response.Receipt, &receipt) == nil && receipt.Source != "" && receipt.DataSHA != "" {
				j.CancelReceipt = response.Receipt
				if err = r.save(j); err != nil {
					return runner.Observation{}, err
				}
				o.Reason = "release cancellation is restoring the unchanged previous service before migration"
				return o, nil
			}
		}
		outcome := "ready"
		if response.Outcome != "" {
			outcome = response.Outcome
		}
		if response.Failed {
			outcome = "blocked"
		}
		if len(response.Receipt) == 0 {
			response.Receipt = json.RawMessage(`{}`)
		}
		if sourceAdopt {
			return r.finishSourceAdoption(ctx, j, o, response, outcome)
		}
		if request.Action == "publish" && outcome == "ready" && !j.Stopped {
			if err = r.recordPublication(j, response.Receipt); err != nil {
				return runner.Observation{}, err
			}
		}
		reason := o.Reason
		var handle struct {
			Source string `json:"source_ref"`
		}
		if outcome == "ready" && json.Unmarshal(response.Receipt, &handle) == nil && handle.Source != "" {
			switch request.Action {
			case "readiness", "publish", "qualify", "merge", "stop", "migrate", "pin", "deploy":
				// The private journal retains the complete operation narrative.
				// Persist decision/failure reasons and compact successful handles.
				reason = ""
			}
		}
		j.End = &runner.ExecutionEnd{
			Kind:   "returned",
			Result: &runner.StepResult{Outcome: outcome, Reason: reason, Outputs: map[string]runner.Value{"receipt": {Inline: response.Receipt}}},
		}
		if response.Failed && response.Retryable && (request.Action == "readiness" || request.Action == "qualify") {
			j.End = &runner.ExecutionEnd{Kind: "failed", Reason: o.Reason, Retryable: true}
		}
		if j.Stopped {
			j.End = &runner.ExecutionEnd{
				Kind:   "stopped",
				Reason: "release cancellation settled: " + o.Reason + "; retained operator receipt " + request.Key,
			}
		}
		if err = r.save(j); err != nil {
			return runner.Observation{}, err
		}
	}
	o.State, o.End, o.Source = runner.Finished, j.End, j.Source
	return o, nil
}
func (r *Releases) Stop(ctx context.Context, id string) error {
	j, err := r.load(id)
	if err != nil || j.End != nil {
		return err
	}
	j.Stopped = true
	if err = r.save(j); err != nil {
		return err
	}
	if j.Assignment.Setup.Profile.Mode == "source-adopt" {
		if err = r.stopSourceAdoption(ctx, id); err != nil {
			return err
		}
	}
	_, err = r.Observe(ctx, id)
	return err
}
func (r *Releases) Release(_ context.Context, id string) error {
	j, err := r.load(id)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if j.End == nil {
		return errors.New("cannot release an unsettled external operation")
	}
	if j.Assignment.Setup.Profile.Mode == "source-adopt" {
		if err = r.releaseSourceAdoption(j); err != nil {
			return err
		}
	}
	return r.disk.Remove(releaseName(id))
}

// Cancellation can race with a successful stop result before its next step is
// dispatched. The normal run cleanup owns the same configured compensation;
// it cannot discard the workspace and leave an unchanged old service stopped.
func (r *Releases) recoverCancelledStop(ctx context.Context, run runner.Run) error {
	if len(run.Attempts) == 0 {
		return nil
	}
	last := run.Attempts[len(run.Attempts)-1]
	if !run.CancelRequested && !last.TimedOut {
		return nil
	}
	step := run.Process.Steps[last.Step]
	profile, err := run.Roles[step.Role].Profile(last.Profile)
	if err != nil || profile.Harness != "release" || profile.Mode != "stop" || last.End == nil || last.End.Result == nil || last.End.Result.Outcome != "ready" {
		return nil
	}
	previous := last.End.Result.Outputs["receipt"].Inline
	var receipt struct {
		Source  string `json:"source_ref"`
		DataSHA string `json:"data_sha256"`
	}
	if json.Unmarshal(previous, &receipt) != nil || receipt.Source == "" || receipt.DataSHA == "" {
		return nil
	}
	a := runner.Assignment{
		ProjectID: run.ProjectID, RunID: run.ID, AttemptID: last.ID + "_cancel", Repository: run.Repository,
		Source: &runner.SourceSettings{Ref: receipt.Source},
		Setup:  runner.ExecutionSetup{Profile: runner.Profile{Harness: "release", Access: "input-only", Mode: "recover-stop"}},
		Inputs: map[string]runner.Value{"receipt": {Inline: previous}},
		Limits: runner.ExecutionLimits{MaxRecord: r.disk.Policy.Database.MaxRecord},
	}
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		o, err := r.Start(ctx, a)
		if err != nil {
			return err
		}
		if o.State == runner.Finished {
			var proof struct {
				Recovery string `json:"recovery"`
			}
			if o.End == nil || o.End.Result == nil || json.Unmarshal(o.End.Result.Outputs["receipt"].Inline, &proof) != nil || proof.Recovery == "" {
				return errors.New("cancelled release needs owner inspection; configured previous-service recovery did not establish health")
			}
			return r.Release(ctx, a.AttemptID)
		}
		select {
		case <-ctx.Done():
			return errors.New("cancelled release recovery remains active; retain its saved operation")
		case <-ticker.C:
		}
	}
}
