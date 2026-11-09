package execution

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
)

type commandJob struct {
	Assignment    runner.Assignment    `json:"assignment"`
	Workspace     *workspaceRecord     `json:"workspace,omitempty"`
	Stage         string               `json:"stage"`
	StopRequested bool                 `json:"stop_requested,omitempty"`
	End           *runner.ExecutionEnd `json:"end,omitempty"`
}
type commandResult struct {
	AttemptID  string `json:"attempt_id"`
	Kind       string `json:"kind"`
	ExitCode   int    `json:"exit_code"`
	Diagnostic string `json:"diagnostic,omitempty"`
}

// Commands shares the core/workspace lifecycle. Its small Go worker secures the
// command's exit independently of the coordinating process or a connection.
type Commands struct {
	disk         *runner.Disk
	cfg          *runner.CodexSettings
	workspaces   *Workspaces
	read         func(context.Context, string) ([]byte, error)
	mu           sync.Mutex
	preparing    map[string]bool
	running      map[string]bool
	wake         func(string)
	closed       bool
	launchWorker func(string, func()) error
	ctx          context.Context
	cancel       context.CancelFunc
	setup        sync.WaitGroup
}

func NewCommands(d *runner.Disk, cfg *runner.CodexSettings, w *Workspaces, read func(context.Context, string) ([]byte, error)) *Commands {
	ctx, cancel := context.WithCancel(context.Background())
	c := &Commands{
		disk:       d,
		cfg:        runner.Clone(cfg),
		workspaces: w,
		read:       read,
		preparing:  map[string]bool{},
		running:    map[string]bool{},
		ctx:        ctx,
		cancel:     cancel,
	}
	c.launchWorker = func(spec string, finished func()) error {
		binary, err := os.Executable()
		if err != nil {
			return err
		}
		cmd := exec.Command(binary, "command-worker", spec)
		setNativeProcessGroup(cmd)
		if err = cmd.Start(); err != nil {
			return err
		}
		go func() {
			_ = cmd.Wait()
			finished()
		}()
		return nil
	}
	return c
}
func commandName(id string) string    { return "jobs/" + id + ".command.json" }
func commandControl(id string) string { return "work/" + id + "/command" }
func (c *Commands) SetWake(wake func(string)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.wake = wake
}
func (c *Commands) notify(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.closed && c.wake != nil {
		c.wake(id)
	}
}

