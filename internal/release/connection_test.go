//go:build darwin || linux

package release

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestImageBuilderReadinessReconcilesLostCleanupAcknowledgement(t *testing.T) {
	for _, remaining := range []bool{false, true} {
		t.Run(map[bool]string{false: "removed", true: "retained"}[remaining], func(t *testing.T) {
			b := Live{}
			j := Job{Request: Request{RunID: "probe_run", Key: "probe_attempt"}, Worker: filepath.Join(t.TempDir(), "work.json")}
			removes, confirmations := 0, 0
			b.runCommand = func(_ context.Context, _ []byte, _ string, args ...string) ([]byte, error) {
				switch {
				case slices.Contains(args, "build"):
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
