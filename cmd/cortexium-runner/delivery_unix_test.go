//go:build darwin || linux

package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"golang.org/x/sys/unix"
)

func TestDeliveryAmendmentCLIRefusesFIFOWithoutWaitingForWriter(t *testing.T) {
	if request := os.Getenv("RUNNER_TEST_AMENDMENT_FIFO"); request != "" {
		var out bytes.Buffer
		err := run(t.Context(), []string{"delivery", "amend", "--config", os.Getenv("RUNNER_TEST_AMENDMENT_CONFIG"), "--item", "PVTI_parent", "--amendment-file", request, "--json"}, strings.NewReader(""), &out)
		if err == nil || !strings.Contains(err.Error(), "regular") || out.Len() != 0 {
			t.Fatalf("FIFO request was not refused as a nonregular file: %v", err)
		}
		return
	}
	root := t.TempDir()
	request := filepath.Join(root, "amend.fifo")
	if err := unix.Mkfifo(request, 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "runner.json")
	if err := config.SaveConfig(path, completeCLITestConfig(root)); err != nil {
		t.Fatal(err)
	}
	// No writer is ever opened. A child process bounds a regressed blocking
	// open without leaking a stuck goroutine into the remaining CLI tests.
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestDeliveryAmendmentCLIRefusesFIFOWithoutWaitingForWriter$")
	child.Env = append(os.Environ(), "RUNNER_TEST_AMENDMENT_FIFO="+request, "RUNNER_TEST_AMENDMENT_CONFIG="+path)
	if out, err := child.CombinedOutput(); err != nil {
		t.Fatalf("FIFO reader blocked or failed: %v (deadline=%v)\n%s", err, ctx.Err(), out)
	}
}
