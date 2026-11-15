//go:build darwin || linux

package execution

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
)

func TestHarnessStructuredTasksAndDeliverable(t *testing.T) {
	a := harnessAssignment(t)
	a.TaskContext = &runner.TaskContext{Goal: "Write an article", AllowedRoles: []runner.TaskRoleContract{{ID: "writer", Inputs: map[string]string{"brief": "text"}}}}
	prompt, schema, err := harnessPayload(a)
	if err != nil || !strings.Contains(prompt, "Write an article") || !strings.Contains(string(schema), "task_action") {
		t.Fatalf("task context/schema: %v", err)
	}
	control := `{"outcome":"ready","outputs":{},"reason":"Independent draft","task_action":{"kind":"delegate","reason":"Independent draft","tasks":[{"id":"draft","role":"writer","brief":"Write the article","inputs":[{"name":"brief","kind":"text","value":"Complete draft"}],"acceptance":["Complete article"],"depends_on":[],"write_paths":[]}],"replace":[],"question":""}}`
	end, err := harnessResult(a, []byte(control), t.TempDir(), nil)
	if err != nil || end.Result == nil || end.Result.TaskAction == nil || end.Result.TaskAction.Tasks[0].Inputs["brief"].Validate() != nil {
		t.Fatalf("task action lost: %+v %v", end, err)
	}
	article := strings.Repeat("Substantial complete writing. ", 350)
	result, _ := json.Marshal(map[string]any{"outcome": "ready", "outputs": map[string]string{"article": article}, "reason": "Complete", "task_action": nil})
	end, err = harnessResult(a, result, t.TempDir(), nil)
	if err != nil || end.Kind != "returned" || len(end.Result.Outputs["article"].Inline) < 9000 {
		t.Fatalf("deliverable was truncated: %+v %v", end, err)
	}
}

func TestHarnessArtifactBoundary(t *testing.T) {
	a := harnessAssignment(t)
	a.OutputTypes = map[string]string{"document": "artifact"}
	output := t.TempDir()
	output, _ = filepath.EvalSymlinks(output)
	if err := os.WriteFile(filepath.Join(output, "article.md"), []byte("Complete retained article"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "unowned"), filepath.Join(output, "link")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"article.md", "../outside", "link"} {
		stored := false
		result, _ := json.Marshal(map[string]any{"outcome": "ready", "outputs": map[string]string{"document": name}, "reason": ""})
		end, err := harnessResult(a, result, output, func(id string, data []byte) error {
			stored = id == a.AttemptID+"/document" && string(data) == "Complete retained article"
			return nil
		})
		if err != nil || (end.Kind == "returned") != (name == "article.md") || stored != (name == "article.md") {
			t.Fatalf("artifact boundary %q: %+v %v", name, end, err)
		}
	}
}

func TestHarnessNestedArtifactForwarding(t *testing.T) {
	a := harnessAssignment(t)
	a.OutputDir = filepath.Join(t.TempDir(), "artifacts", a.AttemptID)
	a.OutputTypes = map[string]string{"document": "artifact"}
	id := "attempt_writer/document"
	for _, role := range []string{"subplanner", "planner", "root"} {
		a.TaskContext = &runner.TaskContext{TaskID: role, Results: []runner.RoleTaskResult{{ID: "accepted_child", Outputs: map[string]runner.Value{"document": {ArtifactID: id}}}}}
		prompt, _, err := harnessPayload(a)
		if err != nil || !strings.Contains(prompt, id) || !strings.Contains(prompt, "evidence_files") {
			t.Fatalf("nested artifact was not supplied: %v", err)
		}
		wire, _ := json.Marshal(map[string]any{"outcome": "ready", "outputs": map[string]string{"document": id}, "reason": "Forward selected accepted draft", "task_action": nil})
		end, err := harnessResult(a, wire, t.TempDir(), func(string, []byte) error {
			t.Fatal("forwarding copied canonical artifact")
			return nil
		})
		if err != nil || end.Kind != "returned" || end.Result.Outputs["document"].ArtifactID != id {
			t.Fatalf("nested handoff lost identity: %+v %v", end, err)
		}
	}
	unknown := `{"outcome":"ready","outputs":{"document":"attempt_unseen/document"},"reason":"","task_action":null}`
	if end, err := harnessResult(a, []byte(unknown), t.TempDir(), nil); err != nil || end.Kind != "failed" {
		t.Fatalf("invented artifact authority accepted: %+v %v", end, err)
	}
}

