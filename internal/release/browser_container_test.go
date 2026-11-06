package release

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func browserTestImage(id string) []byte {
	data, _ := json.Marshal([]any{map[string]any{"Id": id, "Os": "linux", "Architecture": "amd64", "Config": map[string]any{"User": "1000:1000", "Entrypoint": []string{"node", "/runner/browser_container.mjs"}, "Labels": map[string]string{"cortexium.runner.browser.contract": "2", "cortexium.runner.browser.playwright": "1.64.1", "cortexium.runner.browser.driver": browserDriverDigest()}}}})
	return data
}

func browserTestArchive(t *testing.T, receipt any) []byte {
	t.Helper()
	data, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	w := tar.NewWriter(&out)
	if err = w.WriteHeader(&tar.Header{Name: "receipt.json", Mode: 0600, Size: int64(len(data))}); err != nil {
		t.Fatal(err)
	}
	if _, err = w.Write(data); err != nil {
		t.Fatal(err)
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestBrowserRuntimeRejectsMutableOrDifferentIdentity(t *testing.T) {
	id := "sha256:" + strings.Repeat("a", 64)
	for _, change := range []string{"none", "driver", "user", "platform", "contract", "entrypoint", "image"} {
		t.Run(change, func(t *testing.T) {
			var images []map[string]any
			_ = json.Unmarshal(browserTestImage(id), &images)
			c := images[0]["Config"].(map[string]any)
			switch change {
			case "driver":
				c["Labels"].(map[string]any)["cortexium.runner.browser.driver"] = "old"
			case "user":
				c["User"] = "0"
			case "platform":
				images[0]["Architecture"] = "arm64"
			case "contract":
				c["Labels"].(map[string]any)["cortexium.runner.browser.contract"] = "1"
			case "entrypoint":
				c["Entrypoint"] = []string{"sh"}
			case "image":
				images[0]["Id"] = "mutable:tag"
			}
			b := Live{Config: Config{Delivery: &DeliveryConfig{BrowserImage: id}}}
			b.runCommand = func(context.Context, []byte, string, ...string) ([]byte, error) { return json.Marshal(images) }
			if err := b.verifyBrowserRuntime(t.Context()); (err == nil) != (change == "none") {
				t.Fatal("unproven runtime accepted", err)
			}
		})
	}
}

func TestBrowserContainerKeepsBoundaryEvidenceAndUnknownDispatch(t *testing.T) {
	for _, mode := range []string{"passed", "source failure", "runtime failure", "HTTP429", "malformed context", "wrong receipt", "lost reply", "wrong owner", "wrong attempt"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			id := "sha256:" + strings.Repeat("a", 64)
			containerID := strings.Repeat("b", 64)
			j := Job{Request: Request{Key: "attempt", RunID: "run"}, Worker: filepath.Join(root, "work.json")}
			receipt := map[string]any{"contract": 2, "complete": true, "mode": "readiness", "engines": []string{"chromium", "webkit"}, "transportComplete": true, "runtime": map[string]any{"image": id, "driver": browserDriverDigest(), "playwright": "1.64.1"}}
			input := browserContainerInput{Mode: "readiness"}
			if mode == "source failure" {
				receipt["failure"] = "AssertionError: actual board clipped"
				input.Mode = "qualification"
			}
			if mode == "runtime failure" {
				receipt["failure"] = "BrowserRuntimeError: WebKit unavailable"
			}
			if mode == "HTTP429" || mode == "wrong receipt" || mode == "malformed context" {
				receipt["failure"] = "Error: PRIVATE_COOKIE PRIVATE_BODY admission HTTP429\nPRIVATE_STACK"
				receipt["failureContext"] = map[string]any{"engine": "chromium", "device": "mobile", "game": "Shikaku", "path": "/shikaku/fixture"}
				receipt["failureDiagnostic"] = browser429DiagnosticFixture()
				input.Mode = "qualification"
				if mode == "malformed context" {
					input.Mode = "readiness"
					receipt["failureContext"] = map[string]any{"engine": []string{"PRIVATE_COOKIE"}}
				}
			}
			if mode == "wrong receipt" {
				receipt["runtime"].(map[string]any)["driver"] = "other"
			}
			state := "created"
			starts, removes, stops := 0, 0, 0
			b := Live{Config: Config{Docker: "docker", Delivery: &DeliveryConfig{BrowserImage: id}}}
			b.runCommand = func(_ context.Context, payload []byte, _ string, args ...string) ([]byte, error) {
				switch args[2] {
				case "image":
					return browserTestImage(id), nil
				case "create":
					for _, bad := range []string{"--mount", "--volume", "--publish", "--privileged", "--ipc=host", "SYS_ADMIN"} {
						if slices.Contains(args, bad) {
							t.Fatal("broadened browser boundary", args)
						}
					}
					for _, required := range []string{"--read-only", "--cap-drop", "ALL", "no-new-privileges:true", "--pull=never", "none", "2g", "256", "1000:1000", id} {
						if !slices.Contains(args, required) {
							t.Fatal("missing fixed boundary", required, args)
						}
					}
					return []byte(containerID), nil
				case "inspect":
					owner := "run"
					attempt := "attempt"
					if mode == "wrong attempt" {
						attempt = "other"
					}
					if mode == "wrong owner" {
						owner = "other"
					}
					return json.Marshal([]any{map[string]any{"Id": containerID, "Image": id, "Config": map[string]any{"User": "1000:1000", "Labels": map[string]string{"cortexium.runner.run": owner, "cortexium.runner.attempt": attempt}}, "State": map[string]any{"Status": state, "Running": state == "running", "ExitCode": 0}, "HostConfig": map[string]any{"NetworkMode": "none", "IpcMode": "private", "ReadonlyRootfs": true, "CapDrop": []string{"ALL"}, "SecurityOpt": []string{"no-new-privileges:true"}, "Memory": int64(2 << 30), "NanoCpus": int64(2e9), "PidsLimit": 256, "ShmSize": 256 << 20, "Tmpfs": map[string]string{"/tmp": "rw,size=256m,mode=1777,noexec,nosuid,nodev", "/work": "rw,size=128m,mode=0700,uid=1000,gid=1000,noexec,nosuid,nodev"}}}})
				case "start":
					if args[len(args)-1] != containerID {
						t.Fatal("browser start used a mutable name", args)
					}
					starts++
					var sent browserContainerInput
					if json.Unmarshal(payload, &sent) != nil || sent.Image != id || sent.TimeoutMS < 1 {
						t.Fatal("invalid owned input")
					}
					if mode == "lost reply" {
						state = "running"
						return nil, &commandUncertain{program: "docker"}
					}
					state = "exited"
					return browserTestArchive(t, receipt), nil
				case "stop":
					stops++
					state = "exited"
					return nil, nil
				case "rm":
					removes++
					return nil, nil
				default:
					t.Fatal("unexpected operation", args)
					return nil, nil
				}
			}
			err := b.runBrowserContainer(t.Context(), &j, "none", filepath.Join(root, "output"), input)
			if (err == nil) != (mode == "passed") {
				t.Fatal("incorrect result", err)
			}
			var source *sourceFailure
			if errors.As(err, &source) != (mode == "source failure") {
				t.Fatal("source/runtime classification changed", err)
			}
			authenticatedFailure := mode == "source failure" || mode == "runtime failure" || mode == "HTTP429" || mode == "malformed context"
			if (j.Receipt.BrowserFailure.Status != "") != authenticatedFailure {
				t.Fatal("feedback missing or accepted before authenticated settlement", mode, j.Receipt.BrowserFailure)
			}
			if mode == "HTTP429" {
				f := j.Receipt.BrowserFailure
				if f.Kind != "reload" || f.Phase != "reload" || f.Reload.Response.Status != 429 || f.Reload.Response.Path != "/api/games/shikaku" || !strings.Contains(err.Error(), "chromium/mobile/Shikaku/reload") || !strings.Contains(err.Error(), "HTTP 429") {
					t.Fatal("429 evidence omitted", f, err)
				}
				if mode == "malformed context" && (!strings.Contains(err.Error(), "invalid failure context") || j.Receipt.BrowserFailure.Status != "partial") {
					t.Fatal("malformed failure context escaped as readiness success", j.Receipt.BrowserFailure, err)
				}
				_ = fail(&j, err.Error())
				j.Receipt.Note = j.Reason
				if !j.Failed || j.Outcome == "changes_requested" {
					t.Fatal("429 changed source routing", j)
				}
				public, _ := json.Marshal(publicReceipt(j.Receipt, true))
				if bytes.Contains(public, []byte("PRIVATE_")) {
					t.Fatal("private browser failure escaped", string(public))
				}
			}
			foreign := mode == "wrong owner" || mode == "wrong attempt"
			if starts > 1 || foreign && (starts != 0 || removes != 0) || !foreign && removes != 1 || mode == "lost reply" && stops != 1 {
				t.Fatal("replayed or removed unowned execution", starts, removes, stops)
			}
		})
	}
}

