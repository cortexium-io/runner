package execution

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
)

type harnessJob struct {
	Assignment      runner.Assignment       `json:"assignment,omitzero"`
	AttemptID       string                  `json:"attempt_id,omitempty"`
	AssignmentFile  string                  `json:"assignment_file,omitempty"`
	AssignmentLimit runner.Bytes            `json:"assignment_limit,omitempty"`
	Directory       string                  `json:"directory,omitempty"`
	Workspace       *workspaceRecord        `json:"workspace,omitempty"`
	Stage           string                  `json:"stage"`
	StopRequested   bool                    `json:"stop_requested,omitempty"`
	StopReason      string                  `json:"stop_reason,omitempty"`
	ScratchDir      string                  `json:"scratch_dir,omitempty"`
	ResultsFile     *runner.NativeInputFile `json:"results_file,omitempty"`
	End             *runner.ExecutionEnd    `json:"end,omitempty"`
	Usage           runner.Usage            `json:"usage"`
	Effective       *runner.ExecutionFacts  `json:"effective,omitempty"`
}

// Native is the explicit opt-in CLI adapter. The existing restricted Codex
// service and its private home are never shared with these invocations.
type Native struct {
	disk         *runner.Disk
	cfg          *runner.CodexSettings
	workspaces   *Workspaces
	mu           sync.Mutex
	preparing    map[string]bool
	running      map[string]bool
	wake         func(string)
	closed       bool
	ctx          context.Context
	cancel       context.CancelFunc
	setup        sync.WaitGroup
	launchWorker func(string, func()) error
}

func NewNative(d *runner.Disk, cfg *runner.CodexSettings, w *Workspaces, _ func(context.Context, string) ([]byte, error)) *Native {
	ctx, cancel := context.WithCancel(context.Background())
	n := &Native{disk: d, cfg: runner.Clone(cfg), workspaces: w, ctx: ctx, cancel: cancel, preparing: map[string]bool{}, running: map[string]bool{}}
	n.launchWorker = func(spec string, finished func()) error {
		binary, err := os.Executable()
		if err != nil {
			return err
		}
		cmd := exec.Command(binary, "native-worker", spec)
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
	return n
}

func harnessName(id string) string    { return "jobs/" + id + ".harness.json" }
func harnessControl(id string) string { return "work/" + id + "/harness" }

func (n *Native) SetWake(wake func(string)) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.wake = wake
}

func (n *Native) notify(id string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if !n.closed && n.wake != nil {
		n.wake(id)
	}
}

// Close detaches admitted workers. Stop is the explicit cancellation operation.
func (n *Native) Close() {
	n.mu.Lock()
	n.closed = true
	n.cancel()
	n.mu.Unlock()
	n.setup.Wait()
}

func (n *Native) Owns(id string) bool {
	_, err := os.Lstat(filepath.Join(n.disk.Root, harnessName(id)))
	return !errors.Is(err, os.ErrNotExist)
}

func (n *Native) load(id string) (harnessJob, error) {
	var job harnessJob
	data, err := n.disk.Read(harnessName(id), n.disk.Policy.Database.MaxRecord)
	if err == nil {
		err = json.Unmarshal(data, &job)
	}
	if err == nil && job.AssignmentFile != "" {
		if job.AttemptID != id || job.AssignmentFile != harnessControl(id)+"/assignment.json" || job.AssignmentLimit < 1 || job.AssignmentLimit > n.disk.Policy.MaxManaged {
			return job, errors.New("native assignment reference differs from saved attempt")
		}
		data, err = n.disk.Read(job.AssignmentFile, job.AssignmentLimit)
		if err == nil {
			err = json.Unmarshal(data, &job.Assignment)
		}
	}
	if err == nil && job.Assignment.AttemptID != id {
		err = errors.New("native identity differs from saved attempt")
	}
	return job, err
}

