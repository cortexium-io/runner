//go:build darwin

package execution

import (
	"crypto/rand"
	"errors"
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

func TestHarnessDarwinSettlementRechecksOwnershipProbeFailure(t *testing.T) {
	for _, operation := range []string{"termination", "inventory"} {
		for _, exited := range []bool{false, true} {
			t.Run(operation+"/"+map[bool]string{false: "same process remains alive", true: "process exits during ownership probe"}[exited], func(t *testing.T) {
				cmd := exec.Command("/bin/sleep", "30")
				if err := cmd.Start(); err != nil {
					t.Fatal(err)
				}
				defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
				child, err := nativeChildIdentity(cmd.Process.Pid)
				if err != nil || child.Identity == "" {
					t.Fatal("fixture birth identity unavailable", err)
				}
				known := map[string]bool{}
				if operation == "inventory" {
					// This captured birth is known, but neither its current marker
					// nor a previously recorded unique identity can prove ownership.
					known["birth:"+child.Identity] = true
				}
				probes := 0
				marker := func(pid int, owner string) (bool, error) {
					if pid != child.PID {
						return nativeProcessHasMarker(pid, owner)
					}
					probes++
					if pid != child.PID || owner != "disposable-owner" {
						t.Fatal("ownership probe received a different process")
					}
					if exited {
						if err := cmd.Process.Kill(); err != nil {
							t.Fatal(err)
						}
						_ = cmd.Wait() // Deterministic exit between identity and environment reads.
					}
					return false, unix.EINVAL
				}
				if operation == "inventory" {
					_, err = ownedDarwinNativeProcesses("disposable-owner", time.Now().Add(-time.Second), known, marker)
				} else {
					err = killDarwinNativeProcess(child, "disposable-owner", known, marker)
				}
				if probes != 1 {
					t.Fatal("fixture did not reach the ownership probe", probes)
				}
				if exited {
					if err != nil {
						t.Fatal("positively absent original process remained uncertain", err)
					}
				} else {
					if !errors.Is(err, unix.EINVAL) {
						t.Fatal("ownership probe failure was treated as settlement", err)
					}
					current, probeErr := nativeChildIdentity(child.PID)
					if probeErr != nil || current.Identity != child.Identity {
						t.Fatal("unconfirmed process was signaled or its identity changed", probeErr)
					}
				}
			})
		}
	}
}
