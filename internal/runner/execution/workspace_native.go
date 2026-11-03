package execution

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
)

func writableLocation(root string) error {
	real, e := filepath.EvalSymlinks(root)
	if e != nil {
		return e
	}
	for _, temp := range []string{"/tmp", "/private/tmp", os.TempDir()} {
		canonical, er := filepath.EvalSymlinks(temp)
		if er != nil {
			continue
		}
		rel, er := filepath.Rel(canonical, real)
		if er == nil && (rel == "." || (!strings.HasPrefix(rel, "../") && !filepath.IsAbs(rel))) {
			return errors.New("workspace execution requires private state outside system temporary directories")
		}
	}
	return nil
}
func (c *Codex) goInstallation(ctx context.Context) (string, string, error) {
	if c.cfg == nil || c.cfg.GoBinary == "" {
		return "", "", errors.New("workspace execution requires explicit codex.go_binary")
	}
	binary, e := exec.LookPath(c.cfg.GoBinary)
	if e != nil {
		return "", "", e
	}
	binary, e = filepath.EvalSymlinks(binary)
	if e != nil {
		return "", "", e
	}
	cmd := exec.CommandContext(ctx, binary, "env", "GOROOT")
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "GOENV=off", "GOTOOLCHAIN=local"}
	out, e := cmd.Output()
	if e != nil {
		return "", "", e
	}
	root, e := filepath.EvalSymlinks(strings.TrimSpace(string(out)))
	if e != nil {
		return "", "", e
	}
	return binary, root, nil
}
func (c *Codex) nativeConfiguration(ctx context.Context) (string, error) {
	if c.cfg.GoBinary == "" {
		return nativeConfig, nil
	}
	if e := c.workspaceRuntimeReadiness(); e != nil {
		return "", e
	}
	if e := writableLocation(c.disk.Root); e != nil {
		return "", e
	}
	_, goRoot, e := c.goInstallation(ctx)
	if e != nil {
		return "", e
	}
	config := strings.Replace(nativeConfig, "sandbox_mode=\"read-only\"", "default_permissions=\"runner-input\"", 1)
	for _, name := range []string{"runner-input", "runner-edit", "runner-check"} {
		config += "\n[permissions." + name + ".filesystem]\n\":minimal\"=\"read\"\n" + strconv.Quote(goRoot) + "=\"read\"\n"
		if name != "runner-input" {
			if c.cfg.GoModuleCache != "" {
				config += strconv.Quote(c.cfg.GoModuleCache) + "=\"read\"\n"
			}
			if c.cfg.GoCheckBinary != "" {
				config += strconv.Quote(c.cfg.GoCheckBinary) + "=\"read\"\n"
			}
			config += "[permissions." + name + ".filesystem.\":workspace_roots\"]\n"
			mode := "read"
			if name == "runner-edit" {
				mode = "write"
			}
			config += "\".\"=" + strconv.Quote(mode) + "\n\".runner-tmp\"=\"write\"\n"
			for _, p := range c.cfg.ReadOnlyPaths {
				config += strconv.Quote(p) + "=\"read\"\n"
			}
			for _, p := range []string{".git", ".codex", ".agents", ".aws"} {
				config += strconv.Quote(p) + "=\"deny\"\n"
			}
		}
		config += "[permissions." + name + ".network]\nenabled=false\n"
	}
	return config, nil
}
func (c *Codex) workerEnvironment(j nativeJob) (map[string]string, error) {
	if e := c.workspaceRuntimeReadiness(); e != nil {
		return nil, e
	}
	binary, goRoot, e := c.goInstallation(c.ctx)
	if e != nil {
		return nil, e
	}
	scratch := filepath.Join(j.Workspace.WorkDir, ".runner-tmp", j.Assignment.AttemptID)
	if info, e := os.Lstat(filepath.Join(j.Workspace.WorkDir, ".runner-tmp")); e == nil && !info.IsDir() {
		return nil, errors.New("worker scratch root is not a directory")
	}
	for _, dir := range []string{scratch, filepath.Join(scratch, "tmp"), filepath.Join(scratch, "cache")} {
		if e = os.MkdirAll(dir, 0700); e != nil {
			return nil, e
		}
	}
	env := map[string]string{
		"PATH":        filepath.Dir(binary) + ":/usr/bin:/bin:/usr/sbin:/sbin",
		"TMPDIR":      scratch,
		"GOTMPDIR":    filepath.Join(scratch, "tmp"),
		"GOCACHE":     filepath.Join(scratch, "cache"),
		"GOROOT":      goRoot,
		"GOTOOLCHAIN": "local",
		"GOENV":       "off",
		"GOPROXY":     "off",
		"GOSUMDB":     "off",
		"GOWORK":      "off",
		"CGO_ENABLED": "0",
		"GOFLAGS":     "-buildvcs=false",
	}
	if c.cfg.GoModuleCache != "" {
		env["GOMODCACHE"] = c.cfg.GoModuleCache
	}
	if c.cfg.GoCheckBinary != "" {
		env["RUNNER_GO_CHECK"] = c.cfg.GoCheckBinary
	}
	return env, nil
}

