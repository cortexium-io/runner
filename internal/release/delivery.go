package release

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// DeliveryConfig selects installed runtimes and private owned storage. Candidate
// code supplies neither these paths nor release commands. Builds run sandboxed.
type DeliveryConfig struct {
	Root              string            `json:"root"`
	Git               string            `json:"git"`
	Codex             string            `json:"codex"`
	Python            string            `json:"python"`
	Node              string            `json:"node"`
	Go                string            `json:"go"`
	ModuleCache       string            `json:"module_cache"`
	ReadPaths         []string          `json:"read_paths"`
	BrowserModule     string            `json:"browser_module"`
	BrowserExecutable string            `json:"browser_executable"`
	BrowserImage      string            `json:"browser_image,omitempty"`
	WebKitExecutable  string            `json:"webkit_executable,omitempty"`
	TrustBundle       string            `json:"trust_bundle"`
	Probe             string            `json:"probe"`
	Ownership         []string          `json:"ownership"`
	Timeout           string            `json:"timeout"`
	StorageLimit      int64             `json:"storage_limit"`
	MinFree           int64             `json:"min_free"`
	Retention         *ArchiveRetention `json:"retention,omitempty"`
}

func (c DeliveryConfig) validate() error {
	if err := c.Retention.validate(); err != nil {
		return err
	}
	if c.BrowserImage != "" && !regexp.MustCompile(`^sha256:[0-9a-f]{64}$`).MatchString(c.BrowserImage) {
		return errors.New("browser runtime requires an immutable full image ID")
	}
	if c.WebKitExecutable != "" && (!filepath.IsAbs(c.WebKitExecutable) || filepath.Clean(c.WebKitExecutable) != c.WebKitExecutable) {
		return errors.New("WebKit executable must be an explicit absolute runtime path")
	}
	if len(c.Ownership) == 0 || len(c.Ownership) > 16 || !filepath.IsAbs(c.Ownership[0]) {
		return errors.New("delivery requires its fixed generated-ownership command")
	}
	paths := []string{
		c.Root,
		c.Git,
		c.Codex,
		c.Python,
		c.Node,
		c.Go,
		c.ModuleCache,
		c.Probe,
		c.TrustBundle,
	}
	if c.BrowserImage == "" {
		paths = append(paths, c.BrowserModule, c.BrowserExecutable)
	}
	for _, path := range append(paths, c.ReadPaths...) {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == string(filepath.Separator) {
			return errors.New("delivery runtime/storage paths must be absolute")
		}
	}
	if c.StorageLimit <= 0 || c.StorageLimit > 8<<30 || c.MinFree < 5<<30 {
		return errors.New("delivery storage must fit the pilot's 8GiB bound and preserve at least 5GiB free")
	}
	d, err := time.ParseDuration(c.Timeout)
	if err != nil || d <= 0 || d > 20*time.Minute {
		return errors.New("delivery phase needs a positive deadline of at most 20m")
	}
	return nil
}

type deliveryWork struct {
	Config Config `json:"config"`
	Job    Job    `json:"job"`
}

type deliveryDiagnosticKey struct{}

