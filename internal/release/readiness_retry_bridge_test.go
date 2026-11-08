//go:build darwin || linux

package release

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Run the real worker with its inherited lease, without a browser, remote host,
// product source or model. The fixed fake programs supply command evidence only.
func TestReadinessRetryBridgeWorker(t *testing.T) {
	file := os.Getenv("RUNNER_READINESS_RETRY_BRIDGE_WORK")
	if file == "" {
		return
	}
	if err := RunDeliveryWorker(file); err != nil {
		t.Fatal(err)
	}
}

func TestReadinessRetryReachesOperatorOnlyAfterOwnedProbeCleanup(t *testing.T) {
	for _, cleanupFails := range []bool{false, true} {
		t.Run(map[bool]string{false: "settled safe failure", true: "cleanup uncertainty"}[cleanupFails], func(t *testing.T) {
			root := t.TempDir()
			program := func(name, body string) string {
				t.Helper()
				file := filepath.Join(root, name)
				if err := os.WriteFile(file, []byte("#!/bin/sh\nset -eu\n"+body), 0700); err != nil {
					t.Fatal(err)
				}
				return file
			}
			const key, run, policy = "attempt_bridge", "run_bridge", "policy_bridge"
			image, id := "sha256:"+strings.Repeat("a", 64), strings.Repeat("b", 64)
			work := filepath.Join(root, "delivery", run, key, "work.json")
			if err := os.MkdirAll(filepath.Dir(work), 0700); err != nil {
				t.Fatal(err)
			}
			imageFile, inspectFile := filepath.Join(root, "image.json"), filepath.Join(root, "inspect.json")
			if err := os.WriteFile(imageFile, browserTestImage(image), 0600); err != nil {
				t.Fatal(err)
			}
			inspection := []any{map[string]any{
				"Id": id, "Image": image,
				"Config":     map[string]any{"User": "1000:1000", "Labels": map[string]string{"cortexium.runner.run": run, "cortexium.runner.attempt": key}},
				"State":      map[string]any{"Status": "created", "Running": false, "ExitCode": 0, "StartedAt": "0001-01-01T00:00:00Z", "FinishedAt": "0001-01-01T00:00:00Z"},
				"HostConfig": map[string]any{"NetworkMode": "none", "IpcMode": "private", "ReadonlyRootfs": true, "CapDrop": []string{"ALL"}, "SecurityOpt": []string{"no-new-privileges:true"}, "Memory": int64(2 << 30), "NanoCpus": int64(2e9), "PidsLimit": 256, "ShmSize": 256 << 20, "Tmpfs": map[string]string{"/tmp": "rw,size=256m,mode=1777,noexec,nosuid,nodev", "/work": "rw,size=128m,mode=0700,uid=1000,gid=1000,noexec,nosuid,nodev"}},
			}}
			if err := writeJSON(inspectFile, inspection); err != nil {
				t.Fatal(err)
			}
			startFailure := "error during connect: Post \"http://docker.example.com/v1.41/containers/" + id + "/start\": command [ssh -l dan -o ConnectTimeout=30 -T -- fixture docker system dial-stdio] has exited with exit status 255, make sure the URL is valid, and Docker 18.09 or later is installed on the remote host: stderr=Connection timed out during banner exchange\r\nConnection to 192.0.2.1 port 22 timed out\r\n"
			remove := "printf 'removed\\n' > " + quote(filepath.Join(root, "removed")) + "\n"
			if cleanupFails {
				remove = "printf 'fixture cleanup rejected\\n' >&2; exit 1\n"
			}
			docker := program("docker", "[ \"$1\" = --host ] && [ \"$2\" = ssh://fixture ]\nshift 2\ncase \"$1\" in\n"+
				"info) printf '%s\\n' engine_bridge;;\n"+
				"build) :;;\n"+
				"image) case \"$2\" in inspect) case \"$3\" in runner-readiness:*) printf '%s\\n' "+quote(key)+";; *) /bin/cat "+quote(imageFile)+";; esac;; rm|ls) :;; *) exit 91;; esac;;\n"+
				"create) printf '%s\\n' "+quote(id)+";;\n"+
				"inspect) [ \"$2\" = "+quote(id)+" ]; /bin/cat "+quote(inspectFile)+";;\n"+
				"start) printf 'start\\n' >> "+quote(filepath.Join(root, "starts"))+"; printf '%s\\n' "+quote(startFailure)+" >&2; exit 1;;\n"+
				"rm) [ \"$2\" = "+quote(id)+" ]; "+remove+";;\n"+
				"ps) [ -f "+quote(filepath.Join(root, "removed"))+" ]; printf '%s\\n' \"$*\" >> "+quote(filepath.Join(root, "absence-queries"))+";;\n"+
				"*) printf 'unexpected Docker fixture command\\n' >&2; exit 92;;\nesac\n")
			ssh := program("ssh", "case \"$*\" in *\"'du' '-sk'\"*) printf '0 fixture\\n';; *\"'df' '-Pk'\"*) printf 'Filesystem 1024-blocks Used Available Capacity Mounted\\nfixture 999999999 0 999999999 0%% /fixture\\n';; *) :;; esac\n")
			noop := program("noop", ":\n")
			cfg := Config{Docker: docker, DockerHost: "ssh://fixture", SSH: ssh, Host: "fixture", Engine: "engine_bridge", BackupHelper: filepath.Join(root, "runner-snapshot"), BackupDir: filepath.Join(root, "backups"), StateDir: filepath.Join(root, "state"), Delivery: &DeliveryConfig{
				Root: filepath.Join(root, "delivery"), Git: noop, Codex: noop,
				Go: program("go", "printf 'go version go1.26.7 fixture\\n'\n"), Node: program("node", "printf 'v24.21.0\\n'\n"), Python: program("python", "printf 'Python 3.12.0\\n'\n"),
				ModuleCache: root, TrustBundle: noop, Probe: noop, Ownership: []string{noop}, BrowserImage: image, Timeout: "1m", StorageLimit: 8 << 30, MinFree: 5 << 30,
			}}
			q := Request{Project: "demo", RunID: run, Key: key, Action: "readiness", Source: strings.Repeat("c", 40)}
			j := Job{Request: q, Receipt: Receipt{Key: key, RunID: run, Action: q.Action, Policy: policy}, Worker: work}
			if err := writeJSON(work, deliveryWork{Config: cfg, Job: j}); err != nil {
				t.Fatal(err)
			}
			lease, err := os.OpenFile(filepath.Join(filepath.Dir(work), "worker.lock"), os.O_CREATE|os.O_RDWR, 0600)
			if err != nil {
				t.Fatal(err)
			}
			defer lease.Close()
			if err = lockOperator(lease); err != nil {
				t.Fatal(err)
			}
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.CommandContext(t.Context(), executable, "-test.run=^TestReadinessRetryBridgeWorker$")
			cmd.Env = append(os.Environ(), "RUNNER_READINESS_RETRY_BRIDGE_WORK="+work)
			cmd.ExtraFiles = []*os.File{lease}
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatal(err, string(out))
			}
			// The parent owns the same open-file description until this close.
			if err = lease.Close(); err != nil {
				t.Fatal(err)
			}
			var result Job
			data, err := os.ReadFile(filepath.Join(filepath.Dir(work), "result.json"))
			if err != nil || json.Unmarshal(data, &result) != nil {
				t.Fatal("worker result missing", err)
			}
			if result.Retryable != !cleanupFails || result.Done != !cleanupFails || result.Failed != !cleanupFails {
				t.Fatalf("worker misclassified safe/uncertain cleanup: %+v", result)
			}
			starts, err := os.ReadFile(filepath.Join(root, "starts"))
			if err != nil || string(starts) != "start\n" {
				t.Fatal("mutating start replayed or readiness stopped before probe", string(starts), err)
			}
			if !cleanupFails {
				query, err := os.ReadFile(filepath.Join(root, "absence-queries"))
				if err != nil || !strings.Contains(string(query), "--all") || !strings.Contains(string(query), "--no-trunc") || !strings.Contains(string(query), id) {
					t.Fatal("retry lacks exact stopped-container absence witness", string(query), err)
				}
			}
			o := Operator{Directory: filepath.Join(root, "journal"), PolicyHash: policy, Project: q.Project, Delivery: true, Backend: &Live{Config: cfg}}
			if err = os.MkdirAll(o.Directory, 0700); err != nil {
				t.Fatal(err)
			}
			if err = writeJSON(filepath.Join(o.Directory, key+".json"), j); err != nil {
				t.Fatal(err)
			}
			response, err := o.Call(t.Context(), q)
			if err != nil || response.Retryable != !cleanupFails || response.Failed != !cleanupFails || !cleanupFails && response.State != "finished" || cleanupFails && response.State != "unknown" {
				t.Fatal("operator lost settled retry proof or hid cleanup uncertainty", response, err)
			}
		})
	}
}
