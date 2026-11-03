// Package execution adapts execution mechanisms to Runner's small executor contract.
package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
)

type job struct {
	Assignment      runner.Assignment    `json:"assignment"`
	Ready           time.Time            `json:"ready"`
	End             *runner.ExecutionEnd `json:"end,omitempty"`
	DroppedLogBytes int64                `json:"dropped_log_bytes,omitempty"`
}

// Deterministic is a durable, controlled test executor. Its job record IS the
// execution: it does not claim OS-process or model-session restart support.
type Deterministic struct {
	mu           sync.Mutex
	disk         *runner.Disk
	readArtifact func(context.Context, string) ([]byte, error)
}

func New(d *runner.Disk, read func(context.Context, string) ([]byte, error)) *Deterministic {
	return &Deterministic{disk: d, readArtifact: read}
}
func (d *Deterministic) load(id string) (job, error) {
	var j job
	b, e := d.disk.Read("jobs/"+id+".json", d.disk.Policy.Database.MaxRecord)
	if e == nil {
		e = json.Unmarshal(b, &j)
	}
	return j, e
}
func (d *Deterministic) save(j job) error {
	b, e := json.Marshal(j)
	if e != nil {
		return e
	}
	return d.disk.Write("jobs/"+j.Assignment.AttemptID+".json", b, d.disk.Policy.Database.MaxRecord)
}
func (d *Deterministic) Start(ctx context.Context, a runner.Assignment) (runner.Observation, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if e := ctx.Err(); e != nil {
		return runner.Observation{}, e
	}
	if j, e := d.load(a.AttemptID); e == nil {
		return d.observe(ctx, j)
	} else if !errors.Is(e, os.ErrNotExist) {
		return runner.Observation{}, e
	}
	delay, _ := time.ParseDuration(a.Setup.Profile.Delay)
	j := job{Assignment: a, Ready: time.Now().Add(delay)}
	// The durable record precedes every controlled side effect.
	if e := d.save(j); e != nil {
		return runner.Observation{}, e
	}
	if e := d.disk.Write("work/"+a.AttemptID+"/owner", []byte(a.AttemptID), 128); e != nil {
		return runner.Observation{}, e
	}
	return runner.Observation{State: runner.Running, NativeHandle: a.AttemptID, NextCheck: j.Ready}, nil
}
func (d *Deterministic) Observe(ctx context.Context, id string) (runner.Observation, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	j, e := d.load(id)
	if errors.Is(e, os.ErrNotExist) {
		return runner.Observation{
			State:  runner.NotStarted,
			Reason: "no controlled durable job exists; this executor has no external launch side effect",
		}, nil
	}
	if e != nil {
		return runner.Observation{State: runner.Unknown, Reason: runner.Clip(e.Error())}, nil
	}
	return d.observe(ctx, j)
}
func (d *Deterministic) observe(ctx context.Context, j job) (runner.Observation, error) {
	if e := ctx.Err(); e != nil {
		return runner.Observation{}, e
	}
	id := j.Assignment.AttemptID
	if j.End == nil && time.Now().Before(j.Ready) {
		return runner.Observation{State: runner.Running, NativeHandle: id, NextCheck: j.Ready}, nil
	}
	if j.End == nil {
		p := j.Assignment.Setup.Profile
		result := runner.StepResult{Outcome: "ready", Outputs: map[string]runner.Value{}}
		var data []byte
		var e error
		if p.Mode == "collect" {
			values := map[string]any{}
			for name, v := range j.Assignment.Inputs {
				if v.ArtifactID != "" {
					values[name] = map[string]string{"artifact_id": v.ArtifactID}
				} else {
					var value any
					if err := json.Unmarshal(v.Inline, &value); err != nil {
						return runner.Observation{}, err
					}
					values[name] = value
				}
			}
			data, e = json.Marshal(values)
		} else if p.Input != "" {
			v, ok := j.Assignment.Inputs[p.Input]
			if !ok {
				e = fmt.Errorf("missing profile input %s", p.Input)
			} else if v.ArtifactID != "" {
				data, e = d.readArtifact(ctx, v.ArtifactID)
			} else {
				var s string
				e = json.Unmarshal(v.Inline, &s)
				data = []byte(s)
			}
		} else {
			data = []byte(p.Text)
		}
		if e == nil {
			switch p.Mode {
			case "fail":
				e = errors.New(p.Text)
				if p.Text == "" {
					e = errors.New("controlled execution failure")
				}
			case "needs_help":
				result.Outcome = "needs_help"
				result.Reason = p.Text
				if result.Reason == "" {
					result.Reason = "configured profile requests escalation"
				}
			case "file":
				artifact := id + "/result"
				e = d.disk.Write("artifacts/"+artifact, data, j.Assignment.Limits.MaxArtifact)
				if e == nil {
					result.Outputs[p.Output] = runner.Value{ArtifactID: artifact}
				}
			case "collect":
				result.Outputs[p.Output] = runner.Text(string(data))
				if p.Input != "" {
					result.Outputs["last"] = j.Assignment.Inputs[p.Input]
				}
			case "echo", "log":
				result.Outputs[p.Output] = runner.Text(string(data))
			default:
				e = errors.New("unsupported deterministic operation")
			}
		}
		if e == nil && p.LogBytes > 0 {
			n := min(p.LogBytes, int64(j.Assignment.Limits.MaxLog))
			j.DroppedLogBytes = p.LogBytes - n
			e = d.disk.Write("logs/"+id, []byte(strings.Repeat("x", int(n))), j.Assignment.Limits.MaxLog)
		}
		if e != nil {
			j.End = &runner.ExecutionEnd{Kind: "failed", Reason: runner.Clip(e.Error())}
		} else {
			j.End = &runner.ExecutionEnd{Kind: "returned", Result: &result}
		}
		encoded, _ := json.Marshal(j)
		if len(encoded) > int(d.disk.Policy.Database.MaxRecord) {
			j.End = &runner.ExecutionEnd{Kind: "failed", Reason: "result exceeds bounded executor record allowance"}
		}
		if e = d.save(j); e != nil {
			return runner.Observation{}, e
		}
	}
	return runner.Observation{State: runner.Finished, NativeHandle: id, End: j.End, DroppedLogBytes: j.DroppedLogBytes}, nil
}
func (d *Deterministic) Stop(ctx context.Context, id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if e := ctx.Err(); e != nil {
		return e
	}
	j, e := d.load(id)
	if errors.Is(e, os.ErrNotExist) {
		return nil
	}
	if e != nil {
		return e
	}
	if j.End == nil {
		j.End = &runner.ExecutionEnd{Kind: "stopped", Reason: "explicit stop confirmed by controlled executor"}
		return d.save(j)
	}
	return nil
}
func (d *Deterministic) Release(ctx context.Context, id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if e := ctx.Err(); e != nil {
		return e
	}
	j, e := d.load(id)
	if e != nil && !errors.Is(e, os.ErrNotExist) {
		return e
	}
	if e == nil && j.End == nil {
		return errors.New("cannot release an unsettled execution")
	}
	if e = d.disk.Remove("work/" + id); e != nil {
		return e
	}
	return d.disk.Remove("jobs/" + id + ".json")
}
