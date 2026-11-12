//go:build darwin || linux

package execution

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
)

func TestGennyPermissionChild(t *testing.T) {
	if os.Getenv("RUNNER_GENNY_PERMISSION_CHILD") != "1" {
		t.Skip("internal zero-model source permission probe")
	}
	root := os.Getenv("RUNNER_GENNY_SOURCE")
	genny := os.Getenv("RUNNER_GENNY")
	project := filepath.Join(root, "platform")
	invoke := func(args ...string) []byte {
		t.Helper()
		out, err := exec.Command(genny, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("supported generation failed: %v: %s", err, out)
		}
		return out
	}
	invoke("init", project, "--module", "example.invalid/runner-saas-fixture", "--project-name", "RunnerFixture")
	invoke("auth", "add", "--tenancy", "--project", project)
	invoke("models", "add", "Record", "title:string", "body:text", "tenant_id:uuid", "--access", "tenant", "--project", project)
	metadata, err := os.ReadFile(filepath.Join(project, ".genny", "genny.json"))
	if err != nil || !strings.Contains(string(metadata), `"tenancy"`) || !strings.Contains(string(metadata), `"auth"`) {
		t.Fatal("genuine auth/tenant metadata missing", err)
	}
	invoke("inspect", "--json", "--project", project)
	// Real overwrite protection: an edited .gny file must not be silently
	// refreshed. This is synthetic source drift, not an App modification.
	var generated string
	if err := filepath.WalkDir(project, func(path string, entry os.DirEntry, err error) error {
		if err == nil && !entry.IsDir() && strings.HasSuffix(path, ".gny.go") && generated == "" {
			generated = path
		}
		return err
	}); err != nil || generated == "" {
		t.Fatal("generated glue absent", err)
	}
	old, err := os.ReadFile(generated)
	if err != nil {
		t.Fatal(err)
	}
	changed := append(old, []byte("\n// synthetic user edit: preserve\n")...)
	if err := os.WriteFile(generated, changed, 0600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(genny, "refresh", "generated", "--project", project).CombinedOutput(); err == nil {
		t.Fatalf("overwrite guard accepted changed glue: %s", out)
	}
	after, _ := os.ReadFile(generated)
	if string(after) != string(changed) {
		t.Fatal("non-force refresh overwrote source")
	}
	denied := func(err error) {
		t.Helper()
		if !errors.Is(err, syscall.EPERM) && !errors.Is(err, syscall.EACCES) {
			t.Fatalf("expected permission denial, got %v", err)
		}
	}
	denied(os.WriteFile(filepath.Join(root, "go.mod"), []byte("forbidden"), 0600))
	for _, path := range []string{os.Getenv("RUNNER_GENNY_PRIVATE"), os.Getenv("RUNNER_GENNY_OTHER"), os.Getenv("RUNNER_GENNY_CHECK"), os.Getenv("RUNNER_GENNY_NODE"), filepath.Join(root, ".git")} {
		_, err := os.ReadFile(path)
		denied(err)
	}
	if out, err := exec.Command(os.Getenv("RUNNER_GENNY_GO"), "version").CombinedOutput(); err != nil || !strings.Contains(string(out), "go1.27.3") {
		t.Fatalf("pinned compiler unavailable for Genny type resolution: %v %s", err, out)
	}
	link := filepath.Join(root, ".runner-tmp", "escape")
	if err := os.Symlink(os.Getenv("RUNNER_GENNY_PRIVATE"), link); err != nil {
		t.Fatal(err)
	}
	_, err = os.ReadFile(link)
	denied(err)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if listener != nil {
		listener.Close()
	}
	denied(err)
	t.Log("real non-force init/auth+tenancy/tenant-model generation; changed glue, check-helper/Node, protected/private/other-source, symlink and network boundaries passed; compiler present, checks remain separate process gates")
}

func TestNativeGennySourcePermissions(t *testing.T) {
	if os.Getenv("RUNNER_GENNY_PREFLIGHT") != "1" {
		t.Skip("explicit zero-model Genny permission preflight")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(filepath.Join(home, ".codex"), "runner-genny-preflight-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	policyConfig := runner.DefaultStorage()
	policyConfig.MinFree = 0
	disk, err := runner.OpenDisk(filepath.Join(root, "state"), policyConfig)
	if err != nil {
		t.Fatal(err)
	}
	source := sourceRepositoryAt(t, filepath.Join(root, "source"))
	private, other, check := filepath.Join(root, "private-dummy"), filepath.Join(root, "other-source"), filepath.Join(root, "check-helper")
	for _, p := range []string{private, other, check} {
		if err := os.WriteFile(p, []byte("dummy"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	genny, err := filepath.EvalSymlinks(os.Getenv("RUNNER_PREFLIGHT_GENNY"))
	if err != nil {
		t.Fatal(err)
	}
	goApp := os.Getenv("RUNNER_PREFLIGHT_APP_GO")
	gb, err := os.ReadFile(genny)
	if err != nil {
		t.Fatal(err)
	}
	goBytes, err := os.ReadFile(goApp)
	if err != nil {
		t.Fatal(err)
	}
	g := &runner.GennyRuntime{Binary: genny, SHA256: captureDigest(gb), GoBinary: goApp, GoSHA256: captureDigest(goBytes)}
	c := &Codex{disk: disk, ctx: t.Context(), cfg: &runner.CodexSettings{
		Binary: os.Getenv("RUNNER_PREFLIGHT_CODEX"), GoBinary: os.Getenv("RUNNER_PREFLIGHT_GO"),
		GoCheckBinary: check, ReadOnlyPaths: []string{"go.mod"}, Genny: g,
	}}
	config, err := c.nativeConfiguration(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	policy := filepath.Join(root, "sandbox")
	if err := os.MkdirAll(policy, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(policy, "config.toml"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	j := nativeJob{Assignment: runner.Assignment{AttemptID: "genny-permission", Setup: runner.ExecutionSetup{Profile: runner.Profile{Harness: "codex", Mode: "genny-source", Access: "workspace-write", GeneratorSHA256: g.SHA256, GeneratorGoSHA256: g.GoSHA256}}}, Workspace: &workspaceRecord{WorkDir: source}}
	env, err := c.workerEnvironment(j)
	if err != nil {
		t.Fatal(err)
	}
	probe := filepath.Join(source, ".runner-tmp", "genny-probe")
	binary, _ := os.Executable()
	data, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(probe, data, 0700); err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]string{"RUNNER_GENNY_PERMISSION_CHILD": "1", "RUNNER_GENNY_SOURCE": source, "RUNNER_GENNY_PRIVATE": private, "RUNNER_GENNY_OTHER": other, "RUNNER_GENNY_CHECK": check, "RUNNER_GENNY_GO": goApp, "RUNNER_GENNY_NODE": os.Getenv("RUNNER_PREFLIGHT_NODE")} {
		env[key] = value
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	args := append([]string{"sandbox", "-P", "runner-genny-edit", "-C", source, "--"}, envCommand(env, probe, "-test.run=^TestGennyPermissionChild$", "-test.v", "-test.timeout=2m")...)
	cmd := exec.CommandContext(ctx, c.cfg.Binary, args...)
	// sandbox-only configuration; no app-server, account probe, hosted turn or
	// provider/Keychain state is started, copied or modified.
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "CODEX_HOME=" + policy}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Genny source permission preflight: %v\n%s", err, out)
	} else {
		t.Log(string(out))
	}
	if evidence := os.Getenv("RUNNER_GENNY_PREFLIGHT_EVIDENCE"); evidence != "" {
		if !filepath.IsAbs(evidence) {
			t.Fatal("preflight evidence directory must be absolute")
		}
		if err := os.MkdirAll(evidence, 0700); err != nil {
			t.Fatal(err)
		}
		for name, relative := range map[string]string{"reference-go.mod": "go.mod", "reference-package.json": "frontend/package.json", "reference-genny.json": ".genny/genny.json"} {
			data, err := os.ReadFile(filepath.Join(source, "platform", relative))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(evidence, name), data, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
}
