package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
)

type nativeJob struct {
	Workspace     *workspaceRecord       `json:"workspace,omitempty"`
	Assignment    runner.Assignment      `json:"assignment"`
	Stage         string                 `json:"stage"`
	ThreadID      string                 `json:"thread_id,omitempty"`
	TurnID        string                 `json:"turn_id,omitempty"`
	Generation    string                 `json:"generation,omitempty"`
	End           *runner.ExecutionEnd   `json:"end,omitempty"`
	Usage         runner.Usage           `json:"usage"`
	Effective     *runner.ExecutionFacts `json:"effective,omitempty"`
	Reason        string                 `json:"reason,omitempty"`
	PeakBytes     int64                  `json:"native_store_peak_bytes,omitempty"`
	StopRequested bool                   `json:"stop_requested,omitempty"`
	StopReason    string                 `json:"stop_reason,omitempty"`
}
type nativeTurn struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Items  []struct {
		Type  string `json:"type"`
		Text  string `json:"text"`
		Phase string `json:"phase"`
	} `json:"items"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}
type nativeThread struct {
	ID           string          `json:"id"`
	HistoryMode  string          `json:"historyMode"`
	Environments json.RawMessage `json:"environments"`
	Turns        []nativeTurn    `json:"turns"`
}

type nativeCommand struct {
	peer codexPeer
	id   string
}

// Codex owns native correlation and collection. It makes no process/retry/domain
// decisions for the core. Access profiles distinguish input-only work from the
// bounded local Go workspace/check capability. External tools remain disabled.
type Codex struct {
	commands    map[string]nativeCommand
	workspaces  *Workspaces
	disk        *runner.Disk
	cfg         *runner.CodexSettings
	mu          sync.Mutex
	jobs        map[string]nativeJob
	preparing   map[string]bool
	probing     map[string]bool
	wake        func(string)
	changed     chan struct{}
	closing     bool
	corruptJobs bool
	ctx         context.Context
	cancel      context.CancelFunc
	wg          sync.WaitGroup
	events      chan rpcMessage
	overflow    chan struct{}
	connMu      sync.Mutex
	peer        codexPeer
	proc        *nativeProcess
	generation  string
	tail        *diagnosticTail
	connect     func(context.Context, string, func(rpcMessage)) (codexPeer, error)
	subscribed  map[string]codexPeer
}

func NewCodex(d *runner.Disk, cfg *runner.CodexSettings) *Codex {
	ctx, cancel := context.WithCancel(context.Background())
	c := &Codex{
		commands:   map[string]nativeCommand{},
		disk:       d,
		cfg:        runner.Clone(cfg),
		jobs:       map[string]nativeJob{},
		preparing:  map[string]bool{},
		probing:    map[string]bool{},
		changed:    make(chan struct{}),
		ctx:        ctx,
		cancel:     cancel,
		events:     make(chan rpcMessage, 64),
		overflow:   make(chan struct{}, 1),
		tail:       &diagnosticTail{limit: int(d.Policy.Logs.MaxPerAttempt)},
		connect:    dialCodex,
		subscribed: map[string]codexPeer{},
	}
	if entries, err := os.ReadDir(filepath.Join(d.Root, "jobs")); err == nil {
		for _, entry := range entries {
			if id, ok := strings.CutSuffix(entry.Name(), ".codex.json"); ok {
				if _, e := c.loadLocked(id); e != nil {
					c.corruptJobs = true
				}
			}
		}
	} else {
		c.corruptJobs = true
	}
	c.wg.Add(1)
	go c.listen()
	return c
}
func (c *Codex) SetWake(wake func(string)) { c.mu.Lock(); c.wake = wake; c.mu.Unlock() }
func (c *Codex) signal() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.wake != nil {
		for id, j := range c.jobs {
			if j.End == nil {
				c.wake(id)
			}
		}
	}
}
func nativeName(id string) string { return "jobs/" + id + ".codex.json" }
func (c *Codex) loadLocked(id string) (nativeJob, error) {
	if j, ok := c.jobs[id]; ok {
		return runner.Clone(j), nil
	}
	b, err := c.disk.Read(nativeName(id), c.disk.Policy.Database.MaxRecord)
	if err != nil {
		return nativeJob{}, err
	}
	var j nativeJob
	if err = json.Unmarshal(b, &j); err != nil {
		return j, err
	}
	if j.Assignment.AttemptID != id {
		return j, errors.New("native job identity does not match the saved attempt")
	}
	c.jobs[id] = j
	return runner.Clone(j), nil
}
func (c *Codex) saveLocked(j nativeJob) error {
	if old, ok := c.jobs[j.Assignment.AttemptID]; ok && reflect.DeepEqual(old, j) {
		return nil
	}
	b, err := json.Marshal(j)
	if err != nil {
		return err
	}
	if err = c.disk.Write(nativeName(j.Assignment.AttemptID), b, c.disk.Policy.Database.MaxRecord); err != nil {
		return err
	}
	c.jobs[j.Assignment.AttemptID] = j
	close(c.changed)
	c.changed = make(chan struct{})
	if c.wake != nil {
		c.wake(j.Assignment.AttemptID)
	}
	return nil
}
func (c *Codex) mutate(id string, f func(*nativeJob)) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	j, err := c.loadLocked(id)
	if err != nil {
		return err
	}
	f(&j)
	return c.saveLocked(j)
}
func (c *Codex) Start(ctx context.Context, a runner.Assignment) (runner.Observation, error) {
	if err := ctx.Err(); err != nil {
		return runner.Observation{}, err
	}
	if a.Setup.Profile.Harness == "go-check" && (a.Setup.Profile.Access != "workspace-read" || a.OutputTypes["check"] != "text" || len(a.OutputTypes) != 1 || a.Inputs["patch"].ArtifactID == "" || len(a.Inputs) != 1) {
		return runner.Observation{
			State: runner.Finished,
			End:   &runner.ExecutionEnd{Kind: "failed", Reason: "readiness: invalid fixed Go check assignment"},
		}, nil
	}
	if _, _, err := nativePayload(a); a.Setup.Profile.Harness != "go-check" && err != nil {
		return runner.Observation{
			State: runner.Finished,
			End:   &runner.ExecutionEnd{Kind: "failed", Reason: "readiness: " + runner.Clip(err.Error())},
		}, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closing {
		return runner.Observation{}, errors.New("native executor is shutting down")
	}
	if j, err := c.loadLocked(a.AttemptID); err == nil {
		return observation(j), nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return runner.Observation{}, err
	}
	j := nativeJob{Assignment: runner.Clone(a), Stage: "allocated"}
	b, _ := json.Marshal(j)
	if len(b)+int(a.Setup.Profile.MaxOutput)+2048 > int(a.Limits.MaxRecord) {
		return runner.Observation{
			State: runner.Finished,
			End:   &runner.ExecutionEnd{Kind: "failed", Reason: "readiness: assignment and result do not fit the native record allowance"},
		}, nil
	}
	if err := c.saveLocked(j); err != nil {
		return runner.Observation{}, err
	}
	c.preparing[a.AttemptID] = true
	c.wg.Add(1)
	go c.launch(a.AttemptID)
	return observation(j), nil
}
func observation(j nativeJob) runner.Observation {
	o := runner.Observation{
		State:           runner.Running,
		Reason:          "native " + j.Stage,
		Usage:           j.Usage,
		NativeHandle:    j.ThreadID,
		NativePeakBytes: j.PeakBytes,
		Effective:       j.Effective,
	}
	if j.Workspace != nil {
		o.Source = &runner.SourceResult{
			Repository:   j.Workspace.Repository,
			Base:         j.Workspace.Base,
			WorkDir:      j.Workspace.WorkDir,
			AttemptID:    j.Assignment.AttemptID,
			InputPatchID: j.Workspace.SeedPatchID,
		}
		if j.Workspace.Candidate != nil {
			o.Source.InputPatchID = j.Workspace.Candidate.PatchID
		}
	}
	if j.Assignment.Setup.Profile.Harness == "go-check" {
		o.NativeHandle = "command/" + j.Assignment.AttemptID
	}
	if j.TurnID != "" {
		o.NativeHandle += "/" + j.TurnID
	}
	if j.End != nil {
		o.State = runner.Finished
		o.End = j.End
	} else if j.Reason != "" {
		o.State = runner.Unknown
		o.Reason = j.Reason
	}
	return o
}
func (c *Codex) launch(id string) {
	defer c.wg.Done()
	defer func() { c.mu.Lock(); delete(c.preparing, id); c.mu.Unlock(); c.signal() }()
	ctx, cancel := context.WithTimeout(c.ctx, c.setupTimeout(id))
	defer cancel()
	if err := c.begin(ctx, id); err != nil {
		_ = c.mutate(id, func(j *nativeJob) {
			if j.End != nil {
				return
			}
			if j.Stage == "starting_turn" || j.Stage == "active" || j.Stage == "checking" || j.Stage == "checking_readiness" {
				j.Reason = "native turn dispatch requires reconciliation: " + runner.Clip(err.Error())
			} else {
				j.End = &runner.ExecutionEnd{Kind: "failed", Reason: "readiness: " + runner.Clip(err.Error())}
			}
		})
	}
}
func (c *Codex) begin(ctx context.Context, id string) error {
	if err := c.measure(); err != nil {
		return err
	}
	c.mu.Lock()
	j, err := c.loadLocked(id)
	c.mu.Unlock()
	if err != nil {
		return err
	}
	if j.Assignment.Setup.Profile.Access != "input-only" {
		if c.workspaces == nil {
			return errors.New("source workspace support is absent")
		}
		workspace, e := c.workspaces.Prepare(ctx, j.Assignment)
		if e != nil {
			return e
		}
		j.Workspace = &workspace
		if err := c.workspaceInputs(j); err != nil {
			return err
		}
		if e = c.mutate(id, func(saved *nativeJob) { saved.Workspace = &workspace }); e != nil {
			return e
		}
	}
	peer, err := c.openPeer(ctx)
	if err != nil {
		return err
	}

	if err = checkNativeConfig(ctx, peer); err != nil {
		return err
	}
	if j.Workspace != nil {
		if err = c.checkWorkspaceConfig(ctx, peer); err != nil {
			return err
		}
		c.connMu.Lock()
		readyGeneration := c.generation
		c.connMu.Unlock()
		if j.Workspace.ReadyGeneration != readyGeneration {
			if err = c.mutate(id, func(j *nativeJob) { j.Stage = "checking_readiness" }); err != nil {
				return err
			}
			if err = c.workspaceReadiness(ctx, peer, j); err != nil {
				return err
			}
			if err = c.workspaces.MarkReady(j.Assignment.RunID, readyGeneration); err != nil {
				return err
			}
			if err = c.mutate(id, func(j *nativeJob) { j.Stage = "allocated" }); err != nil {
				return err
			}
		}
	}
	if j.Assignment.Setup.Profile.Harness == "go-check" {
		return c.beginCheck(ctx, id, peer, j)
	}

	c.connMu.Lock()
	generation := c.generation
	samePeer := c.peer == peer
	c.connMu.Unlock()
	if !samePeer {
		return errors.New("native connection changed during setup; reconcile before launch")
	}
	if err = checkNativeModel(ctx, peer, j.Assignment.Setup.Profile); err != nil {
		return err
	}
	if err = c.mutate(id, func(j *nativeJob) { j.Stage = "creating_thread" }); err != nil {
		return err
	}
	p := j.Assignment.Setup.Profile
	var started struct {
		Thread    nativeThread `json:"thread"`
		Model     string       `json:"model"`
		Reasoning string       `json:"reasoningEffort"`
		Approval  string       `json:"approvalPolicy"`
		Sandbox   struct {
			Type    string `json:"type"`
			Network bool   `json:"networkAccess"`
		} `json:"sandbox"`
		Sources []string        `json:"instructionSources"`
		Roots   json.RawMessage `json:"runtimeWorkspaceRoots"`
		Active  struct {
			ID string `json:"id"`
		} `json:"activePermissionProfile"`
	}
	params := map[string]any{
		"model":                      p.Model,
		"approvalPolicy":             "never",
		"sandbox":                    "read-only",
		"cwd":                        filepath.Join(c.disk.Root, "native", "empty"),
		"baseInstructions":           p.Instructions + nativeBase,
		"developerInstructions":      "Perform only the supplied Runner assignment and return its declared structured result.",
		"environments":               []any{},
		"runtimeWorkspaceRoots":      []any{},
		"allowProviderModelFallback": false,
		"historyMode":                "legacy",
		"config":                     map[string]any{"model_reasoning_effort": p.Reasoning},
	}
	if c.cfg.GoBinary != "" {
		delete(params, "sandbox")
		params["permissions"] = "runner-input"
	}
	if j.Workspace != nil {
		params["environments"] = []any{map[string]any{
			"environmentId":         "local",
			"cwd":                   j.Workspace.WorkDir,
			"runtimeWorkspaceRoots": []string{j.Workspace.WorkDir},
		}}
		params["permissions"] = "runner-edit"
		params["cwd"] = j.Workspace.WorkDir
		params["runtimeWorkspaceRoots"] = []string{j.Workspace.WorkDir}
		params["baseInstructions"] = p.Instructions + workspaceBase
		if p.Access == "workspace-read" {
			params["permissions"] = "runner-check"
			params["baseInstructions"] = p.Instructions + reviewBase
		}
		env, e := c.workerEnvironment(j)
		if e != nil {
			return e
		}
		params["config"] = map[string]any{
			"model_reasoning_effort":   p.Reasoning,
			"features.shell_tool":      true,
			"allow_login_shell":        false,
			"shell_environment_policy": map[string]any{"inherit": "none", "set": env},
		}
	}
	if err = peer.Call(ctx, "thread/start", params, &started); err != nil {
		return err
	}
	if started.Thread.ID == "" {
		return errors.New("thread/start returned no identity")
	}
	c.connMu.Lock()
	c.subscribed[started.Thread.ID] = peer
	c.connMu.Unlock()
	if err = c.mutate(id, func(j *nativeJob) {
		j.ThreadID = started.Thread.ID
		j.Generation = generation
		j.Stage = "thread_known"
	}); err != nil {
		return err
	}
	envsOK := string(started.Thread.Environments) == "[]"
	rootsOK := string(started.Roots) == "[]"
	sandboxOK := started.Sandbox.Type == "readOnly" && !started.Sandbox.Network
	if j.Workspace != nil {
		var roots []string
		_ = json.Unmarshal(started.Roots, &roots)
		rootsOK = len(roots) == 1 && roots[0] == j.Workspace.WorkDir
		sandboxOK = started.Active.ID == "runner-edit" && started.Sandbox.Type == "workspaceWrite" && !started.Sandbox.Network
		if p.Access == "workspace-read" {
			sandboxOK = started.Active.ID == "runner-check" && started.Sandbox.Type == "workspaceWrite" && !started.Sandbox.Network
		}
		var envs []struct {
			ID    string   `json:"environmentId"`
			Cwd   string   `json:"cwd"`
			Roots []string `json:"runtimeWorkspaceRoots"`
		}
		_ = json.Unmarshal(started.Thread.Environments, &envs)
		envsOK = len(envs) == 1 && envs[0].ID == "local" && envs[0].Cwd == j.Workspace.WorkDir && len(envs[0].Roots) == 1 && envs[0].Roots[0] == j.Workspace.WorkDir
	}
	if started.Model != p.Model || started.Reasoning != p.Reasoning || started.Approval != "never" || !sandboxOK || len(started.Sources) != 0 || !rootsOK || !envsOK || started.Thread.HistoryMode != "legacy" {
		return fmt.Errorf("native setup mismatch: model=%q effort=%q approval=%q profile=%q sandbox=%q roots_match=%t environments_match=%t instruction_sources=%d history=%q", started.Model, started.Reasoning, started.Approval, started.Active.ID, started.Sandbox.Type, rootsOK, envsOK, len(started.Sources), started.Thread.HistoryMode)
	}
	prompt, schema, err := nativePayload(j.Assignment)
	if err != nil {
		return err
	}
	stopping := false
	if err = c.mutate(id, func(j *nativeJob) {
		if c.closing || j.StopRequested {
			stopping = true
			j.End = &runner.ExecutionEnd{Kind: "stopped", Reason: "native setup stopped before turn dispatch"}
			return
		}
		j.Effective = &runner.ExecutionFacts{Model: p.Model, Reasoning: p.Reasoning, Access: p.Access, HistoryMode: "legacy"}
		j.Stage = "starting_turn"
	}); err != nil {
		return err
	}
	if stopping {
		return nil
	}
	var result struct {
		Turn nativeTurn `json:"turn"`
	}
	err = peer.Call(ctx, "turn/start", map[string]any{
		"threadId":     started.Thread.ID,
		"input":        []any{map[string]any{"type": "text", "text": prompt}},
		"model":        p.Model,
		"effort":       p.Reasoning,
		"outputSchema": schema,
	}, &result)
	if err != nil {
		return err
	}
	if result.Turn.ID == "" {
		return errors.New("turn/start returned no identity")
	}
	if err = c.mutate(id, func(j *nativeJob) {
		if j.TurnID != "" && j.TurnID != result.Turn.ID {
			j.Reason = "native thread returned conflicting turn identities"
			return
		}
		j.TurnID = result.Turn.ID
		j.Stage = "active"
		if j.End == nil {
			j.Reason = ""
		}
	}); err != nil {
		return err
	}
	c.mu.Lock()
	latest, _ := c.loadLocked(id)
	c.mu.Unlock()
	if latest.StopRequested && latest.End == nil {
		return peer.Call(ctx, "turn/interrupt", map[string]string{"threadId": latest.ThreadID, "turnId": latest.TurnID}, nil)
	}
	return nil
}

func (c *Codex) setupTimeout(id string) time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	j, e := c.loadLocked(id)
	if e == nil && j.Assignment.Setup.Profile.Access != "input-only" {
		return max(time.Millisecond, time.Until(j.Assignment.Deadline))
	}
	return 20 * time.Second
}

const workspaceBase = "You execute one bounded source task in the supplied workspace. Use apply_patch for edits and bounded local shell reads for context. This profile provides Go and basic system commands; do not try alternate interpreters or request login shells. Do not use Git, add dependencies, change protected files, use Python or start other work. Run checks only when explicitly authorized by the selected role and task, within their exact scope and budget. Treat deadline_utc as the hard cutoff; remaining_seconds is a prompt-time snapshot. Return the declared structured result before cutoff. When measurement is needed, hand off runnable source to the configured steps and state the missing proof. Use ready only for a complete role handoff; final acceptance belongs to configured checks, measurement and review. Runner retains the source.\n"
const reviewBase = "You review one Runner candidate against the original request and supplied check results. Source is read-only; scratch is writable. Read the supplied patch and only the affected source/contracts needed to judge it. Do not edit source, run tests, use Git, install tools, launch agents or repair anything. Return the declared structured review, including concrete blockers or missing proof. Do not approve incomplete work.\n"
const nativeBase = "You execute one input-only Runner assignment. Use the supplied inputs only. Do not call tools, access files, ask follow-up questions or start other work. Return exactly the structured result required by the output schema. Use ready when the requested outputs are available. Use needs_help only when that outcome is declared.\n"

func nativePayload(a runner.Assignment) (string, any, error) {
	return nativePayloadAt(a, time.Now())
}

func nativePayloadAt(a runner.Assignment, now time.Time) (string, any, error) {
	if strings.TrimSpace(a.Task) == "" {
		return "", nil, errors.New("an explicit task purpose is required")
	}
	p := a.Setup.Profile
	if p.Harness != "codex" || (p.Access != "input-only" && p.Access != "workspace-write" && p.Access != "workspace-read") || p.Model == "" || p.Reasoning == "" || p.MaxInput < 128 || p.MaxOutput < 128 {
		return "", nil, errors.New("incomplete input-only Codex profile")
	}
	inputs := map[string]any{}
	for k, v := range a.Inputs {
		if p.Access == "workspace-write" && k == "patch" && v.ArtifactID != "" {
			inputs[k] = "Runner already restored the retained patch into this workspace. Continue from these files; do not reapply the patch."
			continue
		}
		if (p.Access == "workspace-read" || p.Access == "workspace-write") && v.ArtifactID != "" {
			inputs[k] = filepath.Join(".runner-tmp", "inputs", a.AttemptID, k)
			continue
		}
		if v.ArtifactID != "" || !json.Valid(v.Inline) {
			return "", nil, errors.New("Codex inputs must be inline JSON or declared workspace artifacts")
		}
		inputs[k] = json.RawMessage(v.Inline)
	}
	request := struct {
		Task             string         `json:"task"`
		Inputs           map[string]any `json:"inputs"`
		DeadlineUTC      string         `json:"deadline_utc,omitempty"`
		RemainingSeconds *int64         `json:"remaining_seconds,omitempty"`
	}{Task: a.Task, Inputs: inputs}
	if !a.Deadline.IsZero() {
		request.DeadlineUTC = a.Deadline.UTC().Format(time.RFC3339Nano)
		remaining := max(int64(0), int64(a.Deadline.Sub(now)/time.Second))
		request.RemainingSeconds = &remaining
	}
	payload, _ := json.Marshal(request)
	props := map[string]any{}
	required := []string{}
	for k, kind := range nativeOutputTypes(a) {
		if kind != "text" {
			return "", nil, errors.New("only declared text outputs are supported by this adapter")
		}
		props[k] = map[string]any{"type": "string"}
		required = append(required, k)
	}
	sort.Strings(required)
	if len(required) == 0 {
		return "", nil, errors.New("at least one output is required")
	}
	out := map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}
	outcomes := append([]string{"ready"}, a.Outcomes...)
	slices.Sort(outcomes)
	outcomes = slices.Compact(outcomes)
	var outputs any = out
	if a.AllowHelp {
		outcomes = append(outcomes, "needs_help")
		outputs = map[string]any{"anyOf": []any{
			out,
			map[string]any{
				"type":                 "object",
				"properties":           map[string]any{},
				"required":             []string{},
				"additionalProperties": false,
			},
		}}
	}
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"outcome": map[string]any{"type": "string", "enum": outcomes},
			"outputs": outputs,
			"reason":  map[string]any{"type": "string"},
		},
		"required":             []string{"outcome", "outputs", "reason"},
		"additionalProperties": false,
	}
	sb, _ := json.Marshal(schema)
	base := nativeBase
	if p.Access == "workspace-write" {
		base = workspaceBase
	} else if p.Access == "workspace-read" {
		base = reviewBase
	}
	if len(payload)+len(base)+len(p.Instructions)+len(sb) > int(p.MaxInput) {
		return "", nil, errors.New("resolved input/instructions/schema exceed max_input")
	}
	return string(payload), schema, nil
}
func checkNativeConfig(ctx context.Context, peer codexPeer) error {
	var r struct {
		Config map[string]json.RawMessage `json:"config"`
	}
	if err := peer.Call(ctx, "config/read", map[string]any{"includeLayers": false}, &r); err != nil {
		return err
	}
	for _, key := range []string{"mcp_servers", "plugins", "marketplaces"} {
		var v map[string]any
		if json.Unmarshal(r.Config[key], &v) != nil || len(v) != 0 {
			return fmt.Errorf("native %s is not empty", key)
		}
	}
	// Some unrelated flags have structured/null values; inspect required ones only.
	var raw map[string]json.RawMessage
	if json.Unmarshal(r.Config["features"], &raw) != nil {
		return errors.New("native feature settings were not reported")
	}
	for _, k := range []string{
		"shell_tool",
		"apply_patch_freeform",
		"apps",
		"hooks",
		"plugins",
		"remote_plugin",
		"multi_agent",
		"goals",
		"memories",
		"view_image",
		"request_permissions",
		"shell_snapshot",
		"skill_mcp_dependency_install",
	} {
		var v bool
		if json.Unmarshal(raw[k], &v) != nil || v {
			return fmt.Errorf("native feature %s was not disabled", k)
		}
	}
	var skipSkills bool
	if json.Unmarshal(raw["skip_host_skill_discovery"], &skipSkills) != nil || !skipSkills {
		return errors.New("native host skill discovery was not disabled")
	}
	var skills struct {
		Include *bool `json:"include_instructions"`
		Bundled struct {
			Enabled *bool `json:"enabled"`
		} `json:"bundled"`
	}
	if json.Unmarshal(r.Config["skills"], &skills) != nil || skills.Include == nil || *skills.Include || skills.Bundled.Enabled == nil || *skills.Bundled.Enabled {
		return errors.New("native skill catalog/instructions were not disabled")
	}
	var web string
	_ = json.Unmarshal(r.Config["web_search"], &web)
	if web != "disabled" {
		return errors.New("native web search was not disabled")
	}
	var doc int
	if json.Unmarshal(r.Config["project_doc_max_bytes"], &doc) != nil || doc != 0 {
		return errors.New("native project instruction discovery was not disabled")
	}
	return nil
}
func checkNativeModel(ctx context.Context, peer codexPeer, p runner.Profile) error {
	var r struct {
		Data []struct {
			ID      string `json:"id"`
			Efforts []struct {
				Effort string `json:"reasoningEffort"`
			} `json:"supportedReasoningEfforts"`
		} `json:"data"`
	}
	if err := peer.Call(ctx, "model/list", map[string]bool{"includeHidden": true}, &r); err != nil {
		return err
	}
	for _, m := range r.Data {
		if m.ID == p.Model {
			for _, e := range m.Efforts {
				if e.Effort == p.Reasoning {
					return nil
				}
			}
		}
	}
	return errors.New("selected model/reasoning is not advertised by the native harness; no fallback was selected")
}

func (c *Codex) Observe(ctx context.Context, id string) (runner.Observation, error) {
	measureErr := c.measure()
	c.mu.Lock()
	defer c.mu.Unlock()
	j, err := c.loadLocked(id)
	if err != nil {
		return runner.Observation{}, err
	}
	if j.End == nil && measureErr != nil {
		return runner.Observation{State: runner.Unknown, Reason: runner.Clip(measureErr.Error())}, nil
	}
	if j.End == nil && !c.preparing[id] && !c.probing[id] && !c.closing {
		c.probing[id] = true
		c.wg.Add(1)
		go c.reconcile(id)
	}
	j.PeakBytes = max(j.PeakBytes, c.disk.NativePeak())
	return observation(j), nil
}
func (c *Codex) reconcile(id string) {
	defer c.wg.Done()
	defer func() { c.mu.Lock(); delete(c.probing, id); c.mu.Unlock() }()
	ctx, cancel := context.WithTimeout(c.ctx, 10*time.Second)
	defer cancel()
	c.mu.Lock()
	j, err := c.loadLocked(id)
	c.mu.Unlock()
	if err != nil || j.End != nil {
		return
	}
	if j.Stage == "checking" || j.Stage == "checking_readiness" {
		_ = c.mutate(id, func(j *nativeJob) {
			j.Reason = "native command outcome is unknown; retain its workspace and inspect the original execution"
		})
		return
	}
	if j.Stage != "active" && j.Stage != "starting_turn" {
		_ = c.mutate(id, func(j *nativeJob) {
			j.End = &runner.ExecutionEnd{Kind: "failed", Reason: "native setup interrupted before turn dispatch"}
		})
		return
	}
	peer, err := c.openPeer(ctx)
	if err == nil {
		c.connMu.Lock()
		currentGeneration := c.generation
		subscribed := c.subscribed[j.ThreadID] == peer
		c.connMu.Unlock()
		if j.Workspace != nil && j.Generation != "" && j.Generation != currentGeneration {
			_ = c.mutate(id, func(j *nativeJob) {
				j.Reason = "native service changed; original child execution settlement is unconfirmed"
			})
			return
		}
		// The accepted turn and live subscription are the execution evidence.
		// Its rollout may not be flushed yet. Read history only to reconcile lost
		// ownership, dispatch uncertainty or a requested stop.
		if j.Stage == "active" && j.TurnID != "" && j.Reason == "" && !j.StopRequested &&
			j.Generation != "" && j.Generation == currentGeneration && subscribed {
			return
		}
		var r struct {
			Thread nativeThread `json:"thread"`
		}
		err = peer.Call(ctx, "thread/read", map[string]any{"threadId": j.ThreadID, "includeTurns": true}, &r)
		if err == nil && len(r.Thread.Turns) == 1 {
			turn := r.Thread.Turns[0]
			c.connMu.Lock()
			generation := c.generation
			c.connMu.Unlock()
			if turn.Status == "inProgress" && j.Generation != "" && j.Generation != generation {
				turn.Status = "failed"
				turn.Error = &struct {
					Message string `json:"message"`
				}{"native service ended before a terminal result was retained"}
			}
			c.acceptTurn(id, turn)
			if turn.Status == "inProgress" {
				c.connMu.Lock()
				subscribed := c.subscribed[j.ThreadID] == peer
				c.connMu.Unlock()
				if !subscribed { // Reattach notifications; resume itself starts no turn.
					if err = peer.Call(ctx, "thread/resume", map[string]any{"threadId": j.ThreadID, "excludeTurns": true}, nil); err == nil {
						c.connMu.Lock()
						c.subscribed[j.ThreadID] = peer
						c.connMu.Unlock()
					}
				}
			}
			if j.StopRequested && turn.Status == "inProgress" {
				_ = peer.Call(ctx, "turn/interrupt", map[string]string{"threadId": j.ThreadID, "turnId": turn.ID}, nil)
			}
			return
		}
		if err == nil {
			err = errors.New("native history did not identify exactly one turn for this attempt")
		}
	}
	_ = c.mutate(id, func(j *nativeJob) {
		if j.End == nil {
			j.Reason = "reconciliation required: " + runner.Clip(err.Error())
		}
	})
}
func (c *Codex) receive(m rpcMessage) {
	switch m.Method {
	case "turn/completed", "thread/tokenUsage/updated":
		select {
		case c.events <- m:
		case <-c.ctx.Done():
		default:
			select {
			case c.overflow <- struct{}{}:
			default:
			}
		}
	}
}
func (c *Codex) listen() {
	defer c.wg.Done()
	timer := time.NewTicker(250 * time.Millisecond)
	defer timer.Stop()
	for {
		select {
		case <-c.ctx.Done():
			return
		case m := <-c.events:
			c.event(m)
		case <-c.overflow:
			c.connMu.Lock()
			if c.peer != nil {
				c.peer.Close()
			}
			c.connMu.Unlock()
			c.signal()
		case <-timer.C:
			if err := c.measure(); err != nil {
				c.enforceStorage(err)
			}
		}
	}
}
func (c *Codex) enforceStorage(cause error) {
	c.mu.Lock()
	ids := []string{}
	for id, j := range c.jobs {
		if j.End == nil {
			ids = append(ids, id)
			j.StopReason = "native resource check: " + runner.Clip(cause.Error())
			j.Reason = j.StopReason
			_ = c.saveLocked(j)
		}
	}
	idle := len(c.jobs) == 0 && !c.corruptJobs
	if len(ids) == 0 && !idle && !c.corruptJobs {
		c.mu.Unlock()
		return
	} // Allow committed terminal jobs to be cleaned up.
	c.mu.Unlock()
	_, _ = c.tail.Write([]byte("\nRunner native resource check: " + runner.Clip(cause.Error()) + "\n"))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	err := c.stopServer(ctx)
	cancel()
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, id := range ids {
		j, e := c.loadLocked(id)
		if e == nil && j.End == nil && err == nil {
			if j.Workspace != nil {
				j.Reason = "resource stop requested; child execution settlement remains unconfirmed"
			} else {
				j.End = &runner.ExecutionEnd{Kind: "failed", Reason: j.StopReason}
			}
			j.PeakBytes = max(j.PeakBytes, c.disk.NativeUsed())
			_ = c.saveLocked(j)
		}
	}
	if len(c.jobs) == 0 && !c.corruptJobs {
		_ = c.clearHomeLocked()
	}
}
func (c *Codex) event(m rpcMessage) {
	var p struct {
		ThreadID   string     `json:"threadId"`
		TurnID     string     `json:"turnId"`
		Turn       nativeTurn `json:"turn"`
		TokenUsage struct {
			Total struct {
				Input  int64 `json:"inputTokens"`
				Output int64 `json:"outputTokens"`
				Cached int64 `json:"cachedInputTokens"`
			} `json:"total"`
		} `json:"tokenUsage"`
	}
	if json.Unmarshal(m.Params, &p) != nil {
		return
	}
	c.mu.Lock()
	id := ""
	for k, j := range c.jobs {
		if j.ThreadID == p.ThreadID {
			id = k
			break
		}
	}
	c.mu.Unlock()
	if id == "" {
		return
	}
	if m.Method == "turn/completed" {
		c.acceptTurn(id, p.Turn)
	} else {
		_ = c.mutate(id, func(j *nativeJob) {
			j.Usage = runner.Usage{
				InputTokens:       &p.TokenUsage.Total.Input,
				OutputTokens:      &p.TokenUsage.Total.Output,
				CachedInputTokens: &p.TokenUsage.Total.Cached,
			}
		})
	}
}
func (c *Codex) acceptTurn(id string, t nativeTurn) {
	c.connMu.Lock()
	generation := c.generation
	c.connMu.Unlock()
	_ = c.mutate(id, func(j *nativeJob) {
		if j.End != nil {
			return
		}
		if j.Workspace != nil && j.Generation != "" && j.Generation != generation {
			j.Reason = "native service changed; original child execution settlement is unconfirmed"
			return
		}
		if j.TurnID != "" && j.TurnID != t.ID {
			j.Reason = "native history conflicts with the saved turn identity"
			return
		}
		j.TurnID = t.ID
		j.Reason = ""
		j.PeakBytes = max(j.PeakBytes, c.disk.NativePeak())
		switch t.Status {
		case "inProgress":
			j.Stage = "active"
		case "interrupted":
			j.End = &runner.ExecutionEnd{Kind: "stopped", Reason: "native turn interruption confirmed"}
		case "failed":
			reason := "native execution failed"
			if t.Error != nil {
				reason = runner.Clip(t.Error.Message)
			}
			j.End = &runner.ExecutionEnd{Kind: "failed", Reason: reason}
		case "completed":
			j.End = parseNativeResult(j.Assignment, t)
		default:
			j.Reason = "unrecognized native turn status"
		}
	})
}
func parseNativeResult(a runner.Assignment, t nativeTurn) *runner.ExecutionEnd {
	text := ""
	for _, item := range t.Items {
		if item.Type == "agentMessage" && (item.Phase == "final_answer" || item.Phase == "") {
			text = item.Text
		}
	}
	fail := func(reason string) *runner.ExecutionEnd { return &runner.ExecutionEnd{Kind: "failed", Reason: reason} }
	if len(text) == 0 || len(text) > int(a.Setup.Profile.MaxOutput) {
		return fail("native final result is missing or exceeds max_output")
	}
	var result struct {
		Outcome string            `json:"outcome"`
		Outputs map[string]string `json:"outputs"`
		Reason  string            `json:"reason"`
	}
	dec := json.NewDecoder(strings.NewReader(text))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&result); err != nil {
		return fail("native final result is not the declared JSON object")
	}
	if dec.Decode(new(any)) != io.EOF {
		return fail("native final result contains trailing data")
	}
	r := &runner.StepResult{Outcome: result.Outcome, Reason: result.Reason, Outputs: map[string]runner.Value{}}
	for k, v := range result.Outputs {
		r.Outputs[k] = runner.Text(v)
	}
	if r.Outcome == "ready" || slices.Contains(a.Outcomes, r.Outcome) {
		if err := runner.ValidateValues(r.Outputs, nativeOutputTypes(a)); err != nil {
			return fail(runner.Clip(err.Error()))
		}
	} else if r.Outcome != "needs_help" || !a.AllowHelp || r.Reason == "" || len(r.Reason) > 512 || len(r.Outputs) != 0 {
		return fail("native result has an undeclared outcome or invalid help request")
	}
	return &runner.ExecutionEnd{Kind: "returned", Result: r}
}
func (c *Codex) measure() error {
	if c.cfg == nil {
		return nil
	}
	if err := c.disk.Refresh(); err != nil {
		return err
	}
	if c.disk.NativeUsed() > int64(c.cfg.StorageLimit) {
		return errors.New("Codex-owned content exceeded codex.storage_limit; this is a monitored stop threshold, not an OS quota")
	}
	if c.disk.Used() > int64(c.disk.Policy.MaxManaged-c.disk.Policy.Database.MaxSize)-4096 {
		return runner.ErrCapacity
	}
	free, err := runner.FreeBytes(c.disk.Root)
	if err != nil {
		return err
	}
	if free < int64(c.disk.Policy.MinFree)+2*int64(c.disk.Policy.Database.MaxRecord) {
		return errors.New("minimum free-space recovery reserve reached")
	}
	return nil
}
func (c *Codex) Stop(ctx context.Context, id string) error {
	c.mu.Lock()
	j, err := c.loadLocked(id)
	preparing := c.preparing[id]
	c.mu.Unlock()
	if err != nil {
		return err
	}
	if err = c.mutate(id, func(j *nativeJob) { j.StopRequested = true }); err != nil {
		return err
	}
	if j.End != nil {
		return nil
	}
	if (j.Stage == "checking" || j.Stage == "checking_readiness") && !preparing {
		return c.stopCheck(ctx, id)
	}
	if j.Stage == "checking" {
		return c.stopCheck(ctx, id)
	}
	if preparing && j.TurnID == "" {
		return nil
	}
	if j.Assignment.Setup.Profile.Harness == "go-check" {
		return c.stopCheck(ctx, id)
	}
	peer, err := c.openPeer(ctx)
	if err != nil {
		return err
	}
	c.connMu.Lock()
	generation := c.generation
	c.connMu.Unlock()
	if j.Workspace != nil && j.Generation != "" && j.Generation != generation {
		return errors.New("original child settlement is unconfirmed after native service change")
	}
	if j.TurnID == "" {
		c.reconcileNow(id)
		return nil
	}
	return peer.Call(ctx, "turn/interrupt", map[string]string{"threadId": j.ThreadID, "turnId": j.TurnID}, nil)
}
func (c *Codex) reconcileNow(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.probing[id] && c.ctx.Err() == nil {
		c.probing[id] = true
		c.wg.Add(1)
		go c.reconcile(id)
	}
}
func (c *Codex) Release(ctx context.Context, id string) error {
	c.mu.Lock()
	j, err := c.loadLocked(id)
	c.mu.Unlock()
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if j.End == nil {
		return errors.New("cannot release unsettled native execution")
	}
	if j.ThreadID != "" {
		peer, err := c.openPeer(ctx)
		if err != nil {
			return err
		}
		if err = peer.Call(ctx, "thread/delete", map[string]string{"threadId": j.ThreadID}, nil); err != nil {
			// Qualified native legacy-store absence after an earlier successful
			// deletion whose acknowledgement/local cleanup may have been lost.
			var rpc *rpcError
			if !errors.As(err, &rpc) || rpc.Code != -32600 || rpc.Message != "no rollout found for thread id "+j.ThreadID {
				return err
			}
		}
	}
	if j.Workspace != nil && c.workspaces != nil {
		if err = c.workspaces.ReleaseAttempt(j.Assignment); err != nil {
			return err
		}
	}
	if err = c.disk.Remove("work/" + id); err != nil {
		return err
	}
	if err = c.disk.Remove(nativeName(id)); err != nil {
		return err
	}
	c.mu.Lock()
	delete(c.jobs, id)
	c.mu.Unlock()
	c.connMu.Lock()
	delete(c.subscribed, j.ThreadID)
	c.connMu.Unlock()
	return c.disk.Refresh()
}

// Close stops new launches, requests native interruption, retains unsettled job
// records for restart, then stops the instance-owned service. It never Releases
// an uncommitted result merely because the client/service is shutting down.
func (c *Codex) Close(ctx context.Context) error {
	c.mu.Lock()
	c.closing = true
	ids := []string{}
	for id, j := range c.jobs {
		if j.End == nil {
			ids = append(ids, id)
		}
	}
	c.mu.Unlock()
	for _, id := range ids {
		_ = c.Stop(ctx, id)
	}
	for {
		c.mu.Lock()
		active := false
		for _, j := range c.jobs {
			if j.End == nil {
				active = true
			}
		}
		changed := c.changed
		c.mu.Unlock()
		if !active {
			break
		}
		select {
		case <-ctx.Done():
			goto stopped
		case <-changed:
		}
	}
stopped:
	c.cancel()
	err := c.stopServer(ctx)
	c.wg.Wait()
	c.mu.Lock()
	if len(c.jobs) == 0 && !c.corruptJobs {
		if e := c.clearHomeLocked(); err == nil {
			err = e
		}
	}
	c.mu.Unlock()
	if e := c.disk.Refresh(); err == nil {
		err = e
	}
	return err
}

func (c *Codex) clearHomeLocked() error {
	entries, err := os.ReadDir(filepath.Join(c.disk.Root, "native"))
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err = c.disk.Remove("native/" + entry.Name()); err != nil {
			return err
		}
	}
	if err = c.disk.Remove("jobs/codex-server.json"); err != nil {
		return err
	}
	return c.disk.Refresh()
}

func nativeOutputTypes(a runner.Assignment) map[string]string {
	out := runner.Clone(a.OutputTypes)
	if a.Setup.Profile.Access == "workspace-write" && out["patch"] == "artifact" {
		delete(out, "patch")
	}
	return out
}
