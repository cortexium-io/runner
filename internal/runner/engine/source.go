package engine

import (
	"context"
	"errors"
	"fmt"
	"html"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/cortexium-io/runner/internal/runner/core"
	"github.com/cortexium-io/runner/internal/runner/execution"
)

// RequestSource observes requests only. Approval remains a local command.
type RequestSource interface {
	Requests(context.Context) ([]core.SourceRequest, error)
	Request(context.Context, string) (core.SourceRequest, error)
}

// ProgressPublisher receives presentation only, never tasks or private evidence.
type ProgressPublisher interface {
	ObserveOverview(context.Context, string) (string, error)
	// BeforeWrite must succeed before dispatch. An uncertain error retains that
	// intent; a definite refusal permits a later fresh attempt.
	PublishOverview(context.Context, string, string, func() error) (uncertain bool, err error)
}

type InboxRequest struct {
	Request core.SourceRequest `json:"request"`
	TaskID  string             `json:"task_id,omitempty"`
}

func (e *Engine) Inbox(ctx context.Context) ([]InboxRequest, error) {
	if e.Settings.Config.Issues == nil || e.Sources == nil {
		return nil, errors.New("GitHub Issues is not configured")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	requests, err := e.Sources.Requests(ctx)
	if err != nil {
		return nil, err
	}
	tasks, err := e.Store.Tasks(ctx)
	if err != nil {
		return nil, err
	}
	known := map[string]string{}
	for _, t := range tasks {
		if t.Source != nil {
			known[t.Source.Key()] = t.ID
		}
	}
	out := make([]InboxRequest, 0, len(requests))
	for _, r := range requests {
		out = append(out, InboxRequest{Request: r, TaskID: known[r.Source.Key()]})
	}
	return out, nil
}

func (e *Engine) ImportIssue(ctx context.Context, number int, plan bool, brief string) (core.Task, error) {
	if e.Settings.Config.Issues == nil || e.Sources == nil {
		return core.Task{}, errors.New("GitHub Issues is not configured")
	}
	if err := e.validate(ctx); err != nil {
		return core.Task{}, err
	}
	if err := e.currentQuality(ctx); err != nil {
		return core.Task{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	r, err := e.Sources.Request(ctx, strconv.Itoa(number))
	if err != nil {
		return core.Task{}, err
	}
	if existing, found, err := e.Store.ImportedTask(ctx, r.Source); err != nil || found {
		return existing, err
	}
	if !r.Open || number == e.Settings.Config.Issues.OverviewIssue {
		return core.Task{}, errors.New("import requires an open request, separate from the overview issue")
	}
	if brief == "" {
		brief = r.Source.Body
		if plan {
			brief = "# " + r.Source.Title + "\n\n" + brief
		}
	}
	var v core.Version
	if plan {
		v, err = core.NewPlanVersion(1, brief, e.Settings.RepositoryID, e.policy(), e.Settings.Quality.Revision())
	} else {
		v, err = core.NewVersion(1, brief, e.Settings.RepositoryID, e.policy(), e.Settings.Quality.Revision())
	}
	if err != nil {
		return core.Task{}, fmt.Errorf("request needs a Ready brief or --plan: %w", err)
	}
	t := core.Task{ID: core.NewID(), Source: &r.Source, PlanRequest: plan, Stage: core.Draft, Versions: []core.Version{v}}
	return e.Store.Import(ctx, t)
}

// SyncOverview serializes manual and coordinator writes before observing the
// latest state. Remote errors are retained on the connection, never on a task.
func (e *Engine) SyncOverview(ctx context.Context) (err error) {
	g := e.Settings.Config.Issues
	if g == nil || e.Progress == nil {
		return errors.New("GitHub Issues is not configured")
	}
	if err := e.Store.AllowMutation(ctx); err != nil {
		return err
	}
	lock, err := execution.Lock(filepath.Join(e.Settings.Config.StateDir, "projection.lock"))
	if err != nil {
		return errors.New("overview synchronization already owns this connection; try again after it settles")
	}
	defer lock.Close()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	previous, err := e.Store.Projection(ctx)
	if err != nil {
		return err
	}
	defer func() {
		previous.LastAttempt = e.now()
		previous.Error = ""
		if err != nil {
			previous.Error = "GitHub overview unavailable; check authentication, network and the configured issue, then github sync"
			if ctx.Err() != nil {
				previous.Error = "Overview synchronization interrupted or timed out; github sync to verify the retained progress"
			}
			if previous.Pending != nil {
				previous.Error = "Earlier overview update is unconfirmed; github sync observes it before any newer progress can publish. Local work continues."
			}
		}
		// Retain failures even when the bounded network context expired.
		recordCtx, recordCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer recordCancel()
		if recordErr := e.Store.RecordProjection(recordCtx, previous); recordErr != nil {
			err = recordErr
		}
	}()
	marker := "<!-- runner-overview:" + core.Digest([]string{e.Settings.RepositoryID, e.Settings.Config.StateDir, g.Repository, strconv.Itoa(g.OverviewIssue)}) + " -->"
	if pending := previous.Pending; pending != nil {
		body, readErr := e.Progress.ObserveOverview(ctx, marker)
		if readErr != nil {
			return readErr
		}
		if body != pending.Body {
			return errors.New("earlier overview update is unconfirmed; observe its receipt before publishing newer progress")
		}
		verified := e.now()
		previous.LastSuccess = &verified
		previous.Revision, previous.SnapshotAt = pending.Revision, pending.SnapshotAt
		previous.Pending = nil
	}
	status, err := e.Status(ctx, e.Settings.Config)
	if err != nil {
		return err
	}
	rows := overviewRows(status.Tasks)
	revision := core.Digest(rows)
	snapshotAt := previous.SnapshotAt
	if previous.Revision != revision || snapshotAt.IsZero() {
		snapshotAt = status.ObservedAt
	}
	body := fmt.Sprintf("%s\n\n# Runner work overview\n\nSnapshot updated: %s. This is an optional view of local Runner state.\nNew issues are requests; only explicit local owner approval authorizes work.\n\n| Work | Status | Reason / next step | Plan | Delivery |\n| --- | --- | --- | --- | --- |\n%s\n", marker, snapshotAt.Format(time.RFC3339), rows)
	uncertain, err := e.Progress.PublishOverview(ctx, marker, body, func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		previous.Pending = &core.ProjectionWrite{Body: body, Revision: revision, SnapshotAt: snapshotAt}
		previous.LastAttempt = e.now()
		return e.Store.RecordProjection(ctx, previous)
	})
	if err == nil || !uncertain {
		previous.Pending = nil
	}
	if err == nil {
		verified := e.now()
		previous.LastSuccess = &verified
		previous.Revision, previous.SnapshotAt = revision, snapshotAt
	}
	return err
}

func overviewRows(views []View) string {
	titles := map[string]string{}
	for _, v := range views {
		title := publicText(TaskTitle(v.Task))
		if s := v.Task.Source; s != nil {
			title = publicText(s.Title)
			if publicGitHubURL.MatchString(s.URL) {
				title = "[" + title + "](" + s.URL + ")"
			}
		}
		titles[v.Task.ID] = title
	}
	var out strings.Builder
	for _, v := range views {
		t := v.Task
		title := titles[t.ID]
		label, reason := publicProgress(v)
		if label == "Waiting" && v.Eligibility.WaitKind == "dependency" {
			prerequisites := []string{}
			for _, d := range t.Current().Dependencies {
				if title, ok := titles[d.TaskID]; ok {
					prerequisites = append(prerequisites, title)
				}
			}
			if len(prerequisites) != 0 {
				reason += "<br>Prerequisites: " + strings.Join(prerequisites, ", ")
			}
		}
		plan := "—"
		if t.ParentID != "" {
			plan = "`" + publicText(t.ParentID) + "`"
			if title, ok := titles[t.ParentID]; ok {
				plan = title + "<br>" + plan
			}
		} else if t.PlanRequest {
			plan = "Plan"
		}
		delivery := "—"
		for _, p := range v.Publications {
			if p.Observation != nil && publicGitHubURL.MatchString(p.Observation.URL) {
				delivery = "[PR](" + p.Observation.URL + ")"
			}
		}
		fmt.Fprintf(&out, "| %s<br><code>%s</code> v%d | %s | %s | %s | %s |\n", title, publicText(t.ID), t.Current().Number, label, reason, plan, delivery)
	}
	if len(views) == 0 {
		out.WriteString("| No imported or local tasks yet | Waiting | Import a request, then inspect and approve its local version | — | — |\n")
	}
	return out.String()
}

// Deliberately do not publish Summary, NextAction or Attention.Detail: they may
// contain paths, private check output, source text or approval previews.
func publicProgress(v View) (string, string) {
	t := v.Task
	switch {
	case t.Stage == core.Completed:
		if t.Integration != nil {
			return "Completed", "Confirmed GitHub merge"
		}
		if t.Completion != nil && t.Completion.Kind == "result" {
			return "Completed", "Verified local result; base unchanged"
		}
		return "Completed", "Verified delivery confirmed"
	case t.Stage == core.Stopped:
		return "Stopped", "Work and evidence retained; not completed"
	case t.Stage == core.Draft:
		return "Needs owner", "Inspect and approve the captured local request"
	case t.Stage == core.AwaitingApproval && t.Attention == nil:
		if v.ReleasePreview == "" {
			return "Needs attention", "Approved base changed; owner must reconsider the Plan"
		}
		return "Needs owner", "Inspect the proposed Plan and explicitly release it"
	case t.Attention != nil:
		reason := "Owner action required; inspect the local task diagnostics"
		switch t.Attention.Kind {
		case core.Decision:
			reason = "Owner decision required before work can continue"
		case core.ProofGap:
			reason = "Required proof needs attention; inspect the local checks"
		case core.Interrupted:
			reason = "Execution interrupted; owner must choose recovery"
		case core.Integrity:
			reason = "Candidate or evidence integrity needs attention"
		case core.UnsupportedConflict:
			reason = "Unsupported conflict requires owner action"
		}
		return "Needs attention", reason
	case t.Inclusion != nil:
		return "Waiting", "Included in Plan; combined verification and delivery pending"
	case v.Group == "Working":
		return "Working", publicText(string(t.Stage))
	}
	switch v.Eligibility.WaitKind {
	case "dependency", "plan":
		return "Waiting", "Waiting for prerequisites or combined Plan work"
	case "capacity":
		return "Waiting", "Waiting for configured capacity"
	case "drained":
		return "Waiting", "Runner is drained; admission paused"
	case "backoff":
		return "Waiting", "Bounded retry is waiting"
	case "repair_limit", "retry_limit", "invocation_limit", "configuration", "ownership":
		return "Needs attention", "Inspect local configuration, allowances or ownership"
	}
	if v.Group == "Needs you" {
		return "Needs attention", "Owner action required; inspect local status"
	}
	return "Waiting", publicText(string(t.Stage))
}

var publicGitHubURL = regexp.MustCompile(`^https://github\.com/[A-Za-z0-9][A-Za-z0-9-]*/[A-Za-z0-9][A-Za-z0-9_.-]*/(issues|pull)/[1-9][0-9]*$`)

func publicText(s string) string {
	runes := []rune(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return ' '
		}
		return r
	}, s))
	if len(runes) > 160 {
		runes = append(runes[:157], '.', '.', '.')
	}
	s = html.EscapeString(string(runes))
	return strings.NewReplacer("\\", "&#92;", "@", "&#64;", "|", "&#124;", "[", "&#91;", "]", "&#93;", "`", "&#96;", "*", "&#42;", "_", "&#95;").Replace(s)
}
