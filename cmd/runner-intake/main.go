// runner-intake launches one fresh restricted coordinator, never a Runner job loop.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"
)

const (
	maxInput = 128 << 10
	maxEvent = 1 << 20
	maxFinal = 32 << 10
	maxTail  = 4 << 10
)

type options struct {
	execute                                   bool
	cli, config, auth, token, request, parent string
	deadline                                  time.Time
}

type usage struct {
	InputTokens       *int64 `json:"input_tokens"`
	CachedInputTokens *int64 `json:"cached_input_tokens"`
	OutputTokens      *int64 `json:"output_tokens"`
	Complete          bool   `json:"complete"`
}

type receipt struct {
	Status               string  `json:"status"`
	Started              bool    `json:"started"`
	ChildExited          bool    `json:"child_exited"`
	ExitCode             *int    `json:"exit_code"`
	ElapsedSeconds       float64 `json:"elapsed_seconds"`
	Usage                usage   `json:"usage"`
	FinalMessage         string  `json:"final_message,omitempty"`
	FinalTruncated       bool    `json:"final_truncated,omitempty"`
	DiagnosticTail       string  `json:"diagnostic_tail,omitempty"`
	DiagnosticsTruncated bool    `json:"diagnostics_truncated,omitempty"`
	Error                string  `json:"error,omitempty"`
	CleanupComplete      bool    `json:"cleanup_complete"`
	RetainedDirectory    string  `json:"retained_directory,omitempty"`
}