// Closing the owner detaches; the already admitted worker retains its deadline.
func (c *Commands) Close() {
	c.mu.Lock()
	c.closed = true
	c.cancel()
	c.mu.Unlock()
	c.setup.Wait()
}
func (c *Commands) Owns(id string) bool {
	_, err := os.Lstat(filepath.Join(c.disk.Root, commandName(id)))
	return !errors.Is(err, os.ErrNotExist)
}
func (c *Commands) load(id string) (commandJob, error) {
	var job commandJob
	data, err := c.disk.Read(commandName(id), c.disk.Policy.Database.MaxRecord)
	if err == nil {
		err = json.Unmarshal(data, &job)
	}
	if err == nil && job.Assignment.AttemptID != id {
		err = errors.New("command identity differs from saved attempt")
	}
	return job, err
}
func (c *Commands) save(job commandJob) error {
	data, err := json.Marshal(job)
	if err != nil {
		return err
	}
	return c.disk.Write(commandName(job.Assignment.AttemptID), data, c.disk.Policy.Database.MaxRecord)
}
func commandObservation(job commandJob) runner.Observation {
	o := runner.Observation{
		State:        runner.Running,
		NativeHandle: "command/" + job.Assignment.AttemptID,
		Reason:       "command " + job.Stage,
		NextCheck:    time.Now().Add(time.Second),
	}
	zero := int64(0)
	o.Usage = runner.Usage{InputTokens: &zero, OutputTokens: &zero, CachedInputTokens: &zero}
	if job.Workspace != nil {
		o.Source = &runner.SourceResult{
			Repository: job.Workspace.Repository,
			Base:       job.Workspace.Base,
			WorkDir:    job.Workspace.WorkDir,
			AttemptID:  job.Assignment.AttemptID,
		}
		if job.Workspace.Candidate != nil {
			o.Source.InputPatchID = job.Workspace.Candidate.PatchID
		}
	}
	if job.End != nil {
		o.State = runner.Finished
		o.End = job.End
		o.NextCheck = time.Time{}
	}
	return o
}
func (c *Commands) Start(ctx context.Context, a runner.Assignment) (runner.Observation, error) {
	if err := ctx.Err(); err != nil {
		return runner.Observation{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return runner.Observation{}, errors.New("command executor is closed")
	}
	if old, err := c.load(a.AttemptID); err == nil {
		return commandObservation(old), nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return runner.Observation{}, err
	}
	if !commandGroupsSupported || c.cfg == nil || a.Setup.Profile.Command == nil || len(a.Setup.Profile.Command.Args) == 0 || a.Setup.Profile.Access != "workspace-read" {
		return runner.Observation{
			State: runner.Finished,
			End: &runner.ExecutionEnd{
				Kind:   "failed",
				Reason: "readiness: command needs a supported platform, read-only workspace and runtime settings",
			},
		}, nil
	}
	job := commandJob{Assignment: runner.Clone(a), Stage: "allocated"}
	if err := c.save(job); err != nil {
		return runner.Observation{}, err
	}
	c.preparing[a.AttemptID] = true
	c.setup.Add(1)
	go c.prepare(a.AttemptID)
	return commandObservation(job), nil
}
func (c *Commands) prepare(id string) {
	defer c.setup.Done()
	defer func() {
		c.mu.Lock()
		delete(c.preparing, id)
		c.mu.Unlock()
		c.notify(id)
	}()
	job, err := c.load(id)
	if err != nil {
		return
	}
	ctx, cancel := context.WithDeadline(c.ctx, job.Assignment.Deadline)
	defer cancel()
	if err = c.prepareWorker(ctx, &job); err != nil {
		c.mu.Lock()
		defer c.mu.Unlock()
		latest, e := c.load(id)
		if e == nil && latest.Stage == "allocated" {
			latest.Workspace = job.Workspace
			latest.End = &runner.ExecutionEnd{Kind: "failed", Reason: "command readiness: " + runner.Clip(err.Error())}
			_ = c.save(latest)
		}
	}
}
func (c *Commands) prepareWorker(ctx context.Context, job *commandJob) error {
	a := job.Assignment
	w, err := c.workspaces.Prepare(ctx, a)
	if err != nil {
		return err
	}
	job.Workspace = &w
	if patch := a.Inputs["patch"].ArtifactID; patch != "" {
		expected, ready := w.SeedPatchID, w.SeedReady
		if w.Candidate != nil {
			expected, ready = w.Candidate.PatchID, true
		}
		if !ready || expected != patch {
			return errors.New("patch does not identify this workspace's prepared source")
		}
	}
	if pins := a.FrontendCheckPins; pins != nil && (a.Inputs["patch"].ArtifactID == "" || w.Capture == nil || w.Capture.PatchSHA != pins.PatchSHA256) {
		return errors.New("prepared frontend check patch differs from the authorized capture")
	}
	local := &Codex{disk: c.disk, cfg: c.cfg, ctx: ctx}
	env, err := local.workerEnvironment(nativeJob{Assignment: a, Workspace: &w})
	if err != nil {
		return err
	}
	_, goRoot, err := local.goInstallation(ctx)
	if err != nil {
		return err
	}
	for name, input := range a.Inputs {
		if input.ArtifactID == "" {
			continue
		}
		data, e := c.read(ctx, input.ArtifactID)
		if e != nil {
			return e
		}
		if pins := a.FrontendCheckPins; pins != nil && name == "patch" && captureDigest(data) != pins.PatchSHA256 {
			return errors.New("frontend check patch bytes changed before dispatch")
		}
		file := filepath.Join("work", a.RunID, "source", ".runner-tmp", a.AttemptID, "inputs", name)
		if e = c.disk.Write(file, data, a.Limits.MaxArtifact); e != nil {
			return e
		}
	}
	spec := a.Setup.Profile.Command
	args := make([]string, len(spec.Args))
	for i, arg := range spec.Args {
		args[i] = commandValue(arg, *job, env)
	}
	baseEnv := runner.Clone(env)
	for key, value := range spec.Env {
		env[key] = commandValue(value, *job, baseEnv)
	}
	var config strings.Builder
	config.WriteString("default_permissions=\"runner-command\"\n[permissions.runner-command.filesystem]\n\":minimal\"=\"read\"\n")
	paths := map[string]string{w.WorkDir: "read", baseEnv["TMPDIR"]: "write", goRoot: "read"}
	control := commandControl(a.AttemptID)
	if pins := a.FrontendCheckPins; pins != nil {
		// Stage the verified bytes under this attempt. A replacement at the
		// configured installation path cannot change the sandboxed command.
		data, err := frontendCheckHelperBytes(args[0], pins.HelperSHA256)
		if err != nil {
			return err
		}
		rel := control + "/runner-frontend-check"
		if err := c.disk.Write(rel, data, a.Limits.MaxArtifact); err != nil {
			return err
		}
		args[0] = filepath.Join(c.disk.Root, rel)
		if err := os.Chmod(args[0], 0500); err != nil {
			return err
		}
		paths[args[0]] = "read"
	}
	for _, p := range c.cfg.CommandReadPaths {
		paths[p] = "read"
	}
	for _, p := range []string{".git", ".codex", ".agents", ".aws"} {
		paths[filepath.Join(w.WorkDir, p)] = "deny"
	}
	for p, mode := range paths {
		fmt.Fprintf(&config, "%s=%s\n", strconv.Quote(p), strconv.Quote(mode))
	}
	fmt.Fprintf(&config, "[permissions.runner-command.network]\nenabled=%t\n", spec.Network)
	if err = c.disk.Write(control+"/config/config.toml", []byte(config.String()), 16384); err != nil {
		return err
	}
	if err = c.disk.Write(control+"/stop", []byte{0}, 1); err != nil {
		return err
	}
	worker := commandSpec{
		AttemptID: a.AttemptID,
		Binary:    c.cfg.Binary,
		Args:      args,
		Env:       env,
		Directory: filepath.Join(w.WorkDir, spec.Directory),
		Control:   filepath.Join(c.disk.Root, control),
		Deadline:  a.Deadline,
		MaxLog:    a.Limits.MaxLog,
	}
	if a.FrontendCheckPins != nil {
		worker.HelperSHA256 = a.FrontendCheckPins.HelperSHA256
	}
	data, _ := json.Marshal(worker)
	if err = c.disk.Write(control+"/spec.json", data, c.disk.Policy.Database.MaxRecord); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	latest, err := c.load(a.AttemptID)
	if err != nil {
		return err
	}
	job.StopRequested = latest.StopRequested
	if c.closed {
		return errors.New("command setup ended with its owner before dispatch")
	}
	if job.StopRequested {
		job.End = &runner.ExecutionEnd{Kind: "stopped", Reason: "command stopped before dispatch"}
		return c.save(*job)
	}
	job.Stage = "dispatching"
	if err = c.save(*job); err != nil {
		return err
	}
	if err = c.launchWorker(filepath.Join(c.disk.Root, control, "spec.json"), func() {
		c.mu.Lock()
		delete(c.running, a.AttemptID)
		c.mu.Unlock()
		c.notify(a.AttemptID)
	}); err != nil {
		job.End = &runner.ExecutionEnd{Kind: "failed", Reason: "command worker could not start: " + runner.Clip(err.Error())}
		return c.save(*job)
	}
	c.running[a.AttemptID] = true
	return nil
}
func (c *Commands) Observe(ctx context.Context, id string) (runner.Observation, error) {
	if err := ctx.Err(); err != nil {
		return runner.Observation{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	job, err := c.load(id)
	if err != nil {
		return runner.Observation{}, err
	}
	o := commandObservation(job)
	if job.End != nil {
		return o, nil
	}
	data, err := c.disk.Read(commandControl(id)+"/result.json", c.disk.Policy.Database.MaxRecord)
	if err == nil {
		var result commandResult
		if json.Unmarshal(data, &result) != nil || result.AttemptID != id || (result.Kind != "returned" && result.Kind != "stopped") {
			o.State = runner.Unknown
			o.Reason = "command receipt is invalid; preserve its resources"
			return o, nil
		}
		job.End, err = collectCommand(c.disk, job, result)
		if err == nil {
			var log []byte
			log, err = c.disk.Read(commandControl(id)+"/log", job.Assignment.Limits.MaxLog)
			if err == nil {
				err = c.disk.Write("logs/"+id+"/check", log, job.Assignment.Limits.MaxLog)
			}
		}
		if err != nil {
			return runner.Observation{}, err
		}
		if err = c.save(job); err != nil {
			return runner.Observation{}, err
		}
		return commandObservation(job), nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return runner.Observation{}, err
	}
	if c.preparing[id] {
		return o, nil
	}
	if job.Stage == "allocated" {
		job.End = &runner.ExecutionEnd{Kind: "failed", Reason: "command setup interrupted before dispatch"}
		if err = c.save(job); err != nil {
			return runner.Observation{}, err
		}
		return commandObservation(job), nil
	}
	started, e := c.disk.Read(commandControl(id)+"/started.json", 1024)
	var receipt struct {
		PID int `json:"pid"`
	}
	if errors.Is(e, os.ErrNotExist) && c.running[id] {
		// The launch succeeded and its wait callback has not observed exit.
		// A restarted owner lacks this evidence and still needs the receipt.
		o.Reason = "command worker starting"
	} else if e != nil || json.Unmarshal(started, &receipt) != nil || !processAlive(receipt.PID) {
		o.State = runner.Unknown
		o.Reason = "command worker settlement is unconfirmed; retain its workspace"
	} else {
		o.Reason = "command active"
	}
	if err = c.disk.Refresh(); err != nil || c.disk.Used() > int64(c.disk.Policy.MaxManaged-c.disk.Policy.Database.MaxSize)-4096 {
		_ = c.stop(job)
		o.Reason = "command storage limit: stop requested; awaiting worker receipt"
	}
	if free, e := runner.FreeBytes(c.disk.Root); e != nil || free < int64(c.disk.Policy.MinFree) {
		_ = c.stop(job)
		o.Reason = "command free-space reserve: stop requested; awaiting worker receipt"
	}
	return o, nil
}
func (c *Commands) stop(job commandJob) error {
	job.StopRequested = true
	file := filepath.Join(c.disk.Root, commandControl(job.Assignment.AttemptID), "stop")
	f, err := os.OpenFile(file, os.O_WRONLY, 0)
	if err == nil {
		_, err = f.WriteAt([]byte{1}, 0)
		if err == nil {
			err = f.Sync()
		}
		f.Close()
	} else if errors.Is(err, os.ErrNotExist) {
		err = nil
	}
	if err != nil {
		return err
	}
	return c.save(job)
}
func (c *Commands) Stop(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	job, err := c.load(id)
	if err != nil {
		return err
	}
	if job.End != nil {
		return nil
	}
	return c.stop(job)
}
func (c *Commands) Release(ctx context.Context, id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	job, err := c.load(id)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if job.End == nil {
		return errors.New("cannot release an unsettled command")
	}
	if job.Workspace != nil {
		if err = c.workspaces.ReleaseAttempt(ctx, job.Assignment); err != nil {
			return err
		}
	}
	if err = c.disk.Remove("work/" + id); err != nil {
		return err
	}
	return c.disk.Remove(commandName(id))
}
func commandValue(value string, job commandJob, env map[string]string) string {
	bindings := []string{
		"{workspace}",
		job.Workspace.WorkDir,
		"{scratch}",
		env["TMPDIR"],
		"{source.base}",
		job.Workspace.Base,
		"{artifact_id.patch}",
		job.Assignment.Inputs["patch"].ArtifactID,
	}
	for name, input := range job.Assignment.Inputs {
		if input.ArtifactID != "" {
			bindings = append(bindings, "{input."+name+"}", filepath.Join(job.Workspace.WorkDir, ".runner-tmp", job.Assignment.AttemptID, "inputs", name))
		} else {
			var text string
			if json.Unmarshal(input.Inline, &text) == nil {
				bindings = append(bindings, "{text."+name+"}", text)
			}
		}
	}
	return strings.NewReplacer(bindings...).Replace(value)
}
func collectCommand(d *runner.Disk, job commandJob, result commandResult) (*runner.ExecutionEnd, error) {
	if result.Kind == "stopped" {
		return &runner.ExecutionEnd{Kind: "stopped", Reason: "command worker confirmed its process group stopped"}, nil
	}
	cmd := job.Assignment.Setup.Profile.Command
	outcome, mapped := cmd.ExitOutcomes[result.ExitCode]
	if result.ExitCode != 0 && !mapped {
		return &runner.ExecutionEnd{Kind: "failed", Reason: fmt.Sprintf("command exited %d: %s", result.ExitCode, runner.Clip(result.Diagnostic))}, nil
	}
	if !mapped {
		outcome = "ready"
	}
	reason := ""
	if cmd.Result != "" {
		data, err := commandArtifact(filepath.Join(job.Workspace.WorkDir, ".runner-tmp", job.Assignment.AttemptID), cmd.Result, job.Assignment.Limits.MaxRecord/2)
		if err != nil {
			return &runner.ExecutionEnd{Kind: "failed", Reason: "command result: " + runner.Clip(err.Error())}, nil
		}
		var domain struct {
			Outcome string `json:"outcome"`
			Reason  string `json:"reason"`
		}
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		if err = decoder.Decode(&domain); err != nil || decoder.Decode(new(any)) != io.EOF || domain.Outcome == "" || len(domain.Reason) > 512 {
			return &runner.ExecutionEnd{Kind: "failed", Reason: "invalid command domain result"}, nil
		}
		outcome, reason = domain.Outcome, domain.Reason
	}
	outputs := map[string]runner.Value{"check": runner.Text(fmt.Sprintf("command exited %d\n%s", result.ExitCode, runner.Clip(result.Diagnostic)))}
	if file := job.Assignment.Setup.Profile.Command.Artifact; file != "" {
		data, err := commandArtifact(filepath.Join(job.Workspace.WorkDir, ".runner-tmp", job.Assignment.AttemptID), file, job.Assignment.Limits.MaxArtifact)
		if err != nil {
			return &runner.ExecutionEnd{Kind: "failed", Reason: "command artifact: " + runner.Clip(err.Error())}, nil
		}
		artifact := job.Assignment.AttemptID + "/result"
		if err = d.Write("artifacts/"+artifact, data, job.Assignment.Limits.MaxArtifact); err != nil {
			return nil, err
		}
		outputs["artifact"] = runner.Value{ArtifactID: artifact}
	}
	return &runner.ExecutionEnd{Kind: "returned", Result: &runner.StepResult{Outcome: outcome, Reason: reason, Outputs: outputs}}, nil
}
func commandArtifact(scratch, name string, limit runner.Bytes) ([]byte, error) {
	file := filepath.Join(scratch, name)
	parent, err := filepath.EvalSymlinks(filepath.Dir(file))
	if err != nil {
		return nil, err
	}
	rel, err := filepath.Rel(scratch, parent)
	if err != nil || rel == ".." || strings.HasPrefix(rel, "../") || filepath.IsAbs(rel) {
		return nil, errors.New("output must stay inside command scratch")
	}
	info, err := os.Lstat(file)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > int64(limit) {
		return nil, errors.New("output must be a regular file within max_artifact")
	}
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if len(data) > int(limit) {
		return nil, errors.New("output exceeds max_artifact")
	}
	return data, err
}
