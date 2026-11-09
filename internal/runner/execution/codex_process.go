package execution

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

const nativeConfig = `sandbox_mode="read-only"
approval_policy="never"
project_doc_max_bytes=0
web_search="disabled"
include_apps_instructions=false
include_collaboration_mode_instructions=false
include_environment_context=false
include_environment_context_time=false
[features]
shell_tool=false
apply_patch_freeform=false
view_image=false
multi_agent=false
apps=false
hooks=false
remote_plugin=false
plugins=false
goals=false
memories=false
skill_mcp_dependency_install=false
skip_host_skill_discovery=true
shell_snapshot=false
request_permissions=false
tool_suggest=false
[tools.update_plan]
enabled=false
[tools.experimental_request_user_input]
enabled=false
[skills]
include_instructions=false
[skills.bundled]
enabled=false
[history]
persistence="none"
max_bytes=0
[analytics]
enabled=false
[feedback]
enabled=false
`

type serverReceipt struct {
	PID        int    `json:"pid"`
	Binary     string `json:"binary"`
	AuthFile   string `json:"auth_file"`
	Generation string `json:"generation"`
}
type nativeProcess struct {
	cmd  *exec.Cmd
	done chan struct{}
}

// A bounded diagnostic tail; stdout is also drained here because the service
// protocol uses the private socket, never stdout parsing.
type diagnosticTail struct {
	mu    sync.Mutex
	data  []byte
	limit int
}

func (t *diagnosticTail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	n := len(p)
	t.data = append(t.data, p...)
	if len(t.data) > t.limit {
		t.data = append([]byte(nil), t.data[len(t.data)-t.limit:]...)
	}
	return n, nil
}
func (t *diagnosticTail) bytes() []byte {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]byte(nil), t.data...)
}

func (c *Codex) openPeer(ctx context.Context) (codexPeer, error) {
	c.connMu.Lock()
	defer c.connMu.Unlock()
	if c.peer != nil {
		select {
		case <-c.peer.Done():
			c.peer = nil
		default:
			return c.peer, nil
		}
	}
	if c.cfg == nil {
		return nil, errors.New("readiness: instance Codex settings are absent")
	}
	if err := ValidateNativeStatePath(c.disk.Root, c.cfg); err != nil {
		return nil, err
	}
	socket := filepath.Join(c.disk.Root, "jobs", "codex.sock")
	if _, err := os.Lstat(socket); err == nil {
		var receipt serverReceipt
		b, e := c.disk.Read("jobs/codex-server.json", 4096)
		if e != nil || json.Unmarshal(b, &receipt) != nil || receipt.AuthFile != c.cfg.AuthFile {
			return nil, errors.New("native service ownership/authentication receipt is unavailable or differs from this configuration")
		}
		peer, err := c.connect(ctx, socket, c.receive)
		if err == nil {
			c.bindPeerLocked(peer)
			c.generation = receipt.Generation
			return peer, nil
		}
		// Never replace a service that may still be running.
		if processAlive(receipt.PID) {
			return nil, errors.New("native service is unreachable but may still be running; reconcile its recorded process")
		}
		if err = os.Remove(socket); err != nil {
			return nil, err
		}
	} else if b, e := c.disk.Read("jobs/codex-server.json", 4096); e == nil {
		var r serverReceipt
		if json.Unmarshal(b, &r) != nil || r.AuthFile != c.cfg.AuthFile || processAlive(r.PID) {
			return nil, errors.New("native socket is absent but recorded service ownership is unresolved")
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return nil, e
	}
	if err := c.prepareHome(); err != nil {
		return nil, err
	}
	binary, err := exec.LookPath(c.cfg.Binary)
	if err != nil {
		return nil, fmt.Errorf("readiness: %w", err)
	}
	cmd := exec.Command(binary, "app-server", "--listen", "unix://"+socket, "--strict-config")
	cmd.Dir = filepath.Join(c.disk.Root, "native", "empty")
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
		"CODEX_HOME=" + filepath.Join(c.disk.Root, "native"),
	}
	setNativeProcessGroup(cmd)
	cmd.Stdout = c.tail
	cmd.Stderr = c.tail
	if err = cmd.Start(); err != nil {
		return nil, err
	}
	p := &nativeProcess{cmd: cmd, done: make(chan struct{})}
	c.proc = p
	go func() { _ = cmd.Wait(); close(p.done); c.signal() }()
	c.generation = rand.Text()
	raw, _ := json.Marshal(serverReceipt{cmd.Process.Pid, binary, c.cfg.AuthFile, c.generation})
	if err = c.disk.Write("jobs/codex-server.json", raw, 4096); err != nil {
		_ = cmd.Process.Kill()
		return nil, err
	}
	timer := time.NewTicker(20 * time.Millisecond)
	defer timer.Stop()
	for {
		peer, e := c.connect(ctx, socket, c.receive)
		if e == nil {
			c.bindPeerLocked(peer)
			return peer, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-p.done:
			return nil, errors.New("native app-server exited during readiness; inspect its bounded diagnostic log")
		case <-timer.C:
		}
	}
}