func main() {
	o, err := parseOptions(os.Args[1:], os.Stderr)
	if err != nil {
		_ = json.NewEncoder(os.Stdout).Encode(receipt{Status: "not_started", Error: err.Error(), CleanupComplete: true})
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	r := run(ctx, o)
	_ = json.NewEncoder(os.Stdout).Encode(r)
	if r.Status != "succeeded" {
		os.Exit(1)
	}
}

func parseOptions(args []string, out io.Writer) (options, error) {
	var o options
	var deadline string
	f := flag.NewFlagSet("runner-intake", flag.ContinueOnError)
	f.SetOutput(out)
	f.BoolVar(&o.execute, "execute", false, "authorize exactly one native CLI execution")
	f.StringVar(&o.cli, "cli", "", "explicit pinned Codex executable")
	f.StringVar(&o.config, "config", "", "explicit restricted template config")
	f.StringVar(&o.auth, "auth", "", "explicit auth file or link (linked, never copied)")
	f.StringVar(&o.token, "token", "", "explicit Runner owner token file")
	f.StringVar(&o.request, "request", "", "file containing the authorized task (sent on stdin)")
	f.StringVar(&o.parent, "temp-parent", os.TempDir(), "parent for owned private temporary directory")
	f.StringVar(&deadline, "deadline", "", "required absolute RFC3339 deadline")
	if err := f.Parse(args); err != nil {
		return o, errors.New("invalid flags; see --help")
	}
	if f.NArg() != 0 || !o.execute || o.request == "" || o.cli == "" || o.config == "" || o.auth == "" || o.token == "" {
		return o, errors.New("--execute, --cli, --config, --auth, --token, --request and an absolute --deadline are required")
	}
	var err error
	o.deadline, err = time.Parse(time.RFC3339, deadline)
	if err != nil || !o.deadline.After(time.Now()) {
		return o, errors.New("--deadline must be a future absolute RFC3339 time")
	}
	return o, nil
}

func run(parent context.Context, o options) (r receipt) {
	r.Status = "not_started"
	r.CleanupComplete = true
	if !o.execute || o.deadline.IsZero() || !o.deadline.After(time.Now()) {
		r.Error = "explicit execution and a future absolute deadline are required"
		return
	}
	ctx, cancel := context.WithDeadline(parent, o.deadline)
	defer cancel()
	config, err := readBounded(o.config, maxInput)
	if err != nil {
		r.Error = "cannot read restricted template config"
		return
	}
	prompt, err := readBounded(o.request, maxInput)
	if err != nil || len(bytes.TrimSpace(prompt)) == 0 {
		r.Error = "cannot read a nonempty request within 128 KiB"
		return
	}
	token, err := readBounded(o.token, maxInput)
	if err != nil || len(bytes.TrimSpace(token)) == 0 || bytes.ContainsAny(bytes.TrimSpace(token), "\r\n\x00") {
		r.Error = "cannot read a valid owner token"
		return
	}
	token = bytes.TrimSpace(token)
	authPath, err := filepath.EvalSymlinks(o.auth)
	if err == nil {
		authPath, err = filepath.Abs(authPath)
	}
	if err != nil {
		r.Error = "cannot resolve auth link"
		return
	}
	auth, err := readBounded(authPath, maxInput)
	if err != nil || !json.Valid(auth) {
		r.Error = "cannot read auth for diagnostic redaction"
		return
	}
	clean := newRedactor(string(token), string(prompt), auth)
	tempParent, err := filepath.EvalSymlinks(o.parent)
	if err == nil {
		tempParent, err = filepath.Abs(tempParent)
	}
	if err != nil {
		r.Error = "cannot resolve temporary parent directory"
		return
	}
	root, err := os.MkdirTemp(tempParent, "runner-intake-")
	if err != nil {
		r.Error = "cannot create private temporary directory"
		return
	}
	r.CleanupComplete = false
	defer func() {
		// Never remove a live child's home or session state.
		if !r.Started || r.ChildExited {
			_ = os.Chmod(filepath.Join(root, "empty"), 0700)
			if os.RemoveAll(root) == nil {
				r.CleanupComplete = true
				return
			}
		}
		r.RetainedDirectory = root
	}()
	home, cwd, tmp := filepath.Join(root, "home"), filepath.Join(root, "empty"), filepath.Join(root, "tmp")
	for _, path := range []string{home, cwd, tmp} {
		if err = os.Mkdir(path, 0700); err != nil {
			r.Error = "cannot prepare private intake directories"
			return
		}
	}
	if err = os.WriteFile(filepath.Join(home, "config.toml"), config, 0600); err == nil {
		err = os.Symlink(authPath, filepath.Join(home, "auth.json"))
	}
	if err == nil {
		err = os.Chmod(cwd, 0500)
	}
	if err != nil {
		r.Error = "cannot prepare private config and read-only workspace"
		return
	}
	last := filepath.Join(root, "last-message")
	cmd := exec.CommandContext(ctx, o.cli, "exec", "--json", "--ephemeral", "--ignore-rules", "--color", "never", "--skip-git-repo-check", "--sandbox", "read-only", "--disable", "shell_tool", "-c", `approval_policy="never"`, "--cd", cwd, "--output-last-message", last, "-")
	cmd.Dir = cwd
	cmd.Env = childEnv(home, tmp, string(token))
	cmd.Stdin = bytes.NewReader(prompt)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Killing this client only disconnects MCP. It never sends Runner cancellation.
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 2 * time.Second
	stream := &events{}
	tail := &boundedTail{limit: maxTail + maxInput}
	cmd.Stdout, cmd.Stderr = stream, tail
	start := time.Now()
	if err = cmd.Start(); err != nil {
		r.Error = "native CLI did not start"
		return
	}
	r.Started = true
	err = cmd.Wait()
	r.ElapsedSeconds = time.Since(start).Seconds()
	r.ChildExited = cmd.ProcessState != nil && cmd.ProcessState.Exited()
	// ProcessState.Exited is false for a signaled process, which is still settled.
	if cmd.ProcessState != nil {
		if status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			r.ChildExited = true
		}
		code := cmd.ProcessState.ExitCode()
		r.ExitCode = &code
	}
	stream.finish()
	r.Usage = stream.usage
	r.FinalMessage = stream.final
	if final, readErr := readBounded(last, maxEvent); readErr == nil {
		r.FinalMessage, r.FinalTruncated = string(final), false
	} else if errors.Is(readErr, errTooLarge) {
		// Keep the bounded actual agent message from JSON instead.
		r.FinalTruncated = true
	}
	r.FinalMessage = clean(r.FinalMessage)
	r.FinalTruncated = r.FinalTruncated || len(r.FinalMessage) > maxFinal
	r.FinalMessage = prefix(r.FinalMessage, maxFinal)
	diagnostics := string(tail.data)
	if tail.truncated {
		// Drop a clipped line so a cut credential cannot evade exact redaction.
		if newline := strings.IndexByte(diagnostics, '\n'); newline >= 0 {
			diagnostics = diagnostics[newline+1:]
		} else {
			diagnostics = ""
		}
	}
	r.DiagnosticTail = clean(diagnostics + stream.problem)
	r.DiagnosticsTruncated = tail.truncated || len(r.DiagnosticTail) > maxTail
	r.DiagnosticTail = suffix(r.DiagnosticTail, maxTail)
	r.Status = "succeeded"
	switch {
	case ctx.Err() != nil:
		r.Status = "interrupted"
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			r.Status = "deadline"
		}
		r.Error = "intake stopped; any Runner run must be rejoined by its existing identity"
	case err != nil || stream.failed:
		r.Status = "failed"
		r.Error = "native CLI failed; do not retry submission automatically"
	case r.FinalMessage == "":
		r.Status, r.Error = "failed", "native CLI exited without a final agent message"
	}
	if stream.discarded {
		diagnostic := r.DiagnosticTail + "\nMalformed or oversized CLI JSON events were discarded."
		r.DiagnosticsTruncated = r.DiagnosticsTruncated || len(diagnostic) > maxTail
		r.DiagnosticTail = suffix(diagnostic, maxTail)
	}
	return
}