func (c *Codex) workspaceRuntimeReadiness() error {
	if c.cfg == nil {
		return nil
	}
	if c.cfg.GoModuleCache != "" {
		path, err := filepath.EvalSymlinks(c.cfg.GoModuleCache)
		if err != nil {
			return fmt.Errorf("codex.go_module_cache: %w", err)
		}
		info, err := os.Stat(path)
		if err != nil || !info.IsDir() || !filepath.IsAbs(path) || path == string(filepath.Separator) {
			return errors.New("codex.go_module_cache must name an existing absolute directory other than the filesystem root")
		}
	}
	if c.cfg.GoCheckBinary != "" {
		info, err := os.Stat(c.cfg.GoCheckBinary)
		if err != nil || !filepath.IsAbs(c.cfg.GoCheckBinary) || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
			return errors.New("codex.go_check_binary must name an existing absolute executable file")
		}
	}
	return nil
}
func envCommand(env map[string]string, argv ...string) []string {
	args := []string{"/usr/bin/env", "-i"}
	for k, v := range env {
		args = append(args, k+"="+v)
	}
	return append(args, argv...)
}

// A reviewer reads the retained patch, never an unbounded transcript or a new
// source snapshot. The run's current candidate must still identify that patch.
func (c *Codex) reviewInput(j nativeJob) error {
	id := j.Assignment.Inputs["patch"].ArtifactID
	if id == "" {
		return nil
	}
	if j.Workspace.Candidate == nil || j.Workspace.Candidate.PatchID != id {
		return errors.New("review patch differs from the workspace candidate")
	}
	data, err := c.disk.Read("artifacts/"+id, j.Assignment.Limits.MaxArtifact)
	if err != nil {
		return err
	}
	name := filepath.Join("work", j.Assignment.RunID, "source", ".runner-tmp", j.Assignment.AttemptID, "inputs", "patch")
	return c.disk.Write(name, data, j.Assignment.Limits.MaxArtifact)
}

