package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Re-executing the test binary supplies a model-free fake native CLI.
func TestMain(m *testing.M) {
	if filepath.Base(os.Args[0]) == "codex-fake" {
		fakeCLI()
		return
	}
	os.Exit(m.Run())
}

type fakeConfig struct {
	Marker, Behavior string
}

type observation struct {
	Home, CWD, Temp string
	Args            []string
	HomeMode        os.FileMode
	ConfigMode      os.FileMode
	CWDMode         os.FileMode
	AuthLinked      bool
	TokenCorrect    bool
	PromptCorrect   bool
}

func fakeCLI() {
	home := os.Getenv("CODEX_HOME")
	b, _ := os.ReadFile(filepath.Join(home, "config.toml"))
	var c fakeConfig
	if json.Unmarshal(b, &c) != nil {
		os.Exit(90)
	}
	prompt, _ := io.ReadAll(os.Stdin)
	link, _ := os.Lstat(filepath.Join(home, "auth.json"))
	hi, _ := os.Stat(home)
	ci, _ := os.Stat(filepath.Join(home, "config.toml"))
	cwd, _ := os.Getwd()
	wi, _ := os.Stat(cwd)
	observed := observation{
		Home:          home,
		CWD:           cwd,
		Temp:          os.Getenv("TMPDIR"),
		Args:          os.Args[1:],
		HomeMode:      hi.Mode().Perm(),
		ConfigMode:    ci.Mode().Perm(),
		CWDMode:       wi.Mode().Perm(),
		AuthLinked:    link.Mode()&os.ModeSymlink != 0,
		TokenCorrect:  os.Getenv("RUNNER_OWNER_TOKEN") == "owner-secret-unique",
		PromptCorrect: string(prompt) == "authorized secret request\nacceptance condition",
	}
	launches, _ := os.OpenFile(c.Marker+".launches", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	_, _ = launches.WriteString("launch\n")
	_ = launches.Close()
	b, _ = json.Marshal(observed)
	_ = os.WriteFile(c.Marker, b, 0600)
	if c.Behavior == "wait" {
		for {
			time.Sleep(time.Hour)
		}
	}
	if c.Behavior == "fail" {
		fmt.Println(`{"type":"turn.failed","error":{"message":"failure"}}`)
		fmt.Fprintln(os.Stderr, "native error owner-secret-unique auth-secret-unique")
		os.Exit(7)
	}
	// Large unrelated events must be drained without being retained as diagnostics.
	fmt.Println(`{"type":"item.completed","item":{"type":"reasoning","text":"` + strings.Repeat("x", maxEvent) + `"}}`)
	fmt.Println(`{"type":"item.completed","item":{"type":"agent_message","text":"JSON fallback final"}}`)
	fmt.Println(`{"type":"turn.completed","usage":{"input_tokens":123,"cached_input_tokens":100,"output_tokens":17}}`)
	for i, arg := range os.Args {
		if arg == "--output-last-message" && i+1 < len(os.Args) {
			_ = os.WriteFile(os.Args[i+1], []byte("Reviewed local candidate run-123."), 0600)
		}
	}
	fmt.Fprintln(os.Stderr, strings.Repeat("diagnostic line\n", 800))
	fmt.Fprintln(os.Stderr, "owner-secret-unique auth-secret-unique "+string(prompt))
}

func fixture(t *testing.T, behavior string) (options, string) {
	t.Helper()
	dir := t.TempDir()
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cli := filepath.Join(dir, "codex-fake")
	if err := os.Symlink(bin, cli); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, "observed.json")
	config, _ := json.Marshal(fakeConfig{Marker: marker, Behavior: behavior})
	files := map[string][]byte{
		"config.toml": config,
		"auth.json":   []byte(`{"tokens":{"access_token":"auth-secret-unique"}}`),
		"token":       []byte("owner-secret-unique\n"),
		"request":     []byte("authorized secret request\nacceptance condition"),
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return options{
		execute:  true,
		cli:      cli,
		config:   filepath.Join(dir, "config.toml"),
		auth:     filepath.Join(dir, "auth.json"),
		token:    filepath.Join(dir, "token"),
		request:  filepath.Join(dir, "request"),
		parent:   dir,
		deadline: time.Now().Add(10 * time.Second),
	}, marker
}

func TestFreshSingleLaunchReceiptAndCleanup(t *testing.T) {
	o, marker := fixture(t, "success")
	r := run(context.Background(), o)
	if r.Status != "succeeded" || !r.Started || !r.ChildExited || !r.CleanupComplete || r.ExitCode == nil || *r.ExitCode != 0 {
		t.Fatalf("launch did not settle cleanly: %+v", r)
	}
	if r.FinalMessage != "Reviewed local candidate run-123." || r.FinalTruncated {
		t.Fatalf("actual final message was not captured: %+v", r)
	}
	if !r.Usage.Complete || *r.Usage.InputTokens != 123 || *r.Usage.CachedInputTokens != 100 || *r.Usage.OutputTokens != 17 || r.ElapsedSeconds <= 0 {
		t.Fatalf("incorrect usage/elapsed: %+v", r)
	}
	b, _ := json.Marshal(r)
	for _, secret := range []string{"owner-secret-unique", "auth-secret-unique", "authorized secret request", "acceptance condition", strings.Repeat("x", 100)} {
		if strings.Contains(string(b), secret) {
			t.Fatalf("receipt exposed sensitive input or transcript: %q", secret)
		}
	}
	if len(r.DiagnosticTail) > maxTail || !r.DiagnosticsTruncated || !strings.Contains(r.DiagnosticTail, "[redacted]") {
		t.Fatalf("diagnostic tail not bounded/redacted: %+v", r)
	}
	b, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	var observed observation
	if err := json.Unmarshal(b, &observed); err != nil {
		t.Fatal(err)
	}
	if observed.HomeMode != 0700 || observed.ConfigMode != 0600 || observed.CWDMode != 0500 || !observed.AuthLinked || !observed.TokenCorrect || !observed.PromptCorrect {
		t.Fatalf("incorrect private launch boundary: %+v", observed)
	}
	for _, path := range []string{observed.Home, observed.CWD, observed.Temp} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("owned state retained after exit: %s (%v)", path, err)
		}
	}
	wantArgs := []string{"exec", "--json", "--ephemeral", "--ignore-rules", "--color", "never", "--skip-git-repo-check", "--sandbox", "read-only", "--disable", "shell_tool", "-c", `approval_policy="never"`, "--cd", observed.CWD, "--output-last-message", filepath.Join(filepath.Dir(observed.Home), "last-message"), "-"}
	if !reflect.DeepEqual(observed.Args, wantArgs) {
		t.Fatalf("unexpected CLI argv: %q", observed.Args)
	}
	launches, _ := os.ReadFile(marker + ".launches")
	if string(launches) != "launch\n" {
		t.Fatalf("expected exactly one launch, got %q", launches)
	}
	if _, err := os.Stat(o.auth); err != nil {
		t.Fatalf("cleanup removed source auth: %v", err)
	}
}

