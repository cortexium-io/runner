//go:build darwin || linux

package execution

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
	"golang.org/x/sys/unix"
)

type outputCloseProbe func() error

func (f outputCloseProbe) Close() error { return f() }

func TestHarnessOutputHandoffRetainsReaderUntilCheckpointCommit(t *testing.T) {
	storageError := errors.New("checkpoint storage unavailable")
	closed := false
	drained := make(chan error, 1)
	drained <- nil
	reader := outputCloseProbe(func() error { closed = true; return nil })
	if handed, err := handoffNativeOutput(reader, drained, func(bool) error { return storageError }); handed || !errors.Is(err, storageError) || closed || len(drained) != 1 {
		t.Fatal("failed checkpoint discarded the original observer", handed, err)
	}
	committed := false
	handed, err := handoffNativeOutput(reader, drained, func(expired bool) error {
		if !expired {
			if closed {
				t.Fatal("reader closed before its terminal checkpoint was durable")
			}
			committed = true
			return nil
		}
		if !closed || len(drained) != 0 {
			t.Fatal("handoff was advertised before the original observer stopped")
		}
		return storageError
	})
	if !handed || !errors.Is(err, storageError) || !committed {
		t.Fatal("failed advertisement lost the committed recovery checkpoint", handed, err)
	}
}

func TestHarnessOutputKernelEOF(t *testing.T) {
	control := t.TempDir()
	identity, err := createNativeOutput(control)
	if err != nil {
		t.Fatal(err)
	}
	read, write, err := openNativeOutput(control, identity)
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	defer write.Close()
	if _, err := write.Write([]byte("saved output\n")); err != nil {
		t.Fatal(err)
	}
	if err := write.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(read)
	if err != nil || string(data) != "saved output\n" {
		t.Fatalf("FIFO did not reach EOF after its sole writer closed: %q %v", data, err)
	}
}

func TestHarnessOutputRecoveryRequiresOriginalEOFAndReaderLease(t *testing.T) {
	control := t.TempDir()
	identity, err := createNativeOutput(control)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(control, "output.lease"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	read, write, err := openNativeOutput(control, identity)
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	defer write.Close()
	lease, locked, err := nativeOutputLease(control)
	if err != nil || !locked {
		t.Fatal("original reader could not own its lease", err)
	}
	defer lease.Close()
	if second, locked, err := nativeOutputLease(control); err != nil || locked || second != nil {
		t.Fatal("two readers can consume one stream", err)
	}
	if err := read.Close(); err != nil {
		t.Fatal(err)
	}
	before := time.Now().Add(-time.Second).UTC()
	receipt := harnessReceipt{
		AttemptID:             "attempt",
		Kind:                  "returned",
		Final:                 []byte("frozen final"),
		Since:                 before,
		ExecutionEndedAt:      before.Add(time.Millisecond),
		Output:                identity,
		OutputFinalized:       true,
		OutputWritersPending:  true,
		OutputObserverExpired: true,
	}
	spec := harnessSpec{Control: control, Output: identity}
	if closed, err := recoverNativeOutput(spec, &receipt); err != nil || closed || !receipt.OutputWritersPending || string(receipt.Final) != "frozen final" {
		t.Fatal("an empty but open FIFO was treated as closed", closed, err)
	}
	if err := write.Close(); err != nil {
		t.Fatal(err)
	}
	receipt.OutputFinalized = false
	if _, err := recoverNativeOutput(spec, &receipt); err == nil {
		t.Fatal("EOF promoted an incomplete checkpoint")
	}
	receipt.OutputFinalized = true
	if closed, err := recoverNativeOutput(spec, &receipt); err != nil || !closed || receipt.OutputWritersPending || receipt.OutputRecoveredAt.IsZero() || string(receipt.Final) != "frozen final" {
		t.Fatal("original FIFO EOF did not retain its finalized result", closed, err)
	}
	// Holding the original inode open prevents inode reuse in this replacement probe.
	anchor, err := openNativeOutputRead(control, identity)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(anchor)
	if err := os.Remove(nativeOutputPath(control)); err != nil {
		t.Fatal(err)
	}
	if _, err := createNativeOutput(control); err != nil {
		t.Fatal(err)
	}
	if _, _, err := drainNativeOutput(control, identity); err == nil {
		t.Fatal("replacement FIFO was accepted as the original stream")
	}
	if err := os.Remove(nativeOutputPath(control)); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "unowned"), nativeOutputPath(control)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := drainNativeOutput(control, identity); err == nil {
		t.Fatal("symlink substituted for the output transport")
	}
}

