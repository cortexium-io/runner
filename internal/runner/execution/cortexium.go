package execution

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"time"

	"github.com/cortexium-io/runner/internal/release"
	"github.com/cortexium-io/runner/internal/runner"
	"github.com/cortexium-io/runner/internal/saas"
)

func (r *Releases) cortexiumInput(a runner.Assignment) (release.CortexiumRequest, []byte, saas.Evidence, map[string][]byte, error) {
	var p release.CortexiumRequest
	var empty saas.Evidence
	bad := errors.New("Cortexium browser requires the current checked candidate and independent source APPROVE")
	var verdict string
	if a.ProjectID != "cortexium-local" || a.Setup.Profile.Harness != "release" || a.Setup.Profile.Mode != "cortexium-browser" || a.Setup.Profile.Access != "input-only" || json.Unmarshal(a.Inputs["verdict"].Inline, &verdict) != nil || verdict != "APPROVE" {
		return p, nil, empty, nil, bad
	}
	w, err := r.workspaces.load(a.RunID)
	if err != nil || w.Released || w.CleanupIntent != nil || w.Candidate == nil || w.Candidate.Base != w.Base || w.Candidate.PatchID != a.Inputs["patch"].ArtifactID {
		return p, nil, empty, nil, bad
	}
	read := func(name string) ([]byte, error) {
		id := a.Inputs[name].ArtifactID
		if id == "" {
			return nil, bad
		}
		return r.disk.Read("artifacts/"+id, a.Limits.MaxArtifact)
	}
	patch, err := read("patch")
	if err != nil {
		return p, nil, empty, nil, err
	}
	authority, err := read("authority")
	if err != nil {
		return p, nil, empty, nil, err
	}
	bound, err := saas.DecodeAuthority(authority, w.Base)
	if err != nil {
		return p, nil, empty, nil, err
	}
	checks, err := read("checks")
	if err != nil {
		return p, nil, empty, nil, err
	}
	e, files, err := saas.DecodeCheckedBuild(checks, w.Base, saas.Digest(patch), saas.Digest(authority))
	if err == nil {
		err = saasRuntimeBinding(e, bound)
	}
	if err != nil {
		return p, nil, empty, nil, err
	}
	p = release.CortexiumRequest{
		Deadline:        a.Deadline.UnixNano(),
		PolicySHA256:    bound.OperatorPolicySHA256,
		Version:         1,
		ArtifactID:      a.Inputs["checks"].ArtifactID,
		Bytes:           int64(len(checks)),
		SHA256:          saas.Digest(checks),
		PatchID:         a.Inputs["patch"].ArtifactID,
		PatchSHA256:     e.PatchSHA256,
		AuthorityID:     a.Inputs["authority"].ArtifactID,
		AuthoritySHA256: e.AuthoritySHA256,
		ApprovalSHA256:  saas.Digest([]byte(`"APPROVE"`)),
	}
	return p, checks, e, files, nil
}