func TestFailurePreservesUnknownUsageWithoutRetry(t *testing.T) {
	o, marker := fixture(t, "fail")
	r := run(context.Background(), o)
	if r.Status != "failed" || !r.ChildExited || !r.CleanupComplete || r.ExitCode == nil || *r.ExitCode != 7 {
		t.Fatalf("unexpected failure receipt: %+v", r)
	}
	if r.Usage.Complete || r.Usage.InputTokens != nil || r.Usage.CachedInputTokens != nil || r.Usage.OutputTokens != nil {
		t.Fatalf("invented usage: %+v", r.Usage)
	}
	launches, _ := os.ReadFile(marker + ".launches")
	if string(launches) != "launch\n" || strings.Contains(r.DiagnosticTail, "secret-unique") || !strings.Contains(r.DiagnosticTail, "failure") {
		t.Fatalf("retry or secret exposure: %+v", r)
	}
}

func TestDeadlineSettlesClientBeforeCleanup(t *testing.T) {
	o, _ := fixture(t, "wait")
	o.deadline = time.Now().Add(time.Second)
	r := run(context.Background(), o)
	if r.Status != "deadline" || !r.Started || !r.ChildExited || !r.CleanupComplete || r.Usage.Complete {
		t.Fatalf("deadline failed to settle and clean client: %+v", r)
	}
}

func TestExecutionRequiresOptInAndAbsoluteDeadline(t *testing.T) {
	for _, args := range [][]string{
		{},
		{"--request", "unused", "--deadline", time.Now().Add(time.Hour).Format(time.RFC3339)},
		{"--execute", "--request", "unused", "--deadline", "5m"},
		{"--execute", "--request", "unused", "--deadline", "2000-01-01T00:00:00Z"},
	} {
		if _, err := parseOptions(args, io.Discard); err == nil {
			t.Fatalf("accepted unbounded/unapproved launch: %q", args)
		}
	}
	o, marker := fixture(t, "success")
	o.execute = false
	r := run(context.Background(), o)
	if r.Started || !r.CleanupComplete || r.Status != "not_started" {
		t.Fatalf("execution occurred without opt-in: %+v", r)
	}
	if _, err := os.Stat(marker + ".launches"); !os.IsNotExist(err) {
		t.Fatal("CLI launched without opt-in")
	}
}

func TestIntakeSelectsAnExplicitInstance(t *testing.T) {
	deadline := time.Now().Add(time.Hour).Format(time.RFC3339)
	args := []string{"--execute", "--request", "request.txt", "--deadline", deadline}
	if _, err := parseOptions(args, io.Discard); err == nil {
		t.Fatal("implicit personal instance accepted")
	}
	args = append(args, "--cli", "/tools/codex", "--config", "/private/config.toml", "--auth", "/private/auth.json", "--token", "/private/owner.token")
	o, err := parseOptions(args, io.Discard)
	if err != nil || o.config != "/private/config.toml" || o.token != "/private/owner.token" {
		t.Fatal(o, err)
	}
}