func TestBrowserArchiveRejectsPathsLinksDuplicatesAndOversize(t *testing.T) {
	for _, fault := range []string{"../outside", "symlink", "duplicate", "oversize", "missing receipt"} {
		t.Run(fault, func(t *testing.T) {
			root := t.TempDir()
			var out bytes.Buffer
			w := tar.NewWriter(&out)
			name, kind, size := "receipt.json", byte(tar.TypeReg), int64(2)
			switch fault {
			case "../outside":
				name = "../outside"
			case "symlink":
				kind = tar.TypeSymlink
				size = 0
			case "oversize":
				size = (128 << 10) + 1
			case "missing receipt":
				name = "failure.png"
			}
			count := 1
			if fault == "duplicate" {
				count = 2
			}
			for i := 0; i < count; i++ {
				if err := w.WriteHeader(&tar.Header{Name: name, Mode: 0600, Typeflag: kind, Linkname: "/private", Size: size}); err != nil {
					t.Fatal(err)
				}
				if size > 0 {
					_, _ = w.Write(bytes.Repeat([]byte{'x'}, int(size)))
				}
			}
			_ = w.Close()
			if err := extractBrowserEvidence(out.Bytes(), root); err == nil {
				t.Fatal("unsafe archive accepted")
			}
			if _, err := os.Stat(filepath.Join(filepath.Dir(root), "outside")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("archive escaped owned directory")
			}
		})
	}
}

