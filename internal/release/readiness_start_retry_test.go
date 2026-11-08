package release

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func readinessStartDiagnostic(id string) string {
	return `error during connect: Post "http://docker.example.com/v1.41/containers/` + id + `/start": command [ssh -l dan -o ConnectTimeout=30 -T -- fixture docker system dial-stdio] has exited with exit status 255, make sure the URL is valid, and Docker 18.09 or later is installed on the remote host: stderr=Connection timed out during banner exchange` + "\r\nConnection to 192.0.2.1 port 22 timed out\r\n"
}

func TestBrowserStartPreAuthenticationMarkerNeverReconnects(t *testing.T) {
	id := strings.Repeat("a", 64)
	for _, mode := range []string{"pre-auth", "post-auth", "wrong target", "unattached start", "plain start", "non-SSH", "stdout", "full stderr", "appended stderr", "exit255", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			program, calls := filepath.Join(root, "docker"), filepath.Join(root, "calls")
			message, stdout, host, code := readinessStartDiagnostic(id), "", "ssh://fixture", "1"
			args := []string{"start", "--attach", "--interactive", id}
			switch mode {
			case "post-auth":
				message = strings.ReplaceAll(message, "Connection timed out during banner exchange", "Connection lost after authentication")
			case "wrong target":
				message = readinessStartDiagnostic(strings.Repeat("b", 64))
			case "unattached start":
				args = []string{"start", "--interactive", id}
			case "plain start":
				args = []string{"start", id}
			case "non-SSH":
				host = "tcp://fixture"
			case "stdout":
				stdout = "started output"
			case "full stderr":
				message += strings.Repeat(" ", (64<<10)-len(message))
			case "appended stderr":
				message += "remote command executed\n"
			case "exit255":
				code = "255"
			}
			script := "#!/bin/sh\nprintf 'call\\n' >> " + quote(calls) + "\nprintf '%s' " + quote(stdout) + "\nprintf '%s' " + quote(message) + " >&2\nexit " + code + "\n"
			if err := os.WriteFile(program, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			b := Live{Config: Config{Docker: program, DockerHost: host}}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if mode == "cancelled" {
				cancel()
			}
			_, err := b.docker(ctx, args...)
			if err == nil || errors.Is(err, errStartBeforeAuthentication) != (mode == "pre-auth") {
				t.Fatal("pre-authentication fact accepted without exact evidence", mode, err)
			}
			var unavailable *connectionUnavailable
			if errors.As(err, &unavailable) {
				t.Fatal("start acquired a generic reconnect grant", err)
			}
			data, readErr := os.ReadFile(calls)
			want := 1
			if mode == "cancelled" {
				want = 0
			}
			if readErr != nil && !os.IsNotExist(readErr) || strings.Count(string(data), "\n") != want {
				t.Fatal("mutating start was repeated", readErr, string(data))
			}
		})
	}
}

func readinessInspection(id, image, network string) browserContainerInspection {
	i := browserContainerInspection{ID: id, Image: image}
	i.Config.User = "1000:1000"
	i.Config.Labels = map[string]string{"cortexium.runner.run": "run", "cortexium.runner.attempt": "attempt"}
	i.State.Status = "created"
	i.State.StartedAt = "0001-01-01T00:00:00Z"
	i.State.FinishedAt = "0001-01-01T00:00:00Z"
	i.HostConfig.NetworkMode = network
	i.HostConfig.IpcMode = "private"
	i.HostConfig.ReadonlyRootfs = true
	i.HostConfig.CapDrop = []string{"ALL"}
	i.HostConfig.SecurityOpt = []string{"no-new-privileges:true"}
	i.HostConfig.Memory = 2 << 30
	i.HostConfig.NanoCpus = 2e9
	i.HostConfig.PidsLimit = 256
	i.HostConfig.ShmSize = 256 << 20
	i.HostConfig.Tmpfs = map[string]string{
		"/tmp":  "rw,size=256m,mode=1777,noexec,nosuid,nodev",
		"/work": "rw,size=128m,mode=0700,uid=1000,gid=1000,noexec,nosuid,nodev",
	}
	return i
}

