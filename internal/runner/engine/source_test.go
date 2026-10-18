package engine

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner/config"
	"github.com/cortexium-io/runner/internal/runner/core"
	"github.com/cortexium-io/runner/internal/runner/execution"
)

type requestAdapter struct{ requests map[int]core.SourceRequest }

func (s requestAdapter) Requests(context.Context) ([]core.SourceRequest, error) {
	out := []core.SourceRequest{}
	for _, r := range s.requests {
		out = append(out, r)
	}
	return out, nil
}
func (s requestAdapter) Request(_ context.Context, key string) (core.SourceRequest, error) {
	number, _ := strconv.Atoi(key)
	r, ok := s.requests[number]
	if !ok {
		return r, errors.New("missing request")
	}
	return r, nil
}

type progressAdapter func(context.Context, string, string) error

func (f progressAdapter) ObserveOverview(context.Context, string) (string, error) {
	return "", errors.New("unexpected observation without an outstanding write")
}

func (f progressAdapter) PublishOverview(ctx context.Context, marker, body string, _ func() error) (bool, error) {
	return false, f(ctx, marker, body) // These cases model a blocked read, not a write.
}

type uncertainOverview struct {
	body, pending string
	writes        int
}

func (p *uncertainOverview) ObserveOverview(context.Context, string) (string, error) {
	return p.body, nil
}

func (p *uncertainOverview) PublishOverview(_ context.Context, _, body string, beforeWrite func() error) (bool, error) {
	if err := beforeWrite(); err != nil {
		return false, err
	}
	p.writes++
	if p.writes == 1 {
		p.pending = body
		return true, errors.New("response lost while the server still owns the write")
	}
	p.body = body
	return false, nil
}