func TestHarnessProvenanceExcludesSecretsAndBoundsEvents(t *testing.T) {
	e := harnessEvents{Limit: 64 << 10}
	for range 150 {
		e.accept([]byte(`{"type":"item.completed","item":{"type":"mcp_tool_call","server":"design","tool":"compare","arguments":{"api_key":"NEVER_RETAIN"},"result":{"private":"NEVER_RETAIN"},"status":"completed"}}`))
		e.accept([]byte(`{"type":"item.completed","item":{"type":"reasoning","text":"PRIVATE_REASONING"}}`))
	}
	e.accept([]byte(`{"type":"turn.completed","usage":{"input_tokens":123,"output_tokens":45,"cached_input_tokens":67}}`))
	data, err := json.Marshal(e.Proof)
	if err != nil || len(e.Proof.Used) != maxNativeFacts || e.Proof.Dropped != 54 || strings.Contains(string(data), "NEVER_RETAIN") || strings.Contains(string(data), "PRIVATE_REASONING") || e.Usage.InputTokens == nil || *e.Usage.CachedInputTokens != 67 {
		t.Fatalf("unsafe/unbounded telemetry: %s %+v %v", data, e.Usage, err)
	}
}

func TestHarnessClaudeUsageIncludesCache(t *testing.T) {
	e := harnessEvents{Limit: 1024}
	e.accept([]byte(`{"type":"result","usage":{"input_tokens":12,"output_tokens":8135,"cache_read_input_tokens":4000,"cache_creation_input_tokens":6000}}`))
	if e.Usage.InputTokens == nil || *e.Usage.InputTokens != 10012 || e.Usage.CachedInputTokens == nil || *e.Usage.CachedInputTokens != 4000 || *e.Usage.OutputTokens != 8135 {
		t.Fatal("Claude cached input was omitted", e.Usage)
	}
	e.accept([]byte(`{"type":"result","usage":{"input_tokens":12,"output_tokens":8135}}`))
	if e.Usage.InputTokens != nil || e.Usage.CachedInputTokens != nil || e.Usage.OutputTokens == nil {
		t.Fatal("missing Claude cache telemetry was treated as zero", e.Usage)
	}
}

func TestHarnessSelectedToolsAreEnforcedArguments(t *testing.T) {
	a := harnessAssignment(t)
	a.Setup.Profile.Native = &runner.NativeProfile{Harness: "claude", Tools: runner.CapabilitySelection{Mode: "selected", Names: []string{"Read", "Grep"}}, Skills: runner.CapabilitySelection{Mode: "none"}}
	if err := a.Setup.Profile.Native.Validate(); err != nil {
		t.Fatal(err)
	}
	args := strings.Join(harnessArgs(harnessSpec{Assignment: a}, "schema", []byte(`{}`)), "\x00")
	for _, required := range []string{"--restricted", "--strict-mcp-config", "--tools\x00Read,Grep", "--disable-slash-commands", "--permission-mode\x00dontAsk"} {
		if !strings.Contains(args, required) {
			t.Fatal("missing enforced restriction", required, args)
		}
	}
	a.Setup.Profile.Native.Tools.Names = []string{"Bash"}
	if err := a.Setup.Profile.Native.Validate(); err == nil {
		t.Fatal("unsupported tools were silently permitted")
	}
}

func TestHarnessRejectsCoordinatorSourceBeforeDispatch(t *testing.T) {
	for _, placement := range []string{"same", "inside", "ancestor", "symlink", "case-alias", "root"} {
		t.Run(placement, func(t *testing.T) {
			d := workspaceDisk(t)
			a := harnessAssignment(t)
			a.TaskContext = &runner.TaskContext{Goal: "Do not write coordinator state"}
			a.Setup.Profile.Access = "workspace-write"
			a.WorkDir = d.Root
			switch placement {
			case "inside":
				a.WorkDir = filepath.Join(d.Root, "work")
			case "ancestor":
				a.WorkDir = filepath.Dir(d.Root)
			case "symlink":
				a.WorkDir = filepath.Join(t.TempDir(), "alias")
				if err := os.Symlink(d.Root, a.WorkDir); err != nil {
					t.Fatal(err)
				}
			case "case-alias":
				a.WorkDir = strings.ToUpper(d.Root)
				if _, err := os.Stat(a.WorkDir); err != nil {
					t.Skip("filesystem does not alias differently cased paths")
				}
			case "root":
				a.WorkDir = string(filepath.Separator)
			}
			n := NewNative(d, nil, nil, nil)
			defer n.Close()
			dispatched := false
			n.launchWorker = func(string, func()) error { dispatched = true; return nil }
			job := harnessJob{Assignment: a, Stage: "allocated"}
			if err := n.prepareWorker(t.Context(), &job); err == nil || dispatched {
				t.Fatal("coordinator source boundary was admitted", err, dispatched)
			}
		})
	}
}