func (n *Native) save(job harnessJob) error {
	id := job.Assignment.AttemptID
	job.AttemptID, job.AssignmentFile = id, harnessControl(id)+"/assignment.json"
	job.AssignmentLimit = job.Assignment.Setup.Profile.MaxInput + n.disk.Policy.Database.MaxRecord
	assignment, err := json.Marshal(job.Assignment)
	if err != nil {
		return err
	}
	old, readErr := n.disk.Read(job.AssignmentFile, job.AssignmentLimit)
	if !errors.Is(readErr, os.ErrNotExist) && readErr != nil {
		return readErr
	}
	if readErr != nil || !bytes.Equal(old, assignment) {
		if err = n.disk.Write(job.AssignmentFile, assignment, job.AssignmentLimit); err != nil {
			return err
		}
	}
	job.Assignment = runner.Assignment{}
	data, err := json.Marshal(job)
	if err != nil {
		return err
	}
	return n.disk.Write(harnessName(id), data, n.disk.Policy.Database.MaxRecord)
}

func harnessObservation(job harnessJob) runner.Observation {
	o := runner.Observation{State: runner.Running, NativeHandle: "harness/" + job.Assignment.AttemptID, Reason: "native " + job.Stage, NextCheck: time.Now().Add(time.Second), Effective: job.Effective, Usage: job.Usage}
	if job.Workspace != nil {
		o.Source = &runner.SourceResult{Repository: job.Workspace.Repository, Base: job.Workspace.Base, WorkDir: job.Workspace.WorkDir, AttemptID: job.Assignment.AttemptID}
	}
	if job.End != nil {
		o.State = runner.Finished
		o.End = job.End
		o.NextCheck = time.Time{}
	}
	return o
}