func (r *Releases) observeCortexium(ctx context.Context, j releaseJob, o runner.Observation) (runner.Observation, error) {
	if j.Stopped && !j.OperatorDispatched {
		j.End = &runner.ExecutionEnd{
			Kind:   "stopped",
			Reason: "Cortexium browser cancelled before operator dispatch",
		}
		if err := r.save(j); err != nil {
			return o, err
		}
		o.State, o.End = runner.Finished, j.End
		return o, nil
	}
	p, payload, checked, files, err := r.cortexiumInput(j.Assignment)
	if err == nil && j.CortexiumInput != nil && (p != *j.CortexiumInput || checked.Base != j.CortexiumBase) {
		err = errors.New("Cortexium input identity differs from the original dispatched operation")
	}
	if err != nil {
		// No externally selected artifact or stale source reaches the operator.
		if j.OperatorDispatched && j.CortexiumInput != nil {
			p = *j.CortexiumInput
			checked.Base = j.CortexiumBase
			j.CortexiumIntegrityLost, j.Stopped = true, true
		} else {
			j.End = &runner.ExecutionEnd{Kind: "failed", Reason: runner.Clip(err.Error())}
			if err = r.save(j); err != nil {
				return o, err
			}
			o.State, o.End = runner.Finished, j.End
			return o, nil
		}
	}
	if j.CortexiumInput == nil {
		saved := p
		j.CortexiumInput = &saved
		j.CortexiumBase = checked.Base
	}
	p.Transport = "payload"
	if j.CortexiumAcknowledged {
		p.Transport = "observe"
		payload = nil
	}
	if j.Stopped {
		p.Transport = "stop"
		payload = nil
	}
	request := release.Request{
		Project:   j.Assignment.ProjectID,
		RunID:     j.Assignment.RunID,
		Key:       j.Assignment.AttemptID,
		Action:    "cortexium-browser",
		Source:    checked.Base,
		Cortexium: &p,
	}
	header, _ := json.Marshal(request)
	if len(header) > 32<<10 {
		return o, errors.New("Cortexium control header exceeds32KiB")
	}
	input := bytes.NewBuffer(make([]byte, 0, len(header)+1+len(payload)))
	input.Write(header)
	input.WriteByte('\n')
	input.Write(payload)
	j.OperatorDispatched = true
	if err = r.save(j); err != nil {
		return o, err
	}
	call, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(call, j.Command[0], j.Command[1:]...)
	if lease, ok := ctx.Value(captureLeaseKey{}).(*os.File); ok {
		cmd.ExtraFiles = []*os.File{lease}
	}
	cmd.Stdin = input
	output := &diagnosticTail{limit: 24 << 20} // Existing base64 envelope for one16MiB evidence artifact.
	cmd.Stdout = output
	cmd.Stderr = &diagnosticTail{limit: 1024}
	if err = cmd.Run(); err != nil {
		o.State, o.Reconciling, o.Reason = runner.Unknown, true, "Cortexium operator disconnected; rejoin the same saved operation"
		return o, nil
	}
	var response release.Response
	if json.Unmarshal(output.bytes(), &response) != nil || response.Key != request.Key {
		o.State, o.Reason = runner.Unknown, "Cortexium response identity/format differs; retain the same operation"
		return o, nil
	}
	j.CortexiumAcknowledged = response.PayloadRetained
	if response.State != "finished" {
		if err = r.save(j); err != nil {
			return o, err
		}
		o.Reason = runner.Clip(response.Reason)
		if response.State != "running" {
			o.State = runner.Unknown
		}
		return o, nil
	}
	var receipt release.Receipt
	if json.Unmarshal(response.Receipt, &receipt) != nil || receipt.Key != request.Key || receipt.RunID != request.RunID || receipt.Action != request.Action || receipt.Policy != p.PolicySHA256 || receipt.Source != checked.Base || receipt.BuildSHA256 != p.SHA256 {
		o.State, o.Reason = runner.Unknown, "Cortexium result is not bound to its checked build"
		return o, nil
	}
	if j.Stopped {
		j.End = &runner.ExecutionEnd{
			Kind:   "stopped",
			Reason: "Cortexium operator cancellation and owned resource settlement confirmed",
		}
		if j.CortexiumIntegrityLost {
			j.End.Kind = "failed"
			j.End.Reason = "Cortexium input integrity changed after dispatch; original operation stopped and settled"
		}
		if err = r.save(j); err != nil {
			return o, err
		}
		o.State, o.End = runner.Finished, j.End
		return o, nil
	}
	outcome := response.Outcome
	if response.Failed {
		outcome = "blocked"
	}
	if outcome != "ready" && outcome != "changes_requested" && outcome != "blocked" {
		return o, errors.New("Cortexium returned an undeclared outcome")
	}
	data := response.Evidence
	if len(data) > 0 && (len(data) > saas.MaxBundle || saas.Digest(data) != response.EvidenceSHA256 || response.EvidenceSHA256 != receipt.BrowserEvidenceSHA256) {
		return o, errors.New("Cortexium evidence length/hash differs")
	}
	if outcome == "ready" {
		e, _, err := saas.Decode(data, checked.Base, checked.PatchSHA256, checked.AuthoritySHA256)
		if err != nil {
			return o, fmt.Errorf("Cortexium returned incomplete browser evidence: %w", err)
		}
		if !reflect.DeepEqual(e.Build, checked.Build) || e.ServerSHA256 != checked.ServerSHA256 || e.FrontendSHA256 != checked.FrontendSHA256 || receipt.ServerSHA != checked.ServerSHA256 || receipt.Image == "" {
			return o, errors.New("Cortexium browser proof is stale, foreign or incomplete")
		}
	} else if len(data) == 0 {
		// Preserve actionable owned check facts even when a runtime prerequisite
		// failed before a viewport existed. This diagnostic cannot pass review.
		checked.Status, checked.Reason = "incomplete", runner.Clip(response.Reason)
		data, err = saas.Pack(checked, files)
		if err != nil {
			return o, err
		}
	}
	artifact := j.Assignment.AttemptID + "/result"
	if err = r.disk.Write("artifacts/"+artifact, data, j.Assignment.Limits.MaxArtifact); err != nil {
		return o, err
	}
	j.End = &runner.ExecutionEnd{
		Kind: "returned",
		Result: &runner.StepResult{
			Outcome: outcome,
			Reason:  runner.Clip(response.Reason),
			Outputs: map[string]runner.Value{
				"check":    runner.Text(runner.Clip(response.Reason)),
				"artifact": {ArtifactID: artifact},
			},
		},
	}
	if err = r.save(j); err != nil {
		return o, err
	}
	o.State, o.End = runner.Finished, j.End
	return o, nil
}
