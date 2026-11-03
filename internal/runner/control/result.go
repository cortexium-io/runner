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
	ElapsedSeconds    float64           `json:"elapsed_seconds"`
	ExecutionSettled  bool              `json:"execution_settled"`
	CleanupPending    bool              `json:"cleanup_pending"`
	CleanupError      string            `json:"cleanup_error,omitempty"`
	WorkspaceRetained bool              `json:"workspace_retained"`
	Usage             Usage             `json:"usage"`
	Outputs           map[string]Output `json:"outputs,omitempty"`
	Candidate         *Candidate        `json:"candidate,omitempty"`
	Decision          *runner.Decision  `json:"decision,omitempty"`
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
		if !in.Wait || r.Status == "Waiting" || r.Terminal() && (!out.CleanupPending || out.CleanupError != "") {
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
		Outputs:          map[string]Output{},
		Decision:         runner.Clone(r.Decision),
	}
	end := now
	if r.Terminal() {
		end = r.Created
	}
	for _, a := range r.Attempts {
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
	out.ElapsedSeconds = max(0, end.Sub(r.Created).Seconds())
	out.Usage.KnownTotalTokens = out.Usage.KnownInputTokens + out.Usage.KnownOutputTokens
	if r.Terminal() && r.Source != nil && !r.ResourcesReleased {
		out.CleanupPending = true
	}
	// Contracts contain at most eight outputs. Preview bytes are bounded across
	// all of them, and artifacts remain references rather than loaded evidence.
	names := make([]string, 0, len(r.Outputs))
	for name := range r.Outputs {
		names = append(names, name)
	}
	sort.Strings(names)
	remaining := 4096
	for _, name := range names {
		v := r.Outputs[name]
		value := Output{ArtifactID: v.ArtifactID}
		if len(v.Inline) != 0 {
			var text string
			if json.Unmarshal(v.Inline, &text) != nil {
				text = string(v.Inline)
			}
			limit := min(1024, remaining)
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
		out.Outputs[name] = value
	}
	return out
}
