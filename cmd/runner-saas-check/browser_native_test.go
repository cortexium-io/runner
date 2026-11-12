//go:build darwin || linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/saas"
)

// Actual owned CFT failure/cancel cleanup, without a model or product source.
// The generated fixture's successful check path is a separate Runner journey.
func TestOwnedBrowserFailureAndCancellation(t *testing.T) {
	if os.Getenv("RUNNER_SAAS_BROWSER_FIXTURE") != "1" {
		t.Skip("explicit owned headless Chrome fixture")
	}
	var runtime struct {
		Go, Node, Modules, Chrome string
		BrowserRuntime            string `json:"browser_runtime"`
	}
	b, err := os.ReadFile(os.Getenv("RUNNER_SAAS_RUNTIME"))
	if err != nil || json.Unmarshal(b, &runtime) != nil {
		t.Fatal("runtime", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(filepath.Join(home, ".codex"), "runner-saas-browser-failure-")
	if err != nil {
		t.Fatal(err)
	}
	settled := false
	defer func() {
		if settled {
			_ = os.RemoveAll(root)
		} else {
			t.Log("preserved unsettled fixture", root)
		}
	}()
	app := filepath.Join(root, "app")
	if err = sourceCopy(os.Getenv("RUNNER_SAAS_FIXTURE_SOURCE"), app); err != nil {
		t.Fatal(err)
	}
	if err = linkModules(runtime.Modules, filepath.Join(app, "frontend/node_modules")); err != nil {
		t.Fatal(err)
	}
	env := []string{"PATH=" + filepath.Dir(runtime.Go) + ":" + filepath.Dir(runtime.Node) + ":/usr/bin:/bin", "HOME=" + root, "TMPDIR=" + root, "OPENSSL_CONF=/dev/null", "GOCACHE=" + filepath.Join(root, "cache"), "GOENV=off", "GOTOOLCHAIN=local", "GOPROXY=off", "GOSUMDB=off", "GOWORK=off", "CGO_ENABLED=0"}
	front := filepath.Join(app, "frontend")
	for _, args := range [][]string{
		{filepath.Join(runtime.Modules, "vue-tsc/bin/vue-tsc.js"), "--noEmit"},
		{filepath.Join(runtime.Modules, "vite/bin/vite.js"), "build"},
		{filepath.Join(runtime.Modules, "vite/bin/vite.js"), "build", "--config", "vite.static-pages.ssr.config.gny.ts"},
		{filepath.Join(front, ".genny/static-pages/prerender.gny.mjs")},
	} {
		cmd := exec.Command(runtime.Node, args...)
		cmd.Dir = front
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("generated fixture frontend: %v %s", err, out)
		}
	}
	driver := filepath.Join(root, "browser.mjs")
	if err = os.WriteFile(driver, saas.BrowserDriver, 0500); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile("../../testdata/saas/local-server/main.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"failure", "cancellation"} {
		t.Run(mode, func(t *testing.T) {
			source := string(original)
			if mode == "failure" {
				needle := "switch in.Action {"
				if !strings.Contains(source, needle) {
					t.Fatal("fixture injection point changed")
				}
				source = strings.Replace(source, needle, "if in.Action == \"run-flow\" { fmt.Fprintln(os.Stderr,\"synthetic-owned-worker-error: retained cause\"); http.Error(w,\"owned fixture failure\",500); return }; "+needle, 1)
			}
			file := filepath.Join(root, mode+".go")
			if err := os.WriteFile(file, []byte(source), 0600); err != nil {
				t.Fatal(err)
			}
			server := filepath.Join(root, mode+"-server")
			build := exec.Command(runtime.Go, "build", "-trimpath", "-o", server, file)
			build.Env = append(env, "GO111MODULE=off")
			if out, err := build.CombinedOutput(); err != nil {
				t.Fatalf("synthetic server: %v %s", err, out)
			}
			scratch := filepath.Join(root, mode)
			views := filepath.Join(scratch, "viewports")
			if err := os.MkdirAll(scratch, 0700); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, runtime.Node, driver, runtime.BrowserRuntime, runtime.Chrome, server, app, scratch, views)
			cmd.Env = env
			if os.Getenv("RUNNER_SAAS_OUTER_SANDBOX") == "1" {
				policy := filepath.Join(root, mode+"-policy")
				if err := os.Mkdir(policy, 0700); err != nil {
					t.Fatal(err)
				}
				text := "default_permissions=\"saas-browser\"\n[permissions.saas-browser.filesystem]\n\":minimal\"=\"read\"\n"
				for p, access := range map[string]string{app: "read", scratch: "write", server: "read", driver: "read", filepath.Dir(filepath.Dir(runtime.Node)): "read", filepath.Dir(runtime.Modules): "read", runtime.BrowserRuntime: "read", filepath.Dir(filepath.Dir(filepath.Dir(runtime.Chrome))): "read"} {
					text += strconv.Quote(p) + "=" + strconv.Quote(access) + "\n"
				}
				text += "[permissions.saas-browser.network]\nenabled=true\n"
				if err := os.WriteFile(filepath.Join(policy, "config.toml"), []byte(text), 0600); err != nil {
					t.Fatal(err)
				}
				cmd = exec.CommandContext(ctx, os.Getenv("RUNNER_PREFLIGHT_CODEX"), "sandbox", "-P", "saas-browser", "-C", app, "--", runtime.Node, driver, runtime.BrowserRuntime, runtime.Chrome, server, app, scratch, views)
				cmd.Env = append(env, "CODEX_HOME="+policy)
			}
			cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
			cmd.WaitDelay = 3 * time.Second
			var output boundedOutput
			cmd.Stdout = &output
			cmd.Stderr = &output
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			if mode == "cancellation" {
				deadline := time.Now().Add(20 * time.Second)
				for {
					if _, err := os.Stat(filepath.Join(views, "chrome-desktop-setup-viewport.png")); err == nil {
						break
					}
					if time.Now().After(deadline) {
						cancel()
						_ = cmd.Wait()
						t.Fatalf("owned capture not reached: %s", output.String())
					}
					time.Sleep(20 * time.Millisecond)
				}
				cancel()
			}
			runErr := cmd.Wait()
			if runErr == nil {
				t.Fatal("failure/cancellation returned success")
			}
			data, err := boundedFile(filepath.Join(views, "cleanup.json"), 1<<20)
			if err != nil {
				t.Fatalf("cleanup receipt missing: %v %s", err, output.String())
			}
			var c struct {
				ServerPIDs    []int    `json:"server_pids"`
				ServerSettled bool     `json:"server_settled"`
				ChromePID     int      `json:"chrome_pid"`
				ChromeSettled bool     `json:"chrome_settled"`
				Failures      []string `json:"failures"`
				Logs          struct {
					Output    string `json:"output"`
					Bytes     int    `json:"bytes"`
					Truncated bool   `json:"truncated"`
				} `json:"server_diagnostics"`
			}
			if json.Unmarshal(data, &c) != nil || !c.ServerSettled || !c.ChromeSettled || len(c.Failures) != 0 {
				t.Fatalf("owned cleanup unconfirmed: %s", data)
			}
			for _, pid := range append(c.ServerPIDs, c.ChromePID) {
				if pid < 1 || !errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
					t.Fatal("owned child still exists", pid)
				}
			}
			if mode == "failure" && (!strings.Contains(c.Logs.Output, "synthetic-owned-worker-error: retained cause") || c.Logs.Bytes < 1 || c.Logs.Truncated || !strings.Contains(output.String(), "Timeout")) {
				t.Fatalf("primary/server diagnostic lost: %s %s", data, output.String())
			}
			if _, err := os.Stat(filepath.Join(views, "chrome-desktop-setup-viewport.png")); err != nil {
				t.Fatal("partial actual viewport lost", err)
			}
			proof := os.Getenv("RUNNER_SAAS_BROWSER_PROOF")
			if !filepath.IsAbs(proof) {
				t.Fatal("absolute proof directory required")
			}
			proof = filepath.Join(proof, mode)
			if err := os.MkdirAll(proof, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(proof, "cleanup.json"), data, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(proof, "primary.log"), output.Bytes(), 0600); err != nil {
				t.Fatal(err)
			}
			entries, _ := os.ReadDir(views)
			for _, entry := range entries {
				if strings.HasSuffix(entry.Name(), ".png") || entry.Name() == "observations.json" {
					data, err := boundedFile(filepath.Join(views, entry.Name()), saas.MaxImage)
					if err != nil {
						t.Fatal(err)
					}
					if err = os.WriteFile(filepath.Join(proof, entry.Name()), data, 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
		})
	}
	settled = !t.Failed()
}
