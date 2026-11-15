//go:build darwin

package execution

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
)

func TestHarnessSharedCommandReadOnlyAndRetainedFailure(t *testing.T) {
	d := workspaceDisk(t)
	a := sharedCommandAssignment(t)
	private := filepath.Join(t.TempDir(), "configured-read")
	if err := os.WriteFile(private, []byte("fixed command input"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := d.Write("work/private-secret", []byte("coordinator-private"), 128); err != nil {
		t.Fatal(err)
	}
	runtimeRoot := t.TempDir()
	a.Setup.Profile.Command = &runner.Command{
		Args: []string{"/bin/sh", "-c", `set -eu
if printf forbidden > "$1/forbidden" 2>/dev/null; then exit 91; fi
if cat "$2" >/dev/null 2>&1; then exit 92; fi
if /bin/ls "${2%/*}" >/dev/null 2>&1; then exit 93; fi
test "$HOME" = "$EXPECTED_USER_HOME"
cat "$3" >/dev/null
printf retained-failing-evidence > "$4/evidence.txt"
printf shared-cache > "$5/allowed"
printf 'completed fixed checks; domain status=failed\n'
exit 1`, "shared-check", "{workspace}", filepath.Join(d.Root, "work", "private-secret"), private, "{scratch}", runtimeRoot},
		Env: map[string]string{"EXPECTED_USER_HOME": os.Getenv("HOME")}, Artifact: "evidence.txt",
		RuntimeWritePaths: []string{runtimeRoot}, ExitOutcomes: map[int]string{1: "ready"},
	}
	proof := runSharedCommandProof(t, d, a)
	if proof.Harness != "command/shared" || proof.Permitted.Tools.Mode != "selected" || len(proof.Permitted.RuntimeWritePaths) != 1 {
		t.Fatal("fixed command policy was not recorded", proof)
	}
	if _, err := os.Stat(filepath.Join(a.WorkDir, "forbidden")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("source write escaped read-only policy", err)
	}
	if data, err := os.ReadFile(filepath.Join(runtimeRoot, "allowed")); err != nil || string(data) != "shared-cache" {
		t.Fatal("explicit runtime root was not writable", err)
	}
}

func TestHarnessSharedCommandHeadlessChrome(t *testing.T) {
	if os.Getenv("RUNNER_NATIVE_BROWSER_CHECK") != "1" {
		t.Skip("set RUNNER_NATIVE_BROWSER_CHECK=1 for installed headless CFT proof")
	}
	d := workspaceDisk(t)
	a := sharedCommandAssignment(t)
	a.Deadline = time.Now().Add(30 * time.Second)
	script := filepath.Join(t.TempDir(), "headless-proof.mjs")
	data := `import fs from 'node:fs/promises';
import {chromium} from '/Users/dan/.codex/tools/browser-runtime/node_modules/playwright/index.mjs';
const [source,scratch,expectedHome]=process.argv.slice(2);
if(process.env.HOME!==expectedHome)throw new Error('HOME changed');
let denied=false;
try{await fs.writeFile(source+'/forbidden','must not write');}catch(e){denied=['EPERM','EACCES','EROFS'].includes(e.code);}
if(!denied)throw new Error('source write was allowed');
let browser;
try{
 browser=await chromium.launch({headless:true,executablePath:'/Users/dan/.codex/tools/browser-runtime/chrome'});
 const page=await browser.newPage();
 await page.goto('data:text/html,<title>Runner headless proof</title>');
 if(await page.title()!=='Runner headless proof')throw new Error('headless navigation failed');
 await fs.writeFile(scratch+'/evidence.txt','source-readonly; headless CFT '+browser.version()+'; HOME preserved');
 console.log('source-readonly headless CFT completed');
}finally{await browser?.close();}
`
	if err := os.WriteFile(script, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	a.Setup.Profile.Command = &runner.Command{
		Args:     []string{"/opt/homebrew/bin/node", script, "{workspace}", "{scratch}", os.Getenv("HOME")},
		Artifact: "evidence.txt", Network: true,
	}
	runSharedCommandProof(t, d, a)
}

func TestHarnessSharedCommandSQLiteScratch(t *testing.T) {
	d := workspaceDisk(t)
	a := sharedCommandAssignment(t)
	a.Setup.Profile.Command = &runner.Command{
		Args:     []string{"/usr/bin/sqlite3", "{scratch}/proof.db", "CREATE TABLE proof(value TEXT); INSERT INTO proof VALUES('own managed scratch'); SELECT value FROM proof;"},
		Artifact: "proof.db",
	}
	runSharedCommandProof(t, d, a)
}

func TestHarnessSharedCommandGoSQLiteScratch(t *testing.T) {
	d := workspaceDisk(t)
	a := sharedCommandAssignment(t)
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	a.Setup.Profile.Command = &runner.Command{
		Args: []string{binary, "-test.run=^TestHarnessSharedSQLiteProcess$", "--", "{scratch}/proof.db"},
		Env:  map[string]string{"RUNNER_SHARED_SQLITE_PROCESS": "1"}, Artifact: "proof.db",
	}
	runSharedCommandProof(t, d, a)
}

func TestHarnessSharedSQLiteProcess(t *testing.T) {
	if os.Getenv("RUNNER_SHARED_SQLITE_PROCESS") != "1" {
		t.Skip("internal modernc SQLite probe")
	}
	db, err := sql.Open("sqlite", "file:"+os.Args[len(os.Args)-1]+"?_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TABLE proof(value TEXT); INSERT INTO proof VALUES('own managed scratch');"); err != nil {
		t.Fatal(err)
	}
	var value string
	if err := db.QueryRow("SELECT value FROM proof").Scan(&value); err != nil || value != "own managed scratch" {
		t.Fatal("SQLite roundtrip", value, err)
	}
}

func sharedCommandAssignment(t *testing.T) runner.Assignment {
	t.Helper()
	a := harnessAssignment(t)
	a.WorkDir = t.TempDir()
	a.Task = "Run the configured independent verifier"
	a.Inputs = nil
	a.TaskContext = &runner.TaskContext{RootRunID: a.RunID, Goal: a.Task, Brief: a.Task}
	a.OutputTypes = map[string]string{"check": "text", "artifact": "artifact"}
	a.Setup.Profile = runner.Profile{Harness: "command", Mode: "shared", Access: "workspace-read"}
	return a
}

func runSharedCommandProof(t *testing.T, d *runner.Disk, a runner.Assignment) runner.NativeProvenance {
	t.Helper()
	s := NewService(d, nil, nil)
	defer s.Close(context.Background())
	done := make(chan error, 1)
	workerFinished := false
	defer func() {
		_ = s.Stop(context.Background(), a.AttemptID)
		if !workerFinished {
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Error("fixed command test cleanup did not settle")
			}
		}
		if o, err := s.Observe(context.Background(), a.AttemptID); err == nil && o.End != nil {
			if err := s.Release(context.Background(), a.AttemptID); err != nil {
				t.Error("fixed command test release", err)
			}
		}
	}()
	s.harnesses.launchWorker = func(file string, finished func()) error {
		go func() { done <- RunNativeWorker(context.Background(), file); finished() }()
		return nil
	}
	if _, err := s.Start(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		workerFinished = true
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(35 * time.Second):
		_ = s.Stop(context.Background(), a.AttemptID)
		t.Fatal("fixed command did not settle")
	}
	o, err := s.Observe(t.Context(), a.AttemptID)
	if err != nil || o.End == nil || o.End.Kind != "returned" || o.End.Result.Outcome != "ready" {
		data, _ := json.Marshal(o)
		t.Fatalf("fixed command collection: %s %v", data, err)
	}
	if o.Usage.InputTokens == nil || *o.Usage.InputTokens != 0 || o.Usage.OutputTokens == nil || *o.Usage.OutputTokens != 0 {
		t.Fatal("fixed command should report no model usage", o.Usage)
	}
	job, err := s.harnesses.load(a.AttemptID)
	if err != nil || job.ScratchDir == "" {
		t.Fatal("owned short scratch was not persisted", err)
	}
	realScratch, err := filepath.EvalSymlinks(job.ScratchDir)
	if err != nil || realScratch != filepath.Join(d.Root, harnessControl(a.AttemptID), "tmp") {
		t.Fatal("browser bytes escaped managed scratch", err, realScratch)
	}
	id := o.End.Result.Outputs["artifact"].ArtifactID
	if id != a.AttemptID+"/result" {
		t.Fatal("fixed evidence lost canonical ownership", o.End.Result.Outputs)
	}
	evidence, err := d.Read("artifacts/"+id, a.Limits.MaxArtifact)
	if err != nil || len(evidence) == 0 {
		t.Fatal("fixed evidence was not collected", err)
	}
	if err = s.Release(t.Context(), a.AttemptID); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(job.ScratchDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("owned browser scratch retained", err)
	}
	data, err := d.Read("artifacts/"+a.AttemptID+"/provenance.json", 64<<10)
	var proof runner.NativeProvenance
	if err != nil || json.Unmarshal(data, &proof) != nil || proof.CompletedAt.IsZero() || proof.StartupMillis < 0 || strings.Contains(string(data), "coordinator-private") {
		t.Fatal("invalid retained command audit", err, string(data))
	}
	return proof
}
