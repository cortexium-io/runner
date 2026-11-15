package execution

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
)

type harnessSpec struct {
	Assignment       runner.Assignment       `json:"assignment,omitzero"`
	AttemptID        string                  `json:"attempt_id,omitempty"`
	AssignmentFile   string                  `json:"assignment_file,omitempty"`
	MaxInput         runner.Bytes            `json:"max_input,omitempty"`
	Binary           string                  `json:"binary"`
	Directory        string                  `json:"directory"`
	Control          string                  `json:"control"`
	Root             string                  `json:"root"`
	Storage          runner.StoragePolicy    `json:"storage"`
	Owner            string                  `json:"owner"`
	ScratchDir       string                  `json:"scratch_dir,omitempty"`
	Shared           bool                    `json:"shared,omitempty"`
	ContextOverrides []string                `json:"-"`
	ResultsFile      *runner.NativeInputFile `json:"results_file,omitempty"`
	Output           *nativeOutputIdentity   `json:"output,omitempty"`
}

type harnessReceipt struct {
	AttemptID             string                `json:"attempt_id"`
	Kind                  string                `json:"kind"`
	Final                 []byte                `json:"final,omitempty"`
	Usage                 runner.Usage          `json:"usage"`
	Reason                string                `json:"reason,omitempty"`
	ModelLaunched         bool                  `json:"model_launched,omitempty"`
	ChildrenSettled       bool                  `json:"children_settled"`
	Owner                 string                `json:"owner,omitempty"`
	Since                 time.Time             `json:"since,omitempty"`
	Known                 map[string]bool       `json:"known_processes,omitempty"`
	CleanupReason         string                `json:"cleanup_reason,omitempty"`
	EmptyObservedAt       time.Time             `json:"empty_observed_at,omitempty"`
	OutputWritersPending  bool                  `json:"output_writers_pending,omitempty"`
	OutputObserverExpired bool                  `json:"output_observer_expired,omitempty"`
	OutputFinalized       bool                  `json:"output_finalized,omitempty"`
	ExecutionEndedAt      time.Time             `json:"execution_ended_at,omitzero"`
	Output                *nativeOutputIdentity `json:"output,omitempty"`
	OutputRecoveredAt     time.Time             `json:"output_recovered_at,omitzero"`
}

func harnessArgs(spec harnessSpec, schemaFile string, schema []byte) []string {
	p := spec.Assignment.Setup.Profile
	if p.Native.Harness == "claude" {
		args := []string{
			"--print", "--output-format", "stream-json", "--verbose",
			"--no-session-persistence", "--permission-mode", "dontAsk",
			"--permission-prompts", "none", "--restricted", "--no-chrome",
			"--strict-mcp-config", "--disallowedTools", "Agent,Task",
			"--model", p.Model, "--json-schema", string(schema),
		}
		if p.Native.Tools.Mode == "none" {
			args = append(args, "--tools", "")
		} else {
			args = append(args, "--tools", strings.Join(p.Native.Tools.Names, ","))
		}
		if p.Native.Skills.Mode == "none" {
			args = append(args, "--disable-slash-commands")
		}
		if (len(harnessArtifactFiles(spec.Assignment)) > 0 || spec.ResultsFile != nil) && p.Native.Tools.Mode == "selected" {
			args = append(args, "--add-dir", filepath.Join(spec.Control, "evidence"))
		}
		if p.Reasoning != "" {
			args = append(args, "--effort", p.Reasoning)
		}
		return args
	}
	args := []string{
		"exec", "--json", "--ephemeral", "--color", "never", "--skip-git-repo-check",
		"--model", p.Model, "--output-schema", schemaFile,
		"-c", "approval_policy=\"never\"",
		"-c", "shell_environment_policy.set." + nativeOwnerVariable + "=" + strconv.Quote(spec.Owner),
		"-c", "default_permissions=\"runner-native\"",
		"-c", "permissions.runner-native.filesystem=" + harnessFilesystem(spec),
		"-c", "permissions.runner-native.network.enabled=" + strconv.FormatBool(p.Native.Network),
	}
	for _, override := range runnerCodexOverrides {
		args = append(args, "-c", override)
	}
	if p.Native.Context == "lean" {
		args = append(args, "--strict-config")
	}
	if p.Reasoning != "" {
		args = append(args, "-c", "model_reasoning_effort="+strconv.Quote(p.Reasoning))
	}
	if p.Native.Skills.Mode == "none" && p.Native.Context != "lean" {
		args = append(args,
			"-c", "skills.include_instructions=false",
			"-c", "skills.bundled.enabled=false",
			"-c", "features.skip_host_skill_discovery=true",
		)
	}
	for _, override := range spec.ContextOverrides {
		args = append(args, "-c", override)
	}
	return append(args, "-")
}

