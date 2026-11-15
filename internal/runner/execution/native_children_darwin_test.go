//go:build darwin

package execution

import (
	"crypto/rand"
	"os"
	"os/exec"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestHarnessDarwinMarkerInventory(t *testing.T) {
	owner := "runner-inventory-" + rand.Text()
	cmd := exec.Command("/bin/sleep", "30")
	cmd.Env = append(os.Environ(), nativeOwnerVariable+"="+owner)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	// Other tests and chats can launch children concurrently. Seed only this
	// fixture, never the test parent and all of its unrelated children.
	sibling := exec.Command("/bin/sleep", "30")
	if err := sibling.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sibling.Process.Kill(); _ = sibling.Wait() }()
	known := map[string]bool{}
	if err := rememberNativeRoot(cmd.Process.Pid, known); err != nil {
		t.Fatal(err)
	}
	children, err := ownedNativeProcesses(owner, time.Now().Add(-time.Second), known)
	if err != nil || len(children) != 1 || children[0].PID != cmd.Process.Pid {
		process, _ := unix.SysctlKinfoProc("kern.proc.pid", cmd.Process.Pid)
		t.Fatalf("marker inventory absent: children=%v err=%v current_uid=%d child_uid=%d child_start=%v", children, err, os.Getuid(), process.Eproc.Ucred.Uid, process.Proc.P_starttime)
	}
}

func TestHarnessDarwinUniqueIdentity(t *testing.T) {
	self, parent, err := darwinNativeUnique(os.Getpid())
	if err != nil || self == 0 || parent == 0 {
		t.Fatalf("kernel process identity unavailable: %d %d %v", self, parent, err)
	}
}