func TestHarnessClaudeEvidenceUsesPrivateReadRoot(t *testing.T) {
	d := workspaceDisk(t)
	a := harnessAssignment(t)
	a.Setup.Profile.Access = "workspace-read"
	a.Setup.Profile.Native = &runner.NativeProfile{
		Harness: "claude", Binary: "/bin/echo",
		Tools:  runner.CapabilitySelection{Mode: "selected", Names: []string{"Read"}},
		Skills: runner.CapabilitySelection{Mode: "none"},
	}
	a.WorkDir = t.TempDir()
	a.TaskContext = &runner.TaskContext{Goal: "Read supplied evidence"}
	id := "attempt_source/document"
	a.Inputs["document"] = runner.Value{ArtifactID: id}
	if err := d.Write("artifacts/"+id, []byte("accepted artifact"), a.Limits.MaxArtifact); err != nil {
		t.Fatal(err)
	}
	n := NewNative(d, nil, nil, nil)
	defer n.Close()
	var spec harnessSpec
	n.launchWorker = func(file string, _ func()) error {
		var err error
		spec, err = readHarnessSpec(file)
		return err
	}
	job := harnessJob{Assignment: a, Stage: "allocated"}
	if err := n.save(job); err != nil {
		t.Fatal(err)
	}
	if err := n.prepareWorker(t.Context(), &job); err != nil {
		t.Fatal(err)
	}
	alias := harnessArtifactFiles(job.Assignment)[id]
	canonical, err := os.Stat(filepath.Join(d.Root, "artifacts", id))
	if err != nil {
		t.Fatal(err)
	}
	linked, err := os.Stat(alias)
	if err != nil || !os.SameFile(canonical, linked) {
		t.Fatal("evidence was copied or absent", err)
	}
	allowed := filepath.Join(spec.Control, "evidence")
	if filepath.Dir(alias) != allowed || pathsOverlap(allowed, spec.Directory) || pathsOverlap(allowed, spec.Assignment.OutputDir) || pathsOverlap(allowed, filepath.Join(spec.Control, "tmp")) {
		t.Fatal("evidence is reachable through a writable task root", alias)
	}
	args := strings.Join(harnessArgs(spec, "schema", []byte(`{}`)), "\x00")
	if !strings.Contains(args, "--add-dir\x00"+allowed) || strings.Contains(args, "--add-dir\x00"+d.Root+"\x00") {
		t.Fatal("Claude did not receive the exact private read root", args)
	}
}

func TestHarnessActiveFreeSpaceReserveStopsSameAttempt(t *testing.T) {
	d := workspaceDisk(t)
	a := harnessAssignment(t)
	a.WorkDir = filepath.Join(d.Root, "work", a.AttemptID)
	n := NewNative(d, nil, nil, nil)
	defer n.Close()
	job := harnessJob{Assignment: a, Stage: "dispatching", Directory: t.TempDir()}
	if err := n.save(job); err != nil {
		t.Fatal(err)
	}
	if err := d.Write(harnessControl(a.AttemptID)+"/stop", []byte{0}, 1); err != nil {
		t.Fatal(err)
	}
	free, err := runner.FreeBytes(d.Root)
	if err != nil {
		t.Fatal(err)
	}
	d.Policy.MinFree = runner.Bytes(free + 1)
	o, err := n.Observe(t.Context(), a.AttemptID)
	if err != nil || !strings.Contains(o.Reason, "free-space reserve") {
		t.Fatalf("external-workspace free space was ignored: %+v %v", o, err)
	}
	flag, err := os.ReadFile(filepath.Join(d.Root, harnessControl(a.AttemptID), "stop"))
	if err != nil || len(flag) != 1 || flag[0] != 1 {
		t.Fatal("same native attempt was not stopped", err)
	}
}

