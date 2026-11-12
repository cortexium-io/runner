package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
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
		return errors.New("usage: cortexium-runner serve|run|result|start|status|inspect|cancel|retry|recover-corrections|recover-collection|recover-planning|recover-unpublished-card|recover-frontend-check|recover-frontend-timeouts|recover-browser-errors|decide|wait|config-set")
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
	revision := fs.Int64("revision", 0, "exact inspected run revision for owner-authorized recovery")
	patchSHA256 := fs.String("patch-sha256", "", "approved retained patch digest for frontend check recovery")
	helperSHA256 := fs.String("helper-sha256", "", "qualified frontend check helper digest")
	publicationHead := fs.String("publication-head", "", "exact retained draft publication head for owner-authorized recovery")
	reportFile := fs.String("report-file", "", "bounded saved browser-error report for owner-authorized handoff")
	serial := fs.Bool("serial", false, "recheck a settled catalogue candidate with one Vitest worker")
	assertionSHA256 := fs.String("assertion-sha256", "", "exact failed full-check report digest for one authorized assertion correction after diagnostic repair")
	checkAttemptID := fs.String("check-attempt", "", "exact prior check attempt for frontend timeout diagnosis")
	checkSHA256 := fs.String("check-sha256", "", "exact prior check report digest for frontend timeout diagnosis")
	groupAttempt := fs.String("group-attempt", "", "exact failed isolated group attempt")
	childRun := fs.String("child-run", "", "exact deadline-failed source child")
	childRevision := fs.Int64("child-revision", 0, "exact settled child revision")
	bindingsSHA := fs.String("bindings-sha256", "", "authenticated read-only card recovery preflight digest")
	preflight := fs.Bool("preflight", false, "read-only unpublished-card recovery preflight; no grant or dispatch")
	maxUses := fs.Int("max-uses", 0, "authorized total source-correction uses; recover-corrections requires 3")
	preserveSource := fs.Bool("preserve-source", false, "preserve a verified owned partial checkout as recovery evidence before restoring its saved candidate")
	profile := fs.String("profile", "", "named profile for authorized retry")
	extra := fs.Int("extra-attempts", 0, "additional explicitly authorized retry allowance")
	withinBudget := fs.Bool("within-budget", false, "grant retry or source-correction allowance from the existing total attempt budget")
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
	if *preflight && command != "recover-unpublished-card" {
		return errors.New("--preflight requires recover-unpublished-card")
	}
	if command == "recover-unpublished-card" {
		allowed := map[string]bool{
			"data":            true,
			"endpoint":        true,
			"run":             true,
			"revision":        true,
			"group-attempt":   true,
			"child-run":       true,
			"child-revision":  true,
			"attempt":         true,
			"patch-sha256":    true,
			"bindings-sha256": true,
			"request-id":      true,
			"preflight":       true,
		}
		bad := ""
		fs.Visit(func(f *flag.Flag) {
			if !allowed[f.Name] {
				bad = f.Name
			}
		})
		if bad != "" || *runID == "" || *revision < 1 || *groupAttempt == "" || *childRun == "" || *childRevision < 1 || *attemptID == "" || *patchSHA256 == "" || !*preflight && *bindingsSHA == "" {
			return errors.New("recover-unpublished-card requires exact run/revision, group-attempt, child-run/child-revision, attempt and patch-sha256; admission also requires bindings-sha256; no overrides")
		}
	}
	if *reportFile != "" && command != "recover-browser-errors" {
		return errors.New("--report-file requires recover-browser-errors")
	}
	if *serial && command != "recover-frontend-check" {
		return errors.New("--serial requires recover-frontend-check")
	}
	if *assertionSHA256 != "" && (command != "recover-frontend-check" || *serial) {
		return errors.New("--assertion-sha256 requires recover-frontend-check without --serial")
	}
	if *withinBudget && command != "recover-corrections" && (command != "retry" || *extra <= 0 || *cleanupOnly) {
		return errors.New("--within-budget requires retry with positive --extra-attempts and without --cleanup-only")
	}
	if command == "recover-corrections" && (*runID == "" || *attemptID == "" || !*withinBudget && *maxUses != 3 || *withinBudget && (*maxUses != 0 || *preserveSource) || *profile != "" || *extra != 0 || *cleanupOnly) {
		return errors.New("recover-corrections requires --run, --attempt and --max-uses 3 or --within-budget without --max-uses; retry allowances/profiles are separate")
	}
	if command == "recover-collection" && (*runID == "" || *attemptID == "" || *maxUses != 0 || *profile != "" || *extra != 0 || *cleanupOnly || *preserveSource) {
		return errors.New("recover-collection requires --run and --attempt; it grants no retry allowance, profile or preservation override")
	}
	if command == "recover-planning" && (*runID == "" || *attemptID == "" || *revision < 1 || *maxUses != 0 || *profile != "" || *extra != 0 || *cleanupOnly || *preserveSource) {
		return errors.New("recover-planning requires --run, --attempt and --revision; it grants no retry allowance, profile or preservation override")
	}
	if command == "recover-frontend-check" && (*runID == "" || *attemptID == "" || *revision < 1 || *patchSHA256 == "" || *helperSHA256 == "" || *publicationHead == "" || *maxUses != 0 || *profile != "" || *extra != 0 || *cleanupOnly || *preserveSource) {
		return errors.New("recover-frontend-check requires --run, --attempt, --revision, --patch-sha256, --helper-sha256 and --publication-head; it grants no retry or profile override")
	}
	if command == "recover-frontend-timeouts" && (*runID == "" || *attemptID == "" || *revision < 1 || *patchSHA256 == "" || *helperSHA256 == "" || *publicationHead == "" || *checkAttemptID == "" || *checkSHA256 == "" || *maxUses != 0 || *profile != "" || *extra != 0 || *cleanupOnly || *preserveSource) {
		return errors.New("recover-frontend-timeouts requires --run, --attempt, --revision, --patch-sha256, --helper-sha256, --publication-head, --check-attempt and --check-sha256; it grants no retry or profile override")
	}
	if command == "recover-browser-errors" && (*runID == "" || *attemptID == "" || *revision < 1 || *publicationHead == "" || *reportFile == "" || *maxUses != 0 || *profile != "" || *extra != 0 || *cleanupOnly || *preserveSource) {
		return errors.New("recover-browser-errors requires --run, --attempt, --revision, --publication-head and --report-file; it grants no attempts or profile override")
	}
	var browserReportGZIP []byte
	if command == "recover-browser-errors" {
		browserReport, err := readBrowserErrorReport(*reportFile)
		if err != nil {
			return err
		}
		browserReportGZIP, err = compressBrowserErrorReport(browserReport)
		if err != nil {
			return err
		}
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
		a = runner.RetryRequest{
			RequestID:     *requestID,
			RunID:         *runID,
			Profile:       *profile,
			ExtraAttempts: *extra,
			WithinBudget:  *withinBudget,
			CleanupOnly:   *cleanupOnly,
		}
	case "recover-corrections":
		if *requestID == "" {
			*requestID = rand.Text()
		}
		fmt.Fprintln(os.Stderr, "request_id="+*requestID)
		method = "runner_recover_corrections"
		a = runner.RecoverCorrectionsRequest{
			RequestID:      *requestID,
			RunID:          *runID,
			AttemptID:      *attemptID,
			MaxUses:        *maxUses,
			WithinBudget:   *withinBudget,
			PreserveSource: *preserveSource,
		}
	case "recover-collection":
		if *requestID == "" {
			*requestID = rand.Text()
		}
		fmt.Fprintln(os.Stderr, "request_id="+*requestID)
		method = "runner_recover_collection"
		a = runner.RecoverCollectionRequest{RequestID: *requestID, RunID: *runID, AttemptID: *attemptID}
	case "recover-unpublished-card":
		method = "runner_preflight_unpublished_card_recovery"
		if !*preflight {
			method = "runner_recover_unpublished_card"
			if *requestID == "" {
				*requestID = rand.Text()
			}
			fmt.Fprintln(os.Stderr, "request_id="+*requestID)
		}
		a = runner.RecoverUnpublishedCardRequest{
			RequestID:      *requestID,
			RunID:          *runID,
			Revision:       *revision,
			GroupAttemptID: *groupAttempt,
			ChildRunID:     *childRun,
			ChildRevision:  *childRevision,
			AttemptID:      *attemptID,
			PatchSHA256:    *patchSHA256,
			BindingsSHA256: *bindingsSHA,
		}
	case "recover-planning":
		if *requestID == "" {
			*requestID = rand.Text()
		}
		fmt.Fprintln(os.Stderr, "request_id="+*requestID)
		method = "runner_recover_planning"
		a = runner.RecoverPlanningRequest{RequestID: *requestID, RunID: *runID, AttemptID: *attemptID, Revision: *revision}
	case "recover-frontend-check", "recover-frontend-timeouts":
		if *requestID == "" {
			*requestID = rand.Text()
		}
		fmt.Fprintln(os.Stderr, "request_id="+*requestID)
		request := runner.RecoverFrontendCheckRequest{
			RequestID:       *requestID,
			RunID:           *runID,
			AttemptID:       *attemptID,
			Revision:        *revision,
			PatchSHA256:     *patchSHA256,
			HelperSHA256:    *helperSHA256,
			PublicationHead: *publicationHead,
			Serial:          *serial,
		}
		method, a = "runner_recover_frontend_check", request
		if command == "recover-frontend-timeouts" {
			method = "runner_recover_frontend_timeouts"
			a = runner.RecoverFrontendTimeoutsRequest{RecoverFrontendCheckRequest: request, CheckAttemptID: *checkAttemptID, CheckSHA256: *checkSHA256}
		} else {
			request.AssertionSHA256 = *assertionSHA256
			a = request
		}
	case "recover-browser-errors":
		if *requestID == "" {
			*requestID = rand.Text()
		}
		fmt.Fprintln(os.Stderr, "request_id="+*requestID)
		method = "runner_recover_browser_errors"
		a = control.RecoverBrowserErrorsArgs{
			RequestID:       *requestID,
			RunID:           *runID,
			AttemptID:       *attemptID,
			Revision:        *revision,
			PublicationHead: *publicationHead,
			ReportGZIP:      base64.StdEncoding.EncodeToString(browserReportGZIP),
		}
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

func readBrowserErrorReport(path string) ([]byte, error) {
	const limit = 128 << 10
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > limit {
		return nil, errors.New("browser-error report must be a nonempty regular file without a symlink, at most 128KiB")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open browser-error report: %w", err)
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) || !opened.Mode().IsRegular() {
		return nil, errors.New("browser-error report changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	after, statErr := os.Lstat(path)
	if err != nil || len(data) > limit || statErr != nil || !after.Mode().IsRegular() || !os.SameFile(info, after) ||
		int64(len(data)) != info.Size() || after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) {
		return nil, errors.New("browser-error report changed, exceeded 128KiB or could not be read")
	}
	return data, nil
}

func compressBrowserErrorReport(data []byte) ([]byte, error) {
	if len(data) == 0 || len(data) > 128<<10 {
		return nil, errors.New("browser-error report must contain 1..131072 bytes")
	}
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write(data); err != nil {
		writer.Close()
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	if compressed.Len() > 32<<10 {
		return nil, errors.New("browser-error gzip report exceeds the 32KiB handoff bound")
	}
	return compressed.Bytes(), nil
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
