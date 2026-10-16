package execution

import (
	"context"
	"errors"
	"github.com/cortexium-io/runner/internal/runner/core"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type notifyWriter struct{ ready chan struct{} }

func TestCoordinatorObservationDoesNotCreateOrConcealOwnership(t *testing.T) {
	path := filepath.Join(t.TempDir(), "owner.lock")
	if running, err := ObserveCoordinator(path); err != nil || running {
		t.Fatal("missing owner", running, err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatal("observation created lock")
	}
	owner, err := Lock(path)
	if err != nil {
		t.Fatal(err)
	}
	if running, err := ObserveCoordinator(path); err != nil || !running {
		t.Fatal("active owner hidden", running, err)
	}
	owner.Close()
	if running, err := ObserveCoordinator(path); err != nil || running {
		t.Fatal("released owner", running, err)
	}
	link := path + "-link"
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := ObserveCoordinator(link); err == nil || !Running(link) {
		t.Fatal("unreadable ownership reported absent", err)
	}
}

func (w notifyWriter) Write(b []byte) (int, error) {
	select {
	case w.ready <- struct{}{}:
	default:
	}
	return len(b), nil
}
func TestLaunchCannotExecuteBeforeDurableAuthorization(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "executed")
	expected := errors.New("transaction failed")
	_, stopped, err := Run(context.Background(), "/bin/sh", []string{"-c", `echo executed > "$1"`, "test", marker}, root, "", io.Discard, io.Discard, func(p core.Process) error {
		if p.Identity == "" || p.PID < 2 {
			t.Fatal("missing process identity")
		}
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Fatal("command ran before authorization")
		}
		return expected
	})
	if !stopped || !errors.Is(err, expected) {
		t.Fatalf("failed launch: stopped=%t err=%v", stopped, err)
	}
	if _, err = os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("failed transaction still launched command")
	}
}
func TestCancellationUsesOwnedProcessGroup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan struct{}, 1)
	done := make(chan bool, 1)
	go func() {
		_, stopped, _ := Run(ctx, "/bin/sh", []string{"-c", "echo ready; while :; do :; done"}, t.TempDir(), "", notifyWriter{ready}, io.Discard, func(core.Process) error { return nil })
		done <- stopped
	}()
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("child did not start")
	}
	cancel()
	select {
	case stopped := <-done:
		if !stopped {
			t.Fatal("owned process group remained")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancellation stalled")
	}
}