func harnessFilesystem(spec harnessSpec) string {
	mode := "read"
	if spec.Assignment.Setup.Profile.Access == "workspace-write" {
		mode = "write"
	}
	// Native configuration deliberately retains broad host reads. Protect the
	// coordinator's private state and explicitly restore only this task's roots.
	paths := map[string]string{
		":minimal":                         "read",
		"/":                                "read",
		spec.Root:                          "deny",
		spec.Directory:                     mode,
		filepath.Join(spec.Control, "tmp"): "write",
		spec.Assignment.OutputDir:          "write",
		filepath.Join(spec.Assignment.OutputDir, "provenance.json"): "deny",
	}
	for _, path := range harnessArtifactFiles(spec.Assignment) {
		paths[path] = "read"
	}
	if spec.ResultsFile != nil {
		paths[harnessResultsReadPath(spec.Assignment, spec.ResultsFile)] = "read"
	}
	if spec.ScratchDir != "" {
		paths[spec.ScratchDir] = "write"
	}
	for _, path := range executionRuntimePaths(spec.Assignment.Setup.Profile) {
		paths[path] = "write"
	}
	keys := make([]string, 0, len(paths))
	for path := range paths {
		keys = append(keys, path)
	}
	sort.Strings(keys)
	var entries []string
	for _, path := range keys {
		entries = append(entries, strconv.Quote(path)+"="+strconv.Quote(paths[path]))
	}
	return "{" + strings.Join(entries, ",") + "}"
}

func readHarnessSpec(file string) (harnessSpec, error) {
	var spec harnessSpec
	info, err := os.Lstat(file)
	if err != nil {
		return spec, err
	}
	if !info.Mode().IsRegular() || info.Size() > 4<<20 {
		return spec, errors.New("invalid native worker spec")
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return spec, err
	}
	if err = json.Unmarshal(data, &spec); err != nil {
		return spec, err
	}
	if spec.AssignmentFile != "" {
		if !validStagedInputName(spec.AttemptID) || spec.AssignmentFile != filepath.Join(spec.Root, harnessControl(spec.AttemptID), "assignment.json") || spec.Control != filepath.Dir(file) || spec.MaxInput < 128 && !spec.Shared {
			return spec, errors.New("invalid native worker assignment reference")
		}
		limit := spec.MaxInput + spec.Storage.Database.MaxRecord
		if limit < 1 || limit > spec.Storage.MaxManaged {
			return spec, errors.New("invalid native assignment budget")
		}
		info, err := os.Lstat(spec.AssignmentFile)
		if err != nil {
			return spec, err
		}
		if !info.Mode().IsRegular() || info.Size() > int64(limit) {
			return spec, errors.New("invalid native assignment file")
		}
		data, err = os.ReadFile(spec.AssignmentFile)
		if err == nil {
			err = json.Unmarshal(data, &spec.Assignment)
		}
		if err != nil || spec.Assignment.AttemptID != spec.AttemptID {
			return spec, errors.New("native worker assignment identity differs")
		}
	}
	if err := validateHarnessResults(spec.Assignment, spec.Control, spec.ResultsFile); err != nil {
		return spec, err
	}
	return spec, nil
}

