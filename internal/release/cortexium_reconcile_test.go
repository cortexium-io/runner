//go:build darwin || linux

package release

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// These helpers execute only this test binary and a fixed simulated Docker
// protocol. They never invoke Docker, SSH, a browser, App source or a model.
func TestCortexiumReconcileWorkerProcess(t *testing.T) {
	file := os.Getenv("RUNNER_CORTEXIUM_RECONCILE_WORK")
	if file == "" {
		return
	}
	if err := RunDeliveryWorker(file); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(3)
	}
	os.Exit(0)
}

type cleanupFakeEngine struct {
	Engine string
	Jobs   map[string]Job
	Hold   map[string]bool
}

func TestCortexiumReconcileDockerProcess(t *testing.T) {
	root := os.Getenv("RUNNER_CORTEXIUM_RECONCILE_FAKE")
	if root == "" {
		return
	}
	var engine cleanupFakeEngine
	data, err := os.ReadFile(filepath.Join(root, "engine.json"))
	if err != nil || json.Unmarshal(data, &engine) != nil {
		os.Exit(50)
	}
	var args []string
	for i, arg := range os.Args {
		if arg == "--" {
			args = os.Args[i+1:]
			break
		}
	}
	if len(args) < 3 || args[0] != "--host" || args[1] != "fixture-only" {
		os.Exit(51)
	}
	args = args[2:]
	if args[0] == "info" {
		fmt.Print(engine.Engine)
		os.Exit(0)
	}
	if args[0] == "ps" || args[0] == "volume" && args[1] == "ls" {
		os.Exit(0)
	}
	if args[0] == "image" {
		for key, job := range engine.Jobs {
			id := "sha256:" + Digest([]byte(key))
			switch args[1] {
			case "ls":
				if args[len(args)-1] == "reference="+cortexiumTag(&job) {
					if _, err := os.Stat(filepath.Join(root, key+"-removed")); os.IsNotExist(err) {
						fmt.Print(id)
					}
					os.Exit(0)
				}
			case "inspect":
				if args[2] == id {
					v := cortexiumImage{ID: id}
					v.Config.Labels = cortexiumLabels(&job, "image")
					_ = json.NewEncoder(os.Stdout).Encode([]cortexiumImage{v})
					os.Exit(0)
				}
			case "rm":
				if args[2] == id {
					count, _ := os.OpenFile(filepath.Join(root, key+"-calls"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
					_, _ = count.WriteString("remove\n")
					_ = count.Close()
					_ = os.WriteFile(filepath.Join(root, key+"-entered"), []byte("entered\n"), 0600)
					for engine.Hold[key] {
						if _, err := os.Stat(filepath.Join(root, key+"-release")); err == nil {
							break
						}
						time.Sleep(2 * time.Millisecond) // Observe an explicit test barrier.
					}
					_ = os.WriteFile(filepath.Join(root, key+"-removed"), []byte(id), 0600)
					os.Exit(0)
				}
			}
		}
	}
	fmt.Fprintln(os.Stderr, "unexpected simulated cleanup command", args)
	os.Exit(52)
}

type cleanupWorkerVisit struct {
	cmd  *exec.Cmd
	done chan error
}

type cleanupWorkers struct {
	t      *testing.T
	root   string
	mu     sync.Mutex
	visits []*cleanupWorkerVisit
}

func (w *cleanupWorkers) launch(file string, lease *os.File) error {
	binary, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(binary, "-test.run=^TestCortexiumReconcileWorkerProcess$", "-test.timeout=1m")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Helpers have no reporting goroutines to flush after os.Exit. Keep race
	// detection while removing its per-command one-second exit grace.
	cmd.Env = append(os.Environ(), "GORACE=atexit_sleep_ms=0", "RUNNER_CORTEXIUM_RECONCILE_WORK="+file, "RUNNER_CORTEXIUM_RECONCILE_FAKE="+w.root)
	cmd.ExtraFiles = []*os.File{lease}
	if err := cmd.Start(); err != nil {
		return err
	}
	v := &cleanupWorkerVisit{cmd: cmd, done: make(chan error, 1)}
	w.mu.Lock()
	w.visits = append(w.visits, v)
	w.mu.Unlock()
	go func() { v.done <- cmd.Wait() }()
	return nil
}

func (w *cleanupWorkers) count() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.visits)
}