func childEnv(home, tmp, token string) []string {
	var out []string
	for _, key := range []string{"PATH", "HOME", "USER", "LOGNAME", "LANG", "LC_ALL", "SSL_CERT_FILE", "SSL_CERT_DIR"} {
		if value, ok := os.LookupEnv(key); ok {
			out = append(out, key+"="+value)
		}
	}
	return append(out, "CODEX_HOME="+home, "RUNNER_OWNER_TOKEN="+token, "TMPDIR="+tmp, "TMP="+tmp, "TEMP="+tmp, "NO_COLOR=1")
}

var errTooLarge = errors.New("file exceeds limit")

func readBounded(path string, limit int) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if len(b) > limit {
		return nil, errTooLarge
	}
	return b, err
}

// No raw CLI event/transcript is retained. Even oversized lines are fully drained.
type events struct {
	line                        []byte
	dropping, discarded, failed bool
	final                       string
	problem                     string
	usage                       usage
}

func (e *events) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			i = len(p)
		}
		if !e.dropping {
			if len(e.line)+i > maxEvent {
				e.line, e.dropping, e.discarded = nil, true, true
			} else {
				e.line = append(e.line, p[:i]...)
			}
		}
		p = p[i:]
		if len(p) > 0 {
			e.finish()
			p = p[1:]
		}
	}
	return n, nil
}

func (e *events) finish() {
	defer func() { e.line, e.dropping = e.line[:0], false }()
	if e.dropping || len(bytes.TrimSpace(e.line)) == 0 {
		return
	}
	var v struct {
		Type    string                      `json:"type"`
		Item    struct{ Type, Text string } `json:"item"`
		Usage   *usage                      `json:"usage"`
		Error   struct{ Message string }    `json:"error"`
		Message string                      `json:"message"`
	}
	if json.Unmarshal(e.line, &v) != nil {
		e.discarded = true
		return
	}
	switch v.Type {
	case "item.completed":
		if v.Item.Type == "agent_message" {
			e.final = v.Item.Text
		}
	case "turn.completed":
		if v.Usage != nil {
			e.usage = *v.Usage
			e.usage.Complete = e.usage.InputTokens != nil && e.usage.CachedInputTokens != nil && e.usage.OutputTokens != nil
		}
	case "turn.failed", "error":
		e.failed = true
		e.problem = "\n" + v.Error.Message + v.Message
	}
}

type boundedTail struct {
	mu        sync.Mutex
	data      []byte
	limit     int
	truncated bool
}

func (t *boundedTail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	n := len(p)
	if len(t.data)+n > t.limit {
		t.truncated = true
		if n >= t.limit {
			t.data = append(t.data[:0], p[n-t.limit:]...)
			return n, nil
		}
		t.data = t.data[len(t.data)+n-t.limit:]
	}
	t.data = append(t.data, p...)
	return n, nil
}

func newRedactor(token, prompt string, auth []byte) func(string) string {
	secrets := []string{token, prompt}
	secrets = append(secrets, strings.Split(prompt, "\n")...)
	var value any
	if json.Unmarshal(auth, &value) == nil {
		var collect func(any)
		collect = func(v any) {
			switch v := v.(type) {
			case string:
				secrets = append(secrets, v)
				secrets = append(secrets, strings.Split(v, "\n")...)
			case map[string]any:
				for _, child := range v {
					collect(child)
				}
			case []any:
				for _, child := range v {
					collect(child)
				}
			}
		}
		collect(value)
	}
	sort.Slice(secrets, func(i, j int) bool { return len(secrets[i]) > len(secrets[j]) })
	return func(s string) string {
		for _, secret := range secrets {
			if strings.TrimSpace(secret) != "" {
				s = strings.ReplaceAll(s, secret, "[redacted]")
				encoded, _ := json.Marshal(secret)
				s = strings.ReplaceAll(s, string(encoded[1:len(encoded)-1]), "[redacted]")
			}
		}
		return strings.ToValidUTF8(s, "�")
	}
}

func prefix(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	s = s[:limit]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

func suffix(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	s = s[len(s)-limit:]
	for !utf8.ValidString(s) {
		s = s[1:]
	}
	return s
}