func TestBrowserBootstrapDigestMatchesEmbeddedDriver(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node required for runtime bootstrap fixture")
	}
	output := filepath.Join(t.TempDir(), "context")
	data, err := exec.CommandContext(t.Context(), node, "browser-runtime/prepare.mjs", output).CombinedOutput()
	if err != nil {
		t.Fatal(err, string(data))
	}
	var result struct{ Driver string }
	if json.Unmarshal(data, &result) != nil || result.Driver != browserDriverDigest() {
		t.Fatal("bootstrap/runtime source identity differs", string(data), browserDriverDigest())
	}
}

func TestBrowserTransportPreservesArchiveBeyondDiagnosticTail(t *testing.T) {
	// The Docker attachment carries evidence on stdout, while only stderr is a
	// diagnostic tail. Exercise the real command pipe with a >64KiB receipt.
	padding := strings.Repeat("proof", 15000)
	archive := browserTestArchive(t, map[string]any{"padding": padding})
	b := Live{}
	data, err := b.reconnect(t.Context(), archive, "/bin/cat")
	if err != nil || !bytes.Equal(data, archive) {
		t.Fatal("evidence was truncated to diagnostics", err, len(data), len(archive))
	}
	dir := t.TempDir()
	if err = extractBrowserEvidence(data, dir); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "receipt.json"))
	var receipt map[string]string
	if err != nil || json.Unmarshal(raw, &receipt) != nil || receipt["padding"] != padding {
		t.Fatal("complete bounded receipt did not survive transport", err)
	}
}