func TestUncertainOverviewMustSettleBeforeNewerProgress(t *testing.T) {
	e := fixture(t, issues)
	p := &uncertainOverview{}
	e.Progress = p
	task, err := e.Add(t.Context(), brief)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.SyncOverview(t.Context()); err == nil {
		t.Fatal("lost response was treated as success")
	}
	reopen(t, e)
	saved, err := e.Store.Projection(t.Context())
	if err != nil || saved.Pending == nil || saved.Pending.Body != p.pending || saved.LastSuccess != nil {
		t.Fatal("restart lost the sole outstanding write", saved, err)
	}
	v, err := e.Show(t.Context(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.Approve(t.Context(), task.ID, 1, v.Preview); err != nil {
		t.Fatal(err)
	}
	if err = e.SyncOverview(t.Context()); err == nil || p.writes != 1 {
		t.Fatal("new progress bypassed an unresolved older write", p.writes, err)
	}
	// The sole outstanding server write finally settles. Only its observed
	// receipt permits publication of the newer, already-approved local state.
	p.body = p.pending
	if err = e.SyncOverview(t.Context()); err != nil || p.writes != 2 || strings.Contains(p.body, "| Needs owner |") {
		t.Fatal("settled write did not release the latest snapshot", p.writes, err)
	}
	saved, err = e.Store.Projection(t.Context())
	if err != nil || saved.Pending != nil || saved.LastSuccess == nil {
		t.Fatal("confirmed projection stayed unresolved", saved, err)
	}
	got := current(t, e, task.ID)
	if got.Approval == nil || got.Stage != core.Ready || got.Usage.Invocations != 0 {
		t.Fatal("view reconciliation changed local authority")
	}
}

func issues(c *config.Config) {
	c.Issues = &config.GitHubIssues{Repository: "owner/repo", OverviewIssue: 7}
	c.Roles["planner"] = "build"
}

func TestIssueImportRequiresOwnerAndRetainsAcceptedSnapshot(t *testing.T) {
	for _, plan := range []bool{false, true} {
		t.Run(map[bool]string{false: "Ready", true: "Plan"}[plan], func(t *testing.T) {
			e := fixture(t, issues)
			r := core.SourceRequest{Open: true, Source: core.Source{Provider: "github-issue", Connection: "owner/repo", ID: "I_request", URL: "https://github.com/owner/repo/issues/3", Title: "Greeting", Body: brief, Revision: "original"}}
			s := requestAdapter{requests: map[int]core.SourceRequest{3: r}}
			e.Sources = s
			task, err := e.ImportIssue(t.Context(), 3, plan, "")
			if err != nil || task.Stage != core.Draft || task.Approval != nil || task.NextRole != "" {
				t.Fatalf("import granted authority: %+v %v", task, err)
			}
			if err = e.Store.SetDrained(t.Context(), false); err != nil {
				t.Fatal(err)
			}
			if _, ok, err := e.claim(t.Context()); err != nil || ok {
				t.Fatal("unapproved issue admitted work", err)
			}
			before := current(t, e, task.ID)
			inbox, err := e.Inbox(t.Context())
			if err != nil || len(inbox) != 1 || inbox[0].TaskID != task.ID || !reflect.DeepEqual(before, current(t, e, task.ID)) {
				t.Fatal("inbox mutated or lost the mapping", err)
			}
			v, err := e.Show(t.Context(), task.ID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = e.Approve(t.Context(), task.ID, 1, v.Preview); err != nil {
				t.Fatal(err)
			}
			accepted := current(t, e, task.ID)
			if accepted.Approval == nil || (plan && accepted.Stage != core.Planning) || (!plan && accepted.Stage != core.Ready) {
				t.Fatal("existing owner gate did not apply", accepted)
			}
			r.Source.Body, r.Source.Revision, r.Open = "edited and no longer a Ready brief", "changed", false
			s.requests[3] = r
			reopen(t, e)
			again, err := e.ImportIssue(t.Context(), 3, !plan, "")
			if err != nil || !reflect.DeepEqual(again, accepted) {
				t.Fatalf("source edit/close/reimport changed accepted work: %+v %v", again, err)
			}
		})
	}
}

func TestSlowOverviewCannotBlockLocalExecutionOrApprovalGate(t *testing.T) {
	e := fixture(t, issues)
	unapproved, err := e.AddPlan(t.Context(), "Do not start this without owner approval")
	if err != nil {
		t.Fatal(err)
	}
	task := approved(t, e)
	started := make(chan struct{})
	e.Progress = progressAdapter(func(ctx context.Context, _, _ string) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	})
	executor := e.Executor
	e.Executor = adapter(func(ctx context.Context, r execution.Request, saved func(core.Process) error) execution.Completion {
		select {
		case <-started:
		case <-ctx.Done():
			return execution.Completion{Stopped: true, Error: ctx.Err().Error()}
		}
		return executor.Execute(ctx, r, saved)
	})
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- e.Run(ctx) }()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("overview did not start")
	}
	if err = e.SyncOverview(ctx); err == nil || !strings.Contains(err.Error(), "already owns") {
		t.Fatal("parallel publication was not excluded", err)
	}
	// Wait for the observable completion receipt, rather than sleeping for a
	// presumed duration. The publisher remains blocked throughout this journey.
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for current(t, e, task.ID).Stage != core.Completed {
		select {
		case <-ticker.C:
		case err = <-done:
			t.Fatal("coordinator stopped before completion", err)
		case <-ctx.Done():
			t.Fatal("slow view blocked local completion")
		}
	}
	if err = e.Store.SetDrained(ctx, true); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("slow view blocked local execution")
	}
	if got := current(t, e, task.ID); got.Stage != core.Completed {
		t.Fatalf("local work failed to complete: %+v", got)
	}
	if got := current(t, e, unapproved.ID); got.Stage != core.Draft || got.Usage.Invocations != 0 {
		t.Fatal("view granted approval to pending work")
	}
	status, err := e.Status(t.Context(), e.Settings.Config)
	if err != nil || status.Issues == nil || status.Issues.Sync.Error == "" {
		t.Fatal("connection failure not visible independently of completed work", err)
	}
}