// RunNativeWorker is an internal execution mode, not another scheduler. Its
// claim and result survive coordinator restarts. It never launches a worktree.
func RunNativeWorker(parent context.Context, file string) (returnErr error) {
	if !commandGroupsSupported {
		return errors.New("native workers require macOS or Linux")
	}
	spec, err := readHarnessSpec(file)
	if err != nil {
		return err
	}
	a := spec.Assignment
	if !validStagedInputName(a.AttemptID) || spec.Control != filepath.Dir(file) || spec.Control != filepath.Join(spec.Root, harnessControl(a.AttemptID)) || !filepath.IsAbs(spec.Binary) || !filepath.IsAbs(spec.Directory) || a.Deadline.IsZero() || len(spec.Owner) < 16 {
		return errors.New("incomplete native worker identity")
	}
	if sharedCommand(a) != spec.Shared || spec.Shared && (!filepath.IsAbs(spec.ScratchDir) || !strings.HasPrefix(filepath.Base(spec.ScratchDir), "runner-shared-") || pathsOverlap(spec.ScratchDir, spec.Root) || pathsOverlap(spec.ScratchDir, spec.Directory)) {
		return errors.New("invalid shared command scratch identity")
	}
	if spec.Shared {
		scratch, err := filepath.EvalSymlinks(spec.ScratchDir)
		if err != nil || scratch != filepath.Join(spec.Control, "tmp") {
			return errors.New("shared command scratch must alias its managed attempt tmp")
		}
	}
	prompt, schema := "", []byte(nil)
	if sharedCommand(a) {
		a, err = sharedAssignment(a)
	} else {
		prompt, schema, err = harnessPayload(a, harnessPayloadOptions{Scratch: filepath.Join(spec.Control, "tmp"), Results: spec.ResultsFile})
	}
	if err != nil {
		return err
	}
	lease, err := os.OpenFile(filepath.Join(spec.Control, "lease"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lease.Close()
	locked, err := tryCaptureLease(lease)
	if err != nil || !locked {
		return errors.New("native worker dispatch already claimed")
	}
	claim, err := os.OpenFile(filepath.Join(spec.Control, "started.json"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("native worker dispatch already claimed or unavailable: %w", err)
	}
	launched := false
	knownProcesses := map[string]bool{}
	defer func() {
		if returnErr != nil && !launched {
			// Before launch, settlement is certain. Persist the failure instead
			// of stranding an unstarted assignment in Unknown after restart.
			result := harnessReceipt{AttemptID: a.AttemptID, Kind: "failed", Reason: "native worker setup failed before model launch: " + runner.Clip(returnErr.Error())}
			if err := writeHarnessReceipt(spec, result, knownProcesses); err == nil {
				returnErr = nil
			} else {
				returnErr = errors.Join(returnErr, err)
			}
		}
	}()
	err = json.NewEncoder(claim).Encode(map[string]any{"attempt_id": a.AttemptID, "pid": os.Getpid()})
	if err == nil {
		err = claim.Sync()
	}
	claim.Close()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithDeadline(parent, a.Deadline)
	defer cancel()
	stopped := func() bool {
		b, e := os.ReadFile(filepath.Join(spec.Control, "stop"))
		return e != nil || len(b) != 1 || b[0] != 0
	}
	if stopped() {
		cancel()
	}
	var proof runner.NativeProvenance
	if sharedCommand(a) {
		proof = sharedProvenance(a, spec.Binary)
	} else {
		proof = harnessProvenance(a, spec.Binary, spec.Directory)
	}
	digest := sha256.Sum256([]byte(prompt))
	if !sharedCommand(a) {
		proof.RequestDigest = hex.EncodeToString(digest[:])
		proof.PromptBytes, proof.SchemaBytes = len(prompt), len(schema)
		proof.ResultsInput = runner.Clone(spec.ResultsFile)
		if spec.ResultsFile != nil {
			proof.Gaps = append(proof.Gaps, "The complete task-results array is an immutable temporary input, removed on Release. Its canonical accepted outputs remain owned by the referenced child runs.")
		}
	}
	if !sharedCommand(a) {
		versionCtx, versionCancel := context.WithTimeout(ctx, 3*time.Second)
		defer versionCancel()
		versionStarted := time.Now()
		version := exec.CommandContext(versionCtx, spec.Binary, "--version")
		version.Dir = spec.Directory
		version.Env = append(os.Environ(), nativeOwnerVariable+"="+spec.Owner)
		v := &diagnosticTail{limit: 256}
		version.Stdout, version.Stderr = v, io.Discard
		if version.Start() == nil {
			identityErr := rememberNativeRoot(version.Process.Pid, knownProcesses)
			versionErr := version.Wait()
			if identityErr != nil {
				return identityErr
			}
			if versionErr == nil {
				proof.Version = strings.TrimSpace(string(v.bytes()))
			}
		}
		versionCancel()
		proof.VersionMillis = time.Since(versionStarted).Milliseconds()
	}
	if !sharedCommand(a) && a.Setup.Profile.Native.Harness == "codex" {
		started := time.Now()
		proof.Context, err = prepareCodexContext(ctx, spec, knownProcesses)
		proof.ContextMillis = time.Since(started).Milliseconds()
		if err != nil {
			return err
		}
		spec.ContextOverrides = proof.Context.Overrides
	}
	proofFile := filepath.Join(spec.Root, "artifacts", a.AttemptID, "provenance.json")
	if err = os.MkdirAll(filepath.Dir(proofFile), 0700); err != nil {
		return err
	}
	proofLimit := min(a.Limits.MaxLog, 64<<10)
	if spec.Output != nil {
		proofLimit -= nativeOutputRecoveryReserve
	}
	proofBytes, err := boundedHarnessProof(proof, proofLimit)
	if err != nil {
		return err
	}
	if err = workerWrite(proofFile, proofBytes); err != nil {
		return err
	}
	schemaFile := filepath.Join(spec.Control, "schema.json")
	if !sharedCommand(a) {
		if err = workerWrite(schemaFile, schema); err != nil {
			return err
		}
	}
	var cmd *exec.Cmd
	if sharedCommand(a) {
		cmd, err = sharedProcess(ctx, spec)
	} else {
		cmd = exec.CommandContext(ctx, spec.Binary, harnessArgs(spec, schemaFile, schema)...)
		cmd.Env = append(os.Environ(), "TMPDIR="+filepath.Join(spec.Control, "tmp"), nativeOwnerVariable+"="+spec.Owner)
	}
	if err != nil {
		return err
	}
	cmd.Dir = spec.Directory
	cmd.Stdin = strings.NewReader(prompt)
	cmd.ExtraFiles = []*os.File{lease}
	cmd.Stderr = nil // DevNull, without a second anonymous pipe or retained diagnostics.
	setNativeProcessGroup(cmd)
	cmd.Cancel = func() error { return killNativeProcess(cmd) }
	cmd.WaitDelay = 2 * time.Second
	var stdout io.ReadCloser
	var output *os.File
	if spec.Output != nil {
		readerLease, locked, leaseErr := nativeOutputLease(spec.Control)
		if leaseErr != nil || !locked {
			return errors.New("native output observer is already active or unavailable")
		}
		defer readerLease.Close()
		stdout, output, err = openNativeOutput(spec.Control, spec.Output)
	} else {
		stdout, output, err = os.Pipe() // Existing anonymous-pipe assignments.
	}
	if err != nil {
		return err
	}
	defer stdout.Close()
	defer output.Close()
	cmd.Stdout = output
	if sharedCommand(a) {
		cmd.Stderr = output
	}
	if err = cmd.Start(); err != nil {
		kind, reason := "failed", "native executable could not start"
		if ctx.Err() != nil {
			kind, reason = "stopped", "native worker stopped before model launch"
		}
		return writeHarnessReceipt(spec, harnessReceipt{AttemptID: a.AttemptID, Kind: kind, Reason: reason}, knownProcesses)
	}
	_ = output.Close()
	launched = true
	if err = rememberNativeRoot(cmd.Process.Pid, knownProcesses); err != nil {
		cancel()
		_ = cmd.Wait()
		return err
	}
	proof.LaunchedAt = time.Now().UTC()
	proof.StartupMillis = proof.LaunchedAt.Sub(proof.StartedAt).Milliseconds()
	events := harnessEvents{Proof: proof, Limit: int(a.Setup.Profile.MaxOutput)}
	commandLog := &diagnosticTail{limit: int(a.Limits.MaxLog)}
	var eventMu sync.Mutex
	snapshot := func() harnessEvents {
		eventMu.Lock()
		defer eventMu.Unlock()
		return runner.Clone(events)
	}
	streamDone := make(chan error, 1)
	go func() {
		reader := bufio.NewReaderSize(stdout, 32<<10)
		lineLimit := max(int(a.Setup.Profile.MaxOutput)*2+64<<10, 256<<10)
		var line []byte
		overflow := false
		count := 0
		for {
			part, readErr := reader.ReadSlice('\n')
			if sharedCommand(a) {
				_, _ = commandLog.Write(part)
			}
			if len(line)+len(part) > lineLimit {
				overflow, line = true, nil
			}
			if !overflow {
				line = append(line, part...)
			}
			if errors.Is(readErr, bufio.ErrBufferFull) {
				continue
			}
			eventMu.Lock()
			if sharedCommand(a) && len(part) > 0 && events.Proof.FirstEventAt.IsZero() {
				events.Proof.FirstEventAt = time.Now().UTC()
			}
			if overflow {
				events.Proof.Dropped++
				if !sharedCommand(a) && !events.Terminal {
					// The skipped event might contain a newer final message. Only
					// a later bounded result can restore final-result authority.
					events.Final, events.FinalOverflow = nil, true
				}
			} else if len(line) > 0 && !sharedCommand(a) {
				events.accept(line)
			}
			eventMu.Unlock()
			line, overflow = nil, false
			if readErr != nil {
				if readErr == io.EOF {
					readErr = nil
				}
				streamDone <- readErr
				return
			}
			count++
			if count%16 == 0 {
				b, err := boundedHarnessProof(snapshot().Proof, proofLimit)
				if err == nil {
					err = workerWrite(proofFile, b)
				}
				if err != nil {
					cancel()
					streamDone <- err
					return
				}
			}
		}
	}()
	type primaryExit struct {
		err     error
		at      time.Time
		stopped bool
	}
	waitDone := make(chan primaryExit, 1)
	go func() {
		err := cmd.Wait()
		waitDone <- primaryExit{err: err, at: time.Now().UTC(), stopped: ctx.Err() != nil}
	}()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	spaceCheck := time.Now().Add(time.Second)
	var runErr error
	var exited primaryExit
	stopReason := ""
running:
	for {
		select {
		case exited = <-waitDone:
			runErr = exited.err
			break running
		case <-ticker.C:
			if stopped() {
				stopReason = "native worker confirmed cancellation"
				cancel()
			}
			if !time.Now().Before(spaceCheck) {
				for _, path := range []string{spec.Root, spec.Directory} {
					free, e := runner.FreeBytes(path)
					if e != nil || free < int64(spec.Storage.MinFree)+2*int64(a.Limits.MaxRecord) {
						stopReason = "native worker stopped at the configured free-space reserve"
						cancel()
						break
					}
				}
				spaceCheck = time.Now().Add(time.Second)
			}
			// Retain observable ancestry while parents are still alive. A
			// final inventory, not this sampling, decides settlement.
			_, _ = ownedNativeProcesses(spec.Owner, proof.StartedAt, knownProcesses)
		}
	}
	if errors.Is(runErr, exec.ErrWaitDelay) {
		runErr = errors.New("native executable I/O did not settle within the wait bound")
	}
	executionStopped := exited.stopped
	eventMu.Lock()
	events.ExecutionEndedAt = exited.at
	eventMu.Unlock()
	var terminated []nativeChild
	cleanupErr := settleNativeChildren(spec.Owner, proof.StartedAt, knownProcesses, func(child nativeChild) {
		if len(terminated) < maxNativeFacts {
			terminated = append(terminated, child)
		}
	})
	completedEvents := func() harnessEvents {
		e := snapshot()
		if sharedCommand(a) {
			e = sharedCommandEvents(a, e, runErr, commandLog.bytes())
		}
		return e
	}
	receiptErr := runErr
	if sharedCommand(a) {
		receiptErr = nil // The command result retains its configured exit status.
	}
	savePending := func(e harnessEvents, expired bool) error {
		pending := completedHarnessReceipt(a, e, executionStopped, receiptErr, stopReason)
		pending.ChildrenSettled, pending.OutputWritersPending = false, true
		pending.OutputObserverExpired = expired
		pending.Since = proof.StartedAt
		pending.CleanupReason = "waiting for native output writers to close"
		if expired {
			pending.CleanupReason = "native output observer handed off its terminal checkpoint"
		}
		data, err := boundedHarnessProof(e.Proof, proofLimit)
		if err != nil {
			return err
		}
		if err := workerWrite(proofFile, data); err != nil {
			return err
		}
		return writeHarnessReceipt(spec, pending, knownProcesses)
	}
	var streamErr error
	select {
	case streamErr = <-streamDone:
	case <-time.After(2 * time.Second):
		delay := 30 * time.Second
		if err := savePending(completedEvents(), false); err != nil {
			delay = time.Second // Retain the reader and bounded state while storage recovers.
		}
		for waiting := true; waiting; {
			select {
			case streamErr = <-streamDone:
				waiting = false
			case <-time.After(delay):
				e := completedEvents()
				checkpoint := completedHarnessReceipt(a, e, executionStopped, receiptErr, stopReason)
				delay = 30 * time.Second
				if spec.Output != nil && !checkpoint.OutputFinalized {
					// A partial frame cannot be recovered without retaining raw
					// model output. Keep its original bounded reader until EOF.
					if err := savePending(e, false); err != nil {
						delay = time.Second
					}
					continue
				}
				closed, err := handoffNativeOutput(stdout, streamDone, func(expired bool) error {
					return savePending(e, expired)
				})
				if closed {
					return err
				}
				delay = time.Second
			}
		}
	}
	for _, child := range terminated {
		events.use("owned_process", fmt.Sprintf("pid %d", child.PID), "terminated", "birth identity "+child.Identity)
	}
	if sharedCommand(a) {
		events = sharedCommandEvents(a, events, runErr, commandLog.bytes())
		events.use("configured_command", filepath.Base(spec.Binary), "completed", "fixed argv; no model")
		runErr = nil // Configured exit outcomes are collected by the shared command contract.
	}
	if cleanupErr == nil {
		runErr = errors.Join(runErr, streamErr)
	}
	events.Proof.CompletedAt = time.Now().UTC()
	events.Proof.ExecutionMillis = events.Proof.CompletedAt.Sub(events.Proof.LaunchedAt).Milliseconds()
	result := completedHarnessReceipt(a, events, executionStopped, runErr, stopReason)
	if cleanupErr != nil {
		result.ChildrenSettled = false
		result.Since = proof.StartedAt
		result.CleanupReason = runner.Clip(cleanupErr.Error())
	}
	var decision struct {
		Reason string `json:"reason"`
	}
	if json.Unmarshal(events.Final, &decision) == nil {
		events.Proof.Decision = runner.Clip(decision.Reason)
	}
	proofBytes, err = boundedHarnessProof(events.Proof, proofLimit)
	if err != nil {
		return err
	}
	if err = workerWrite(proofFile, proofBytes); err != nil {
		return err
	}
	return writeHarnessReceipt(spec, result, knownProcesses)
}

func completedHarnessReceipt(a runner.Assignment, events harnessEvents, stopped bool, runErr error, stopReason string) harnessReceipt {
	result := harnessReceipt{
		AttemptID:        a.AttemptID,
		Kind:             "returned",
		Final:            events.Final,
		Usage:            events.Usage,
		ModelLaunched:    true,
		ChildrenSettled:  true,
		ExecutionEndedAt: events.ExecutionEndedAt,
	}
	if stopped {
		result.Kind, result.Reason = "stopped", stopReason
		if result.Reason == "" {
			result.Reason = "native worker reached its configured deadline"
		}
	} else if runErr != nil || events.Failed != "" || events.FinalOverflow {
		result.Kind, result.Reason = "failed", events.Failed
		if result.Reason == "" && events.FinalOverflow {
			result.Reason = "native final message exceeds max_output or the stream event bound"
		}
		if result.Reason == "" {
			result.Reason = "native executable exited unsuccessfully; inspect the permitted harness configuration"
		}
	}
	result.OutputFinalized = events.Terminal || sharedCommand(a) || stopped || runErr != nil
	return result
}

func writeHarnessReceipt(spec harnessSpec, result harnessReceipt, known map[string]bool) error {
	result.Output = spec.Output
	if !result.ModelLaunched {
		if err := settleNativeChildren(spec.Owner, time.Now().Add(-time.Minute), known, func(nativeChild) {}); err != nil {
			result.CleanupReason = runner.Clip(err.Error())
		} else {
			result.ChildrenSettled = true
		}
	}
	if !result.ChildrenSettled {
		result.Owner, result.Known = spec.Owner, known
		if result.Since.IsZero() {
			result.Since = time.Now().Add(-time.Minute)
		}
	}
	data, err := json.Marshal(result)
	if err != nil {
		return err
	}
	if int64(len(data)) > int64(spec.Assignment.Limits.MaxRecord) {
		return errors.New("native receipt exceeds max_record")
	}
	return workerWrite(filepath.Join(spec.Control, "result.json"), data)
}
