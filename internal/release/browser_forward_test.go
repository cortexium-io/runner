//go:build darwin || linux

package release

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestBrowserForwardProcess(t *testing.T) {
	root := os.Getenv("RUNNER_FORWARD_FIXTURE")
	if root == "" {
		return
	}
	file := filepath.Join(root, "calls")
	prior, _ := os.ReadFile(file)
	if err := os.WriteFile(file, append(prior, 'x'), 0600); err != nil {
		os.Exit(1)
	}
	mode := os.Getenv("RUNNER_FORWARD_MODE")
	if mode == "always" || mode == "once" && len(prior) == 0 {
		fmt.Fprintln(os.Stderr, "Connection timed out during banner exchange")
		os.Exit(255)
	}
	if mode == "denied" {
		fmt.Fprintln(os.Stderr, "bind: Address already in use")
		os.Exit(255)
	}
	address := os.Getenv("RUNNER_FORWARD_ADDRESS")
	listener, err := net.Listen("tcp", address)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(255)
	}
	_, port, _ := net.SplitHostPort(address)
	fmt.Fprintln(os.Stderr, "debug1: Local forwarding listening on 127.0.0.1 port "+port+".")
	for {
		conn, err := listener.Accept()
		if err != nil {
			os.Exit(1)
		}
		conn.Close()
	}
}

func TestBrowserForwardOwnsReadinessAndBoundsReconnect(t *testing.T) {
	for _, mode := range []string{"once", "always", "denied", "ready"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Chmod(root, 0700); err != nil {
				t.Fatal(err)
			}
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			port := listener.Addr().(*net.TCPAddr).Port
			address := listener.Addr().String()
			if mode == "denied" {
				defer listener.Close() // An unrelated listener must not count as SSH readiness.
			} else {
				listener.Close()
			}
			t.Setenv("RUNNER_FORWARD_FIXTURE", root)
			t.Setenv("RUNNER_FORWARD_MODE", mode)
			t.Setenv("RUNNER_FORWARD_ADDRESS", address)
			program := filepath.Join(root, "ssh")
			script := "#!/bin/sh\nexec " + quote(os.Args[0]) + " -test.run=^TestBrowserForwardProcess$ -- \"$@\"\n"
			if err = os.WriteFile(program, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			b := Live{Config: Config{SSH: program, Host: "fixture"}}
			stop, err := b.browserForward(t.Context(), root, port, "9")
			wantReady := mode == "once" || mode == "ready"
			if (err == nil) != wantReady {
				t.Fatal("unexpected forward readiness", err)
			}
			if stop != nil {
				if err = stop(); err != nil {
					t.Fatal(err)
				}
				conn, err := net.Dial("tcp", address)
				if err == nil {
					conn.Close()
					t.Fatal("owned forward remained after settlement")
				}
			}
			calls, _ := os.ReadFile(filepath.Join(root, "calls"))
			wantCalls := 1
			if mode == "once" || mode == "always" {
				wantCalls = 2
			}
			if len(calls) != wantCalls {
				t.Fatal("startup reconnect was not bounded", string(calls))
			}
			for i := 1; i <= wantCalls; i++ {
				data, err := os.ReadFile(filepath.Join(root, "browser-forward-"+strconv.Itoa(i)+".log"))
				if err != nil || len(data) == 0 {
					t.Fatal("startup diagnostics lost", err)
				}
			}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			if _, err = b.browserForward(ctx, root, port, "9"); err != context.Canceled {
				t.Fatal("canceled startup was launched", err)
			}
			after, _ := os.ReadFile(filepath.Join(root, "calls"))
			if string(after) != string(calls) {
				t.Fatal("cancellation caused another connection")
			}
			if mode == "denied" {
				data, _ := os.ReadFile(filepath.Join(root, "browser-forward-1.log"))
				if !strings.Contains(string(data), "Address already in use") {
					t.Fatal("forwarding denial was hidden")
				}
			}
		})
	}
}

func TestBrowserForwardPlatform(t *testing.T) {
	policy := os.Getenv("RUNNER_BROWSER_FORWARD_POLICY")
	if policy == "" {
		t.Skip("explicit read-only operator SSH fixture required; no model or product requests")
	}
	op, err := Load(policy)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	root := t.TempDir()
	stop, err := op.Backend.(*Live).browserForward(t.Context(), root, port, "9")
	if err != nil {
		t.Fatal(err)
	}
	if err = stop(); err != nil {
		t.Fatal(err)
	}
	t.Log("Real SSH reported its own loopback bind and settled; no remote application request, model or production action")
}