func TestOverviewCleanupCannotDelayWorkerCancellation(t *testing.T) {
	e := fixture(t, issues)
	approved(t, e)
	workerStarted, workerCancelled := make(chan struct{}), make(chan struct{})
	viewStarted, viewCancelled := make(chan struct{}), make(chan struct{})
	releaseView := make(chan struct{})
	e.Executor = adapter(func(ctx context.Context, _ execution.Request, _ func(core.Process) error) execution.Completion {
		close(workerStarted)
		<-ctx.Done()
		close(workerCancelled)
		return execution.Completion{Stopped: true, Error: ctx.Err().Error()}
	})
	e.Progress = progressAdapter(func(ctx context.Context, _, _ string) error {
		close(viewStarted)
		<-ctx.Done()
		close(viewCancelled)
		<-releaseView
		return ctx.Err()
	})
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- e.Run(ctx) }()
	defer func() {
		close(releaseView)
		cancel()
		wait, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		select {
		case <-done:
		case <-wait.Done():
			t.Error("coordinator did not settle after releasing the view")
		}
	}()
	for _, started := range []chan struct{}{workerStarted, viewStarted} {
		select {
		case <-started:
		case <-ctx.Done():
			t.Fatal("worker/view did not start")
		}
	}
	// A fatal store error takes the cleanup path rather than normal draining.
	if err := e.Store.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-viewCancelled:
	case <-ctx.Done():
		t.Fatal("fatal error did not cancel the view")
	}
	cleanup, stop := context.WithTimeout(ctx, 5*time.Second)
	defer stop()
	select {
	case <-workerCancelled:
	case <-cleanup.Done():
		t.Fatal("worker cancellation waited for unrelated view cleanup")
	}
}

func TestOverviewShowsPublicProgressWithoutPrivateEvidence(t *testing.T) {
	views := []View{}
	for _, id := range []string{"owner", "working", "dependency", "included", "attention", "done", "stopped", "proposal"} {
		task := core.Task{ID: id, Stage: core.Ready, Versions: []core.Version{{Number: 1, Body: "public title\nSECRET_BODY"}}}
		v := View{Task: task, Summary: "/private/SECRET_DETAIL", NextAction: "approve SECRET_PREVIEW", Group: "Waiting"}
		switch id {
		case "owner":
			v.Task.Stage = core.Draft
			v.Task.Source = &core.Source{Title: "@everyone | <script> [link]\\", URL: "https://github.com/owner/repo/issues/3", Body: "SECRET_SOURCE"}
		case "working":
			v.Group, v.Task.Stage = "Working", core.Implementing
		case "dependency":
			v.Eligibility = Eligibility{WaitKind: "dependency", WaitingFor: "SECRET_WAIT"}
			v.Task.Versions[0].Dependencies = []core.Dependency{{TaskID: "working", Version: 1}}
		case "included":
			v.Task.ParentID, v.Task.Inclusion = "parent", &core.PlanInclusion{}
		case "attention":
			v.Task.Attention = &core.Attention{Detail: "SECRET_ERROR"}
		case "done":
			v.Task.Stage = core.Completed
			v.Task.Completion = &core.CompletionReceipt{Kind: "result"}
			v.Publications = []core.Publication{{Observation: &core.RemotePublication{URL: "https://github.com/owner/repo/pull/9"}}}
		case "stopped":
			v.Task.Stage = core.Stopped
		case "proposal":
			v.Task.Stage, v.Task.PlanRequest = core.AwaitingApproval, true
			v.ReleasePreview = "SECRET_RELEASE_PREVIEW"
		}
		views = append(views, v)
	}
	body := overviewRows(views)
	for _, want := range []string{"Needs owner", "Working", "Waiting for prerequisites", "Prerequisites: public title", "Included in Plan; combined verification and delivery pending", "Needs attention", "Verified local result; base unchanged", "| Completed |", "| Stopped |", "explicitly release", "https://github.com/owner/repo/pull/9", "https://github.com/owner/repo/issues/3", "&#64;everyone &#124; &lt;script&gt;", "&#92;](https://github.com/owner/repo/issues/3)"} {
		if !strings.Contains(body, want) {
			t.Error("missing public progress", want)
		}
	}
	if strings.Contains(body, "SECRET") || strings.Contains(body, "@everyone") || strings.Contains(body, "<script>") || strings.Count(body, "| Completed |") != 1 {
		t.Fatal("private data, active content or premature completion in overview", body)
	}
}