func TestReadinessStartRetryRequiresNeverStartedOwnedAbsence(t *testing.T) {
	for _, mode := range []string{"safe", "post-auth", "uncertain start", "foreign before", "foreign after", "wrong image", "running", "exited", "started timestamp", "finished timestamp", "empty times", "missing times", "invalid time", "isolation drift", "inspect failed", "cleanup failed", "starts before removal", "absence failed", "still present", "cancelled after cleanup", "qualification", "candidate network"} {
		t.Run(mode, func(t *testing.T) {
			id, image := strings.Repeat("b", 64), "sha256:"+strings.Repeat("a", 64)
			root := t.TempDir()
			j := Job{
				Request: Request{RunID: "run", Key: "attempt", Action: "readiness"},
				Worker:  filepath.Join(root, "work.json"),
			}
			input, network := browserContainerInput{Mode: "readiness"}, "none"
			if mode == "qualification" {
				j.Request.Action, input.Mode = "qualify", "qualification"
			}
			if mode == "candidate network" {
				network = "container:" + strings.Repeat("c", 64)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			starts, inspections, removes, confirmations := 0, 0, 0, 0
			becameRunning := false
			b := Live{Config: Config{Docker: "docker", DockerHost: "ssh://fixture", Delivery: &DeliveryConfig{BrowserImage: image}}}
			b.runCommand = func(_ context.Context, _ []byte, _ string, args ...string) ([]byte, error) {
				switch args[2] {
				case "image":
					return browserTestImage(image), nil
				case "create":
					return []byte(id), nil
				case "inspect":
					inspections++
					i := readinessInspection(id, image, network)
					if mode == "foreign before" || inspections > 1 && mode == "foreign after" {
						i.Config.Labels["cortexium.runner.attempt"] = "foreign"
					}
					if inspections > 1 {
						switch mode {
						case "wrong image":
							i.Image = "sha256:" + strings.Repeat("c", 64)
						case "running":
							i.State.Running = true
						case "exited":
							i.State.Status = "exited"
						case "started timestamp":
							i.State.StartedAt = "2026-11-08T19:00:00Z"
						case "finished timestamp":
							i.State.FinishedAt = "2026-11-08T19:00:01Z"
						case "empty times":
							i.State.StartedAt, i.State.FinishedAt = "", ""
						case "missing times":
							data, _ := json.Marshal(i)
							var row map[string]any
							_ = json.Unmarshal(data, &row)
							delete(row["State"].(map[string]any), "StartedAt")
							delete(row["State"].(map[string]any), "FinishedAt")
							return json.Marshal([]any{row})
						case "invalid time":
							i.State.StartedAt = "not-a-timestamp"
						case "isolation drift":
							i.HostConfig.Privileged = true
						case "inspect failed":
							return nil, &commandUncertain{program: "docker inspect"}
						}
					}
					if mode == "starts before removal" {
						if becameRunning {
							i.State.Status, i.State.Running = "running", true
						}
						if inspections == 2 {
							becameRunning = true
						}
					}
					return json.Marshal([]browserContainerInspection{i})
				case "start":
					starts++
					failure := &commandFailure{program: "docker", code: 1}
					if mode == "post-auth" {
						return nil, failure
					}
					if mode == "uncertain start" {
						return nil, errors.Join(failure, errStartBeforeAuthentication, &commandUncertain{program: "docker start"})
					}
					return nil, errors.Join(failure, errStartBeforeAuthentication)
				case "rm":
					removes++
					if !slices.Equal(args, []string{"--host", "ssh://fixture", "rm", id}) {
						t.Fatal("cleanup gained force or mutable identity", args)
					}
					if mode == "cleanup failed" || mode == "starts before removal" {
						if mode == "starts before removal" && !becameRunning {
							t.Fatal("fixture did not transition after never-started proof")
						}
						return nil, &commandFailure{program: "docker rm", code: 1}
					}
					if mode == "cancelled after cleanup" {
						cancel()
					}
					return nil, nil
				case "ps":
					confirmations++
					want := []string{"--host", "ssh://fixture", "ps", "--all", "--no-trunc", "--filter", "id=" + id, "--format", "{{.ID}}"}
					if !slices.Equal(args, want) {
						t.Fatal("absence did not include stopped/created containers or exact ID", args)
					}
					if mode == "absence failed" {
						return nil, &commandFailure{program: "docker ps", code: 1}
					}
					if mode == "still present" {
						return []byte(id), nil
					}
					return nil, nil
				default:
					t.Fatal("unexpected mutating action", args)
					return nil, nil
				}
			}
			err := b.runBrowserContainer(ctx, &j, network, filepath.Join(root, "output"), input)
			var unavailable *connectionUnavailable
			if err == nil || errors.As(err, &unavailable) != (mode == "safe") {
				t.Fatal("unsafe/uncertain readiness was promoted", mode, err)
			}
			if starts > 1 || mode == "foreign before" && (starts != 0 || removes != 0) {
				t.Fatal("start replayed or foreign container removed", starts, removes)
			}
			if mode == "safe" && (starts != 1 || removes != 1 || confirmations != 1) {
				t.Fatal("retry granted without one owned cleanup and absence witness", starts, removes, confirmations)
			}
			if slices.Contains([]string{"foreign after", "wrong image", "running", "exited", "started timestamp", "finished timestamp", "empty times", "missing times", "invalid time", "isolation drift", "inspect failed", "cleanup failed", "starts before removal", "absence failed", "still present", "uncertain start"}, mode) {
				var uncertain *commandUncertain
				if !errors.As(err, &uncertain) {
					t.Fatal("uncertainty lost to pre-auth marker", mode, err)
				}
			}
		})
	}
}
