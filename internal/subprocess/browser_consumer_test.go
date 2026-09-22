//go:build darwin || linux

package subprocess

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/securefs"
)

// This opt-in integration fixture imports the actual selected consumer module;
// no private application code is copied into Runner. The operator pins its
// exact bytes. No browser, dependency install, live model or real host claim is
// involved. Ordinary OS ownership fixtures run independently of this check.
func TestHeavyActualBrowserConsumerParentDeath(t *testing.T) {
	module := os.Getenv("RUNNER_BROWSER_CONSUMER_MODULE")
	if module == "" {
		t.Skip("set RUNNER_BROWSER_CONSUMER_MODULE and SHA256 to check the actual managed browser launcher")
	}
	content, err := os.ReadFile(module)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(content)
	if hex.EncodeToString(digest[:]) != os.Getenv("RUNNER_BROWSER_CONSUMER_SHA256") {
		t.Fatal("consumer source differs from explicitly selected hash")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	for _, args := range [][]string{{"init", "-b", "main"}, {"config", "user.email", "fixture@example.invalid"}, {"config", "user.name", "Fixture"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git: %s %v", out, err)
		}
	}
	if err := os.Mkdir(filepath.Join(root, "node_modules"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("node_modules/\ntest-results/\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "."}, {"commit", "-m", "fixture"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git: %s %v", out, err)
		}
	}
	readyPath := filepath.Join(t.TempDir(), "ready")
	if err := syscall.Mkfifo(readyPath, 0600); err != nil {
		t.Fatal(err)
	}
	ready, err := os.OpenFile(readyPath, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer ready.Close()
	readyJSON, _ := json.Marshal(readyPath)
	server := filepath.Join(t.TempDir(), "managed-server.mjs")
	source := fmt.Sprintf(`import { createServer } from 'node:http'; import { writeFileSync } from 'node:fs';
const server = createServer((q,r) => r.end('fixture'));
server.listen(0,'127.0.0.1',()=>{
process.send({baseURL:'http://127.0.0.1:'+server.address().port});
writeFileSync(%s, JSON.stringify({pid:process.pid,parent:process.ppid,outer:process.env.CORTEXIUM_RUNNER_PROCESS_OWNER,heavy:process.env.CORTEXIUM_RUNNER_HEAVY_OWNER,leaked:process.env.RUNNER_FIXTURE_SECRET??null})+'\n');
});`, readyJSON)
	if err := os.WriteFile(server, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	_, outer, err := PrepareHarness(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	owner := &invocationOwnership{marker: outer}
	defer owner.cleanup()
	claimRoot := filepath.Join(t.TempDir(), "claims")
	helper := exec.Command(os.Args[0], "-test.run=^TestHeavyActualBrowserConsumerHelper$")
	helper.Env = append(os.Environ(), "RUNNER_BROWSER_HELPER_CLAIM="+claimRoot, "RUNNER_BROWSER_HELPER_ROOT="+root, "RUNNER_BROWSER_HELPER_SERVER="+server, "RUNNER_BROWSER_HELPER_NODE="+node, ownershipVariable+"="+outer, "RUNNER_FIXTURE_SECRET=must-not-forward")
	var log bytes.Buffer
	helper.Stdout, helper.Stderr = &log, &log
	if err := helper.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = helper.Process.Kill(); _ = helper.Wait() }()
	line := make(chan string, 1)
	go func() { s, _ := bufio.NewReader(ready).ReadString('\n'); line <- s }()
	var child struct {
		PID    int     `json:"pid"`
		Parent int     `json:"parent"`
		Outer  string  `json:"outer"`
		Heavy  string  `json:"heavy"`
		Leaked *string `json:"leaked"`
	}
	select {
	case data := <-line:
		if err := json.Unmarshal([]byte(data), &child); err != nil {
			t.Fatal(err)
		}
	case <-time.After(15 * time.Second):
		_ = helper.Process.Kill()
		_ = helper.Wait()
		t.Fatalf("managed consumer did not start: %s", log.String())
	}
	// If the broken consumer drops markers, still clean the exact synthetic server.
	serverIdentity, err := inspectProcess(child.PID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if current, err := inspectProcess(child.PID); err == nil && current.birth == serverIdentity.birth {
			_ = syscall.Kill(child.PID, syscall.SIGKILL)
		}
	}()
	dir, err := securefs.OpenDir(claimRoot)
	if err != nil {
		t.Fatal(err)
	}
	record, _, err := readHeavyClaim(dir)
	_ = dir.Close()
	if err != nil || record == nil {
		t.Fatalf("claim missing: %v", err)
	}
	defer (&invocationOwnership{marker: record.Marker, variable: HeavyOwnershipEnvironmentVariable}).cleanup()
	if child.Outer != outer || child.Heavy != record.Marker || child.Leaked != nil {
		t.Fatal("actual consumer did not preserve only the two explicit ownership markers")
	}
	parent, err := inspectProcess(child.Parent)
	if err != nil {
		t.Fatal(err)
	}
	if err := helper.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = helper.Wait()
	// Simulate the browser launcher dying without its finally cleanup, using
	// only the exact observed owned process identity, never a name-based kill.
	current, err := inspectProcess(child.Parent)
	if err != nil || current.birth != parent.birth {
		t.Fatal("consumer parent identity changed")
	}
	if err := syscall.Kill(child.Parent, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	if _, _, err := acquireHeavyVerificationAt(t.Context(), claimRoot); err == nil {
		t.Fatal("abandoned claim admitted while managed server survived")
	}
	if _, err := inspectProcess(child.PID); err != nil {
		t.Fatalf("server did not survive launcher death: %v", err)
	}
	if err := owner.cleanup(); err != nil {
		t.Fatal(err)
	}
	if _, err := inspectProcess(child.PID); !processDisappeared(err) {
		t.Fatalf("outer ownership lost detached managed server: %v", err)
	}
	preview, err := recoverHeavyVerificationAt(t.Context(), claimRoot, "", false)
	if err != nil || !preview.Recoverable {
		t.Fatalf("cleaned consumer claim not recoverable: %+v %v", preview, err)
	}
	if result, err := recoverHeavyVerificationAt(t.Context(), claimRoot, preview.Token, true); err != nil || !result.Cleared {
		t.Fatalf("recovery failed: %+v %v", result, err)
	}
}

func TestHeavyActualBrowserConsumerHelper(t *testing.T) {
	claimRoot := os.Getenv("RUNNER_BROWSER_HELPER_CLAIM")
	if claimRoot == "" {
		return
	}
	ctx, claim, err := acquireHeavyVerificationAt(context.Background(), claimRoot)
	if err != nil {
		panic(err)
	}
	module, _ := json.Marshal(os.Getenv("RUNNER_BROWSER_CONSUMER_MODULE"))
	root, _ := json.Marshal(os.Getenv("RUNNER_BROWSER_HELPER_ROOT"))
	server, _ := json.Marshal(os.Getenv("RUNNER_BROWSER_HELPER_SERVER"))
	script := fmt.Sprintf(`import { pathToFileURL } from 'node:url'; const {runBrowser}=await import(pathToFileURL(%s)); await runBrowser({root:%s, server:%s, commandArgs:['-e','setInterval(()=>{},1000)']});`, module, root, server)
	_, err = (OSRunner{}).RunBoundedHeadTailInput(ctx, os.Getenv("RUNNER_BROWSER_HELPER_NODE"), []string{"--input-type=module", "-e", script}, os.Getenv("RUNNER_BROWSER_HELPER_ROOT"), time.Minute, nil, 4096, "...")
	if finishErr := claim.Finish(err); finishErr != nil {
		fmt.Fprintln(os.Stderr, finishErr)
	}
}
