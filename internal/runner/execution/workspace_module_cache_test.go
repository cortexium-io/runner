package execution

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
)

func TestWorkspaceModuleCacheProbeHonorsConfiguredAndOwnedBoundaries(t *testing.T) {
	root := t.TempDir()
	source, binary := filepath.Join(root, "probe.go"), filepath.Join(root, "probe")
	program := "package main\nimport(\"errors\";\"os\";\"path/filepath\";\"strconv\")\n" + workspaceModuleCacheProbe + "\nfunc main(){readOnly,err:=strconv.ParseBool(os.Args[3]);if err!=nil{panic(err)};probeModuleCache(os.Args[1],os.Args[2],readOnly)}\n"
	if err := os.WriteFile(source, []byte(program), 0600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.CommandContext(t.Context(), "go", "build", "-o", binary, source).CombinedOutput(); err != nil {
		t.Fatal("module-cache probe did not compile", string(out), err)
	}
	for _, tc := range []struct {
		name     string
		readOnly bool
		kind     string
		want     string
	}{
		{name: "owned fallback"},
		{name: "owned readonly", kind: "readonly", want: "owned module cache write boundary"},
		{name: "owned outside scratch", kind: "outside", want: "owned module cache containment boundary"},
		{name: "owned symlink", kind: "symlink", want: "owned module cache directory boundary"},
		{name: "configured readonly inside scratch", readOnly: true, kind: "readonly"},
		{name: "configured writable inside scratch", readOnly: true, want: "module cache write boundary"},
		{name: "unknown existing probe", kind: "existing", want: "owned module cache write boundary"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.kind == "readonly" && os.Geteuid() == 0 {
				t.Skip("host root bypasses Unix mode denial; native sandbox qualification owns that boundary")
			}
			scratch := t.TempDir()
			cache := filepath.Join(scratch, "module-cache")
			if tc.kind == "outside" {
				cache = filepath.Join(t.TempDir(), "module-cache")
			}
			if tc.kind == "symlink" {
				if err := os.Symlink(t.TempDir(), cache); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Mkdir(cache, 0700); err != nil {
				t.Fatal(err)
			}
			if tc.kind == "readonly" {
				if err := os.Chmod(cache, 0555); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(cache, 0700) })
			}
			probe := filepath.Join(cache, ".runner-write-probe")
			if tc.kind == "existing" {
				if err := os.WriteFile(probe, []byte("preserve"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			out, err := exec.CommandContext(t.Context(), binary, cache, scratch, strconv.FormatBool(tc.readOnly)).CombinedOutput()
			if (err == nil) != (tc.want == "") || tc.want != "" && !bytes.Contains(out, []byte(tc.want)) {
				t.Fatal("incorrect trusted probe mode", string(out), err)
			}
			if tc.kind == "existing" {
				if data, err := os.ReadFile(probe); err != nil || string(data) != "preserve" {
					t.Fatal("probe replaced unknown existing content", string(data), err)
				}
			} else if _, err := os.Lstat(probe); !os.IsNotExist(err) {
				t.Fatal("probe left its temporary write file", err)
			}
		})
	}
}

func TestWorkerFallbackModuleCacheDiscoversAddedSourceInSameAttempt(t *testing.T) {
	binary, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	root := sourceRepository(t)
	if err = os.WriteFile(filepath.Join(root, "seats.go"), []byte(seatsImplementation), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	c := &Codex{cfg: &runner.CodexSettings{GoBinary: binary}, ctx: ctx}
	j := nativeJob{
		Assignment: runner.Assignment{AttemptID: "same_actor"},
		Workspace:  &workspaceRecord{WorkDir: root},
	}
	env, err := c.workerEnvironment(j)
	if err != nil {
		t.Fatal(err)
	}
	scratch := filepath.Join(root, ".runner-tmp", j.Assignment.AttemptID)
	if env["GOMODCACHE"] != filepath.Join(scratch, "module-cache") || env["GOCACHE"] != filepath.Join(scratch, "cache") || env["HOME"] != "" || env["GOPATH"] != "" {
		t.Fatal("fallback changed ownership or the isolated worker environment", env)
	}
	var environment []string
	for name, value := range env {
		environment = append(environment, name+"="+value)
	}
	run := func(args ...string) []byte {
		t.Helper()
		cmd := exec.CommandContext(ctx, binary, args...)
		cmd.Dir, cmd.Env = root, environment
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatal("same-attempt Go command failed", string(out), err)
		}
		return out
	}
	run("test", "-count=1", "-run", "^TestParseSeats$", ".")
	added := "package seats\nimport \"testing\"\nfunc TestAddedAfterFirstCommand(t *testing.T){}\nfunc BenchmarkAddedAfterFirstCommand(b *testing.B){b.ReportMetric(1,\"proof/op\")}\n"
	if err = os.WriteFile(filepath.Join(root, "added_test.go"), []byte(added), 0600); err != nil {
		t.Fatal(err)
	}
	again, err := c.workerEnvironment(j)
	if err != nil || again["GOMODCACHE"] != env["GOMODCACHE"] || again["GOCACHE"] != env["GOCACHE"] {
		t.Fatal("same attempt did not retain its owned cache identities", again, err)
	}
	out := run("test", "-v", "-count=1", "-run", "^TestAddedAfterFirstCommand$", "-bench", "^BenchmarkAddedAfterFirstCommand$", "-benchtime=1x", ".")
	if !bytes.Contains(out, []byte("--- PASS: TestAddedAfterFirstCommand")) || !bytes.Contains(out, []byte("BenchmarkAddedAfterFirstCommand-")) {
		t.Fatal("same-attempt cache omitted newly added test or benchmark source", string(out))
	}
}

func TestWorkerFallbackModuleCachePreservesUnownedPaths(t *testing.T) {
	for _, kind := range []string{"module symlink", "module file", "attempt symlink"} {
		t.Run(kind, func(t *testing.T) {
			root, outside := t.TempDir(), t.TempDir()
			marker := filepath.Join(outside, "keep")
			if err := os.WriteFile(marker, []byte("preserve"), 0600); err != nil {
				t.Fatal(err)
			}
			scratch := filepath.Join(root, ".runner-tmp", "actor")
			if err := os.MkdirAll(filepath.Dir(scratch), 0700); err != nil {
				t.Fatal(err)
			}
			path := scratch
			if kind != "attempt symlink" {
				if err := os.Mkdir(scratch, 0700); err != nil {
					t.Fatal(err)
				}
				path = filepath.Join(scratch, "module-cache")
			}
			var err error
			if kind == "module file" {
				err = os.WriteFile(path, []byte("preserve"), 0600)
			} else {
				err = os.Symlink(outside, path)
			}
			if err != nil {
				t.Fatal(err)
			}
			c := &Codex{cfg: &runner.CodexSettings{GoBinary: "go"}, ctx: t.Context()}
			j := nativeJob{
				Assignment: runner.Assignment{AttemptID: "actor"},
				Workspace:  &workspaceRecord{WorkDir: root},
			}
			if _, err = c.workerEnvironment(j); err == nil {
				t.Fatal("fallback accepted unowned cache path", kind)
			}
			if _, err = os.Lstat(path); err != nil {
				t.Fatal("fallback removed the conflicting path", err)
			}
			entries, err := os.ReadDir(outside)
			if err != nil || len(entries) != 1 || entries[0].Name() != "keep" {
				t.Fatal("fallback traversed or modified the foreign directory", entries, err)
			}
		})
	}
}
