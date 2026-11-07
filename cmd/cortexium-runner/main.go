package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
	"github.com/cortexium-io/runner/internal/runner/config"
	"github.com/cortexium-io/runner/internal/runner/control"
	"github.com/cortexium-io/runner/internal/runner/execution"
	"github.com/cortexium-io/runner/internal/runner/sqlite"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if e := run(ctx, os.Args[1:]); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
func run(ctx context.Context, args []string) error {
	if len(args) == 2 && args[0] == "command-worker" {
		return execution.RunCommandWorker(ctx, args[1])
	}
	if len(args) == 0 {
		return errors.New("usage: cortexium-runner serve|run|result|start|status|inspect|cancel|retry|recover-corrections|recover-collection|decide|wait|config-set")
	}
	command := args[0]
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	data := fs.String("data", "", "private state directory (required; use fresh state for this slice)")
	cfg := fs.String("config", "", "instance YAML/JSON file")
	endpoint := fs.String("endpoint", "http://127.0.0.1:8787/mcp", "existing local MCP endpoint")
	listen := fs.String("listen", "127.0.0.1:8787", "local service address")
	project := fs.String("project", "", "project ID")
	sourceRef := fs.String("source-ref", "", "local Git ref/commit; otherwise the project source default")
	process := fs.String("process", "", "process ID; otherwise project default")
	input := fs.String("inputs", "{}", "JSON object of inline inputs")
	artifacts := fs.String("artifacts", "{}", "JSON object of input artifact IDs")
	runID := fs.String("run", "", "run ID")
	decisionID := fs.String("decision", "", "pending decision ID")
	choice := fs.String("choice", "", "explicit approve or reject choice")
	wait := fs.Bool("wait", false, "wait for completion/Waiting with result")
	requestID := fs.String("request-id", "", "idempotency key; generated and printed before a write if omitted")
	attemptID := fs.String("attempt", "", "exact saved failed attempt for owner-authorized recovery")
	maxUses := fs.Int("max-uses", 0, "authorized total source-correction uses; recover-corrections requires 3")
	preserveSource := fs.Bool("preserve-source", false, "preserve a verified owned partial checkout as recovery evidence before restoring its saved candidate")
	profile := fs.String("profile", "", "named profile for authorized retry")
	extra := fs.Int("extra-attempts", 0, "additional explicitly authorized retry allowance")
	cleanupOnly := fs.Bool("cleanup-only", false, "retry retained terminal cleanup without execution or allowance changes")
	file := fs.String("file", "", "selected configuration file")
	field := fs.String("field", "", "dot-separated configuration field")
	value := fs.String("value", "null", "JSON value for config-set")
	if e := fs.Parse(args[1:]); e != nil {
		return e
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	if *data == "" {
		return errors.New("--data is required")
	}
	if command == "recover-corrections" && (*runID == "" || *attemptID == "" || *maxUses != 3 || *profile != "" || *extra != 0 || *cleanupOnly) {
		return errors.New("recover-corrections requires --run, --attempt and --max-uses 3; retry allowances/profiles are separate")
	}
	if command == "recover-collection" && (*runID == "" || *attemptID == "" || *maxUses != 0 || *profile != "" || *extra != 0 || *cleanupOnly || *preserveSource) {
		return errors.New("recover-collection requires --run and --attempt; it grants no retry allowance, profile or preservation override")
	}
	root, e := filepath.Abs(*data)
	if e != nil {
		return e
	}
	if command == "serve" {
		if *cfg == "" {
			return errors.New("serve requires --config")
		}
		return serve(ctx, *cfg, root, *listen)
	}
	token, e := os.ReadFile(filepath.Join(root, "owner.token"))
	if e != nil {
		return fmt.Errorf("service credentials unavailable; start the service explicitly: %w", e)
	}
	session, e := control.Connect(ctx, *endpoint, string(token))
	if e != nil {
		return e
	}
	defer session.Close()
	var method string
	var a any
	switch command {
	case "start", "run":
		var inputs map[string]any
		var refs map[string]string
		if e = json.Unmarshal([]byte(*input), &inputs); e != nil {
			return e
		}
		if e = json.Unmarshal([]byte(*artifacts), &refs); e != nil {
			return e
		}
		if *requestID == "" {
			*requestID = rand.Text()
		}
		fmt.Fprintln(os.Stderr, "request_id="+*requestID)
		method = "runner_" + command
		a = control.StartArgs{
			RequestID: *requestID,
			Project:   *project,
			Process:   *process,
			SourceRef: *sourceRef,
			Inputs:    inputs,
			Artifacts: refs,
		}
	case "status":
		method = "runner_status"
		a = struct{}{}
	case "inspect", "cancel":
		method = "runner_" + command
		a = control.RunArgs{RunID: *runID}
	case "result":
		method = "runner_result"
		a = control.ResultArgs{RunID: *runID, Wait: *wait}
	case "decide":
		if *requestID == "" {
			*requestID = rand.Text()
		}
		fmt.Fprintln(os.Stderr, "request_id="+*requestID)
		method = "runner_decide"
		a = runner.DecisionRequest{RequestID: *requestID, RunID: *runID, DecisionID: *decisionID, Choice: *choice}
	case "retry":
		if *requestID == "" {
			*requestID = rand.Text()
		}
		fmt.Fprintln(os.Stderr, "request_id="+*requestID)
		method = "runner_retry"
		a = runner.RetryRequest{RequestID: *requestID, RunID: *runID, Profile: *profile, ExtraAttempts: *extra, CleanupOnly: *cleanupOnly}
	case "recover-corrections":
		if *requestID == "" {
			*requestID = rand.Text()
		}
		fmt.Fprintln(os.Stderr, "request_id="+*requestID)
		method = "runner_recover_corrections"
		a = runner.RecoverCorrectionsRequest{RequestID: *requestID, RunID: *runID, AttemptID: *attemptID, MaxUses: *maxUses, PreserveSource: *preserveSource}
	case "recover-collection":
		if *requestID == "" {
			*requestID = rand.Text()
		}
		fmt.Fprintln(os.Stderr, "request_id="+*requestID)
		method = "runner_recover_collection"
		a = runner.RecoverCollectionRequest{RequestID: *requestID, RunID: *runID, AttemptID: *attemptID}
	case "wait":
		after := int64(0)
		for {
			b, e := control.Call(ctx, session, "runner_wait", control.WaitArgs{RunID: *runID, After: after, TimeoutSeconds: 30})
			if e != nil {
				return e
			}
			var s runner.Summary
			if e = json.Unmarshal(b, &s); e != nil {
				return e
			}
			after = s.Revision
			if s.Status == "Completed" || s.Status == "Failed" || s.Status == "Cancelled" || s.Status == "Waiting" {
				return printJSON(b)
			}
		}
	case "config-set":
		var v any
		if e = json.Unmarshal([]byte(*value), &v); e != nil {
			return e
		}
		method = "runner_configure"
		a = control.SetArgs{File: *file, Field: *field, Value: v}
	default:
		return fmt.Errorf("unknown command %q", command)
	}
	b, e := control.Call(ctx, session, method, a)
	if e != nil {
		return e
	}
	return printJSON(b)
}
func printJSON(b []byte) error {
	var v any
	if e := json.Unmarshal(b, &v); e != nil {
		return e
	}
	out, e := json.MarshalIndent(v, "", "  ")
	if e == nil {
		_, e = fmt.Fprintln(os.Stdout, string(out))
	}
	return e
}
func serve(ctx context.Context, path, root, addr string) error {
	host, _, e := net.SplitHostPort(addr)
	if e != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		return errors.New("listen must use a numeric loopback address")
	}
	loader, e := config.New(path)
	if e != nil {
		return e
	}
	snapshot, e := loader.Load()
	if e != nil {
		return e
	}
	store, e := sqlite.Open(root, snapshot.Instance.Storage)
	if e != nil {
		return e
	}
	defer store.Close()
	disk, e := runner.OpenDisk(root, snapshot.Instance.Storage)
	if e != nil {
		return e
	}
	if e = execution.ValidateNativeStatePath(disk.Root, snapshot.Instance.Codex); e != nil {
		return e
	}
	read := func(ctx context.Context, id string) ([]byte, error) {
		if _, e := store.Artifact(ctx, id); e != nil {
			return nil, e
		}
		return disk.Read("artifacts/"+id, disk.Policy.MaxArtifact)
	}
	executor := execution.NewService(disk, read, snapshot.Instance.Codex, snapshot.Instance.Release)
	defer func() {
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := executor.Close(shutdown); err != nil {
			fmt.Fprintln(os.Stderr, "native shutdown:", runner.Clip(err.Error()))
		}
	}()
	core, e := runner.New(ctx, store, executor, disk, snapshot)
	if e != nil {
		return e
	}
	defer core.Close()
	executor.SetWake(core.ExecutionChanged)
	tokenPath := filepath.Join(root, "owner.token")
	token, e := os.ReadFile(tokenPath)
	if errors.Is(e, os.ErrNotExist) {
		token = []byte(rand.Text() + rand.Text())
		e = os.WriteFile(tokenPath, token, 0600)
	}
	if e != nil {
		return e
	}
	if len(token) < 32 || len(token) > 128 {
		return errors.New("invalid owner token file")
	}
	var configMu sync.Mutex
	set := func(file, field string, value json.RawMessage) error {
		configMu.Lock()
		defer configMu.Unlock()
		next, e := loader.Set(file, field, value)
		if e == nil {
			core.Apply(next, nil)
		}
		return e
	}
	listener, e := net.Listen("tcp", addr)
	if e != nil {
		return e
	}
	server := &http.Server{
		Handler:           control.Handler(core, string(token), set),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       time.Minute,
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	fmt.Fprintln(os.Stderr, "Runner MCP endpoint: http://"+listener.Addr().String()+"/mcp")
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	maintenance := time.NewTicker(time.Minute)
	defer maintenance.Stop()
	for {
		select {
		case e = <-done:
			if !errors.Is(e, http.ErrServerClosed) {
				return e
			}
			return nil
		case <-ctx.Done():
			shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			return server.Shutdown(shutdown)
		case <-ticker.C:
			configMu.Lock()
			if loader.Changed() {
				next, err := loader.Load()
				core.Apply(next, err)
			}
			configMu.Unlock()
		case <-maintenance.C:
			if e = core.Collect(); e != nil {
				fmt.Fprintln(os.Stderr, "maintenance:", runner.Clip(e.Error()))
			}
		}
	}
}
