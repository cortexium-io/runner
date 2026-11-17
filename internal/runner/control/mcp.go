// Package control translates MCP and CLI requests into the same core operations.
package control

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type ConfigureFunc func(file, field string, value json.RawMessage) error

// RecoverBrowserErrorsArgs keeps the full report inside the existing 64KiB
// authenticated envelope. The Core receives only the exact decompressed bytes.
type RecoverBrowserErrorsArgs struct {
	RequestID       string `json:"request_id"`
	RunID           string `json:"run_id"`
	AttemptID       string `json:"attempt_id"`
	Revision        int64  `json:"revision"`
	PublicationHead string `json:"publication_head"`
	ReportGZIP      string `json:"report_gzip"`
}

func (in RecoverBrowserErrorsArgs) request() (runner.RecoverBrowserErrorsRequest, error) {
	var out runner.RecoverBrowserErrorsRequest
	const compressedLimit = 32 << 10
	if len(in.ReportGZIP) == 0 || len(in.ReportGZIP) > base64.StdEncoding.EncodedLen(compressedLimit) {
		return out, errors.New("browser-error gzip report exceeds its base64 envelope")
	}
	compressed, err := base64.StdEncoding.DecodeString(in.ReportGZIP)
	if err != nil || len(compressed) == 0 || len(compressed) > compressedLimit {
		return out, errors.New("browser-error gzip report must contain 1..32768 bytes")
	}
	input := bytes.NewReader(compressed)
	reader, err := gzip.NewReader(input)
	if err != nil {
		return out, errors.New("browser-error gzip report is invalid")
	}
	reader.Multistream(false)
	data, readErr := io.ReadAll(io.LimitReader(reader, (128<<10)+1))
	closeErr := reader.Close()
	if readErr != nil || closeErr != nil || len(data) == 0 || len(data) > 128<<10 || input.Len() != 0 {
		return out, errors.New("browser-error gzip report is invalid, trailing, empty or exceeds 128KiB")
	}
	return runner.RecoverBrowserErrorsRequest{
		RequestID:       in.RequestID,
		RunID:           in.RunID,
		AttemptID:       in.AttemptID,
		Revision:        in.Revision,
		PublicationHead: in.PublicationHead,
		Report:          data,
	}, nil
}

type StartArgs struct {
	Followup  *runner.TaskFollowupRequest `json:"followup,omitempty"`
	SourceRef string                      `json:"source_ref,omitempty"`
	RequestID string                      `json:"request_id"`
	Project   string                      `json:"project"`
	Process   string                      `json:"process,omitempty"`
	Inputs    map[string]any              `json:"inputs,omitempty"`
	Artifacts map[string]string           `json:"artifacts,omitempty"`
}

type ContinueTaskArgs struct {
	RequestID       string            `json:"request_id"`
	RunID           string            `json:"run_id"`
	Revision        int64             `json:"revision"`
	Project         string            `json:"project"`
	Process         string            `json:"process"`
	SourceAttemptID string            `json:"source_attempt_id"`
	PatchSHA256     string            `json:"patch_sha256"`
	CheckAttemptID  string            `json:"check_attempt_id"`
	CheckSHA256     string            `json:"check_sha256"`
	Inputs          map[string]any    `json:"inputs,omitempty"`
	Artifacts       map[string]string `json:"artifacts,omitempty"`
}

func (in ContinueTaskArgs) Request() (runner.ContinueTaskRequest, error) {
	values, err := inputValues(in.Inputs, in.Artifacts)
	return runner.ContinueTaskRequest{
		RequestID: in.RequestID, RunID: in.RunID, Revision: in.Revision,
		Project: in.Project, Process: in.Process, SourceAttemptID: in.SourceAttemptID,
		PatchSHA256: in.PatchSHA256, CheckAttemptID: in.CheckAttemptID, CheckSHA256: in.CheckSHA256,
		Inputs: values,
	}, err
}