func (c *Codex) bindPeerLocked(peer codexPeer) {
	c.peer = peer
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		select {
		case <-peer.Done():
			c.signal()
		case <-c.ctx.Done():
		}
	}()
}
func (c *Codex) prepareHome() error {
	info, err := os.Lstat(c.cfg.AuthFile)
	if err != nil {
		return fmt.Errorf("readiness: authentication file: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 64<<10 {
		return errors.New("readiness: authentication must be a private regular file no larger than 64KiB")
	}
	path := filepath.Join(c.disk.Root, "native", "auth.json")
	if old, e := os.Readlink(path); e == nil {
		if old != c.cfg.AuthFile {
			return errors.New("readiness: native authentication source changed; settle existing work before changing accounts")
		}
	} else if errors.Is(e, os.ErrNotExist) {
		if e = os.Symlink(c.cfg.AuthFile, path); e != nil {
			return e
		}
	} else {
		// Native managed authentication may atomically replace its initial link
		// with a private refreshed file. Cleanup still owns only this private copy.
		i, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if !i.Mode().IsRegular() || i.Mode().Perm()&0077 != 0 || i.Size() > 64<<10 {
			return errors.New("readiness: native authentication cache is not a private regular file")
		}
	}
	config, e := c.nativeConfiguration(context.Background())
	if e != nil {
		return e
	}
	if err = c.disk.Write("native/config.toml", []byte(config), 16384); err != nil {
		return err
	}
	return os.MkdirAll(filepath.Join(c.disk.Root, "native", "empty"), 0700)
}
func (c *Codex) stopServer(ctx context.Context) error {
	c.connMu.Lock()
	defer c.connMu.Unlock()
	if c.peer != nil {
		c.peer.Close()
		c.peer = nil
	}
	if c.proc != nil {
		_ = terminateNativeProcess(c.proc.cmd)
		select {
		case <-c.proc.done:
		case <-ctx.Done():
			_ = killNativeProcess(c.proc.cmd)
			<-c.proc.done
		}
		c.proc = nil
	} else {
		// A recovered service is stopped only through its live private socket's
		// kernel peer identity; a persisted PID alone is never kill authority.
		if err := stopNativeSocket(ctx, filepath.Join(c.disk.Root, "jobs", "codex.sock")); err != nil && !errors.Is(err, os.ErrNotExist) {
			b, e := c.disk.Read("jobs/codex-server.json", 4096)
			var r serverReceipt
			if e == nil && json.Unmarshal(b, &r) == nil && processAlive(r.PID) {
				return err
			}
		}
	}
	_ = os.Remove(filepath.Join(c.disk.Root, "jobs", "codex.sock"))
	return c.disk.Write("logs/codex-service", c.tail.bytes(), c.disk.Policy.Logs.MaxPerAttempt)
}