func TestBrowserCandidateFreezesIdentityAndPreservesForeignAttempt(t *testing.T) {
	for _, mode := range []string{"owned cleanup", "foreign attempt", "replaced name"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			id := strings.Repeat("c", 64)
			image := "sha256:" + strings.Repeat("a", 64)
			j := Job{Request: Request{Key: "attempt", RunID: "run"}, Worker: filepath.Join(root, "work.json"), Receipt: Receipt{Image: image, Remote: "/owned", Report: "/report"}}
			removes, stops, execs, starts := 0, 0, 0, 0
			state := "created"
			b := Live{Config: Config{Docker: "docker", SSH: "ssh", Delivery: &DeliveryConfig{BrowserImage: image}}}
			b.runCommand = func(_ context.Context, _ []byte, program string, args ...string) ([]byte, error) {
				if program == "ssh" {
					return nil, nil
				}
				switch args[2] {
				case "create":
					if !slices.Contains(args, "cortexium.runner.attempt=attempt") || slices.Contains(args, "--publish") {
						t.Fatal("candidate boundary lost", args)
					}
					return []byte(id), nil
				case "inspect":
					if args[3] != id {
						t.Fatal("candidate used mutable identity", args)
					}
					actual, attempt := id, "attempt"
					if mode == "foreign attempt" {
						attempt = "other"
					}
					if mode == "replaced name" {
						actual = strings.Repeat("d", 64)
					}
					return json.Marshal([]any{map[string]any{"Id": actual, "Image": image, "Config": map[string]any{"Labels": map[string]string{"cortexium.runner.run": "run", "cortexium.runner.attempt": attempt}}, "State": map[string]any{"Status": state, "Running": false}}})
				case "start":
					starts++
					if args[3] != id {
						t.Fatal("candidate start used mutable name")
					}
					state = "exited"
					return []byte(id), nil
				case "exec":
					execs++
					if args[3] != id {
						t.Fatal("healthcheck used mutable name")
					}
					return nil, errors.New("synthetic health failure")
				case "stop":
					stops++
					return nil, nil
				case "rm":
					removes++
					if args[3] != id {
						t.Fatal("cleanup used mutable name")
					}
					return nil, nil
				default:
					t.Fatal("unexpected operation", args)
					return nil, nil
				}
			}
			if err := b.browserContainerCandidate(t.Context(), &j); err == nil {
				t.Fatal("fixture unexpectedly qualified")
			}
			if mode == "owned cleanup" && (removes != 1 || execs != 1 || starts != 1) || mode != "owned cleanup" && (removes != 0 || stops != 0 || execs != 0 || starts != 0) {
				t.Fatal("foreign identity mutated or owned cleanup skipped", mode, removes, stops, execs)
			}
		})
	}
}

func TestBrowserCleanupFailureDominatesCandidateAssertion(t *testing.T) {
	for _, mode := range []string{"stop rejected", "still running"} {
		t.Run(mode, func(t *testing.T) {
			id := strings.Repeat("a", 64)
			image := "sha256:" + strings.Repeat("b", 64)
			j := Job{Request: Request{RunID: "run", Key: "attempt"}}
			b := Live{Config: Config{Docker: "docker"}}
			b.runCommand = func(_ context.Context, _ []byte, _ string, args ...string) ([]byte, error) {
				switch args[2] {
				case "inspect":
					return json.Marshal([]any{map[string]any{"Id": id, "Image": image, "State": map[string]any{"Running": true}, "Config": map[string]any{"Labels": map[string]string{"cortexium.runner.run": "run", "cortexium.runner.attempt": "attempt"}}}})
				case "stop":
					if mode == "stop rejected" {
						return nil, &commandFailure{program: "docker", code: 1}
					}
					return nil, nil
				default:
					t.Fatal("unsettled container was removed", args)
					return nil, nil
				}
			}
			err := errors.Join(&sourceFailure{reason: "board clipped"}, b.closeBrowserRuntime(t.Context(), id, &j, image))
			var uncertain *commandUncertain
			if !errors.As(err, &uncertain) {
				t.Fatal("cleanup permitted source correction before settlement", err)
			}
		})
	}
}