// Probe the exact server profiles without requesting a thread or model turn.
func (c *Codex) workspaceReadiness(ctx context.Context, peer codexPeer, j nativeJob) error {
	if c.cfg.GoBinary == "" || j.Workspace == nil {
		return errors.New("workspace native configuration is absent")
	}
	env, e := c.workerEnvironment(j)
	if e != nil {
		return e
	}
	probe := filepath.Join(j.Workspace.WorkDir, ".runner-tmp", j.Assignment.AttemptID, "access-probe.go")
	// Dummy files stand in for credentials; the probe never reads real secrets.
	dummy := "jobs/permission-dummy"
	if e = c.disk.Write(dummy, []byte("dummy"), 128); e != nil {
		return e
	}
	defer c.disk.Remove(dummy)

	if e = os.WriteFile(probe, []byte(workspaceProbe), 0600); e != nil {
		return e
	}
	defer os.Remove(probe)
	binary, _, e := c.goInstallation(ctx)
	if e != nil {
		return e
	}
	for _, p := range []struct{ name, write string }{{"runner-edit", "write"}, {"runner-check", "read"}} {
		target, e := os.CreateTemp(j.Workspace.WorkDir, ".runner-probe-")
		if e != nil {
			return e
		}
		target.Close()
		defer os.Remove(target.Name())
		env["RUNNER_SOURCE_PROBE"] = target.Name()
		var out struct {
			Exit   int    `json:"exitCode"`
			Stdout string `json:"stdout"`
			Stderr string `json:"stderr"`
		}
		if e = c.command(ctx, j.Assignment.AttemptID, peer, map[string]any{
			"command": envCommand(env, append([]string{
				binary,
				"run",
				probe,
				j.Workspace.WorkDir,
				filepath.Join(c.disk.Root, filepath.FromSlash(dummy)),
				p.write,
				"",
			}, c.cfg.ReadOnlyPaths...)...),
			"cwd":               j.Workspace.WorkDir,
			"permissionProfile": p.name,
			"timeoutMs":         120000,
			"outputBytesCap":    2048,
		}, &out); e != nil {
			return e
		}
		if out.Exit != 0 {
			return fmt.Errorf("native %s access probe failed: %s", p.name, runner.Clip(out.Stderr))
		}
	}
	return c.disk.Refresh()
}
func (c *Codex) beginCheck(ctx context.Context, id string, peer codexPeer, j nativeJob) error {
	if j.Workspace.Candidate == nil || j.Workspace.Candidate.PatchID == "" || j.Assignment.Inputs["patch"].ArtifactID != j.Workspace.Candidate.PatchID {
		return errors.New("check input must identify the workspace's saved candidate")
	}
	env, e := c.workerEnvironment(j)
	if e != nil {
		return e
	}
	binary, _, e := c.goInstallation(ctx)
	if e != nil {
		return e
	}
	stopping := false
	if e = c.mutate(id, func(j *nativeJob) {
		if c.closing || j.StopRequested {
			stopping = true
			j.End = &runner.ExecutionEnd{Kind: "stopped", Reason: "check stopped before dispatch"}
		} else {
			j.Stage = "checking"
		}
	}); e != nil {
		return e
	}
	if stopping {
		return nil
	}
	var out struct {
		Exit   int    `json:"exitCode"`
		Stdout string `json:"stdout"`
		Stderr string `json:"stderr"`
	}
	e = c.command(ctx, id, peer, map[string]any{
		"command":           envCommand(env, binary, "test", "./..."),
		"cwd":               j.Workspace.WorkDir,
		"permissionProfile": "runner-check",
		"processId":         id,
		"timeoutMs":         max(1, time.Until(j.Assignment.Deadline).Milliseconds()),
		"outputBytesCap":    int(j.Assignment.Limits.MaxLog),
	}, &out)
	if e != nil {
		return e
	}
	diagnostic := out.Stdout + out.Stderr
	if len(diagnostic) > int(j.Assignment.Limits.MaxLog) {
		diagnostic = diagnostic[:int(j.Assignment.Limits.MaxLog)]
	}
	if e = c.disk.Write("logs/"+id+"/check", []byte(diagnostic), j.Assignment.Limits.MaxLog); e != nil {
		return e
	}
	return c.mutate(id, func(j *nativeJob) {
		if j.End != nil {
			return
		}
		if out.Exit != 0 {
			j.End = &runner.ExecutionEnd{Kind: "failed", Reason: fmt.Sprintf("Go check exited %d: %s", out.Exit, runner.Clip(diagnostic))}
		} else {
			j.End = &runner.ExecutionEnd{
				Kind:   "returned",
				Result: &runner.StepResult{Outcome: "ready", Outputs: map[string]runner.Value{"check": runner.Text("go test ./... passed")}},
			}
		}
	})
}

// A terminate acknowledgement is not settlement; only the command's final
// response ends the job. A lost connection leaves ownership/space reserved.
func (c *Codex) stopCheck(ctx context.Context, id string) error {
	c.mu.Lock()
	cmd, ok := c.commands[id]
	c.mu.Unlock()
	if !ok {
		return errors.New("native command settlement is unconfirmed; retain its workspace and inspect the original execution")
	}
	select {
	case <-cmd.peer.Done():
		return errors.New("native command connection was lost; child settlement is unconfirmed")
	default:
	}
	return cmd.peer.Call(ctx, "command/exec/terminate", map[string]string{"processId": cmd.id}, nil)
}
func (c *Codex) command(ctx context.Context, id string, peer codexPeer, params map[string]any, out any) error {
	stoppedBefore := false
	if e := c.mutate(id, func(j *nativeJob) {
		if j.StopRequested || c.closing {
			stoppedBefore = true
			j.End = &runner.ExecutionEnd{Kind: "stopped", Reason: "command stopped before dispatch"}
		} else if j.Stage != "checking" {
			j.Stage = "checking_readiness"
		}
	}); e != nil {
		return e
	}
	if stoppedBefore {
		return context.Canceled
	}
	processID := id
	params["processId"] = processID
	c.mu.Lock()
	c.commands[id] = nativeCommand{peer: peer, id: processID}
	c.mu.Unlock()
	e := peer.Call(ctx, "command/exec", params, out)
	if e != nil {
		return e
	}
	c.mu.Lock()
	delete(c.commands, id)
	c.mu.Unlock()
	stopped := false
	e = c.mutate(id, func(j *nativeJob) {
		j.Stage = "command_settled"
		if j.StopRequested || c.closing {
			stopped = true
			j.End = &runner.ExecutionEnd{Kind: "stopped", Reason: "native command completion confirmed after stop"}
		}
	})
	if e != nil {
		return e
	}
	if stopped {
		return context.Canceled
	}
	return nil
}