func (w *cleanupWorkers) settle() {
	// Release only this fixture's explicit fake barriers before waiting.
	var engine cleanupFakeEngine
	if data, err := os.ReadFile(filepath.Join(w.root, "engine.json")); err == nil && json.Unmarshal(data, &engine) == nil {
		for key := range engine.Jobs {
			_ = os.WriteFile(filepath.Join(w.root, key+"-release"), nil, 0600)
		}
	}
	w.mu.Lock()
	visits := append([]*cleanupWorkerVisit(nil), w.visits...)
	w.mu.Unlock()
	for _, v := range visits {
		select {
		case <-v.done:
		case <-time.After(5 * time.Second):
			_ = syscall.Kill(-v.cmd.Process.Pid, syscall.SIGKILL)
			<-v.done
			w.t.Error("owned synthetic worker needed teardown")
		}
	}
}

func cleanupWaitFile(t *testing.T, file string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(file); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("owned cleanup barrier absent", filepath.Base(file))
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func cleanupOperatorFixture(t *testing.T) (*Operator, *Live, *cleanupWorkers) {
	t.Helper()
	o, _ := cortexiumPolicy(t)
	cfg := o.Backend.(*Live).Config
	root := t.TempDir()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Docker = filepath.Join(root, "fixed-cleanup-protocol")
	body := "#!/bin/sh\nexec " + quote(binary) + " -test.run=^TestCortexiumReconcileDockerProcess$ -test.timeout=1m -- \"$@\"\n"
	if err := os.WriteFile(cfg.Docker, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	cfg.DockerHost = "fixture-only"
	o, err = loadCortexium(cfg)
	if err != nil {
		t.Fatal(err)
	}
	b := o.Backend.(*Live)
	workers := &cleanupWorkers{t: t, root: root}
	b.launchDeliveryWorker = workers.launch
	t.Cleanup(workers.settle)
	return o, b, workers
}

func cleanupSavedJob(t *testing.T, o *Operator, run, key string) Job {
	t.Helper()
	_, q := cortexiumPolicy(t)
	q.RunID, q.Key, q.Cortexium.Transport, q.Cortexium.PolicySHA256 = run, key, "", o.PolicyHash
	root := filepath.Join(o.Directory, "work", q.RunID, q.Key)
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	j := Job{
		Request: q,
		Receipt: Receipt{
			Key:         key,
			RunID:       q.RunID,
			Action:      q.Action,
			Policy:      o.PolicyHash,
			Source:      q.Source,
			BuildSHA256: q.Cortexium.SHA256,
		},
		Worker:            filepath.Join(root, "work.json"),
		CortexiumDeadline: time.Now().Add(10 * time.Minute).UnixNano(),
		Failed:            true,
		Failure:           "original source failure; preserve this exact cause",
		Outcome:           "blocked",
	}
	b := o.Backend.(*Live)
	for file, value := range map[string]any{
		j.Worker:                                deliveryWork{Kind: cortexiumWorkKind, Config: b.Config, Job: j},
		filepath.Join(root, "result.json"):      j,
		filepath.Join(o.Directory, key+".json"): j,
	} {
		if err := writeJSON(file, value); err != nil {
			t.Fatal(err)
		}
	}
	for name, data := range map[string][]byte{
		"started":            nil,
		"resources-intent":   b.cortexiumIntent(&j),
		"input-request.json": mustCleanupJSON(t, q),
	} {
		if err := writeExclusive(filepath.Join(root, name), data); err != nil {
			t.Fatal(err)
		}
	}
	return j
}

func mustCleanupJSON(t *testing.T, value any) []byte {
	t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func cleanupEngine(t *testing.T, workers *cleanupWorkers, jobs []Job, hold map[string]bool) {
	t.Helper()
	e := cleanupFakeEngine{Engine: "fixture-engine", Jobs: map[string]Job{}, Hold: hold}
	for _, j := range jobs {
		e.Jobs[j.Request.Key] = j
	}
	if err := os.WriteFile(filepath.Join(workers.root, "engine.json"), mustCleanupJSON(t, e), 0600); err != nil {
		t.Fatal(err)
	}
}

func cleanupFinish(t *testing.T, o *Operator, q Request) Job {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		r, err := o.Call(t.Context(), q)
		if err != nil {
			t.Fatal(err)
		}
		if r.State == "finished" {
			data, _ := os.ReadFile(filepath.Join(o.Directory, q.Key+".json"))
			var j Job
			if json.Unmarshal(data, &j) != nil {
				t.Fatal("saved cleanup result absent")
			}
			return j
		}
		if time.Now().After(deadline) {
			t.Fatal("cleanup did not settle", r.State, r.Reason)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestCortexiumCleanupObservationRejoinsOneVisitAndLeavesSiblingFree(t *testing.T) {
	o, b, workers := cleanupOperatorFixture(t)
	slow, fast := cleanupSavedJob(t, o, "run_slow", "attempt_slow"), cleanupSavedJob(t, o, "run_fast", "attempt_fast")
	cleanupEngine(t, workers, []Job{slow, fast}, map[string]bool{slow.Request.Key: true})
	call, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	r, err := o.Call(call, slow.Request)
	if err != nil || r.State != "running" || !r.Reconciling || time.Since(started) > 500*time.Millisecond {
		t.Fatal("short observation blocked on cleanup", r, err, time.Since(started))
	}
	cleanupWaitFile(t, filepath.Join(workers.root, slow.Request.Key+"-entered"))
	spec, _ := os.ReadFile(filepath.Join(filepath.Dir(slow.Worker), "cleanup.json"))
	// Drop the original observer/ACK and reopen the same operator journal. The
	// inherited per-operation lease, not this Go object, owns the slow visit.
	reopened, err := loadCortexium(b.Config)
	if err != nil {
		t.Fatal(err)
	}
	reopened.Backend.(*Live).launchDeliveryWorker = workers.launch
	for range 3 {
		r, err = reopened.Call(t.Context(), slow.Request)
		if err != nil || r.State != "running" || !r.Reconciling {
			t.Fatal("same cleanup was not rejoined", r, err)
		}
	}
	if workers.count() != 1 {
		t.Fatal("lost ACK/restart launched replacement cleanup")
	}
	if _, err = reopened.Call(t.Context(), fast.Request); err != nil {
		t.Fatal(err)
	}
	done := cleanupFinish(t, reopened, fast.Request)
	if !done.Failed || done.Failure != fast.Failure || done.CortexiumCleanupFailure != "" {
		t.Fatal("sibling lost its original failure", done)
	}
	// Integrity loss and stop can only settle this same cleanup. They cannot
	// replace its input, renew its deadline or restart original execution.
	if err := os.WriteFile(filepath.Join(filepath.Dir(slow.Worker), "input.tar.gz"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	observe := slow.Request
	frame := *observe.Cortexium
	frame.Transport = "observe"
	observe.Cortexium = &frame
	if r, err = reopened.CallInput(t.Context(), observe, bytes.NewReader(nil)); err != nil || r.State != "running" || !r.Reconciling {
		t.Fatal("input drift lost original cleanup", r, err)
	}
	frame.Transport = "stop"
	if _, err = reopened.CallInput(t.Context(), observe, bytes.NewReader(nil)); err != nil {
		t.Fatal(err)
	}
	changed := slow.Request
	changed.Source = strings.Repeat("d", 40)
	if _, err := reopened.Call(t.Context(), changed); err == nil {
		t.Fatal("foreign input rejoined cleanup")
	}
	after, _ := os.ReadFile(filepath.Join(filepath.Dir(slow.Worker), "cleanup.json"))
	if !bytes.Equal(after, spec) || workers.count() != 2 {
		t.Fatal("cleanup identity/deadline changed or visit duplicated")
	}
	if err := os.WriteFile(filepath.Join(workers.root, slow.Request.Key+"-release"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	done = cleanupFinish(t, reopened, slow.Request)
	if done.Failure != slow.Failure || !done.Failed || !done.Done || done.CortexiumCleanupFailure != "" {
		t.Fatal("stop masked failure or invented uncertainty", done)
	}
	for _, j := range []Job{slow, fast} {
		calls, _ := os.ReadFile(filepath.Join(workers.root, j.Request.Key+"-calls"))
		if string(calls) != "remove\n" {
			t.Fatal("cleanup mutation was repeated", j.Request.Key, string(calls))
		}
	}
}

func cleanupBudgetFixture(t *testing.T, b *Live, j Job, remaining time.Duration) deliveryWork {
	t.Helper()
	data, err := os.ReadFile(j.Worker)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(remaining)
	j.CortexiumCleanupDeadline = deadline.UnixNano()
	work := deliveryWork{
		Kind: cortexiumCleanupKind,
		Cleanup: &cortexiumCleanupBinding{
			OriginalSHA256: Digest(data),
			StartedAt:      deadline.Add(-cortexiumCleanupTime).UnixNano(),
			Deadline:       deadline.UnixNano(),
		},
		Config: b.Config,
		Job:    j,
	}
	if err := writeJSON(filepath.Join(filepath.Dir(j.Worker), "cleanup.json"), work); err != nil {
		t.Fatal(err)
	}
	return work
}

func cleanupLeaseFree(t *testing.T, j Job) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		f, err := os.OpenFile(filepath.Join(filepath.Dir(j.Worker), "worker.lock"), os.O_RDWR, 0600)
		if err != nil {
			t.Fatal(err)
		}
		err = lockOperator(f)
		if err == nil {
			unlockOperator(f)
			f.Close()
			return
		}
		f.Close()
		if time.Now().After(deadline) {
			t.Fatal("owned cleanup lease did not settle", err)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestCortexiumCleanupExhaustionAndCrashNeverMintAnotherVisit(t *testing.T) {
	for _, crash := range []bool{false, true} {
		t.Run(fmt.Sprint("worker-crash=", crash), func(t *testing.T) {
			o, b, workers := cleanupOperatorFixture(t)
			j := cleanupSavedJob(t, o, "run_bounded", "attempt_bounded")
			cleanupEngine(t, workers, []Job{j}, map[string]bool{j.Request.Key: true})
			work := cleanupBudgetFixture(t, b, j, 2*time.Second)
			path := filepath.Join(filepath.Dir(j.Worker), "cleanup.json")
			before, _ := os.ReadFile(path)
			if r, err := o.Call(t.Context(), j.Request); err != nil || r.State != "running" || !r.Reconciling {
				t.Fatal(r, err)
			}
			cleanupWaitFile(t, filepath.Join(workers.root, j.Request.Key+"-entered"))
			if crash {
				workers.mu.Lock()
				worker := workers.visits[0].cmd
				workers.mu.Unlock()
				// Only the owning worker is killed. The simulated command still
				// holds its inherited lease until its explicit barrier is released.
				if err := worker.Process.Kill(); err != nil {
					t.Fatal(err)
				}
			}
			deadline := time.Unix(0, work.Cleanup.Deadline)
			<-time.After(time.Until(deadline))
			reopened, err := loadCortexium(b.Config)
			if err != nil {
				t.Fatal(err)
			}
			reopened.Backend.(*Live).launchDeliveryWorker = workers.launch
			for range 3 {
				r, err := reopened.Call(t.Context(), j.Request)
				if err != nil || r.State != "unknown" || r.Reconciling || !strings.Contains(r.Reason, "needs operator") {
					t.Fatal("exhausted cleanup concealed attention", r, err)
				}
			}
			if crash {
				if err := os.WriteFile(filepath.Join(workers.root, j.Request.Key+"-release"), nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			cleanupLeaseFree(t, j)
			for range 3 {
				r, err := reopened.Call(t.Context(), j.Request)
				if err != nil || r.State != "unknown" || r.Reconciling {
					t.Fatal("restart replayed an exhausted cleanup", r, err)
				}
			}
			after, _ := os.ReadFile(path)
			calls, _ := os.ReadFile(filepath.Join(workers.root, j.Request.Key+"-calls"))
			if !bytes.Equal(before, after) || workers.count() != 1 || string(calls) != "remove\n" {
				t.Fatal("cleanup deadline/visit was renewed", workers.count(), string(calls))
			}
			data, _ := os.ReadFile(filepath.Join(o.Directory, j.Request.Key+".json"))
			var saved Job
			if json.Unmarshal(data, &saved) != nil || saved.Done || saved.Failure != j.Failure || saved.CortexiumCleanupFailure == "" || saved.CortexiumCleanupDeadline != work.Cleanup.Deadline {
				t.Fatal("primary failure or cleanup uncertainty lost", saved)
			}
		})
	}
}

func TestCortexiumCleanupRefusesUnsupportedDriftedAndMissingAuthority(t *testing.T) {
	for _, fault := range []string{
		"old-work", "expired", "claimed", "foreign-claim", "foreign-source",
		"foreign-cleanup", "deadline-drift", "primary-failure", "false-settlement", "lost-spec",
	} {
		t.Run(fault, func(t *testing.T) {
			o, b, workers := cleanupOperatorFixture(t)
			j := cleanupSavedJob(t, o, "run_refusal", "attempt_refusal")
			root := filepath.Dir(j.Worker)
			source, _ := os.ReadFile(j.Worker)
			if fault != "old-work" && fault != "foreign-source" && fault != "false-settlement" && fault != "lost-spec" {
				remaining := time.Minute / 2
				if fault == "expired" {
					remaining = -time.Second
				}
				work := cleanupBudgetFixture(t, b, j, remaining)
				switch fault {
				case "foreign-cleanup":
					work.Config.Engine = "foreign-engine"
				case "deadline-drift":
					work.Cleanup.Deadline += int64(time.Hour)
				case "primary-failure":
					work.Job.Failure = "different failure"
				}
				if err := writeJSON(filepath.Join(root, "cleanup.json"), work); err != nil {
					t.Fatal(err)
				}
				if fault == "claimed" || fault == "foreign-claim" {
					data, _ := os.ReadFile(filepath.Join(root, "cleanup.json"))
					claim := Digest(data) + "\n"
					if fault == "foreign-claim" {
						claim = "foreign\n"
					}
					if err := writeExclusive(filepath.Join(root, "cleanup-started"), []byte(claim)); err != nil {
						t.Fatal(err)
					}
				}
			} else {
				switch fault {
				case "old-work", "foreign-source":
					var work deliveryWork
					_ = json.Unmarshal(source, &work)
					if fault == "old-work" {
						work.Kind = ""
					} else {
						work.Job.Request.Source = strings.Repeat("f", 40)
					}
					if err := writeJSON(j.Worker, work); err != nil {
						t.Fatal(err)
					}
				case "false-settlement":
					result := j
					result.Done = true
					if err := writeJSON(filepath.Join(root, "result.json"), result); err != nil {
						t.Fatal(err)
					}
				case "lost-spec":
					j.CortexiumCleanupDeadline = time.Now().Add(time.Second).UnixNano()
					if err := writeJSON(filepath.Join(o.Directory, j.Request.Key+".json"), j); err != nil {
						t.Fatal(err)
					}
					for _, name := range []string{"result.json", "started"} {
						if err := os.Remove(filepath.Join(root, name)); err != nil {
							t.Fatal(err)
						}
					}
				}
			}
			before, _ := os.ReadFile(j.Worker)
			for range 2 {
				r, err := o.Call(t.Context(), j.Request)
				if err != nil || r.State != "unknown" || r.Reconciling || !strings.Contains(r.Reason, "needs operator") {
					t.Fatal("unsafe cleanup was not held", r, err)
				}
			}
			after, _ := os.ReadFile(j.Worker)
			if workers.count() != 0 || !bytes.Equal(before, after) {
				t.Fatal("refusal modified original work or dispatched a worker")
			}
		})
	}
}

func TestCortexiumCleanupLegacyWireAndWorkStayUnchanged(t *testing.T) {
	encoded := string(mustCleanupJSON(t, deliveryWork{Config: Config{Project: "legacy"}, Job: Job{}}))
	for _, field := range []string{"\"kind\"", "\"cleanup\"", "cortexium_cleanup_"} {
		if strings.Contains(encoded, field) {
			t.Fatal("legacy work gained Cortexium state", field)
		}
	}
	encoded = string(mustCleanupJSON(t, Response{Key: "legacy", State: "running"}))
	if encoded != `{"key":"legacy","state":"running"}` {
		t.Fatal("legacy response changed", encoded)
	}
}

func TestCortexiumCleanupExpiredOriginalWriterRequiresAttention(t *testing.T) {
	o, b, workers := cleanupOperatorFixture(t)
	j := cleanupSavedJob(t, o, "run_expired_writer", "attempt_expired_writer")
	j.CortexiumDeadline = time.Now().Add(-time.Second).UnixNano()
	if err := writeJSON(j.Worker, deliveryWork{Kind: cortexiumWorkKind, Config: b.Config, Job: j}); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(o.Directory, j.Request.Key+".json"), j); err != nil {
		t.Fatal(err)
	}
	lease, err := os.OpenFile(filepath.Join(filepath.Dir(j.Worker), "worker.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	if err := lockOperator(lease); err != nil {
		t.Fatal(err)
	}
	defer unlockOperator(lease)
	r, err := o.Call(t.Context(), j.Request)
	if err != nil || r.State != "unknown" || r.Reconciling || !strings.Contains(r.Reason, "writer lease remains active") || workers.count() != 0 {
		t.Fatal("expired original writer concealed attention or launched cleanup", r, err)
	}
}

func TestCortexiumCleanupOlderReaderRefusesNewSpecBeforeDispatch(t *testing.T) {
	old := os.Getenv("RUNNER_CORTEXIUM_OLD_OPERATOR")
	if old == "" {
		t.Skip("explicit preserved60bb operator compatibility check")
	}
	binary, err := os.ReadFile(old)
	if err != nil || Digest(binary) != "1746d3b2e80060cf822a03db09dcb42e15313cece8d5ef726721ae1e8409c575" && Digest(binary) != "1eb6b319ca342ca31938d67a83b9999e5080605e70cbada1bf1e995c87164f9e" {
		t.Fatal("older operator is not an exact preserved60bb binary", err)
	}
	o, b, workers := cleanupOperatorFixture(t)
	j := cleanupSavedJob(t, o, "run_compat", "attempt_compat")
	cleanupBudgetFixture(t, b, j, 30*time.Second)
	file := filepath.Join(filepath.Dir(j.Worker), "cleanup.json")
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, old, "--delivery-worker", file).CombinedOutput()
	if err == nil || !bytes.Contains(output, []byte("invalid private delivery work")) || workers.count() != 0 {
		t.Fatal("old reader did not refuse before dispatch", string(output), err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(j.Worker), "cleanup-started")); !os.IsNotExist(err) {
		t.Fatal("old reader claimed cleanup", err)
	}
}