type RunArgs struct {
	RunID string `json:"run_id"`
}
type WaitArgs struct {
	RunID          string `json:"run_id"`
	After          int64  `json:"after"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
}
type SetArgs struct {
	File  string `json:"file"`
	Field string `json:"field"`
	Value any    `json:"value"`
}

func add[In any](s *mcp.Server, name, description string, f func(context.Context, In) (any, error)) {
	mcp.AddTool[In, any](s, &mcp.Tool{Name: name, Description: description}, func(ctx context.Context, _ *mcp.CallToolRequest, in In) (*mcp.CallToolResult, any, error) {
		out, e := f(ctx, in)
		if e != nil {
			return nil, nil, e
		}
		data, e := json.Marshal(out)
		if e != nil {
			return nil, nil, e
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(data)}}}, out, nil
	})
}
func Handler(c *runner.Core, token string, set ConfigureFunc) http.Handler {
	s := mcp.NewServer(&mcp.Implementation{Name: "cortexium-runner", Version: "0.1.0-core"}, nil)
	add(s, "runner_continue_task", "Owner-only audited continuation of exact settled retained source and serial full-check evidence into a configured general-task process. Atomically freezes the old family and carries every consumed attempt into the unchanged original ceiling. Requires observed revision, exact source/check attempts and digests, and declared patch/checks artifact inputs. Transfers no release or acceptance approval. Restricted intake must not invoke this action.", func(ctx context.Context, in ContinueTaskArgs) (any, error) {
		req, err := in.Request()
		if err != nil {
			return nil, err
		}
		r, err := c.ContinueTask(req)
		return compactResult(r, time.Now()), err
	})
	add(s, "runner_start", "Start an explicitly selected project process. An optional followup binds the exact completed parent run/revision while preserving its result. Reuse request_id after an uncertain response.", func(ctx context.Context, in StartArgs) (any, error) {
		r, e := start(c, in)
		if e != nil {
			return nil, e
		}
		return summary(r), nil
	})
	add(s, "runner_run", "Start or rejoin a selected process, optionally linked to an exact completed followup parent, following configured steps, corrections and bounded self-reconciliation until completion or an actionable wait. Reuse request_id after disconnect; disconnect does not cancel work or grant additional allowance.", func(ctx context.Context, in StartArgs) (any, error) {
		r, err := start(c, in)
		if err != nil {
			return nil, err
		}
		return result(ctx, c, ResultArgs{RunID: r.ID, Wait: true})
	})
	add(s, "runner_result", "Read compact status, bounded outputs, elapsed time, known token usage and cleanup state. Set wait to follow completion and bounded self-reconciliation until a decision or unresolved blocker; unknown usage remains explicit.", func(ctx context.Context, in ResultArgs) (any, error) {
		return result(ctx, c, in)
	})
	add(s, "runner_status", "Compact current status, storage accounting and configuration errors; at most 100 run summaries.", func(context.Context, struct{}) (any, error) { return c.Status(), nil })
	add(s, "runner_inspect", "Inspect one bounded run record, including selected setup, attempts and result references.", func(ctx context.Context, in RunArgs) (any, error) { return c.Get(in.RunID) })
	add(s, "runner_cancel", "Request cancellation. Terminal cancellation requires observed execution settlement.", func(ctx context.Context, in RunArgs) (any, error) { r, e := c.Cancel(in.RunID); return summary(r), e })
	add(s, "runner_retry", "Retry settled failed work, retaining consumed attempts. For general tasks, root extra_attempts grants aggregate goal capacity with an immutable owner audit, optional reason and atomic replacement directive. An exact presented revision also permits explicit role_instructions and max_tasks amendments: preserve the prior definition as canonical proof, change only future native guidance and the child-task ceiling, and retain models, permissions, contracts and all historical children. Individual visit limits and continuation history remain unchanged. With within_budget, positive extra_attempts grants up to four current-visit retries from the unchanged aggregate budget, preserving the completion reserve. cleanup_only revalidates retained terminal cleanup without executing work or changing allowances and cannot combine with within_budget.", func(ctx context.Context, in runner.RetryRequest) (any, error) {
		r, e := c.Retry(in)
		return summary(r), e
	})
	add(s, "runner_intervene", "Owner intervention on a general role task or subtree. Pause/resume dispatch, steer future work, answer a saved human question, or reassign a settled future visit to a configured role profile. Requires exact observed revision and idempotent request_id; preserves attempts, usage and unaffected work. Active effects must settle before incompatible replacement.", func(ctx context.Context, in runner.TaskInterventionRequest) (any, error) {
		r, err := c.InterveneTask(in)
		return summary(r), err
	})
	add(s, "runner_amend_roles", "Owner-only instruction amendment on an exact paused, settled general root. Preserves the prior definition as authenticated canonical proof and changes only future native instruction text. Retains historical children/attempts, source, evidence, models, profiles, permissions, spending, total-work policy and local retries. Root source resources remain retained. Grants no allowance or approval and leaves dispatch paused until normal explicit resume. Restricted intake must not invoke this action.", func(ctx context.Context, in runner.TaskRoleAmendmentRequest) (any, error) {
		r, err := c.AmendTaskRoles(in)
		return summary(r), err
	})
	add(s, "runner_recover_tasks", "Owner-only same-generation recovery after a reported runtime maintenance repair. Requires an exact failed owning goal revision and current failed child task IDs, with the whole family settled and released. Atomically reopens those failed visits and only their never-executed dependency-failed closure, then resumes ordinary parent evaluation. Preserves accepted work, all history, task IDs/specs, roles, profiles, revision policy and aggregate attempt ceiling. Grants no allowance or approval. Restricted intake must not invoke this action.", func(ctx context.Context, in runner.RecoverTasksRequest) (any, error) {
		r, err := c.RecoverTasks(in)
		return compactResult(r, time.Now()), err
	})
	add(s, "runner_recover_corrections", "Owner-authorized recovery of an exhausted saved source rejection. Legacy max_uses=3 covers eligible backedges. current_route requires exact revision, attempt, patch/check SHA256 and max_uses=current spent+1; it grants only that one current route, including with aggregate limits disabled. Optional bounded supplemental JSON is retained once with explicit future source/review role recipients, preserving original request bytes. within_budget instead uses existing enforced aggregate headroom with max_uses=0. Modes cannot combine. Frozen definitions, spending, other limits and protected approvals are unchanged. Routes the saved result without repeating its check.", func(ctx context.Context, in runner.RecoverCorrectionsRequest) (any, error) {
		r, e := c.RecoverCorrections(in)
		return summary(r), e
	})
	add(s, "runner_recover_collection", "Owner-authorized one-time repair of an exact saved measurement console overflow. Rejoins the existing source owner with an explicitly incomplete diagnostic; preserves the failed attempt, accepted work, frozen process and all limits. Grants no retry allowance or release approval. Restricted intake must not invoke this action.", func(ctx context.Context, in runner.RecoverCollectionRequest) (any, error) {
		r, e := c.RecoverCollection(in)
		return summary(r), e
	})
	add(s, "runner_preflight_unpublished_card_recovery", "Read-only owner preflight for an exact settled unpublished source-card deadline failure. Authenticates the original capture, dependency manifest, every completed pin, scopes and remaining completion reserve. No checkout restoration, grant or dispatch.", func(ctx context.Context, in runner.RecoverUnpublishedCardRequest) (any, error) {
		return c.CheckUnpublishedCardRecovery(in)
	})
	add(s, "runner_recover_unpublished_card", "Owner-authorized one-use recovery of the exact preflight-bound settled deadline-failed source card. Atomically preserves failed history and completed pins and queues one original capped owner visit, followed by normal pin/join/check/review/publication and all frozen gates. No policy migration, extra attempts, replacement run or late-source acceptance. Restricted intake must not invoke this action.", func(ctx context.Context, in runner.RecoverUnpublishedCardRequest) (any, error) {
		r, e := c.RecoverUnpublishedCard(in)
		return summary(r), e
	})
	add(s, "runner_preflight_source_handoff", "Read-only owner preflight for a separately adjudicated blocked source handoff with four accepted serial predecessors and one unstarted final reservation. Binds the exact reason, source, records and completion reserve; grants no acceptance or dispatch.", func(ctx context.Context, in runner.AdjudicateSourceHandoffRequest) (any, error) {
		return c.CheckSourceHandoff(in)
	})
	add(s, "runner_adjudicate_source_handoff", "Separately owner-authorized one-use source handoff. Preserves the blocked completion and earlier recovery audit, queues only normal pinning and restores the original final reservation. All checks/review/qualification remain required; no new edit owner, budget, permissions or release authority. Restricted intake must not invoke this action.", func(ctx context.Context, in runner.AdjudicateSourceHandoffRequest) (any, error) {
		r, e := c.AdjudicateSourceHandoff(in)
		return summary(r), e
	})
	add(s, "runner_recover_planning", "Owner-authorized one-time recovery of an exact blocked planner after preflight rejected its unadmitted collection. Reuses the retained baseline and adopted source, preserving history, attempt ceilings and protected decisions. Restricted intake must not invoke this action.", func(ctx context.Context, in runner.RecoverPlanningRequest) (any, error) {
		r, e := c.RecoverPlanning(in)
		if e != nil {
			return nil, e
		}
		return summary(r), nil
	})
	add(s, "runner_recover_frontend_check", "Owner-authorized recheck of an exact retained patch from a settled frontend run. Serial mode reopens the exhausted catalogue or unpublished planned frontend check with one Vitest worker. Planned recovery uses the observed run-and-card revision and no publication_head; retained recovery binds its draft head. An explicitly authorized assertion_sha256 instead binds the failed full-check report for one assertion-only correction after diagnostic repair. Preserves the attempt budget and protected decisions. Restricted intake must not invoke this action.", func(ctx context.Context, in runner.RecoverFrontendCheckRequest) (any, error) {
		r, e := c.RecoverFrontendCheck(in)
		return summary(r), e
	})
	add(s, "runner_recover_frontend_timeouts", "Owner-authorized diagnosis after a settled blocked owner or exhausted frontend check. Preserves assertion failures while sampling confirmed timeouts at the same test limit. Complete evidence can authorize one repair within the frozen cap. Restricted intake must not invoke this action.", func(ctx context.Context, in runner.RecoverFrontendTimeoutsRequest) (any, error) {
		r, e := c.RecoverFrontendTimeouts(in)
		return summary(r), e
	})
	add(s, "runner_recover_browser_errors", "Owner-authorized authenticated saved browser page-error handoff for the exact run, attempt, revision and publication. report_gzip is one base64-encoded gzip member, at most 32KiB compressed and 128KiB decompressed. Preserves the historical receipt without dispatching work or granting attempts. Restricted intake must not invoke this action.", func(ctx context.Context, in RecoverBrowserErrorsArgs) (any, error) {
		request, e := in.request()
		if e != nil {
			return nil, e
		}
		r, e := c.RecoverBrowserErrors(request)
		return summary(r), e
	})
	add(s, "runner_decide", "Approve or reject one saved decision using its exact decision_id. Does not grant extra attempts or approve a different candidate.", func(ctx context.Context, in runner.DecisionRequest) (any, error) {
		r, e := c.Decide(in)
		return summary(r), e
	})
	add(s, "runner_wait", "Wait in code for a run revision/completion, without a model status-polling loop. A timeout is not run failure.", func(ctx context.Context, in WaitArgs) (any, error) {
		n := in.TimeoutSeconds
		if n <= 0 {
			n = 30
		}
		if n > 30 {
			n = 30
		}
		ctx, cancel := context.WithTimeout(ctx, time.Duration(n)*time.Second)
		defer cancel()
		r, e := c.Wait(ctx, in.RunID, in.After)
		if errors.Is(e, context.DeadlineExceeded) {
			run, err := c.Get(in.RunID)
			return summary(run), err
		}
		return r, e
	})
	add(s, "runner_configure", "Edit one selected configuration file field and validate the complete configuration before applying it.", func(ctx context.Context, in SetArgs) (any, error) {
		if set == nil {
			return nil, errors.New("configuration editing is not enabled")
		}
		b, e := json.Marshal(in.Value)
		if e == nil {
			e = set(in.File, in.Field, b)
		}
		if e != nil {
			return nil, e
		}
		return map[string]string{
			"file":      in.File,
			"field":     in.Field,
			"effective": "future run definitions; global concurrency affects admission immediately",
		}, nil
	})
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, &mcp.StreamableHTTPOptions{Stateless: true})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, e := net.SplitHostPort(r.Host)
		if e != nil || !(host == "localhost" || net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback()) {
			http.Error(w, "loopback Host required", http.StatusForbidden)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			u, e := url.Parse(origin)
			if e != nil || u.Scheme != "http" || u.Host != r.Host {
				http.Error(w, "invalid Origin", http.StatusForbidden)
				return
			}
		}
		authorization := r.Header.Get("Authorization")
		got := strings.TrimPrefix(authorization, "Bearer ")
		if token == "" || !strings.HasPrefix(authorization, "Bearer ") || subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
			http.Error(w, "owner token required", http.StatusUnauthorized)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		h.ServeHTTP(w, r)
	})
}
func start(c *runner.Core, in StartArgs) (runner.Run, error) {
	values, err := inputValues(in.Inputs, in.Artifacts)
	if err != nil {
		return runner.Run{}, err
	}
	return c.Start(runner.StartRequest{
		Followup:  in.Followup,
		RequestID: in.RequestID,
		Project:   in.Project,
		Process:   in.Process,
		SourceRef: in.SourceRef,
		Inputs:    values,
	})
}

func inputValues(inputs map[string]any, artifacts map[string]string) (map[string]runner.Value, error) {
	values := map[string]runner.Value{}
	for k, v := range inputs {
		b, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		values[k] = runner.Value{Inline: b}
	}
	for k, id := range artifacts {
		if _, ok := values[k]; ok {
			return nil, fmt.Errorf("input %s is both inline and artifact", k)
		}
		values[k] = runner.Value{ArtifactID: id}
	}
	return values, nil
}
func summary(r runner.Run) runner.Summary {
	return runner.Summarize(r, time.Now())
}

type authorizedTransport struct{ token string }

func (t authorizedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r := req.Clone(req.Context())
	r.Header = req.Header.Clone()
	r.Header.Set("Authorization", "Bearer "+t.token)
	return http.DefaultTransport.RoundTrip(r)
}
func Connect(ctx context.Context, endpoint, token string) (*mcp.ClientSession, error) {
	u, e := url.Parse(endpoint)
	if e != nil || u.Scheme != "http" || u.User != nil {
		return nil, errors.New("this slice requires a loopback HTTP endpoint")
	}
	host := u.Hostname()
	if host != "localhost" && (net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback()) {
		return nil, errors.New("remote control is not qualified in this slice")
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "runner-cli", Version: "0.1.0-core"}, nil)
	httpClient := &http.Client{
		Transport:     authorizedTransport{token},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	// A configured run record may be up to 1MiB; leave framing headroom while
	// keeping the client response bound independent of observation count.
	return client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:             endpoint,
		HTTPClient:           httpClient,
		MaxRetries:           -1,
		DisableStandaloneSSE: true,
		MaxEventSize:         2 << 20,
	}, nil)
}
func Call(ctx context.Context, session *mcp.ClientSession, name string, args any) (json.RawMessage, error) {
	r, e := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if e != nil {
		return nil, e
	}
	if r.IsError {
		for _, content := range r.Content {
			if text, ok := content.(*mcp.TextContent); ok && text.Text != "" {
				return nil, fmt.Errorf("%s: %s", name, text.Text)
			}
		}
		return nil, fmt.Errorf("%s failed without a diagnostic", name)
	}
	b, e := json.Marshal(r.StructuredContent)
	return b, e
}
