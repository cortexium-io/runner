package control

import (
	"context"
	"encoding/json"
	"sort"
	"time"
	"unicode/utf8"

	"github.com/cortexium-io/runner/internal/runner"
)

type ResultArgs struct {
	RunID string `json:"run_id"`
	Wait  bool   `json:"wait,omitempty"`
}

// Result is a presentation of saved facts, never another execution record.
// Full setup, inputs and attempt history remain available through inspect.
type Result struct {
	runner.Summary
	ElapsedSeconds    float64             `json:"elapsed_seconds"`
	ExecutionSettled  bool                `json:"execution_settled"`
	CleanupPending    bool                `json:"cleanup_pending"`
	CleanupError      string              `json:"cleanup_error,omitempty"`
	CleanupBlocked    bool                `json:"cleanup_blocked,omitempty"`
	Reconciling       bool                `json:"reconciling,omitempty"`
	WorkspaceRetained bool                `json:"workspace_retained"`
	Usage             Usage               `json:"usage"`
	Outputs           map[string]Output   `json:"outputs,omitempty"`
	Candidate         *Candidate          `json:"candidate,omitempty"`
	Decision          *runner.Decision    `json:"decision,omitempty"`
	DecisionOutputs   map[string]Output   `json:"decision_outputs,omitempty"`
	Tasks             []runner.TaskStatus `json:"tasks,omitempty"`
}

type Usage struct {
	KnownInputTokens       int64 `json:"known_input_tokens"`
	KnownCachedInputTokens int64 `json:"known_cached_input_tokens"`
	KnownOutputTokens      int64 `json:"known_output_tokens"`
	KnownTotalTokens       int64 `json:"known_total_tokens"`
	IncompleteAttempts     int   `json:"incomplete_attempts"`
}

type Output struct {
	ArtifactID string `json:"artifact_id,omitempty"`
	Preview    string `json:"preview,omitempty"`
	Truncated  bool   `json:"truncated,omitempty"`
}

type Candidate struct {
	Base    string `json:"base"`
	PatchID string `json:"patch_id"`
}

func result(ctx context.Context, c *runner.Core, in ResultArgs) (Result, error) {
	for {
		r, err := c.Get(in.RunID)
		if err != nil {
			return Result{}, err
		}
		out := compactResult(r, time.Now())
		// Waiting can still have uncertain execution. Report the actual blocker;
		// never mistake a client disconnect for permission to stop or retry work.
		if !in.Wait || r.Status == "Waiting" && !out.Reconciling || r.Terminal() && (!out.CleanupPending || out.CleanupError != "") {
			return out, nil
		}
		if _, err = c.Wait(ctx, r.ID, r.Revision); err != nil {
			return Result{}, err
		}
	}
}

func compactResult(r runner.Run, now time.Time) Result {
	out := Result{
		Summary:          summary(r),
		ExecutionSettled: true,
		CleanupError:     runner.Clip(r.CleanupError),
		CleanupBlocked:   r.CleanupBlocked,
		Outputs:          map[string]Output{},
		Decision:         runner.Clone(r.Decision),
		Tasks:            r.TaskStatuses,
	}
	if a := r.Current(); r.Status == "Waiting" && r.Decision == nil && a != nil && a.State == runner.Unknown && a.Reconciling && a.Deadline.After(now) {
		out.Reconciling = true
	}
	end := now
	if r.Terminal() {
		end = r.Created
	}
	for _, a := range r.Attempts {
		if r.Decision != nil && a.ID == r.Decision.AttemptID && a.End != nil && a.End.Result != nil {
			// Approval facts belong to the saved decision's exact attempt. A
			// restricted intake needs these before process outputs exist.
			out.DecisionOutputs = previewOutputs(a.End.Result.Outputs, 4096)
		}
		if r.Terminal() && a.FinishedAt.After(end) {
			end = a.FinishedAt
		}
		if a.State != runner.Finished {
			out.ExecutionSettled = false
		} else if !a.Released {
			out.CleanupPending = true
		}
		if a.Source != nil {
			out.WorkspaceRetained = !r.ResourcesReleased
			if a.Source.PatchID != "" {
				out.Candidate = &Candidate{Base: a.Source.Base, PatchID: a.Source.PatchID}
			}
		}
		if a.Usage.InputTokens != nil {
			out.Usage.KnownInputTokens += *a.Usage.InputTokens
		}
		if a.Usage.OutputTokens != nil {
			out.Usage.KnownOutputTokens += *a.Usage.OutputTokens
		}
		if a.Usage.CachedInputTokens != nil {
			out.Usage.KnownCachedInputTokens += *a.Usage.CachedInputTokens
		}
		step := r.Process.Steps[a.Step]
		profile, err := r.Roles[step.Role].Profile(a.Profile)
		if err != nil || profile.Harness == "codex" {
			if a.Usage.InputTokens == nil || a.Usage.OutputTokens == nil || a.Usage.CachedInputTokens == nil {
				out.Usage.IncompleteAttempts++
			}
		}
	}
	if r.Terminal() && len(r.Attempts) == 0 {
		end = r.Updated
	}
	for _, task := range r.TaskStatuses {
		out.Attempts += task.Attempts
		if task.Active {
			out.ExecutionSettled = false
		}
		out.Reconciling = out.Reconciling || task.Reconciling
		out.CleanupPending = out.CleanupPending || task.CleanupPending
		if task.CleanupError != "" && out.CleanupError == "" {
			out.CleanupError = runner.Clip("task " + task.ID + ": " + task.CleanupError)
		}
	}
	if r.Terminal() && len(r.TaskStatuses) > 0 && r.Updated.After(end) {
		end = r.Updated
	}
	for _, usage := range r.TaskUsage {
		if usage.InputTokens != nil {
			out.Usage.KnownInputTokens += *usage.InputTokens
		}
		if usage.OutputTokens != nil {
			out.Usage.KnownOutputTokens += *usage.OutputTokens
		}
		if usage.CachedInputTokens != nil {
			out.Usage.KnownCachedInputTokens += *usage.CachedInputTokens
		}
		if usage.InputTokens == nil || usage.OutputTokens == nil || usage.CachedInputTokens == nil {
			out.Usage.IncompleteAttempts++
		}
	}
	out.ElapsedSeconds = max(0, end.Sub(r.Created).Seconds())
	out.Usage.KnownTotalTokens = out.Usage.KnownInputTokens + out.Usage.KnownOutputTokens
	if r.Terminal() && r.Source != nil && !r.ResourcesReleased {
		out.CleanupPending = true
	}
	out.Outputs = previewOutputs(r.Outputs, 1024)
	return out
}

func previewOutputs(values map[string]runner.Value, perOutput int) map[string]Output {
	// Contracts contain at most eight outputs. Preview bytes are bounded across
	// all of them, and artifacts remain references rather than loaded evidence.
	out := make(map[string]Output, len(values))
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	remaining := 4096
	for _, name := range names {
		v := values[name]
		value := Output{ArtifactID: v.ArtifactID}
		if len(v.Inline) != 0 {
			var text string
			if json.Unmarshal(v.Inline, &text) != nil {
				text = string(v.Inline)
			}
			limit := min(perOutput, remaining)
			if len(text) > limit {
				value.Truncated = true
				for limit > 0 && !utf8.RuneStart(text[limit]) {
					limit--
				}
				text = text[:limit]
			}
			value.Preview = text
			remaining -= len(text)
		}
		out[name] = value
	}
	return out
}