func TestHarnessSharedOutputRecoveryPreservesExitAndBoundedDiagnostic(t *testing.T) {
	control := t.TempDir()
	identity, err := createNativeOutput(control)
	if err != nil {
		t.Fatal(err)
	}
	read, write, err := openNativeOutput(control, identity)
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	defer write.Close()
	before := time.Now().Add(-time.Second).UTC()
	a := runner.Assignment{
		AttemptID: "attempt",
		OutputDir: t.TempDir(),
		Limits:    runner.ExecutionLimits{MaxLog: 64, MaxArtifact: 1024},
	}
	original := []byte("saved before handoff\n")
	proof, err := writeCommandDiagnostic(a, original)
	if err != nil {
		t.Fatal(err)
	}
	final, err := json.Marshal(commandResult{
		AttemptID:          "attempt",
		Kind:               "returned",
		ExitCode:           7,
		Diagnostic:         "saved before handoff\n",
		DiagnosticArtifact: proof,
	})
	if err != nil {
		t.Fatal(err)
	}
	receipt := harnessReceipt{
		AttemptID:            "attempt",
		Kind:                 "returned",
		Final:                final,
		Since:                before,
		ExecutionEndedAt:     before.Add(time.Millisecond),
		Output:               identity,
		OutputFinalized:      true,
		OutputWritersPending: true,
	}
	late := strings.Repeat("late diagnostic\n", 8)
	if _, err := write.Write([]byte(late)); err != nil {
		t.Fatal(err)
	}
	write.Close()
	spec := harnessSpec{
		Control:    control,
		Shared:     true,
		Output:     identity,
		Assignment: a,
	}
	if closed, err := recoverNativeOutput(spec, &receipt); err != nil || !closed {
		t.Fatal("shared output did not recover EOF", closed, err)
	}
	var result commandResult
	if err := json.Unmarshal(receipt.Final, &result); err != nil || result.AttemptID != receipt.AttemptID || result.ExitCode != 7 || result.Kind != "returned" || result.Diagnostic != late[len(late)-64:] {
		t.Fatal("recovery changed the command outcome or lost its bounded tail", result, err)
	}
	log, err := os.ReadFile(filepath.Join(a.OutputDir, filepath.Base(result.DiagnosticArtifact.ID)))
	if err != nil || !result.DiagnosticArtifact.Matches(a.AttemptID, log, a.Limits.MaxLog) || !result.DiagnosticArtifact.AtLimit {
		t.Fatal("recovered log has no exact bounded identity", err)
	}
	old, err := os.ReadFile(filepath.Join(a.OutputDir, filepath.Base(proof.ID)))
	if err != nil || string(old) != string(original) {
		t.Fatal("late output mutated an already referenced log", err)
	}
}