const workspaceProbe = `package main
import("errors";"net";"os";"path/filepath";"syscall";"time")
func main(){
 root,private,mode,escape:=os.Args[1],os.Args[2],os.Args[3],os.Args[4]
 target:=os.Getenv("RUNNER_SOURCE_PROBE");err:=os.WriteFile(target,[]byte("probe"),0600)
 if (err==nil)!=(mode=="write"){panic("source write boundary")};if err==nil{os.Remove(target)}
 denied:=func(path string){_,err:=os.ReadFile(path);if !errors.Is(err,os.ErrPermission){panic("private/Git/symlink read boundary")}}
 denied(private);denied(filepath.Join(root,".git"));if escape!=""{denied(escape)}
 for _,name:=range os.Args[5:]{f,err:=os.OpenFile(filepath.Join(root,name),os.O_WRONLY,0);if err==nil{f.Close();panic("protected source write boundary")};if !errors.Is(err,os.ErrPermission){panic("protected source not checked")}}
 if cache:=os.Getenv("GOMODCACHE");cache!=""{if _,err:=os.ReadDir(cache);err!=nil{panic("module cache read boundary")};target:=filepath.Join(cache,".runner-write-probe");f,err:=os.OpenFile(target,os.O_WRONLY|os.O_CREATE|os.O_EXCL,0600);if err==nil{f.Close();os.Remove(target);panic("module cache write boundary")};if !errors.Is(err,os.ErrPermission){panic("module cache not checked")}}
 if reporter:=os.Getenv("RUNNER_GO_CHECK");reporter!=""{if _,err:=os.ReadFile(reporter);err!=nil{panic("Go check runtime read boundary")};f,err:=os.OpenFile(reporter,os.O_WRONLY,0);if err==nil{f.Close();panic("Go check runtime write boundary")};if !errors.Is(err,os.ErrPermission){panic("Go check runtime not checked")}}
 conn,err:=net.DialTimeout("tcp","127.0.0.1:9",time.Second);if conn!=nil{conn.Close()};if !errors.Is(err,syscall.EPERM)&&!errors.Is(err,syscall.EACCES){panic("network boundary")}
}
`

func workspaceFiles(cfg *runner.CodexSettings, name, goRoot string) map[string]any {
	files := map[string]any{":minimal": "read", goRoot: "read"}
	if name != "runner-input" {
		if cfg.GoModuleCache != "" {
			files[cfg.GoModuleCache] = "read"
		}
		if cfg.GoCheckBinary != "" {
			files[cfg.GoCheckBinary] = "read"
		}
		mode := "read"
		if name == "runner-edit" {
			mode = "write"
		}
		roots := map[string]any{".": mode, ".runner-tmp": "write"}
		for _, path := range cfg.ReadOnlyPaths {
			roots[path] = "read"
		}
		for _, path := range []string{".git", ".codex", ".agents", ".aws"} {
			roots[path] = "deny"
		}
		files[":workspace_roots"] = roots
	}
	return files
}
func (c *Codex) checkWorkspaceConfig(ctx context.Context, peer codexPeer) error {
	_, goRoot, e := c.goInstallation(ctx)
	if e != nil {
		return e
	}
	var r struct {
		Config struct {
			Permissions map[string]struct {
				Filesystem map[string]any  `json:"filesystem"`
				Network    map[string]any  `json:"network"`
				Roots      map[string]bool `json:"workspace_roots"`
				Extends    *string         `json:"extends"`
			} `json:"permissions"`
		} `json:"config"`
	}
	if e = peer.Call(ctx, "config/read", map[string]any{"includeLayers": false}, &r); e != nil {
		return e
	}
	if len(r.Config.Permissions) != 3 {
		return errors.New("native workspace permission catalog differs from the selected configuration")
	}
	for _, name := range []string{"runner-input", "runner-edit", "runner-check"} {
		profile, ok := r.Config.Permissions[name]
		if !ok || profile.Extends != nil || len(profile.Roots) != 0 {
			return errors.New("native workspace permissions include unexpected inheritance/roots")
		}
		if profile.Filesystem["glob_scan_max_depth"] == nil {
			delete(profile.Filesystem, "glob_scan_max_depth")
		}
		if !reflect.DeepEqual(profile.Filesystem, workspaceFiles(c.cfg, name, goRoot)) {
			return fmt.Errorf("native filesystem profile %s differs from the selected boundaries", name)
		}
		if profile.Network["enabled"] != false {
			return errors.New("native task networking was not disabled")
		}
		for key, v := range profile.Network {
			if key != "enabled" && v != nil && v != false {
				return errors.New("native network profile contains unexpected settings")
			}
		}
	}
	return nil
}