func (n *Native) Start(ctx context.Context, a runner.Assignment) (runner.Observation, error) {
	if err := ctx.Err(); err != nil {
		return runner.Observation{}, err
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.closed {
		return runner.Observation{}, errors.New("native executor is closed")
	}
	if old, err := n.load(a.AttemptID); err == nil {
		return harnessObservation(old), nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return runner.Observation{}, err
	}
	var err error
	a.OutputDir = filepath.Join(n.disk.Root, "artifacts", a.AttemptID)
	job := harnessJob{Assignment: runner.Clone(a), Stage: "allocated"}
	var resultBytes []byte
	if sharedCommand(a) {
		a, err = sharedAssignment(a)
		job.Assignment = a
	} else {
		job.Assignment, job.ResultsFile, resultBytes, err = planHarnessAssignment(a, filepath.Join(n.disk.Root, harnessControl(a.AttemptID)), a.Setup.Profile.MaxInput+n.disk.Policy.Database.MaxRecord)
	}
	if err == nil && (!commandGroupsSupported || a.Deadline.IsZero() || a.Limits.MaxLog < 1 || a.Limits.MaxRecord < 1 || !sharedCommand(a) && a.Setup.Profile.MaxOutput > a.Limits.MaxRecord/2) {
		err = errors.New("native execution needs a supported platform, deadline, bounded logs and max_output at most half max_record")
	}
	if err != nil {
		return runner.Observation{State: runner.Finished, End: &runner.ExecutionEnd{Kind: "failed", Reason: "native readiness: " + runner.Clip(err.Error())}}, nil
	}
	if job.ResultsFile != nil {
		if err = n.disk.Write(harnessControl(a.AttemptID)+"/results.json", resultBytes, a.Limits.MaxArtifact); err != nil {
			return runner.Observation{}, err
		}
		if err = os.Chmod(job.ResultsFile.Path, 0400); err != nil {
			return runner.Observation{}, err
		}
	}
	if err = n.save(job); err != nil {
		return runner.Observation{}, err
	}
	n.preparing[a.AttemptID] = true
	n.setup.Add(1)
	go n.prepare(a.AttemptID)
	return harnessObservation(job), nil
}

func (n *Native) prepare(id string) {
	defer n.setup.Done()
	defer func() {
		n.mu.Lock()
		delete(n.preparing, id)
		n.mu.Unlock()
		n.notify(id)
	}()
	job, err := n.load(id)
	if err != nil {
		return
	}
	ctx, cancel := context.WithDeadline(n.ctx, job.Assignment.Deadline)
	defer cancel()
	if err = n.prepareWorker(ctx, &job); err == nil {
		return
	}
	if job.ScratchDir != "" {
		_ = removeSharedScratch(job.ScratchDir)
		job.ScratchDir = ""
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	latest, loadErr := n.load(id)
	if loadErr == nil && latest.Stage == "allocated" {
		latest.Directory, latest.Workspace = job.Directory, job.Workspace
		latest.End = &runner.ExecutionEnd{Kind: "failed", Reason: "native readiness: " + runner.Clip(err.Error())}
		_ = n.save(latest)
	}
}

func (n *Native) prepareWorker(ctx context.Context, job *harnessJob) error {
	a := &job.Assignment
	control := harnessControl(a.AttemptID)
	for _, path := range []string{control + "/tmp/.keep", control + "/workspace/.keep", "artifacts/" + a.AttemptID + "/.keep"} {
		if err := n.disk.Write(path, nil, 1); err != nil {
			return err
		}
	}
	job.Directory = filepath.Join(n.disk.Root, control, "workspace")
	a.OutputDir = filepath.Join(n.disk.Root, "artifacts", a.AttemptID)
	if err := validateHarnessResults(*a, filepath.Join(n.disk.Root, control), job.ResultsFile); err != nil {
		return err
	}
	if job.ResultsFile != nil && a.Setup.Profile.Native.Harness == "claude" {
		if err := n.disk.Write(control+"/evidence/.keep", nil, 1); err != nil {
			return err
		}
		if err := os.Link(job.ResultsFile.Path, harnessResultsReadPath(*a, job.ResultsFile)); err != nil {
			return errors.New("Claude task-results input could not be linked into its private read root")
		}
	}
	if a.Setup.Profile.Access != "input-only" {
		if a.TaskContext != nil {
			path, err := filepath.EvalSymlinks(a.WorkDir)
			if err != nil || !filepath.IsAbs(path) || path == string(filepath.Separator) {
				return errors.New("native task workspace is absent")
			}
			if pathsOverlap(n.disk.Root, path) {
				return errors.New("native task workspace must be separate from coordinator state")
			}
			job.Directory = path
		} else {
			if n.workspaces == nil {
				return errors.New("native source workspace capability is unavailable")
			}
			owned := runner.Clone(*a)
			owned.Setup.Profile.Harness = "codex"
			w, err := n.workspaces.Prepare(ctx, owned)
			if err != nil {
				return err
			}
			job.Workspace, job.Directory = &w, w.WorkDir
		}
	}
	for id, target := range harnessArtifactFiles(*a) {
		size, err := n.disk.ArtifactSize(id)
		if err != nil {
			return err
		}
		if size > int64(a.Limits.MaxArtifact) {
			return runner.ErrCapacity
		}
		if a.Setup.Profile.Native != nil && a.Setup.Profile.Native.Harness == "claude" && a.Setup.Profile.Native.Tools.Mode == "selected" {
			if err := n.disk.Write(control+"/evidence/.keep", nil, 1); err != nil {
				return err
			}
			source := filepath.Join(n.disk.Root, "artifacts", filepath.FromSlash(id))
			if err := os.Link(source, target); err != nil {
				return errors.New("Claude read evidence could not be linked into its restricted working roots")
			}
		}
	}
	paths := executionRuntimePaths(a.Setup.Profile)
	for i, path := range paths {
		canonical, err := filepath.EvalSymlinks(path)
		if err != nil {
			return err
		}
		info, err := os.Stat(canonical)
		if err != nil || !info.IsDir() || pathsOverlap(canonical, n.disk.Root) || pathsOverlap(canonical, job.Directory) {
			return errors.New("native runtime_write_paths must be existing directories separate from coordinator state and product source")
		}
		paths[i] = canonical
	}
	var err error
	binary, harness := "", ""
	if sharedCommand(*a) {
		binary = a.Setup.Profile.Command.Args[0]
		base := os.TempDir()
		if runtime.GOOS == "darwin" {
			base = "/private/tmp"
		}
		job.ScratchDir, err = os.MkdirTemp(base, "runner-shared-")
		if err != nil {
			return err
		}
		// Chromium requires a short socket path. The short name is only an
		// owned alias: all browser bytes remain in the existing managed tmp.
		if err = os.Remove(job.ScratchDir); err != nil {
			return err
		}
		if err = os.Symlink(filepath.Join(n.disk.Root, control, "tmp"), job.ScratchDir); err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Join(job.ScratchDir, "browser-home"), 0700); err != nil {
			return err
		}
	} else {
		binary, harness = a.Setup.Profile.Native.Binary, a.Setup.Profile.Native.Harness
	}
	if binary == "" && harness == "codex" && n.cfg != nil {
		binary = n.cfg.Binary
	}
	if binary == "" {
		var err error
		binary, err = exec.LookPath(harness)
		if err != nil {
			return err
		}
	}
	if !filepath.IsAbs(binary) {
		binary, err = exec.LookPath(binary)
		if err != nil {
			return err
		}
	}
	binary, err = filepath.Abs(binary)
	if err != nil {
		return err
	}
	if err = n.disk.Write(control+"/stop", []byte{0}, 1); err != nil {
		return err
	}
	spec := harnessSpec{
		AttemptID:      a.AttemptID,
		AssignmentFile: filepath.Join(n.disk.Root, control, "assignment.json"),
		MaxInput:       a.Setup.Profile.MaxInput,
		Binary:         binary,
		Directory:      job.Directory,
		Control:        filepath.Join(n.disk.Root, control),
		Root:           n.disk.Root,
		Storage:        n.disk.Policy,
		Owner:          rand.Text(),
		ScratchDir:     job.ScratchDir,
		Shared:         sharedCommand(*a),
		ResultsFile:    job.ResultsFile,
	}
	data, err := json.Marshal(spec)
	if err != nil {
		return err
	}
	if err = n.disk.Write(control+"/spec.json", data, n.disk.Policy.Database.MaxRecord); err != nil {
		return err
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	latest, err := n.load(a.AttemptID)
	if err != nil {
		return err
	}
	job.StopRequested = latest.StopRequested
	if n.closed || ctx.Err() != nil {
		return errors.New("native setup ended before dispatch")
	}
	if job.StopRequested {
		job.End = &runner.ExecutionEnd{Kind: "stopped", Reason: "native worker stopped before dispatch"}
		return n.save(*job)
	}
	job.Stage = "dispatching"
	job.Effective = &runner.ExecutionFacts{Model: a.Setup.Profile.Model, Reasoning: a.Setup.Profile.Reasoning, Access: a.Setup.Profile.Access, HistoryMode: "ephemeral", Provenance: a.AttemptID + "/provenance.json", CleanupAssurance: "cooperative-known-processes"}
	if sharedCommand(*a) {
		job.Effective.HistoryMode = "none"
	}
	if err = n.save(*job); err != nil {
		return err
	}
	if err = n.launchWorker(filepath.Join(spec.Control, "spec.json"), func() {
		n.mu.Lock()
		delete(n.running, a.AttemptID)
		n.mu.Unlock()
		n.notify(a.AttemptID)
	}); err != nil {
		job.End = &runner.ExecutionEnd{Kind: "failed", Reason: "native worker could not start: " + runner.Clip(err.Error())}
		job.Effective.Provenance = "" // No worker ran and no manifest was written.
		return n.save(*job)
	}
	n.running[a.AttemptID] = true
	return nil
}

func (n *Native) Observe(ctx context.Context, id string) (runner.Observation, error) {
	if err := ctx.Err(); err != nil {
		return runner.Observation{}, err
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	job, err := n.load(id)
	if err != nil {
		return runner.Observation{}, err
	}
	o := harnessObservation(job)
	if job.End != nil {
		return o, nil
	}
	data, err := n.disk.Read(harnessControl(id)+"/result.json", n.disk.Policy.Database.MaxRecord)
	if err == nil {
		var receipt harnessReceipt
		if json.Unmarshal(data, &receipt) != nil || receipt.AttemptID != id || (receipt.Kind != "returned" && receipt.Kind != "stopped" && receipt.Kind != "failed") {
			o.State, o.Reason = runner.Unknown, "native worker receipt is invalid; retain resources"
			return o, nil
		}
		if !receipt.ChildrenSettled {
			settled, err := n.reconcileReceipt(id, &receipt)
			if err != nil {
				return runner.Observation{}, err
			}
			if !settled {
				o.State, o.Reason, o.Reconciling, o.Usage = runner.Unknown, "native owned-process settlement unconfirmed: "+runner.Clip(receipt.CleanupReason), true, receipt.Usage
				if receipt.OutputObserverExpired {
					o.Reconciling = false
				}
				if !receipt.EmptyObservedAt.IsZero() {
					o.NextCheck = receipt.EmptyObservedAt.Add(20 * time.Millisecond)
				}
				return o, nil
			}
		}
		job.Usage = receipt.Usage
		if !receipt.ModelLaunched && receipt.Kind != "returned" && job.Effective != nil {
			if _, err := n.disk.ArtifactSize(job.Effective.Provenance); err != nil {
				job.Effective.Provenance = ""
			}
		}
		if receipt.Kind == "returned" && sharedCommand(job.Assignment) {
			var result commandResult
			if json.Unmarshal(receipt.Final, &result) != nil || result.AttemptID != id {
				return runner.Observation{}, errors.New("shared command receipt identity differs")
			}
			job.End, err = collectCommandResult(n.disk, job.Assignment, result, filepath.Join(n.disk.Root, harnessControl(id), "tmp"))
		} else if receipt.Kind == "returned" {
			job.End, err = harnessResult(job.Assignment, receipt.Final, job.Assignment.OutputDir, func(id string, data []byte) error {
				return n.disk.Write("artifacts/"+id, data, job.Assignment.Limits.MaxArtifact)
			})
		} else {
			job.End = &runner.ExecutionEnd{Kind: receipt.Kind, Reason: receipt.Reason}
			if receipt.Kind == "stopped" && job.StopReason != "" {
				job.End.Reason = job.StopReason
			}
		}
		if err != nil {
			return runner.Observation{}, err
		}
		if err = n.save(job); err != nil {
			return runner.Observation{}, err
		}
		return harnessObservation(job), nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return runner.Observation{}, err
	}
	if note, noteErr := n.disk.Read(harnessControl(id)+"/uncertain", 2048); noteErr == nil {
		o.State, o.Reason = runner.Unknown, runner.Clip(string(note))
		return o, nil
	}
	if n.preparing[id] {
		return o, nil
	}
	if job.Stage == "allocated" {
		job.End = &runner.ExecutionEnd{Kind: "failed", Reason: "native setup interrupted before dispatch"}
		if err = n.save(job); err != nil {
			return runner.Observation{}, err
		}
		return harnessObservation(job), nil
	}
	lease, leaseErr := os.OpenFile(filepath.Join(n.disk.Root, harnessControl(id), "lease"), os.O_RDWR, 0600)
	locked := false
	if leaseErr == nil {
		locked, leaseErr = tryCaptureLease(lease)
		lease.Close()
	}
	if leaseErr != nil || locked {
		if n.running[id] {
			o.Reason = "native worker starting"
		} else {
			o.State, o.Reason = runner.Unknown, "native worker settlement is unconfirmed; retain resources"
		}
	}
	if err = n.disk.Refresh(); err != nil || n.disk.Used() > int64(n.disk.Policy.MaxManaged-n.disk.Policy.Database.MaxSize)-4096 {
		_ = n.stop(job, "native managed storage reserve reached")
		o.Reason = "native storage reserve reached; awaiting settlement"
	}
	for _, path := range []string{n.disk.Root, job.Directory} {
		if path == "" {
			continue
		}
		free, freeErr := runner.FreeBytes(path)
		if freeErr != nil || free < int64(n.disk.Policy.MinFree)+2*int64(n.disk.Policy.Database.MaxRecord) {
			_ = n.stop(job, "native free-space reserve reached")
			o.Reason = "native free-space reserve reached; awaiting settlement"
			break
		}
	}
	return o, nil
}

// Reconcile the same saved execution. The model result and usage stay durable
// while cleanup is pending; neither an owner restart nor cancellation reruns it.
func (n *Native) reconcileReceipt(id string, receipt *harnessReceipt) (bool, error) {
	if receipt.OutputWritersPending {
		return false, nil
	}
	var spec harnessSpec
	data, err := n.disk.Read(harnessControl(id)+"/spec.json", n.disk.Policy.Database.MaxRecord)
	if err != nil || json.Unmarshal(data, &spec) != nil || len(receipt.Owner) < 16 || receipt.Owner != spec.Owner || receipt.Known == nil {
		receipt.CleanupReason = "saved native ownership identity is unavailable"
		return false, nil
	}
	children, inventoryErr := ownedNativeProcesses(receipt.Owner, receipt.Since, receipt.Known)
	if inventoryErr != nil {
		receipt.CleanupReason = runner.Clip(inventoryErr.Error())
		receipt.EmptyObservedAt = time.Time{}
	} else if len(children) > 0 {
		receipt.EmptyObservedAt = time.Time{}
		for _, child := range children {
			if err := killOwnedNativeProcess(child, receipt.Owner, receipt.Known); err != nil {
				receipt.CleanupReason = runner.Clip(err.Error())
				break
			}
		}
	} else if receipt.EmptyObservedAt.IsZero() {
		receipt.EmptyObservedAt = time.Now()
	} else if time.Since(receipt.EmptyObservedAt) >= 20*time.Millisecond {
		receipt.ChildrenSettled = true
		receipt.Owner, receipt.Known, receipt.CleanupReason = "", nil, ""
	}
	data, err = json.Marshal(receipt)
	if err == nil {
		err = n.disk.Write(harnessControl(id)+"/result.json", data, n.disk.Policy.Database.MaxRecord)
	}
	return receipt.ChildrenSettled, err
}

func (n *Native) stop(job harnessJob, reason ...string) error {
	job.StopRequested = true
	if len(reason) > 0 {
		job.StopReason = reason[0]
	}
	file := filepath.Join(n.disk.Root, harnessControl(job.Assignment.AttemptID), "stop")
	f, err := os.OpenFile(file, os.O_WRONLY, 0600)
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
	return n.save(job)
}

func (n *Native) Stop(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	job, err := n.load(id)
	if err != nil || job.End != nil {
		return err
	}
	return n.stop(job)
}

func (n *Native) Release(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	job, err := n.load(id)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if job.End == nil {
		return errors.New("cannot release unsettled native work")
	}
	if job.Workspace != nil {
		if err = n.workspaces.ReleaseAttempt(ctx, job.Assignment); err != nil {
			return err
		}
	}
	if err := removeSharedScratch(job.ScratchDir); err != nil {
		return err
	}
	keep := map[string]bool{"provenance.json": true}
	if job.End.Result != nil {
		for _, output := range job.End.Result.Outputs {
			if filepath.Dir(output.ArtifactID) == id {
				keep[filepath.Base(output.ArtifactID)] = true
			}
		}
	}
	if job.Workspace != nil {
		keep["changes.patch"] = true
	}
	entries, err := os.ReadDir(job.Assignment.OutputDir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, entry := range entries {
		if !keep[entry.Name()] {
			if err = n.disk.Remove("artifacts/" + id + "/" + entry.Name()); err != nil {
				return err
			}
		}
	}
	if err = n.disk.Remove("work/" + id); err != nil {
		return err
	}
	return n.disk.Remove(harnessName(id))
}

func pathsOverlap(a, b string) bool {
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		a, b = strings.ToLower(a), strings.ToLower(b)
	}
	ab, errAB := filepath.Rel(a, b)
	ba, errBA := filepath.Rel(b, a)
	return errAB != nil || errBA != nil || ab == "." || ba == "." || filepath.IsLocal(ab) || filepath.IsLocal(ba)
}