func TestHarnessPrelaunchFailureHasTerminalReceipt(t *testing.T) {
	d := workspaceDisk(t)
	a := harnessAssignment(t)
	a.Limits.MaxLog = 1 // Cannot fit even the minimal provenance manifest.
	a.Setup.Profile.Native.Binary = "/bin/echo"
	n := NewNative(d, nil, nil, nil)
	defer n.Close()
	done := make(chan error, 1)
	n.launchWorker = func(file string, finished func()) error {
		go func() { done <- RunNativeWorker(context.Background(), file); finished() }()
		return nil
	}
	if _, err := n.Start(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	o, err := n.Observe(t.Context(), a.AttemptID)
	if err != nil || o.State != runner.Finished || o.End == nil || o.End.Kind != "failed" || !strings.Contains(o.End.Reason, "before model launch") || o.Effective == nil || o.Effective.Provenance != "" {
		t.Fatalf("unstarted worker failure became uncertain or masked: %+v %v", o, err)
	}
	if err := n.Release(t.Context(), a.AttemptID); err != nil {
		t.Fatal(err)
	}
}

func TestHarnessSettlesDetachedChildren(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "native-child-fixture")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", binary, "../../../testdata/native-children")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("fixture build: %v %s", err, output)
	}
	for _, mode := range []string{"detached", "double-fork", "double-fork-stdout", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("RUNNER_CHILD_FIXTURE", mode)
			d := workspaceDisk(t)
			a := harnessAssignment(t)
			a.Setup.Profile.Native.Binary = binary
			n := NewNative(d, nil, nil, nil)
			defer n.Close()
			done := make(chan error, 1)
			n.launchWorker = func(file string, finished func()) error {
				go func() { done <- RunNativeWorker(context.Background(), file); finished() }()
				return nil
			}
			if _, err := n.Start(t.Context(), a); err != nil {
				t.Fatal(err)
			}
			pidFile := filepath.Join(d.Root, harnessControl(a.AttemptID), "tmp", "escaped.pid")
			waitCommandFile(t, pidFile)
			data, err := os.ReadFile(pidFile)
			if err != nil {
				t.Fatal(err)
			}
			pid, err := strconv.Atoi(string(data))
			if err != nil || pid < 1 {
				t.Fatal("invalid owned fixture pid", err)
			}
			var spec harnessSpec
			specBytes, err := d.Read(harnessControl(a.AttemptID)+"/spec.json", d.Policy.Database.MaxRecord)
			if err != nil || json.Unmarshal(specBytes, &spec) != nil {
				t.Fatal("missing owned fixture spec", err)
			}
			// This fixture itself reports the exact resource it created. Retain
			// its kernel birth identity for finally cleanup of uncertainty cases.
			cleanupKnown := map[string]bool{}
			_ = rememberNativeRoot(pid, cleanupKnown)
			identity, _ := nativeChildIdentity(pid)
			t.Cleanup(func() {
				_ = killOwnedNativeProcess(identity, spec.Owner, cleanupKnown)
			})
			if mode == "cancel" {
				if err := n.Stop(t.Context(), a.AttemptID); err != nil {
					t.Fatal(err)
				}
			}
			workerErr := <-done
			if workerErr != nil {
				t.Fatal(workerErr)
			}
			o, err := n.Observe(t.Context(), a.AttemptID)
			want := "returned"
			if mode == "cancel" {
				want = "stopped"
			}
			if err != nil || o.State != runner.Finished || o.End == nil || o.End.Kind != want {
				t.Fatalf("detached child settlement: %+v %v", o, err)
			}
			var proof runner.NativeProvenance
			proofBytes, err := d.Read("artifacts/"+a.AttemptID+"/provenance.json", a.Limits.MaxLog)
			if err != nil || json.Unmarshal(proofBytes, &proof) != nil {
				t.Fatal("missing cleanup provenance", err)
			}
			found := false
			for _, fact := range proof.Used {
				found = found || fact.Kind == "owned_process" && fact.Name == "pid "+strconv.Itoa(pid) && fact.Status == "terminated"
			}
			if !found {
				t.Fatal("detached process was not terminated by worker", pid, string(proofBytes))
			}
			if err := n.Release(t.Context(), a.AttemptID); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestHarnessWorkerSurvivesOwnerAndCleansScratch(t *testing.T) {
	for _, stop := range []bool{false, true} {
		t.Run(map[bool]string{false: "result", true: "cancel"}[stop], func(t *testing.T) {
			a := harnessAssignment(t)
			d := workspaceDisk(t)
			a.WorkDir = filepath.Join(d.Root, "work", a.AttemptID)
			a.OutputDir = filepath.Join(d.Root, "artifacts", a.AttemptID)
			binary := filepath.Join(t.TempDir(), "fake-codex")
			script := "#!/bin/sh\nif [ \"$1\" = --version ]; then printf 'fixture 1\\n'; exit 0; fi\ncat >/dev/null\nprintf ready > \"$TMPDIR/ready\"\nwhile [ ! -f \"$TMPDIR/go\" ]; do sleep 0.02; done\nprintf '%s\\n' '{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"{\\\"outcome\\\":\\\"ready\\\",\\\"outputs\\\":{\\\"article\\\":\\\"Retained complete draft\\\"},\\\"reason\\\":\\\"Complete\\\"}\"}}'\nprintf '%s\\n' '{\"type\":\"turn.completed\",\"usage\":{\"input_tokens\":123,\"output_tokens\":45,\"cached_input_tokens\":0}}'\n"
			if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			a.Setup.Profile.Native.Binary = binary
			n := NewNative(d, nil, nil, nil)
			workerDone := make(chan error, 1)
			n.launchWorker = func(file string, done func()) error {
				go func() { workerDone <- RunNativeWorker(context.Background(), file); done() }()
				return nil
			}
			if _, err := n.Start(t.Context(), a); err != nil {
				t.Fatal(err)
			}
			scratch := filepath.Join(d.Root, harnessControl(a.AttemptID), "tmp")
			waitCommandFile(t, filepath.Join(scratch, "ready"))
			n.Close()
			reopened := NewNative(d, nil, nil, nil)
			defer reopened.Close()
			if _, err := reopened.Start(t.Context(), a); err != nil {
				t.Fatal(err)
			}
			if err := RunNativeWorker(t.Context(), filepath.Join(d.Root, harnessControl(a.AttemptID), "spec.json")); err == nil {
				t.Fatal("duplicate native dispatch admitted")
			}
			if stop {
				if err := reopened.Stop(t.Context(), a.AttemptID); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(filepath.Join(scratch, "go"), nil, 0600); err != nil {
				t.Fatal(err)
			}
			if err := <-workerDone; err != nil {
				t.Fatal(err)
			}
			o, err := reopened.Observe(t.Context(), a.AttemptID)
			want := "returned"
			if stop {
				want = "stopped"
			}
			if err != nil || o.End == nil || o.End.Kind != want {
				t.Fatalf("settlement: %+v %v", o, err)
			}
			if err := reopened.Release(t.Context(), a.AttemptID); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(scratch); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("native scratch retained", err)
			}
			if _, err := os.Stat(filepath.Join(d.Root, "artifacts", a.AttemptID, "provenance.json")); err != nil {
				t.Fatal("provenance was lost", err)
			}
			data, err := os.ReadFile(filepath.Join(d.Root, "artifacts", a.AttemptID, "provenance.json"))
			var proof runner.NativeProvenance
			if err != nil || json.Unmarshal(data, &proof) != nil || proof.PromptBytes == 0 || proof.SchemaBytes == 0 || proof.UsageScope != "cli-turn-cumulative" || proof.Context == nil || proof.Context.Mode != "native" {
				t.Fatal("prompt/schema measurement or cumulative usage scope missing", err)
			}
		})
	}
}

func TestHarnessProvisionalResultReconcilesAfterRestart(t *testing.T) {
	d := workspaceDisk(t)
	a := harnessAssignment(t)
	a.Setup.Profile.Native.Binary = "/bin/echo"
	n := NewNative(d, nil, nil, nil)
	launches := 0
	done := make(chan error, 1)
	var held *exec.Cmd
	article := strings.Repeat("Saved completed writing. ", 100)
	n.launchWorker = func(file string, finished func()) error {
		launches++
		go func() {
			defer finished()
			spec, err := readHarnessSpec(file)
			if err != nil {
				done <- err
				return
			}
			held = exec.Command("/bin/sleep", "60")
			held.Env = append(os.Environ(), nativeOwnerVariable+"="+spec.Owner)
			if err = held.Start(); err != nil {
				done <- err
				return
			}
			known := map[string]bool{}
			if err = rememberNativeRoot(held.Process.Pid, known); err != nil {
				done <- err
				return
			}
			proof, _ := json.Marshal(harnessProvenance(spec.Assignment, spec.Binary, spec.Directory))
			if err = workerWrite(filepath.Join(spec.Assignment.OutputDir, "provenance.json"), proof); err != nil {
				done <- err
				return
			}
			final, _ := json.Marshal(map[string]any{"outcome": "ready", "outputs": map[string]string{"article": article}, "reason": "Saved before cleanup"})
			input, output, cached := int64(123), int64(45), int64(0)
			receipt := harnessReceipt{
				AttemptID:     a.AttemptID,
				Kind:          "returned",
				Final:         final,
				ModelLaunched: true,
				Since:         time.Now().Add(-time.Second),
				CleanupReason: "transient cleanup failure",
				Usage:         runner.Usage{InputTokens: &input, OutputTokens: &output, CachedInputTokens: &cached},
			}
			done <- writeHarnessReceipt(spec, receipt, known)
		}()
		return nil
	}
	if _, err := n.Start(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = held.Process.Kill(); _ = held.Wait() })
	o, err := n.Observe(t.Context(), a.AttemptID)
	if err != nil || o.State != runner.Unknown || !o.Reconciling || o.End != nil || o.Usage.InputTokens == nil || *o.Usage.InputTokens != 123 {
		t.Fatalf("pending resource allowed completion or lost usage: %+v %v", o, err)
	}
	_ = held.Wait() // The first reconciliation terminated the exact owned child.
	if err := n.Stop(t.Context(), a.AttemptID); err != nil {
		t.Fatal(err)
	}
	n.Close()
	reopened := NewNative(d, nil, nil, nil)
	defer reopened.Close()
	if _, err := reopened.Start(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	// An unresolved pipe is independent of an empty process inventory. Even a
	// stale empty timestamp must not turn an expired observer into completion.
	var pending harnessReceipt
	data, err := d.Read(harnessControl(a.AttemptID)+"/result.json", d.Policy.Database.MaxRecord)
	if err != nil || json.Unmarshal(data, &pending) != nil {
		t.Fatal(err)
	}
	pending.OutputWritersPending, pending.OutputObserverExpired = true, true
	pending.EmptyObservedAt = time.Now().Add(-time.Hour)
	data, _ = json.Marshal(pending)
	if err := d.Write(harnessControl(a.AttemptID)+"/result.json", data, d.Policy.Database.MaxRecord); err != nil {
		t.Fatal(err)
	}
	o, err = reopened.Observe(t.Context(), a.AttemptID)
	if err != nil || o.State != runner.Unknown || o.End != nil || o.Reconciling {
		t.Fatalf("empty processes falsely resolved a lost pipe observer: %+v %v", o, err)
	}
	// The original observer's EOF is the authority to clear the pipe flag.
	pending.OutputWritersPending, pending.OutputObserverExpired = false, false
	pending.EmptyObservedAt = time.Time{}
	data, _ = json.Marshal(pending)
	if err := d.Write(harnessControl(a.AttemptID)+"/result.json", data, d.Policy.Database.MaxRecord); err != nil {
		t.Fatal(err)
	}
	o, err = reopened.Observe(t.Context(), a.AttemptID)
	if err != nil || o.State != runner.Unknown || o.End != nil {
		t.Fatalf("missing empty-inventory confirmation: %+v %v", o, err)
	}
	timer := time.NewTimer(time.Until(o.NextCheck))
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-t.Context().Done():
		t.Fatal(t.Context().Err())
	}
	o, err = reopened.Observe(t.Context(), a.AttemptID)
	if err != nil || o.State != runner.Finished || o.End == nil || o.End.Kind != "returned" || launches != 1 {
		t.Fatalf("saved execution was not promoted without rerun: %+v launches=%d %v", o, launches, err)
	}
	var retained string
	_ = json.Unmarshal(o.End.Result.Outputs["article"].Inline, &retained)
	if retained != article {
		t.Fatal("provisional writing was lost or truncated")
	}
	if err := reopened.Release(t.Context(), a.AttemptID); err != nil {
		t.Fatal(err)
	}
}

func TestHarnessLargeContextUsesOneCanonicalRequest(t *testing.T) {
	d := workspaceDisk(t)
	d.Policy.Database.MaxRecord = 32 << 10
	a := harnessAssignment(t)
	a.Limits.MaxRecord = d.Policy.Database.MaxRecord
	a.Setup.Profile.MaxInput = 128 << 10
	a.Setup.Profile.MaxOutput = 4 << 10
	a.TaskContext = &runner.TaskContext{Goal: "Evaluate complete child reports"}
	for _, id := range []string{"first", "second", "third", "fourth"} {
		a.TaskContext.Results = append(a.TaskContext.Results, runner.RoleTaskResult{ID: id, Outputs: map[string]runner.Value{"report": runner.Text(strings.Repeat("x", 12<<10))}})
	}
	binary := filepath.Join(t.TempDir(), "fake-codex")
	progress, _ := json.Marshal(map[string]any{
		"type": "item.completed",
		"item": map[string]string{"type": "agent_message", "text": strings.Repeat("progress ", 1024)},
	})
	script := `#!/bin/sh
if [ "$1" = --version ]; then printf 'fixture 1\n'; exit 0; fi
cat >/dev/null
cat <<'RUNNER_EVENTS'
` + string(progress) + `
{"type":"item.completed","item":{"type":"agent_message","text":"{\"outcome\":\"ready\",\"outputs\":{\"article\":\"Complete\"},\"reason\":\"\",\"task_action\":null}"}}
RUNNER_EVENTS
`
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	a.Setup.Profile.Native.Binary = binary
	n := NewNative(d, nil, nil, nil)
	defer n.Close()
	done := make(chan error, 1)
	n.launchWorker = func(file string, finished func()) error {
		go func() { done <- RunNativeWorker(context.Background(), file); finished() }()
		return nil
	}
	o, err := n.Start(t.Context(), a)
	if err != nil || o.State != runner.Running {
		t.Fatalf("valid large context was rejected: %+v %v", o, err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{harnessName(a.AttemptID), harnessControl(a.AttemptID) + "/spec.json"} {
		data, err := d.Read(name, d.Policy.Database.MaxRecord)
		if err != nil || bytes.Contains(data, []byte(strings.Repeat("x", 100))) {
			t.Fatalf("context was repeated in metadata %s: %v", name, err)
		}
	}
	spec, err := readHarnessSpec(filepath.Join(d.Root, harnessControl(a.AttemptID), "spec.json"))
	if err != nil || len(spec.Assignment.TaskContext.Results) != 4 {
		t.Fatal("canonical context was not hydrated", err)
	}
	var report string
	_ = json.Unmarshal(spec.Assignment.TaskContext.Results[3].Outputs["report"].Inline, &report)
	if len(report) != 12<<10 {
		t.Fatal("child report was truncated")
	}
	o, err = n.Observe(t.Context(), a.AttemptID)
	if err != nil || o.State != runner.Finished || o.End == nil || o.End.Kind != "returned" {
		t.Fatalf("large context execution did not complete: %+v %v", o, err)
	}
	if err := n.Release(t.Context(), a.AttemptID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(d.Root, harnessControl(a.AttemptID), "assignment.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("resolved request was retained after release", err)
	}
}

func TestHarnessIgnoresUnrelatedHiddenEnvironmentOrphan(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "native-child-fixture")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", binary, "../../../testdata/native-children")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("fixture build: %v %s", err, output)
	}
	t.Setenv("RUNNER_CHILD_FIXTURE", "gated")
	d := workspaceDisk(t)
	a := harnessAssignment(t)
	a.Setup.Profile.Native.Binary = binary
	n := NewNative(d, nil, nil, nil)
	defer n.Close()
	done := make(chan error, 1)
	n.launchWorker = func(file string, finished func()) error {
		go func() { done <- RunNativeWorker(context.Background(), file); finished() }()
		return nil
	}
	if _, err := n.Start(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	scratch := filepath.Join(d.Root, harnessControl(a.AttemptID), "tmp")
	waitCommandFile(t, filepath.Join(scratch, "escaped.pid"))
	sentinelDir := t.TempDir()
	sentinel := exec.Command(binary, "--fork")
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, nativeOwnerVariable+"=") && !strings.HasPrefix(entry, "TMPDIR=") && !strings.HasPrefix(entry, "RUNNER_CHILD_FIXTURE=") {
			sentinel.Env = append(sentinel.Env, entry)
		}
	}
	sentinel.Env = append(sentinel.Env, "TMPDIR="+sentinelDir, "RUNNER_CHILD_FIXTURE=double-fork-system")
	if err := sentinel.Run(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(sentinelDir, "escaped.pid"))
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(data))
	if err != nil {
		t.Fatal(err)
	}
	identity, err := nativeChildIdentity(pid)
	if err != nil || identity.Identity == "" {
		t.Fatal("unrelated fixture was not alive", err)
	}
	known := map[string]bool{}
	if err := rememberNativeRoot(pid, known); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = killOwnedNativeProcess(identity, "unrelated-sentinel", known) })
	if err := os.WriteFile(filepath.Join(scratch, "go"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	o, err := n.Observe(t.Context(), a.AttemptID)
	if err != nil || o.State != runner.Finished || o.End == nil || o.End.Kind != "returned" {
		t.Fatalf("unrelated orphan blocked work: %+v %v", o, err)
	}
	current, err := nativeChildIdentity(pid)
	if err != nil || current.Identity != identity.Identity {
		t.Fatal("unrelated process was terminated", err)
	}
	if err := n.Release(t.Context(), a.AttemptID); err != nil {
		t.Fatal(err)
	}
}

func TestHarnessOutputPipeWaitSurvivesOwnerRestart(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "native-child-fixture")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", binary, "../../../testdata/native-children")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("fixture build: %v %s", err, output)
	}
	t.Setenv("RUNNER_CHILD_FIXTURE", "double-fork-system-stdout")
	d := workspaceDisk(t)
	a := harnessAssignment(t)
	a.Setup.Profile.Native.Binary = binary
	n := NewNative(d, nil, nil, nil)
	done := make(chan error, 1)
	n.launchWorker = func(file string, finished func()) error {
		go func() { done <- RunNativeWorker(context.Background(), file); finished() }()
		return nil
	}
	if _, err := n.Start(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	pidFile := filepath.Join(d.Root, harnessControl(a.AttemptID), "tmp", "escaped.pid")
	waitCommandFile(t, pidFile)
	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(data))
	if err != nil {
		t.Fatal(err)
	}
	identity, _ := nativeChildIdentity(pid)
	known := map[string]bool{}
	_ = rememberNativeRoot(pid, known)
	var spec harnessSpec
	data, err = d.Read(harnessControl(a.AttemptID)+"/spec.json", d.Policy.Database.MaxRecord)
	if err != nil || json.Unmarshal(data, &spec) != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = killOwnedNativeProcess(identity, spec.Owner, known); n.Close() })
	resultPath := filepath.Join(d.Root, harnessControl(a.AttemptID), "result.json")
	waitCommandFile(t, resultPath)
	data, err = os.ReadFile(resultPath)
	var provisional harnessReceipt
	if err != nil || json.Unmarshal(data, &provisional) != nil {
		t.Fatal(err)
	}
	if provisional.ChildrenSettled {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		// Some platforms expose the marker or captured the short-lived parent.
		// The independent synthetic receipt test covers sticky pipe uncertainty.
		t.Skip("kernel ownership was visible; worker closed the inherited pipe directly")
	}
	if !provisional.OutputWritersPending || len(provisional.Final) == 0 {
		t.Fatal("pending pipe lost completed writing")
	}
	o, err := n.Observe(t.Context(), a.AttemptID)
	if err != nil || o.State != runner.Unknown || o.End != nil {
		t.Fatalf("open writer allowed completion: %+v %v", o, err)
	}
	n.Close()
	reopened := NewNative(d, nil, nil, nil)
	defer reopened.Close()
	if _, err := reopened.Start(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	o, err = reopened.Observe(t.Context(), a.AttemptID)
	if err != nil || o.State != runner.Unknown || o.End != nil {
		t.Fatalf("restart forgot open output writer: %+v %v", o, err)
	}
	if err := killOwnedNativeProcess(identity, spec.Owner, known); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	o, err = reopened.Observe(t.Context(), a.AttemptID)
	if err != nil || o.State != runner.Finished || o.End == nil || o.End.Kind != "returned" {
		t.Fatalf("original EOF observer did not promote saved writing: %+v %v", o, err)
	}
	if err := reopened.Release(t.Context(), a.AttemptID); err != nil {
		t.Fatal(err)
	}
}