func TestHarnessExpiredOutputObserverRecoversSameExecution(t *testing.T) {
	disk := workspaceDisk(t)
	a := harnessAssignment(t)
	a.OutputTypes = map[string]string{"document": "artifact"}
	binary := filepath.Join(t.TempDir(), "fake-codex")
	document := "completed output preserved through observer recovery"
	script := `#!/bin/sh
if [ "$1" = --version ]; then printf 'fixture 1\n'; exit 0; fi
cat >/dev/null
printf '%s' ` + testShellQuote(document) + ` > ` + testShellQuote(filepath.Join(disk.Root, "artifacts", a.AttemptID, "document.txt")) + `
printf '%s\n' '{"type":"item.completed","item":{"type":"agent_message","text":"{\"outcome\":\"ready\",\"outputs\":{\"document\":\"document.txt\"},\"reason\":\"Complete\"}"}}'
printf '%s\n' '{"type":"turn.completed","usage":{"input_tokens":123,"output_tokens":45,"cached_input_tokens":12}}'
`
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	a.Setup.Profile.Native.Binary = binary
	native := NewNative(disk, nil, nil, nil)
	defer native.Close()
	launches := 0
	var held *os.File
	defer func() {
		if held != nil {
			held.Close()
		}
	}()
	done := make(chan error, 1)
	native.launchWorker = func(file string, finished func()) error {
		launches++
		spec, err := readHarnessSpec(file)
		if err != nil {
			return err
		}
		// The test owns this deliberately idle writer, outside worker process
		// ancestry. An empty owned-process list cannot prove its closure.
		readFD, err := openNativeOutputRead(spec.Control, spec.Output)
		if err != nil {
			return err
		}
		writeFD, err := unix.Open(nativeOutputPath(spec.Control), unix.O_WRONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
		unix.Close(readFD)
		if err != nil {
			return err
		}
		held = os.NewFile(uintptr(writeFD), "held original output writer")
		go func() { done <- RunNativeWorker(context.Background(), file); finished() }()
		return nil
	}
	if _, err := native.Start(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !time.Now().After(a.Deadline) {
		t.Fatal("fixture did not cross the unchanged primary deadline during observer cleanup")
	}
	data, err := disk.Read(harnessControl(a.AttemptID)+"/result.json", disk.Policy.Database.MaxRecord)
	var checkpoint harnessReceipt
	if err != nil || json.Unmarshal(data, &checkpoint) != nil || !checkpoint.OutputObserverExpired || !checkpoint.OutputFinalized || checkpoint.ExecutionEndedAt.After(a.Deadline) {
		t.Fatal("real observer handoff lost its terminal checkpoint", checkpoint, err)
	}
	native.Close()
	reopened := NewNative(disk, nil, nil, nil)
	defer reopened.Close()
	if _, err := reopened.Start(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	o, err := reopened.Observe(t.Context(), a.AttemptID)
	if err != nil || o.State != runner.Unknown || !o.Reconciling || o.End != nil || o.Usage.InputTokens == nil || *o.Usage.InputTokens != 123 || o.ExecutionEndedAt != checkpoint.ExecutionEndedAt {
		t.Fatal("restart accepted an open writer or lost execution evidence", o, err)
	}
	if err := reopened.Release(t.Context(), a.AttemptID); err == nil {
		t.Fatal("open writer permitted cleanup")
	}
	if err := held.Close(); err != nil {
		t.Fatal(err)
	}
	for o.State != runner.Finished {
		o, err = reopened.Observe(t.Context(), a.AttemptID)
		if err != nil {
			t.Fatal(err)
		}
		if o.State != runner.Finished {
			timer := time.NewTimer(time.Until(o.NextCheck))
			select {
			case <-timer.C:
			case <-t.Context().Done():
				timer.Stop()
				t.Fatal(t.Context().Err())
			}
		}
	}
	if launches != 1 || o.End == nil || o.End.Kind != "returned" || o.End.Result.Outputs["document"].ArtifactID != a.AttemptID+"/document" || *o.Usage.OutputTokens != 45 || *o.Usage.CachedInputTokens != 12 {
		t.Fatal("recovery reran the task or changed its result/usage/artifact", o)
	}
	if err := reopened.Release(t.Context(), a.AttemptID); err != nil {
		t.Fatal(err)
	}
	if data, err := disk.Read("artifacts/"+a.AttemptID+"/document", a.Limits.MaxArtifact); err != nil || string(data) != document {
		t.Fatal("collected document did not survive cleanup", string(data), err)
	}
	if _, err := os.Stat(filepath.Join(disk.Root, "work", a.AttemptID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("FIFO or checkpoint survived successful release", err)
	}
	proofData, err := disk.Read("artifacts/"+a.AttemptID+"/provenance.json", a.Limits.MaxLog)
	var proof runner.NativeProvenance
	if err != nil || json.Unmarshal(proofData, &proof) != nil || proof.OutputRecovery == nil || proof.OutputRecovery.ExecutionEndedAt != checkpoint.ExecutionEndedAt || proof.CompletedAt.IsZero() {
		t.Fatal("recovery proof did not survive release", proof, err)
	}
}

func TestHarnessRecoveryProofSurvivesFactTrimming(t *testing.T) {
	disk := workspaceDisk(t)
	a := harnessAssignment(t)
	a.Limits.MaxLog = 8 << 10
	proof := harnessProvenance(a, "fixture", t.TempDir())
	for range 96 {
		proof.Used = append(proof.Used, runner.NativeUse{Kind: "tool", Name: strings.Repeat("x", 300)})
	}
	data, err := boundedHarnessProof(proof, a.Limits.MaxLog-nativeOutputRecoveryReserve)
	if err != nil {
		t.Fatal(err)
	}
	if err := disk.Write("artifacts/"+a.AttemptID+"/provenance.json", data, a.Limits.MaxLog); err != nil {
		t.Fatal(err)
	}
	native := NewNative(disk, nil, nil, nil)
	defer native.Close()
	stamp := time.Now().UTC()
	receipt := harnessReceipt{
		AttemptID:         a.AttemptID,
		ExecutionEndedAt:  stamp,
		OutputRecoveredAt: stamp,
		ChildrenSettled:   true,
	}
	if err := native.recordOutputRecovery(harnessSpec{Assignment: a}, receipt); err != nil {
		t.Fatal(err)
	}
	data, err = disk.Read("artifacts/"+a.AttemptID+"/provenance.json", a.Limits.MaxLog)
	if err != nil || json.Unmarshal(data, &proof) != nil || proof.OutputRecovery == nil || proof.OutputRecovery.EOFAt != stamp || proof.Dropped == 0 {
		t.Fatal("bounded proof dropped mandatory output recovery evidence", err)
	}
}

func TestHarnessCodexFIFOStdoutWithoutCoordinatorAccess(t *testing.T) {
	if os.Getenv("RUNNER_NATIVE_SANDBOX") != "1" {
		t.Skip("set RUNNER_NATIVE_SANDBOX=1 for installed Codex sandbox proof; zero model turns")
	}
	binary, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	control := filepath.Join(root, "control")
	source := t.TempDir()
	a := harnessAssignment(t)
	a.Setup.Profile.Access = "workspace-read"
	a.OutputDir = filepath.Join(root, "artifacts")
	for _, path := range []string{control, a.OutputDir, filepath.Join(control, "tmp"), filepath.Join(root, "home")} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	identity, err := createNativeOutput(control)
	if err != nil {
		t.Fatal(err)
	}
	read, write, err := openNativeOutput(control, identity)
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	defer write.Close()
	spec := harnessSpec{Assignment: a, Root: root, Control: control, Directory: source}
	config := "default_permissions=\"runner-native\"\n[permissions.runner-native]\nfilesystem=" + harnessFilesystem(spec) + "\n[permissions.runner-native.network]\nenabled=false\n"
	if err := os.WriteFile(filepath.Join(root, "home", "config.toml"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	script := `printf 'inherited FIFO output\n'; if rm "$1/output.fifo" 2>/dev/null; then exit 91; fi; if chmod 0600 "$1/output.fifo" 2>/dev/null; then exit 92; fi; if printf forbidden > "$1/private" 2>/dev/null; then exit 93; fi`
	cmd := exec.CommandContext(t.Context(), binary, "sandbox", "-P", "runner-native", "-C", source, "--", "/bin/sh", "-c", script, "sh", control)
	cmd.Env = append(os.Environ(), "CODEX_HOME="+filepath.Join(root, "home"))
	cmd.Stdout = write
	diagnostic := &diagnosticTail{limit: 2048}
	cmd.Stderr = diagnostic
	if err := cmd.Run(); err != nil {
		t.Fatalf("inherited FIFO or coordinator isolation failed: %v %s", err, diagnostic.bytes())
	}
	write.Close()
	data, err := io.ReadAll(read)
	if err != nil || string(data) != "inherited FIFO output\n" {
		t.Fatal("sandbox lost the inherited stream", string(data), err)
	}
}
