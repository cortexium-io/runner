//go:build darwin || linux

package release

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestProcessOutputCopiesRespectConfiguredBounds(t *testing.T) {
	input := func() io.Reader { return io.LimitReader(strings.NewReader(strings.Repeat("x", 32)), 32) }
	diagnostic := &diagnosticOutput{limit: 8}
	if n, err := io.Copy(diagnostic, input()); err != nil || n != 32 || diagnostic.Len() != 8 {
		t.Fatal("stderr copy bypassed its retention bound", n, err, diagnostic.Len())
	}
	output := &limitedOutput{limit: 8}
	if _, err := io.Copy(output, input()); err == nil || output.Len() > 8 {
		t.Fatal("stdout copy bypassed its output allowance", err, output.Len())
	}
}

func TestImageBuilderReadinessReconcilesLostCleanupAcknowledgement(t *testing.T) {
	for _, remaining := range []bool{false, true} {
		t.Run(map[bool]string{false: "removed", true: "retained"}[remaining], func(t *testing.T) {
			b := Live{}
			j := Job{Request: Request{RunID: "probe_run", Key: "probe_attempt"}, Worker: filepath.Join(t.TempDir(), "work.json")}
			removes, confirmations := 0, 0
			b.runCommand = func(_ context.Context, _ []byte, _ string, args ...string) ([]byte, error) {
				switch {
				case slices.Contains(args, "build"):
					if !slices.Contains(args, "--load") {
						t.Fatal("readiness build must export its probe to the engine image store")
					}
					return nil, nil
				case slices.Contains(args, "inspect"):
					return []byte(j.Request.Key), nil
				case slices.Contains(args, "rm"):
					removes++
					return nil, &commandUncertain{program: "docker"}
				case slices.Contains(args, "ls"):
					confirmations++
					if remaining {
						return []byte("retained-image"), nil
					}
					return nil, nil
				default:
					t.Fatalf("unexpected image probe operation %v", args)
					return nil, nil
				}
			}
			err := b.imageBuilderReadiness(t.Context(), &j)
			if removes != 1 || confirmations != 1 || (err != nil) != remaining {
				t.Fatal("cleanup replayed or absence inferred without proof", removes, confirmations, err)
			}
		})
	}
}

func TestFailedDeliveryCommandRetainsProgressAndErrorStreams(t *testing.T) {
	root := t.TempDir()
	program := filepath.Join(root, "command")
	if err := os.WriteFile(program, []byte("#!/bin/sh\nprintf 'builder progress\\n'; printf 'builder error\\n' >&2; exit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	b := Live{}
	ctx := context.WithValue(t.Context(), deliveryDiagnosticKey{}, root)
	if _, err := b.command(ctx, nil, program); err == nil {
		t.Fatal("failed command reported success")
	}
	files, err := filepath.Glob(filepath.Join(root, "diagnostic-*.log"))
	if err != nil || len(files) != 2 {
		t.Fatal("missing bounded private diagnostic streams", files, err)
	}
	for _, file := range files {
		data, err := os.ReadFile(file)
		want := "builder error\n"
		if strings.HasSuffix(file, ".stdout.log") {
			want = "builder progress\n"
		}
		if err != nil || string(data) != want {
			t.Fatal("failure diagnostics lost command evidence", err)
		}
	}
}

func TestSuccessfulDockerBuildRetainsBoundedStreamsOnlyForBuilds(t *testing.T) {
	root := t.TempDir()
	program := filepath.Join(root, "docker")
	if err := os.WriteFile(program, []byte("#!/bin/sh\nhead -c 70000 /dev/zero; printf 'export complete\\n'; head -c 70000 /dev/zero >&2\n"), 0700); err != nil {
		t.Fatal(err)
	}
	b := Live{Config: Config{Docker: program, DockerHost: "fixture"}}
	ctx := context.WithValue(t.Context(), deliveryDiagnosticKey{}, root)
	for _, action := range []string{"build", "cp"} {
		if _, err := b.docker(ctx, action); err != nil {
			t.Fatal(err)
		}
	}
	files, err := filepath.Glob(filepath.Join(root, "build-*.log"))
	if err != nil || len(files) != 2 {
		t.Fatal("successful build evidence missing or unrelated output retained", files, err)
	}
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil || len(data) != 64<<10 || strings.HasSuffix(file, ".stdout.log") && !bytes.HasSuffix(data, []byte("export complete\n")) {
			t.Fatal("build evidence is unbounded or lost its output tail", file, len(data), err)
		}
	}
}

func TestSSHReconnectsOnlyBeforeRemoteDispatch(t *testing.T) {
	for _, mode := range []string{"startup timeout", "lost acknowledgement"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			program := filepath.Join(root, "ssh")
			marker := filepath.Join(root, "calls")
			message := "Connection timed out during banner exchange"
			if mode == "lost acknowledgement" {
				message = "Connection lost after remote session started"
			}
			script := "#!/bin/sh\nif [ -e " + quote(marker) + " ]; then printf 'second\\n' >> " + quote(marker) + "; printf 'connected\\n'; exit 0; fi\nprintf 'first\\n' > " + quote(marker) + "\nprintf '%s\\n' " + quote(message) + " >&2\nexit 255\n"
			if err := os.WriteFile(program, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			b := Live{Config: Config{SSH: program, Host: "fixture"}}
			out, err := b.host(t.Context(), nil, "fixed-operation")
			calls, _ := os.ReadFile(marker)
			if mode == "startup timeout" {
				if err != nil || string(out) != "connected\n" || strings.Count(string(calls), "\n") != 2 {
					t.Fatal("startup did not reconnect once", err, string(calls))
				}
			} else if err == nil || strings.Count(string(calls), "\n") != 1 {
				t.Fatal("uncertain remote command was repeated", err, string(calls))
			}
		})
	}
}