// This is a protocol/sandbox proof with deterministic shell commands and zero
// model calls. It is opt-in because normal tests do not require installed Codex.
func TestHarnessCodexReadOnlySourceWritableScratch(t *testing.T) {
	if os.Getenv("RUNNER_NATIVE_SANDBOX") != "1" {
		t.Skip("set RUNNER_NATIVE_SANDBOX=1 for installed Codex permission proof")
	}
	binary, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	a := harnessAssignment(t)
	a.Setup.Profile.Access = "workspace-read"
	root := t.TempDir()
	source := filepath.Join(root, "source")
	control := filepath.Join(root, "control")
	a.OutputDir = filepath.Join(root, "output")
	for _, path := range []string{source, a.OutputDir, filepath.Join(control, "tmp"), filepath.Join(root, "home")} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	spec := harnessSpec{Assignment: a, Root: root, Directory: source, Control: control}
	resultsPath, privatePath := filepath.Join(control, "results.json"), filepath.Join(root, "private")
	if err := os.WriteFile(resultsPath, []byte("complete read-only results"), 0400); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(privatePath, []byte("private coordinator contents"), 0600); err != nil {
		t.Fatal(err)
	}
	spec.ResultsFile = &runner.NativeInputFile{Path: resultsPath}
	config := "default_permissions=\"runner-native\"\n[permissions.runner-native]\nfilesystem=" + harnessFilesystem(spec) + "\n[permissions.runner-native.network]\nenabled=false\n"
	if err := os.WriteFile(filepath.Join(root, "home", "config.toml"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	script := `if printf denied > "$1/forbidden" 2>/dev/null; then exit 91; fi; printf scratch > "$2/allowed"; printf output > "$3/retained"; cat "$4" > "$2/results-read"; if chmod 0600 "$4" 2>/dev/null; then exit 92; fi; if cat "$5" >/dev/null 2>&1; then exit 93; fi`
	cmd := exec.CommandContext(t.Context(), binary, "sandbox", "-P", "runner-native", "-C", source, "--", "/bin/sh", "-c", script, "sh", source, filepath.Join(control, "tmp"), a.OutputDir, resultsPath, privatePath)
	cmd.Env = append(os.Environ(), "CODEX_HOME="+filepath.Join(root, "home"))
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("permission profile rejected: %v %s", err, output)
	}
	for _, path := range []string{filepath.Join(control, "tmp", "allowed"), filepath.Join(a.OutputDir, "retained")} {
		if _, err := os.Stat(path); err != nil {
			t.Fatal("owned output was not writable", err)
		}
	}
	if data, err := os.ReadFile(filepath.Join(control, "tmp", "results-read")); err != nil || string(data) != "complete read-only results" {
		t.Fatal("exact task-results read grant was unusable", err)
	}
}

func harnessAssignment(t *testing.T) runner.Assignment {
	t.Helper()
	return runner.Assignment{
		AttemptID:   "attempt_native",
		RunID:       "run_native",
		WorkDir:     filepath.Join(t.TempDir(), "work"),
		Task:        "Write a complete article",
		OutputTypes: map[string]string{"article": "text"},
		Inputs:      map[string]runner.Value{"brief": runner.Text("An accessible complete article")},
		Limits:      runner.ExecutionLimits{MaxRecord: 256 << 10, MaxArtifact: 1 << 20, MaxLog: 64 << 10},
		Deadline:    time.Now().Add(15 * time.Second),
		Setup: runner.ExecutionSetup{Profile: runner.Profile{
			Harness:   "native",
			Native:    &runner.NativeProfile{Harness: "codex", Tools: runner.CapabilitySelection{Mode: "native"}, Skills: runner.CapabilitySelection{Mode: "native"}},
			Access:    "input-only",
			Model:     "fixture",
			Reasoning: "high",
			MaxInput:  64 << 10,
			MaxOutput: 32 << 10,
		}},
	}
}
