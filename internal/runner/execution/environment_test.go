package execution

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner/config"
	"github.com/cortexium-io/runner/internal/runner/core"
	"github.com/cortexium-io/runner/internal/runner/workspace"
)

func TestPreparationRejectsUnsupportedInputs(t *testing.T) {
	manifest := []byte(`{"name":"fixture","version":"1.0.0","dependencies":{"small":"1.0.0"}}`)
	lock := `{"lockfileVersion":3,"packages":{"":{"dependencies":{"small":"1.0.0"}},"node_modules/small":{"version":"1.0.0","resolved":"https://registry.npmjs.org/small/-/small-1.0.0.tgz","integrity":"sha512-pinned"}}}`
	for _, tc := range []struct {
		name, lock string
		valid      bool
	}{
		{"public", lock, true},
		{"private", strings.ReplaceAll(lock, "registry.npmjs.org", "127.0.0.1"), false},
		{"git", strings.ReplaceAll(lock, "https://registry.npmjs.org/small/-/small-1.0.0.tgz", "git+https://example.com/small"), false},
		{"link", strings.ReplaceAll(lock, `"version":"1.0.0","resolved"`, `"link":true,"version":"1.0.0","resolved"`), false},
		{"no integrity", strings.ReplaceAll(lock, "sha512-pinned", ""), false},
		{"inconsistent", strings.ReplaceAll(lock, `"small":"1.0.0"`, `"small":"2.0.0"`), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := npmInputs(manifest, []byte(tc.lock), "")
			if (err == nil) != tc.valid {
				t.Fatal(err)
			}
		})
	}
	for _, text := range []string{"replace local => ../other\n", "replace (\nexample.org/a => example.org/b v1.0.0\n)\n", "exclude example.org/a v1.0.0\n"} {
		if validateGoMod([]byte(text)) == nil {
			t.Fatal("accepted unsupported graph", text)
		}
	}
	t.Run("shrinkwrap overrides approved lock", func(t *testing.T) {
		root, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		for name, data := range map[string][]byte{"package.json": manifest, "package-lock.json": []byte(lock)} {
			if err := os.WriteFile(filepath.Join(root, name), data, 0600); err != nil {
				t.Fatal(err)
			}
		}
		spec := config.CheckEnvironmentSpec{Node: "24.21.0", NPMPackages: []string{"."}}
		inputs, err := environmentInputs(root, spec)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(root, "npm-shrinkwrap.json"), []byte(lock), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err = environmentInputs(root, spec); err == nil || !strings.Contains(err.Error(), "shrinkwrap") {
			t.Fatal("preparation accepted an overriding lock", err)
		}
		if err = ValidateEnvironmentInputs(root, config.PreparedEnvironment{Recipe: environmentRecipe, Spec: spec, Inputs: inputs}); err == nil || !strings.Contains(err.Error(), "shrinkwrap") {
			t.Fatal("check accepted an overriding lock", err)
		}
	})
}

