//go:build darwin || linux

package execution

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
)

// The child runs only under the actual configured-command sandbox. No native
// service, thread or model turn is involved in this qualification.
func TestCommandPermissionProbeProcess(t *testing.T) {
	if os.Getenv("RUNNER_COMMAND_PERMISSION_PROBE") != "1" {
		t.Skip("internal native permission probe")
	}
	source, scratch := os.Getenv("RUNNER_PROBE_SOURCE"), os.Getenv("RUNNER_PROBE_SCRATCH")
	denied := func(err error) {
		t.Helper()
		if !errors.Is(err, syscall.EPERM) && !errors.Is(err, syscall.EACCES) {
			t.Fatalf("expected permission denial, got %v", err)
		}
	}
	if _, err := os.ReadFile(filepath.Join(source, "doc.go")); err != nil {
		t.Fatal("source must remain readable", err)
	}
	denied(os.WriteFile(filepath.Join(source, "doc.go"), []byte("forbidden"), 0600))
	_, err := os.ReadFile(filepath.Join(source, ".git"))
	denied(err)
	_, err = os.ReadFile(os.Getenv("RUNNER_PROBE_PRIVATE"))
	denied(err)
	if err := os.WriteFile(filepath.Join(scratch, "allowed"), []byte("scratch"), 0600); err != nil {
		t.Fatal("scratch must remain writable", err)
	}
	link := filepath.Join(scratch, "escape")
	if err := os.Symlink(os.Getenv("RUNNER_PROBE_PRIVATE"), link); err != nil {
		t.Fatal(err)
	}
	_, err = os.ReadFile(link)
	denied(err)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if os.Getenv("RUNNER_PROBE_NETWORK") != "true" {
		if listener != nil {
			listener.Close()
		}
		denied(err)
		conn, err := net.DialTimeout("tcp", os.Getenv("RUNNER_PROBE_LISTENER"), time.Second)
		if conn != nil {
			conn.Close()
		}
		denied(err)
		return
	}
	if err != nil {
		t.Fatal("opt-in loopback listener", err)
	}
	defer listener.Close()
	if err := listener.(*net.TCPListener).SetDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	client, err := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal("opt-in loopback connection", err)
	}
	defer client.Close()
	server, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	if err := server.SetDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Write([]byte("loopback")); err != nil {
		t.Fatal(err)
	}
	data := make([]byte, len("loopback"))
	if _, err := io.ReadFull(server, data); err != nil || string(data) != "loopback" {
		t.Fatal("loopback exchange failed", err)
	}
}

func TestNativeConfiguredCommandPermissions(t *testing.T) {
	if os.Getenv("RUNNER_COMMAND_NETWORK_PREFLIGHT") != "1" {
		t.Skip("explicit opt-in for native command permission checks; zero model turns")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(filepath.Join(home, ".codex"), "runner-command-preflight-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	repo := sourceRepositoryAt(t, filepath.Join(root, "source"))
	private := filepath.Join(root, "private-dummy")
	if err := os.WriteFile(private, []byte("dummy"), 0600); err != nil {
		t.Fatal(err)
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	goBinary := os.Getenv("RUNNER_PREFLIGHT_GO")
	if goBinary == "" {
		goBinary, err = exec.LookPath("go")
		if err != nil {
			t.Fatal(err)
		}
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	for _, enabled := range []bool{false, true} {
		t.Run(strconv.FormatBool(enabled), func(t *testing.T) {
			policy := runner.DefaultStorage()
			policy.MinFree = 0
			disk, err := runner.OpenDisk(filepath.Join(root, strconv.FormatBool(enabled)), policy)
			if err != nil {
				t.Fatal(err)
			}
			cfg := &runner.CodexSettings{
				Binary:           os.Getenv("RUNNER_PREFLIGHT_CODEX"),
				GoBinary:         goBinary,
				CommandReadPaths: []string{binary},
			}
			c := NewCommands(disk, cfg, &Workspaces{disk: disk}, nil)
			defer c.Close()
			var worker string
			c.launchWorker = func(file string, _ func()) error { worker = file; return nil }
			a := sourceAssignment(disk, repo, "attempt_permissions")
			a.Deadline = time.Now().Add(15 * time.Second)
			a.Setup.Profile = runner.Profile{
				Harness: "command",
				Access:  "workspace-read",
				Command: &runner.Command{
					Args: []string{binary, "-test.run=^TestCommandPermissionProbeProcess$", "-test.timeout=10s"},
					Env: map[string]string{
						"RUNNER_COMMAND_PERMISSION_PROBE": "1",
						"RUNNER_PROBE_SOURCE":             "{workspace}",
						"RUNNER_PROBE_SCRATCH":            "{scratch}",
						"RUNNER_PROBE_PRIVATE":            private,
						"RUNNER_PROBE_NETWORK":            strconv.FormatBool(enabled),
						"RUNNER_PROBE_LISTENER":           listener.Addr().String(),
					},
					Network: enabled,
				},
			}
			job := commandJob{Assignment: a, Stage: "allocated"}
			if err := c.save(job); err != nil {
				t.Fatal(err)
			}
			if err := c.prepareWorker(context.Background(), &job); err != nil {
				t.Fatal(err)
			}
			if err := RunCommandWorker(context.Background(), worker); err != nil {
				t.Fatal(err)
			}
			result, err := c.Observe(context.Background(), a.AttemptID)
			if err != nil || result.End == nil || result.End.Kind != "returned" {
				t.Fatalf("native command permissions: %+v: %v", result.End, err)
			}
		})
	}
	t.Log("default network denied; opt-in loopback exchange passed; source/private/Git/symlink and scratch boundaries preserved; zero model turns")
}