func (b *Live) delivery(ctx context.Context, j *Job, save func() error) error {
	if j.Request.Action == "publish" {
		return b.publish(ctx, j, save)
	}
	if j.Worker == "" {
		root := filepath.Join(b.Config.Delivery.Root, j.Request.RunID, j.Request.Key)
		if j.Request.Action == "qualify" {
			j.Receipt.Package = filepath.Join(root, "output")
			j.Receipt.Remote = filepath.Join(b.Config.BackupDir, "delivery-"+j.Request.RunID+"-"+j.Request.Key)
		}
		if err := os.MkdirAll(root, 0700); err != nil {
			return err
		}
		j.Worker = filepath.Join(root, "work.json")
		if err := writeJSON(j.Worker, deliveryWork{Config: b.Config, Job: *j}); err != nil {
			return err
		}
		if err := save(); err != nil {
			return err
		}
		binary, err := os.Executable()
		if err != nil {
			return err
		}
		cmd := exec.Command(binary, "--delivery-worker", j.Worker)
		lease, err := os.OpenFile(filepath.Join(root, "worker.lock"), os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			return err
		}
		defer lease.Close()
		if err = lockOperator(lease); err != nil {
			return err
		}
		// Parent and worker share this open-file description. Close without
		// LOCK_UN so ownership survives the SSH client and startup interval.
		cmd.ExtraFiles = []*os.File{lease}
		if err = cmd.Start(); err != nil {
			return err
		}
		go func() { _ = cmd.Wait() }()
		return nil
	}
	lease, leaseErr := os.OpenFile(filepath.Join(filepath.Dir(j.Worker), "worker.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if leaseErr != nil {
		return leaseErr
	}
	defer lease.Close()
	if leaseErr = lockOperator(lease); errors.Is(leaseErr, errOperatorBusy) {
		j.Reason = "same delivery worker remains active"
		return nil
	} else if leaseErr != nil {
		return leaseErr
	}
	defer unlockOperator(lease)
	data, err := readFile(filepath.Join(filepath.Dir(j.Worker), "result.json"), 32<<10)
	if errors.Is(err, os.ErrNotExist) {
		return errors.New("delivery worker result is not established; retain its dispatch identity")
	}
	if err != nil {
		return err
	}
	var result Job
	if json.Unmarshal(data, &result) != nil || result.Request.Key != j.Request.Key || result.Receipt.Policy != j.Receipt.Policy {
		return errors.New("delivery worker result identity differs")
	}
	if !result.Done {
		return errors.New(result.Reason)
	}
	j.Receipt, j.Done, j.Failed, j.Reason, j.Outcome = result.Receipt, result.Done, result.Failed, result.Reason, result.Outcome
	j.Retryable = result.Retryable
	return nil
}

func RunDeliveryWorker(file string) error {
	data, err := readFile(file, 32<<10)
	if err != nil {
		return err
	}
	var work deliveryWork
	if json.Unmarshal(data, &work) != nil || work.Config.Delivery == nil || work.Config.Delivery.validate() != nil || work.Job.Worker != file || filepath.Dir(filepath.Dir(filepath.Dir(file))) != work.Config.Delivery.Root {
		return errors.New("invalid private delivery work")
	}
	claim, err := os.OpenFile(filepath.Join(filepath.Dir(file), "started"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	claim.Close()
	lease := os.NewFile(3, "delivery-lease")
	actual, err := lease.Stat()
	if err != nil {
		return err
	}
	expected, err := os.Stat(filepath.Join(filepath.Dir(file), "worker.lock"))
	if err != nil || !os.SameFile(actual, expected) {
		return errors.New("delivery worker lease identity differs")
	}
	defer lease.Close()
	duration, _ := time.ParseDuration(work.Config.Delivery.Timeout)
	ctx, cancel := context.WithTimeout(context.Background(), duration)
	defer cancel()
	ctx = context.WithValue(ctx, leaseKey{}, lease)
	ctx = context.WithValue(ctx, deliveryDiagnosticKey{}, filepath.Dir(file))
	b := Live{Config: work.Config}
	j := &work.Job
	if j.Request.Action == "readiness" || j.Request.Action == "qualify" {
		if err = b.collectArchives(ctx); err != nil {
			_ = fail(j, "owned archive retention could not establish safe collection: "+err.Error())
			return writeJSON(filepath.Join(filepath.Dir(file), "result.json"), j)
		}
	}
	if err = b.deliveryCapacity(ctx, j.Request.Action == "readiness"); err == nil {
		capacityFailure := make(chan error, 1)
		monitorDone := make(chan struct{})
		monitorCtx, stopMonitor := context.WithCancel(ctx)
		defer stopMonitor()
		go func() {
			defer close(monitorDone)
			ticker := time.NewTicker(10 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-monitorCtx.Done():
					return
				case <-ticker.C:
					if e := b.deliveryCapacity(monitorCtx, false); e != nil {
						if monitorCtx.Err() != nil {
							return
						}
						capacityFailure <- e
						cancel()
						return
					}
				}
			}
		}()
		switch j.Request.Action {
		case "readiness":
			err = b.deliveryReadiness(ctx, j)
		case "qualify":
			for _, phase := range []func(context.Context, *Job) error{b.packageCandidate, b.qualifyCandidate, b.rehearseCandidate, b.browserCandidate} {
				j.Done = false
				if err = phase(ctx, j); err != nil {
					break
				}
			}
		default:
			err = errors.New("unsupported delivery work")
		}
		stopMonitor()
		<-monitorDone
		select {
		case capacityErr := <-capacityFailure:
			j.Reason = capacityErr.Error()
			err = errors.Join(err, fmt.Errorf("%w: %v", &commandUncertain{program: "delivery capacity stop"}, capacityErr))
		default:
		}
	}
	if err != nil {
		var uncertain *commandUncertain
		if errors.As(err, &uncertain) {
			j.Done = false
			j.Failed = false
			j.Reason = err.Error() + "; retain qualification attempt " + j.Request.Key
			return writeJSON(filepath.Join(filepath.Dir(file), "result.json"), j)
		}
		_ = fail(j, j.Request.Action+" failed; bounded private diagnostics and output retained: "+err.Error())
		var unavailable *connectionUnavailable
		// Repeating a fresh isolated qualification/readiness visit is safe only
		// for a positively established pre-dispatch connection failure. Unknown
		// effects retain this worker identity above and never authorize replay.
		j.Retryable = errors.As(err, &unavailable) && (j.Request.Action == "readiness" || j.Request.Action == "qualify")
		var source *sourceFailure
		if j.Request.Action == "qualify" && errors.As(err, &source) {
			j.Failed = false
			j.Outcome = "changes_requested"
			j.Reason = source.Error()
			j.Receipt.BrowserFailure = source.browser
		}
		j.Receipt.Note = j.Reason
	}
	if j.Done && j.Request.Action == "qualify" {
		active, checkErr := b.docker(ctx, "ps", "--filter", "label=cortexium.runner.run="+j.Request.RunID, "--format", "{{.ID}}")
		if checkErr != nil || strings.TrimSpace(string(active)) != "" {
			j.Done = false
			j.Failed = false
			j.Reason = "qualification resources are not conclusively settled; retain attempt " + j.Request.Key
			return writeJSON(filepath.Join(filepath.Dir(file), "result.json"), j)
		}
	}
	if !j.Done {
		_ = fail(j, "delivery phase returned without a result")
	}
	// Temporary source, dependency/build caches and disposable data are owned
	// by this settled attempt. Keep packages, proof and recovery snapshots.
	if cleanupErr := os.RemoveAll(filepath.Join(filepath.Dir(file), "work")); cleanupErr != nil {
		return cleanupErr
	}
	if cleanupErr := os.RemoveAll(filepath.Join(filepath.Dir(file), "verified-source")); cleanupErr != nil {
		return cleanupErr
	}
	if j.Request.Action == "qualify" && j.Receipt.Remote != "" {
		cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer stop()
		if _, cleanupErr := b.host(cleanup, nil, "rm", "-rf", "--", j.Receipt.Remote+"/source", j.Receipt.Remote+"/rehearsal", j.Receipt.Remote+"/rollback", j.Receipt.Remote+"/browser-data"); cleanupErr != nil {
			j.Receipt.Note += "; settled temporary-data cleanup needs inspection"
		}
	}
	return writeJSON(filepath.Join(filepath.Dir(file), "result.json"), j)
}

func (b *Live) publish(ctx context.Context, j *Job, save func() error) error {
	if !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(j.Request.Candidate) {
		return fail(j, "publication requires an exact retained candidate")
	}
	c := b.Config
	branch, err := b.publicationBranch(ctx, j)
	if err != nil {
		var proof *proofFailure
		if errors.As(err, &proof) {
			return fail(j, err.Error())
		}
		return err
	}
	var prs []struct {
		Number int `json:"number"`
		Head   struct {
			SHA string `json:"sha"`
		} `json:"head"`
		Base struct {
			SHA string `json:"sha"`
		} `json:"base"`
	}
	if err := b.gh(ctx, "repos/"+c.Repository+"/pulls?state=all&head="+strings.Split(c.Repository, "/")[0]+":"+branch, &prs); err != nil {
		return err
	}
	if len(prs) == 0 {
		if j.Phase == "pr_requested" {
			return errors.New("draft PR acknowledgement unresolved; inspect the same head before replacement")
		}
		if err := intent(j, "pr_requested", save); err != nil {
			return err
		}
		if _, err := b.command(ctx, nil, c.GH, "pr", "create", "--repo", c.Repository, "--base", c.Branch, "--head", branch, "--draft", "--title", "Runner delivery "+j.Request.RunID, "--body", "Runner owns qualification and release. Merge and production require separate owner approvals. Run: "+j.Request.RunID); err != nil {
			return err
		}
		return nil
	}
	if len(prs) != 1 || prs[0].Head.SHA != j.Request.Candidate || prs[0].Base.SHA != j.Request.Source || j.Receipt.PublicationRunID != "" && prs[0].Number != j.Receipt.PR {
		return fail(j, "published PR identity differs from the reviewed candidate/base")
	}
	j.Receipt.Head, j.Receipt.Base, j.Receipt.Source, j.Receipt.PR = j.Request.Candidate, j.Request.Source, j.Request.Source, prs[0].Number
	var checks struct {
		SHA      string `json:"sha"`
		Statuses []struct {
			Context string `json:"context"`
			State   string `json:"state"`
		} `json:"statuses"`
	}
	if err := b.gh(ctx, "repos/"+c.Repository+"/commits/"+j.Receipt.Head+"/status", &checks); err != nil {
		return err
	}
	passed := false
	failed := false
	if checks.SHA == j.Receipt.Head {
		for _, s := range checks.Statuses {
			if s.Context == "Techbase / generated ownership" {
				passed = s.State == "success"
				failed = s.State == "failure" || s.State == "error"
				break
			}
		}
	}
	if !passed {
		if j.Phase == "ownership_requested" {
			if failed {
				return fail(j, "exact-head generated ownership failed; preserve the draft PR and inspect its owning-source policy")
			}
			return errors.New("generated ownership result is unresolved; retain this exact PR/head")
		}
		if err := intent(j, "ownership_requested", save); err != nil {
			return err
		}
		args := append(append([]string{}, c.Delivery.Ownership[1:]...), fmt.Sprint(j.Receipt.PR), "--publish-status")
		if _, err := b.command(ctx, nil, c.Delivery.Ownership[0], args...); err != nil {
			return err
		}
		return nil // Confirm the published exact-head status before qualification.
	}
	return done(j, fmt.Sprintf("Reviewed candidate published as draft PR #%d; qualification follows; merge remains approval-gated", j.Receipt.PR))
}
