//go:build darwin || linux

package execution

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
)

// This exercises the real configured-command permissions without an app-server,
// thread or model turn. Ordinary suites leave this explicit qualification off.
func TestNativeBoundMeasurementCommand(t *testing.T) {
	if os.Getenv("RUNNER_BOUND_MEASURE_PREFLIGHT") != "1" {
		t.Skip("explicit opt-in for source-bound command qualification; zero model turns")
	}
	codex, goBinary := os.Getenv("RUNNER_PREFLIGHT_CODEX"), os.Getenv("RUNNER_PREFLIGHT_GO")
	helperGo := os.Getenv("RUNNER_PREFLIGHT_HELPER_GO")
	for _, binary := range []string{codex, goBinary, helperGo} {
		if !filepath.IsAbs(binary) {
			t.Fatal("qualification requires explicit absolute Codex, helper compiler and child Go binaries")
		}
	}
	for _, runtime := range []struct {
		binary string
		args   []string
		want   string
	}{
		{codex, []string{"--version"}, "codex-cli 0.164.0"},
		{helperGo, []string{"version"}, "go version go1.26.9 "},
		{goBinary, []string{"version"}, "go version go1.26.7 "},
	} {
		cmd := exec.CommandContext(t.Context(), runtime.binary, runtime.args...)
		cmd.Env = append(os.Environ(), "GOTOOLCHAIN=local")
		out, err := cmd.CombinedOutput()
		if err != nil || !strings.HasPrefix(strings.TrimSpace(string(out)), runtime.want) {
			t.Fatalf("unexpected qualification runtime: %v: %s", err, out)
		}
		t.Log(strings.TrimSpace(string(out)))
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(filepath.Join(home, ".codex"), "runner-bound-measure-preflight-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	helper := os.Getenv("RUNNER_PREFLIGHT_MEASURE")
	if helper == "" {
		helper = filepath.Join(root, "runner-measure")
		build := exec.CommandContext(t.Context(), helperGo, "build", "-o", helper, "./cmd/runner-measure")
		build.Dir = filepath.Join("..", "..", "..")
		build.Env = append(os.Environ(), "GOTOOLCHAIN=local")
		if out, err := build.CombinedOutput(); err != nil {
			t.Fatalf("build trusted helper: %v: %s", err, out)
		}
	} else if !filepath.IsAbs(helper) {
		t.Fatal("an existing qualification helper must name an absolute executable")
	}
	buildInfo := exec.CommandContext(t.Context(), helperGo, "version", "-m", helper)
	buildInfo.Env = append(os.Environ(), "GOTOOLCHAIN=local")
	info, err := buildInfo.CombinedOutput()
	if err != nil || !strings.Contains(string(info), ": go1.26.9\n") {
		t.Fatalf("helper was not built by the qualified compiler: %v: %s", err, info)
	}
	t.Log(strings.TrimSpace(string(info)))
	repo := sourceRepositoryAt(t, filepath.Join(root, "source"))
	files := map[string]string{
		"seats.go":               seatsImplementation,
		"nested/ordinary.txt":    "included source\n",
		"nested/deeper/data.txt": "included nested source\n",
		".codex":                 "denied tooling metadata\n",
		".agents/dummy":          "denied tooling metadata\n",
		".aws/dummy":             "denied tooling metadata\n",
		"bound_test.go": `package seats
import("encoding/json";"os";"path/filepath";"testing")
func BenchmarkBoundMeasurement(b *testing.B){
 dir:=os.Getenv("RUNNER_MEASURE_EVIDENCE")
 if info,err:=os.Stat(dir);err!=nil||!info.IsDir(){b.Fatal("evidence is not an existing directory")}
 evidence:=map[string]string{}
 for _,key:=range []string{"RUNNER_MEASURE_ROUND","RUNNER_MEASURE_SOURCE_IDENTITY","RUNNER_MEASURE_SOURCE_SHA256","RUNNER_MEASURE_SOURCE_DIGEST_METHOD"}{
  evidence[key]=os.Getenv(key);if evidence[key]==""{b.Fatal("missing helper metadata",key)}
 }
 data,err:=json.Marshal(evidence);if err!=nil{b.Fatal(err)}
 if err=os.WriteFile(filepath.Join(dir,"binding.json"),data,0600);err!=nil{b.Fatal(err)}
 b.ReportMetric(1,"samples/op")
}
`,
	}
	for name, body := range files {
		file := filepath.Join(repo, name)
		if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	fixtureGit(t, repo, "add", "--all")
	fixtureGit(t, repo, "-c", "user.name=Runner fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgSign=false", "commit", "--quiet", "-m", "source-bound command fixture")
	private := filepath.Join(root, "private-dummy")
	if err := os.WriteFile(private, []byte("dummy"), 0600); err != nil {
		t.Fatal(err)
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	policy := runner.DefaultStorage()
	policy.MinFree = 0
	disk, err := runner.OpenDisk(filepath.Join(root, "state"), policy)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &runner.CodexSettings{
		Binary:           codex,
		GoBinary:         goBinary,
		CommandReadPaths: []string{helper, binary},
	}
	c := NewCommands(disk, cfg, &Workspaces{disk: disk}, nil)
	defer c.Close()
	var worker string
	var permissionConfig string
	c.launchWorker = func(file string, _ func()) error { worker = file; return nil }
	run := func(id string, command *runner.Command) runner.Observation {
		t.Helper()
		a := sourceAssignment(disk, repo, id)
		a.Deadline = time.Now().Add(90 * time.Second)
		a.Setup.Profile = runner.Profile{Harness: "command", Access: "workspace-read", Command: command}
		job := commandJob{Assignment: a, Stage: "allocated"}
		if err := c.save(job); err != nil {
			t.Fatal(err)
		}
		if err := c.prepareWorker(t.Context(), &job); err != nil {
			t.Fatal(err)
		}
		config, err := disk.Read(commandControl(id)+"/config/config.toml", 16384)
		if err != nil || !strings.Contains(string(config), "[permissions.runner-command.network]\nenabled=false\n") {
			t.Fatalf("configured network denial changed: %v: %s", err, config)
		}
		lines := strings.Split(strings.ReplaceAll(string(config), id, "<attempt>"), "\n")
		sort.Strings(lines)
		normalized := strings.Join(lines, "\n")
		if permissionConfig != "" && permissionConfig != normalized {
			t.Fatal("measurement and permission probe did not use the same configured filesystem/network policy")
		}
		permissionConfig = normalized
		if err := RunCommandWorker(t.Context(), worker); err != nil {
			t.Fatal(err)
		}
		result, err := c.Observe(context.Background(), id)
		if err != nil || result.End == nil || result.End.Kind != "returned" {
			t.Fatalf("configured command did not settle: %+v: %v", result.End, err)
		}
		return result
	}
	result := run("attempt_bound_measure", &runner.Command{
		Args: []string{
			helper,
			"--go",
			goBinary,
			"--source",
			"{workspace}",
			"--scratch",
			"{scratch}",
			"--package",
			".",
			"--benchmark",
			"^BenchmarkBoundMeasurement$",
			"--previous",
			"",
			"--repair-collection",
			"--bind-source",
			"--require-evidence",
		},
		Result:   "domain.json",
		Artifact: "measurements.json",
	})
	if result.End.Result.Outcome != "continue" {
		t.Fatalf("source-bound benchmark rejected by the real sandbox: %s: %s", result.End.Result.Outcome, result.End.Result.Reason)
	}
	raw, err := disk.Read("artifacts/"+result.End.Result.Outputs["artifact"].ArtifactID, disk.Policy.MaxArtifact)
	var report struct {
		Round            int
		OutputComplete   bool `json:"output_complete"`
		Failed           bool
		CollectionFailed bool   `json:"collection_failed"`
		SourceVerified   bool   `json:"source_verified"`
		EvidenceStatus   string `json:"evidence_status"`
		Evidence         map[string]json.RawMessage
		Source           struct {
			Identity     string
			SHA256       string
			DigestMethod string
			Files        int
			Bytes        int64
		}
	}
	if err != nil || json.Unmarshal(raw, &report) != nil || report.Round != 1 || !report.OutputComplete || report.Failed || report.CollectionFailed || !report.SourceVerified || report.EvidenceStatus != "provided" || report.Source.Files != 7 || report.Source.Bytes < 100 || len(report.Source.SHA256) != 64 || report.Source.Identity != "sha256:"+report.Source.SHA256 || report.Source.DigestMethod != "runner-source-snapshot-sha256-v1" {
		t.Fatalf("real sandbox lost complete source-bound evidence: %v: %s", err, raw)
	}
	var evidence map[string]string
	if err := json.Unmarshal(report.Evidence["binding.json"], &evidence); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		"RUNNER_MEASURE_ROUND":                "1",
		"RUNNER_MEASURE_SOURCE_IDENTITY":      report.Source.Identity,
		"RUNNER_MEASURE_SOURCE_SHA256":        report.Source.SHA256,
		"RUNNER_MEASURE_SOURCE_DIGEST_METHOD": "runner-source-snapshot-sha256-v1",
	} {
		if evidence[key] != want {
			t.Fatal("producer metadata does not match the helper report", key, evidence[key], want)
		}
	}
	// Reuse the owning permission probe rather than adding another policy matrix.
	permission := run("attempt_bound_permissions", &runner.Command{
		Args: []string{binary, "-test.run=^TestCommandPermissionProbeProcess$", "-test.timeout=10s"},
		Env: map[string]string{
			"RUNNER_COMMAND_PERMISSION_PROBE": "1",
			"RUNNER_PROBE_SOURCE":             "{workspace}",
			"RUNNER_PROBE_SCRATCH":            "{scratch}",
			"RUNNER_PROBE_PRIVATE":            private,
			"RUNNER_PROBE_NETWORK":            "false",
			"RUNNER_PROBE_LISTENER":           "127.0.0.1:1",
		},
	})
	if permission.End.Result.Outcome != "ready" {
		t.Fatal("source binding widened configured permissions", permission.End)
	}
	t.Log("strict source-bound helper and existing source/Git/private/symlink/network denial probe passed under the real configured-command sandbox; zero model turns")
}