func TestDockerUsesItsConfiguredRuntimeForCredentialHelpers(t *testing.T) {
	root := t.TempDir()
	runtime := filepath.Join(root, "runtime")
	if err := os.Mkdir(runtime, 0700); err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(runtime, "docker-credential-fixture")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nprintf 'helper available; builder=%s\\n' \"$DOCKER_BUILDKIT\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(runtime, "docker")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nexec docker-credential-fixture\n"), 0700); err != nil {
		t.Fatal(err)
	}
	configured := filepath.Join(root, "docker")
	if err := os.Symlink(binary, configured); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", "/usr/bin:/bin")
	t.Setenv("DOCKER_BUILDKIT", "1")
	b := Live{Config: Config{Docker: configured, DockerHost: "fixture", Delivery: &DeliveryConfig{}}}
	out, err := b.docker(t.Context(), "info")
	if err != nil || string(out) != "helper available; builder=1\n" {
		t.Fatal("SSH-style PATH could not resolve the installed helper", err, string(out))
	}
	if os.Getenv("PATH") != "/usr/bin:/bin" || os.Getenv("DOCKER_BUILDKIT") != "1" {
		t.Fatal("Docker runtime selection changed the parent environment")
	}
}

func TestDockerReconnectsOnlyBeforeBuildDispatch(t *testing.T) {
	for _, test := range []struct {
		name, action, phase string
		alwaysFail          bool
		calls               int
		connected           bool
	}{
		{name: "startup reconnect", action: "build", phase: "listing workers", calls: 2, connected: true},
		{name: "startup exhausted", action: "build", phase: "listing workers", alwaysFail: true, calls: 2},
		{name: "read-only query", action: "inspect", phase: "query", calls: 2, connected: true},
		{name: "read-only exhausted", action: "inspect", phase: "query", alwaysFail: true, calls: 2},
		{name: "build dispatched", action: "build", phase: "solving build", calls: 1},
		{name: "multi-effect run", action: "run", phase: "listing workers", calls: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			program, marker := filepath.Join(root, "docker"), filepath.Join(root, "calls")
			transport := "command [ssh -T fixture docker system dial-stdio] has exited with exit status 255: stderr=Connection timed out during banner exchange"
			message := "ERROR: failed to build: " + test.phase + ": failed to list workers: Unavailable: connection error: desc = transport: Error while dialing: " + transport
			if test.action == "inspect" {
				message = "error during connect: Get http://fixture/containers/image/json: " + transport
			}
			failure := "printf '%s\\n' " + quote(message) + " >&2; exit 1\n"
			second := "printf 'connected\\n'; exit 0\n"
			if test.alwaysFail {
				second = failure
			}
			script := "#!/bin/sh\nif [ -e " + quote(marker) + " ]; then printf 'second\\n' >> " + quote(marker) + "; " + second + "fi\nprintf 'first\\n' > " + quote(marker) + "; " + failure
			if err := os.WriteFile(program, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			b := Live{Config: Config{Docker: program, DockerHost: "fixture"}}
			out, err := b.docker(t.Context(), test.action, "fixed-context")
			calls, _ := os.ReadFile(marker)
			if strings.Count(string(calls), "\n") != test.calls || (err == nil) != test.connected || test.connected && string(out) != "connected\n" {
				t.Fatal("Docker reconnect boundary or bound violated", err, string(calls))
			}
		})
	}
}