func TestEnvironmentInputIdentityAndPrivateArchiveMetadata(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module fixture.invalid\n"), 0600); err != nil {
		t.Fatal(err)
	}
	spec := config.CheckEnvironmentSpec{Go: "1.26.9", GoModules: []string{"."}}
	inputs, err := environmentInputs(root, spec)
	if err != nil {
		t.Fatal(err)
	}
	if inputs["go.sum"] != "absent" {
		t.Fatal(inputs)
	}
	prepared := config.PreparedEnvironment{Recipe: environmentRecipe, Spec: spec, Inputs: inputs}
	if err = ValidateEnvironmentInputs(root, prepared); err != nil {
		t.Fatal(err)
	}
	prepared.Recipe = "go-node-v1"
	if err = ValidateEnvironmentInputs(root, prepared); err == nil || !strings.Contains(err.Error(), "recipe changed") {
		t.Fatal("superseded bootstrap accepted", err)
	}
	prepared.Recipe = environmentRecipe
	if err = os.WriteFile(filepath.Join(root, "go.sum"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if ValidateEnvironmentInputs(root, prepared) == nil {
		t.Fatal("new empty go.sum inherited an absence approval")
	}
	cache, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	tool := privateToolFixture(t, cache)
	mod := filepath.Join(cache, "cache/download/sandbox.invalid/tool/@v/v1.0.0.mod")
	if err = os.WriteFile(mod, []byte("module substituted.invalid\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = importModules(cache, t.TempDir(), []goModule{{Path: tool.Module, Version: tool.Version, Zip: true}}, []string{tool.Module}); err == nil || !strings.Contains(err.Error(), "metadata differs") {
		t.Fatal("metadata not bound to archive")
	}
}

// Real Docker qualification, deliberately opt-in. Both layouts use the same
// recipe; commands and sources require no product-specific wrapper or patch.
func TestLiveReusableEnvironment(t *testing.T) {
	if os.Getenv("RUNNER_LIVE_PREPARATION") == "" {
		t.Skip("set RUNNER_LIVE_PREPARATION=1 for explicit local Docker preparation")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(home, "runner-environment-")
	if err != nil {
		t.Fatal(err)
	}
	t.Log("Retained evidence:", root)
	for i, layout := range []struct{ goDir, nodeDir, output string }{{".", "web", "build"}, {"service", "client/ui", "service/static"}} {
		t.Run(fmt.Sprintf("layout-%d", i+1), func(t *testing.T) {
			dir := filepath.Join(root, fmt.Sprintf("layout-%d", i+1))
			source := filepath.Join(dir, "source")
			state := filepath.Join(dir, "state")
			files := map[string]string{
				filepath.Join(layout.goDir, "go.mod"):              "module runner.invalid/fixture\n\ngo 1.26.9\n",
				filepath.Join(layout.goDir, "main.go"):             "package main\nfunc main() {}\n",
				filepath.Join(layout.output, "index.html"):         "approved placeholder\n",
				filepath.Join(layout.nodeDir, "package.json"):      `{"name":"fixture","version":"1.0.0","scripts":{"postinstall":"node -e \"require('fs').writeFileSync('node_modules/hook.txt','offline install')\""}}`,
				filepath.Join(layout.nodeDir, "package-lock.json"): `{"name":"fixture","version":"1.0.0","lockfileVersion":3,"packages":{"":{"name":"fixture","version":"1.0.0","hasInstallScript":true}}}`,
			}
			if i == 1 {
				files[filepath.Join(layout.goDir, "go.mod")] += "\nrequire github.com/google/uuid v1.6.0\n"
				files[filepath.Join(layout.goDir, "go.sum")] = "github.com/google/uuid v1.6.0 h1:NIvaJDMOsjHA8n1jAhLSgzrAzy1Hgr+hNrb57e+94F0=\ngithub.com/google/uuid v1.6.0/go.mod h1:TIyPZe4MgqvfeYDBFedMoGGpEw/LqOeaOT+nhxU+yHo=\n"
				files[filepath.Join(layout.goDir, "main.go")] = "package main\nimport _ \"github.com/google/uuid\"\nfunc main() {}\n"
			}
			for path, data := range files {
				dest := filepath.Join(source, path)
				if err = os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(dest, []byte(data), 0600); err != nil {
					t.Fatal(err)
				}
			}
			spec := config.CheckEnvironmentSpec{Go: "1.26.9", Node: "24.21.0", Playwright: "1.64.1", GoModules: []string{layout.goDir}, NPMPackages: []string{layout.nodeDir}}
			// Fetch a small integrity-pinned public package. Root lifecycle code
			// attempts a network connection; it must execute only offline.
			manifest := map[string]any{"name": "fixture", "version": "1.0.0", "dependencies": map[string]string{"is-number": "7.0.0"}, "scripts": map[string]string{"postinstall": `node -e "fetch('https://1.1.1.1',{signal:AbortSignal.timeout(1000)}).then(()=>process.exit(7)).catch(()=>require('fs').writeFileSync('node_modules/hook.txt','offline install'))"`}}
			data, _ := json.Marshal(manifest)
			if err = os.WriteFile(filepath.Join(source, layout.nodeDir, "package.json"), data, 0600); err != nil {
				t.Fatal(err)
			}
			lock := `{"lockfileVersion":3,"packages":{"":{"name":"fixture","version":"1.0.0","hasInstallScript":true,"dependencies":{"is-number":"7.0.0"}},"node_modules/is-number":{"version":"7.0.0","resolved":"https://registry.npmjs.org/is-number/-/is-number-7.0.0.tgz","integrity":"sha512-41Cifkg6e8TylSpdtTpeLVMqvSBEVzTttHvERD741+pnZ8ANv0004MRL43QKPDlK9cGvNp6NZWZUBlbGXYxxng=="}}}`
			if err = os.WriteFile(filepath.Join(source, layout.nodeDir, "package-lock.json"), []byte(lock), 0600); err != nil {
				t.Fatal(err)
			}
			moduleCache := ""
			if i == 0 {
				moduleCache = filepath.Join(dir, "import")
				tool := privateToolFixture(t, moduleCache)
				spec.GoTools = []config.GoTool{tool}
				spec.PrivateModules = []string{tool.Module}
			}
			p, err := PreviewEnvironment(t.Context(), source, spec)
			if err != nil {
				t.Fatal(err)
			}
			prepared, err := PrepareEnvironment(t.Context(), state, source, p, moduleCache)
			if err != nil {
				t.Fatal(err)
			}
			if err = CheckEnvironmentOwnership(state); err != nil {
				t.Fatal(err)
			}
			again, err := PrepareEnvironment(t.Context(), state, source, p, moduleCache)
			if err != nil || again.Image != prepared.Image {
				t.Fatal("immutable reuse failed", err)
			}
			outputs := []config.CheckOutput{{Path: filepath.Join(layout.nodeDir, "node_modules")}, {Path: layout.output, Placeholders: []string{filepath.Join(layout.output, "index.html")}}}
			host, err := dockerHost(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv("DOCKER_HOST", host)
			clientConfig := filepath.Join(dir, "host-docker-config")
			if err = os.MkdirAll(clientConfig, 0700); err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(clientConfig, "config.json"), []byte(`{"proxies":{"default":{"httpProxy":"http://runner-proxy-canary:secret@127.0.0.1:1","httpsProxy":"http://runner-proxy-canary:secret@127.0.0.1:1"}}}`), 0600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("DOCKER_CONFIG", clientConfig)
			run := func(id, command, timeout string) core.CheckObservation {
				t.Helper()
				copy, before, err := workspace.Copy(source, "")
				if err != nil {
					t.Fatal(err)
				}
				if err = workspace.PrepareOutputs(copy, before.Source, outputs); err != nil {
					t.Fatal(err)
				}
				var owner core.Process
				check := config.QualityCheck{ID: id, Image: prepared.Image, Prepared: &spec, Outputs: outputs, Command: command, Directory: ".", Prerequisites: []string{"go", "npm"}, Timeout: timeout}
				r := (Codex{}).Check(t.Context(), copy, check, filepath.Join(dir, id+".log"), func(p core.Process) error {
					owner = p
					data, _ := json.Marshal(p)
					return os.WriteFile(filepath.Join(dir, id+"-process.json"), data, 0600)
				})
				if !r.Stopped || !Gone(owner) {
					t.Fatal("ownership unresolved", r, owner)
				}
				if err = workspace.VerifyCopy(copy, before); err != nil {
					t.Fatal(err)
				}
				if r.Status == "passed" {
					r.Artifacts, err = RetainCheckArtifacts(copy, config.OutputArtifacts([]string{layout.output}, outputs), core.Digest(before.Source), r)
					if err != nil {
						t.Fatal(err)
					}
					if _, err = CheckReadRoots(r, core.Digest(before.Source)); err != nil {
						t.Fatal(err)
					}
				}
				return r
			}
			command := fmt.Sprintf(`set -eu; test "$(cat %s/node_modules/hook.txt)" = 'offline install'; (cd %s; go build -o /workspace/%s/app .); printf generated > %s/index.html; printf '{"ok":true}' > %s/report.json; if printf corrupt > %s/go.mod 2>/dev/null; then exit 9; fi; printf poison > "$GOMODCACHE/poison"`, layout.nodeDir, layout.goDir, layout.output, layout.output, layout.output, layout.goDir)
			command += `; test -z "${HTTP_PROXY-}${http_proxy-}${HTTPS_PROXY-}${https_proxy-}"`
			command += `; test "$HOME" = /tmp/runner-home; test "$(stat -c '%u %a' "$HOME")" = "$(id -u) 700"`
			command += `; rm -rf "$npm_config_cache/_cacache"`
			if i == 0 {
				command += `; test "$(fixture-tool)" = 'pinned tool'; rm -rf "$GOMODCACHE/cache"`
			}
			if r := run("build", command, "4m"); r.Status != "passed" {
				t.Fatal(r)
			}
			reuse := `set -eu; test ! -e "$GOMODCACHE/poison"`
			if i == 0 {
				reuse += `; test -f "$GOMODCACHE/cache/download/sandbox.invalid/tool/@v/v1.0.0.zip"; test "$(fixture-tool)" = 'pinned tool'`
			}
			if r := run("reuse", reuse, "2m"); r.Status != "passed" {
				t.Fatal(r)
			}
			if r := run("cancel", `exec sleep 60`, "5s"); r.Status != "unavailable" {
				t.Fatal(r)
			}
			if i == 0 {
				cancelDir := filepath.Join(state, "environments", "prepare-cancel")
				if err = os.MkdirAll(cancelDir, 0700); err != nil {
					t.Fatal(err)
				}
				host, err := dockerHost(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(t.Context(), time.Second)
				prep := preparation{ctx: ctx, host: host, dir: cancelDir, log: io.Discard}
				if err = prep.run(prepared.Image, "none", "/", nil, `exec sleep 60`); err == nil {
					t.Fatal("preparation deadline ignored")
				}
				cancel()
				if err = CheckEnvironmentOwnership(state); err != nil {
					t.Fatal("canceled preparation retained container", err)
				}
				tool := spec.GoTools[0]
				zipPath := filepath.Join(moduleCache, "cache/download/sandbox.invalid/tool/@v/v1.0.0.zip")
				if err = os.WriteFile(zipPath, []byte("tampered archive"), 0600); err != nil {
					t.Fatal(err)
				}
				if _, err = PrepareEnvironment(t.Context(), filepath.Join(dir, "tampered"), source, p, moduleCache); err == nil {
					t.Fatal("tampered private archive accepted", tool)
				}
				if _, err = PrepareEnvironment(t.Context(), filepath.Join(dir, "missing"), source, p, ""); err == nil {
					t.Fatal("missing private archive acquired network fallback")
				}
			}
			// Changed or newly introduced metadata cannot reuse prepared authority.
			if err = os.WriteFile(filepath.Join(source, layout.goDir, "go.sum"), []byte("changed"), 0600); err != nil {
				t.Fatal(err)
			}
			if ValidateEnvironmentInputs(source, prepared) == nil {
				t.Fatal("changed input reused environment")
			}
			if _, err = PreviewEnvironment(context.Background(), source, spec); err == nil {
				t.Fatal("malformed input accepted")
			}
		})
	}
}

func privateToolFixture(t *testing.T, cache string) config.GoTool {
	t.Helper()
	tool := config.GoTool{Module: "sandbox.invalid/tool", Version: "v1.0.0", Package: "sandbox.invalid/tool/cmd/fixture-tool", Name: "fixture-tool"}
	files := map[string]string{"go.mod": "module sandbox.invalid/tool\n\ngo 1.26.9\n", "cmd/fixture-tool/main.go": "package main\nimport \"fmt\"\nfunc main(){fmt.Println(\"pinned tool\")}\n"}
	path := filepath.Join(cache, "cache/download/sandbox.invalid/tool/@v")
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(filepath.Join(path, "v1.0.0.zip"))
	if err != nil {
		t.Fatal(err)
	}
	w := zip.NewWriter(file)
	var names []string
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	hash := sha256.New()
	for _, name := range names {
		full := tool.Module + "@" + tool.Version + "/" + name
		e, err := w.Create(full)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = e.Write([]byte(files[name])); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256([]byte(files[name]))
		fmt.Fprintf(hash, "%x  %s\n", sum, full)
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
	tool.Sum = "h1:" + base64.StdEncoding.EncodeToString(hash.Sum(nil))
	for name, data := range map[string]string{"v1.0.0.mod": files["go.mod"], "v1.0.0.info": `{"Version":"v1.0.0","Time":"2026-10-21T00:00:00Z"}`} {
		if err = os.WriteFile(filepath.Join(path, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return tool
}
