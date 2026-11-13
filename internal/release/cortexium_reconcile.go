package release

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/cortexium-io/runner/internal/saas"
)

const (
	cortexiumWorkKind              = "cortexium-browser-v2"
	cortexiumLegacyWorkKind        = "cortexium-browser-v1"
	cortexiumCleanupKind           = "cortexium-cleanup-v1"
	cortexiumCleanupTime           = 45 * time.Second
	cortexiumCleanupPending        = "cortexium_cleanup_pending"
	cortexiumCleanupActive         = "cortexium_cleanup_active"
	cortexiumCleanupAttentionPhase = "cortexium_cleanup_attention"
	cortexiumCleanupSettled        = "cortexium_cleanup_settled"
)

type cortexiumCleanupBinding struct {
	OriginalSHA256 string `json:"original_sha256"`
	StartedAt      int64  `json:"started_at"`
	Deadline       int64  `json:"deadline"`
}

func cortexiumDurableWork(kind string) bool {
	return kind == cortexiumWorkKind || kind == cortexiumLegacyWorkKind
}

func (b *Live) startCortexiumWorker(file string, lease *os.File) error {
	if b.launchDeliveryWorker != nil {
		return b.launchDeliveryWorker(file, lease)
	}
	return startDeliveryWorker(file, lease)
}

func (b *Live) cortexiumOriginal(j *Job) (deliveryWork, []byte, error) {
	var original deliveryWork
	config, _ := json.Marshal(b.Config)
	op := Operator{Project: b.Config.Project, PolicyHash: Digest(config), Cortexium: true}
	if !op.validCortexiumRequest(j.Request) || j.Request.Cortexium.Transport != "" {
		return original, nil, errors.New("Cortexium saved request is outside its policy")
	}
	want := filepath.Join(b.Config.workRoot(), j.Request.RunID, j.Request.Key, "work.json")
	if j.Worker != want {
		return original, nil, errors.New("Cortexium worker path differs from its operation")
	}
	data, err := readFile(want, 32<<10)
	if err != nil {
		return original, nil, err
	}
	if json.Unmarshal(data, &original) != nil {
		return original, nil, errors.New("Cortexium saved work is malformed")
	}
	observed, _ := json.Marshal(original.Config)
	if string(config) != string(observed) || original.Kind != "" && !cortexiumDurableWork(original.Kind) || original.Cleanup != nil || original.Job.Worker != want || !sameCortexiumRequest(original.Job.Request, j.Request) || original.Job.Receipt.Policy != Digest(config) || original.Job.Receipt.Policy != j.Receipt.Policy || original.Job.CortexiumDeadline <= 0 || original.Job.CortexiumDeadline != j.CortexiumDeadline || original.Job.CortexiumDeadline > j.Request.Cortexium.Deadline {
		return original, nil, errors.New("Cortexium saved work ownership differs")
	}
	return original, data, nil
}

func (b *Live) loadCortexiumCleanup(j *Job) (deliveryWork, []byte, error) {
	var cleanup deliveryWork
	original, source, err := b.cortexiumOriginal(j)
	if err != nil {
		return cleanup, nil, err
	}
	data, err := readFile(filepath.Join(filepath.Dir(j.Worker), "cleanup.json"), 32<<10)
	if err != nil {
		return cleanup, nil, err
	}
	if json.Unmarshal(data, &cleanup) != nil || cleanup.Kind != cortexiumCleanupKind || cleanup.Cleanup == nil || !cortexiumDurableWork(original.Kind) {
		return cleanup, nil, errors.New("Cortexium cleanup contract differs")
	}
	binding := cleanup.Cleanup
	config, _ := json.Marshal(cleanup.Config)
	want, _ := json.Marshal(b.Config)
	if string(config) != string(want) || binding.OriginalSHA256 != Digest(source) || !sameCortexiumRequest(cleanup.Job.Request, j.Request) || cleanup.Job.Worker != j.Worker || cleanup.Job.Receipt.Policy != j.Receipt.Policy || cleanup.Job.CortexiumDeadline != j.CortexiumDeadline || cleanup.Job.CortexiumCleanupDeadline != binding.Deadline || j.CortexiumCleanupDeadline != 0 && j.CortexiumCleanupDeadline != binding.Deadline || binding.StartedAt <= 0 || binding.StartedAt > time.Now().UnixNano() || binding.Deadline <= binding.StartedAt || binding.Deadline > time.Unix(0, binding.StartedAt).Add(cortexiumCleanupTime).UnixNano() || binding.Deadline > time.Unix(0, j.CortexiumDeadline).Add(cortexiumCleanupTime).UnixNano() {
		return cleanup, nil, errors.New("Cortexium cleanup identity or original budget differs")
	}
	if original.Kind == cortexiumWorkKind {
		if j.Receipt.CortexiumReadbackSHA256 != "" && j.Receipt.CortexiumReadbackSHA256 != cleanup.Job.Receipt.CortexiumReadbackSHA256 {
			return cleanup, nil, errors.New("Cortexium cleanup copy digest changed")
		}
		if _, err := b.cortexiumReadbackReport(&cleanup.Job); err != nil {
			return cleanup, nil, err
		}
	}
	return cleanup, data, nil
}

// The same worker lease owns first creation. Existing specs are never rewritten,
// so a lost acknowledgement cannot renew the deadline or mint another visit.
func (b *Live) prepareCortexiumCleanup(j *Job) (deliveryWork, error) {
	cleanup, _, err := b.loadCortexiumCleanup(j)
	if err == nil {
		j.CortexiumCleanupDeadline = cleanup.Cleanup.Deadline
		j.Receipt.CortexiumReadbackSHA256 = cleanup.Job.Receipt.CortexiumReadbackSHA256
		if j.Failure == "" {
			j.Failure = cleanup.Job.Failure
		}
		return cleanup, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return cleanup, err
	}
	original, source, err := b.cortexiumOriginal(j)
	if err != nil {
		return cleanup, err
	}
	if !cortexiumDurableWork(original.Kind) || j.CortexiumCleanupDeadline != 0 || j.CortexiumCleanupFailure != "" {
		return cleanup, errors.New("Cortexium work has no established unused cleanup budget; needs operator")
	}
	if _, err := os.Lstat(filepath.Join(filepath.Dir(j.Worker), "cleanup-started")); !errors.Is(err, os.ErrNotExist) {
		return cleanup, errors.New("Cortexium cleanup claim exists without its immutable spec; needs operator")
	}
	if err := b.sealCortexiumReadback(j); err != nil {
		return cleanup, err
	}
	now := time.Now()
	deadline := min(now.Add(cortexiumCleanupTime).UnixNano(), time.Unix(0, j.CortexiumDeadline).Add(cortexiumCleanupTime).UnixNano())
	if deadline <= now.UnixNano() {
		return cleanup, errors.New("Cortexium original cleanup reserve expired; needs operator")
	}
	j.CortexiumCleanupDeadline = deadline
	j.Phase = cortexiumCleanupPending
	cleanup = deliveryWork{
		Kind: cortexiumCleanupKind,
		Cleanup: &cortexiumCleanupBinding{
			OriginalSHA256: Digest(source),
			StartedAt:      now.UnixNano(),
			Deadline:       deadline,
		},
		Config: b.Config,
		Job:    *j,
	}
	if err := writeJSON(filepath.Join(filepath.Dir(j.Worker), "cleanup.json"), cleanup); err != nil {
		return cleanup, err
	}
	return cleanup, nil
}

func cortexiumCleanupAttention(j *Job, cause error) error {
	j.Done, j.Outcome = false, "blocked"
	j.Phase = cortexiumCleanupAttentionPhase
	if j.CortexiumCleanupFailure == "" {
		j.CortexiumCleanupFailure = clipCleanupFailure(cause)
	}
	j.Reason = "Cortexium cleanup needs operator inspection; retained original failure, resources and cleanup identity"
	return errors.New(j.Reason)
}

func clipCleanupFailure(err error) string {
	if err == nil {
		return ""
	}
	message := err.Error()
	return message[:min(len(message), 1024)]
}

func (b *Live) cortexiumDelivery(j *Job, save func() error) error {
	if j.Worker == "" {
		root := filepath.Join(b.Config.workRoot(), j.Request.RunID, j.Request.Key)
		if err := os.MkdirAll(root, 0700); err != nil {
			return err
		}
		j.Worker = filepath.Join(root, "work.json")
		if err := writeJSON(j.Worker, deliveryWork{Kind: cortexiumWorkKind, Config: b.Config, Job: *j}); err != nil {
			return err
		}
		if err := save(); err != nil {
			return err
		}
	}
	original, _, err := b.cortexiumOriginal(j)
	if err != nil {
		return cortexiumCleanupAttention(j, err)
	}
	root := filepath.Dir(j.Worker)
	lease, err := os.OpenFile(filepath.Join(root, "worker.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lease.Close()
	if err = lockOperator(lease); errors.Is(err, errOperatorBusy) {
		if original.Kind != cortexiumWorkKind {
			_ = cortexiumCleanupAttention(j, errors.New("older active work has no durable cleanup contract"))
			j.Reason += "; owned writer lease remains active"
			return errors.New(j.Reason)
		}
		if cleanup, _, e := b.loadCortexiumCleanup(j); e == nil {
			if j.Failure != "" && j.Failure != cleanup.Job.Failure {
				return cortexiumCleanupAttention(j, errors.New("active cleanup primary failure differs from its saved authority"))
			}
			if j.CortexiumCleanupDeadline == 0 {
				j.CortexiumCleanupFailure = cleanup.Job.CortexiumCleanupFailure
			}
			j.CortexiumCleanupDeadline = cleanup.Cleanup.Deadline
			j.Failure, j.Failed = cleanup.Job.Failure, cleanup.Job.Failed
			j.Receipt.CortexiumReadbackSHA256 = cleanup.Job.Receipt.CortexiumReadbackSHA256
			if time.Now().UnixNano() >= cleanup.Cleanup.Deadline {
				_ = cortexiumCleanupAttention(j, errors.New("saved cleanup deadline exhausted while its writer lease remains active"))
				j.Reason += "; owned writer lease remains active"
				return errors.New(j.Reason)
			}
			if j.CortexiumCleanupFailure != "" {
				return cortexiumCleanupAttention(j, errors.New(j.CortexiumCleanupFailure))
			}
			j.Phase = cortexiumCleanupActive
			j.Reason = "same owned Cortexium cleanup worker remains active"
		} else if !errors.Is(e, os.ErrNotExist) {
			return cortexiumCleanupAttention(j, e)
		} else if j.CortexiumCleanupDeadline != 0 {
			return cortexiumCleanupAttention(j, errors.New("Cortexium cleanup spec disappeared while worker remains active"))
		} else {
			if time.Now().UnixNano() >= j.CortexiumDeadline {
				_ = cortexiumCleanupAttention(j, errors.New("original work deadline expired with an active writer lease"))
				j.Reason += "; owned writer lease remains active"
				return errors.New(j.Reason)
			}
			j.Reason = "same delivery worker remains active"
		}
		return nil
	} else if err != nil {
		return err
	}
	inherited := false
	defer func() {
		if !inherited {
			unlockOperator(lease)
		}
	}()
	data, err := readFile(filepath.Join(root, "result.json"), 32<<10)
	if err == nil {
		var result Job
		if json.Unmarshal(data, &result) != nil || !sameCortexiumRequest(j.Request, result.Request) || result.Receipt.Policy != j.Receipt.Policy || result.Worker != j.Worker || result.CortexiumDeadline != j.CortexiumDeadline {
			return cortexiumCleanupAttention(j, errors.New("Cortexium worker result identity differs"))
		}
		*j = result
		if j.Done {
			if original.Kind == cortexiumWorkKind {
				if _, err := b.cortexiumReadbackReport(j); err != nil {
					return cortexiumCleanupAttention(j, err)
				}
			}
			if cortexiumDurableWork(original.Kind) {
				if j.Phase != cortexiumCleanupSettled {
					return cortexiumCleanupAttention(j, errors.New("Cortexium result lacks settled cleanup phase"))
				}
				if claimed, e := b.cortexiumCleanupClaim(j); e != nil || !claimed {
					return cortexiumCleanupAttention(j, errors.Join(e, errors.New("Cortexium result lacks its cleanup claim")))
				}
			}
			return nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return cortexiumCleanupAttention(j, err)
	} else if original.Kind == cortexiumWorkKind {
		_, cleanupSpec := os.Lstat(filepath.Join(root, "cleanup.json"))
		_, cleanupClaim := os.Lstat(filepath.Join(root, "cleanup-started"))
		unused := errors.Is(cleanupSpec, os.ErrNotExist) && errors.Is(cleanupClaim, os.ErrNotExist) && j.CortexiumCleanupDeadline == 0
		if _, started := os.Lstat(filepath.Join(root, "started")); errors.Is(started, os.ErrNotExist) && unused && original.Kind == cortexiumWorkKind && time.Now().UnixNano() < j.CortexiumDeadline {
			err = b.startCortexiumWorker(j.Worker, lease)
			inherited = err == nil
			return err
		} else if started != nil && !errors.Is(started, os.ErrNotExist) {
			return cortexiumCleanupAttention(j, started)
		}
		j.Failed, j.Outcome = true, "blocked"
		if j.Failure == "" && errors.Is(cleanupSpec, os.ErrNotExist) {
			j.Failure = "Cortexium producer ended without an established result; no replay"
		}
	}
	cleanup, err := b.prepareCortexiumCleanup(j)
	if err != nil {
		return cortexiumCleanupAttention(j, err)
	}
	if claimed, err := b.cortexiumCleanupClaim(j); err != nil {
		return cortexiumCleanupAttention(j, err)
	} else if claimed {
		return cortexiumCleanupAttention(j, errors.New("Cortexium cleanup visit already claimed without settled result; no replay"))
	}
	if time.Now().UnixNano() >= cleanup.Cleanup.Deadline {
		return cortexiumCleanupAttention(j, errors.New("Cortexium saved cleanup deadline exhausted; no renewal"))
	}
	if err := save(); err != nil {
		return err
	}
	j.Reason = "same owned Cortexium cleanup is being dispatched"
	err = b.startCortexiumWorker(filepath.Join(root, "cleanup.json"), lease)
	inherited = err == nil
	if inherited {
		j.Phase = cortexiumCleanupActive
	}
	return err
}

func claimCortexiumWorker(file string, data []byte) error {
	f, err := os.OpenFile(file, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	dir, err := os.Open(filepath.Dir(file))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func runCortexiumDeliveryFile(file string, work deliveryWork) error {
	b := Live{Config: work.Config}
	j := &work.Job
	original, _, err := b.cortexiumOriginal(j)
	if err != nil || !cortexiumDurableWork(original.Kind) {
		return errors.Join(err, errors.New("unsupported Cortexium work cleanup contract; needs operator"))
	}
	lease := os.NewFile(3, "delivery-lease")
	defer lease.Close()
	actual, err := lease.Stat()
	expected, statErr := os.Stat(filepath.Join(filepath.Dir(j.Worker), "worker.lock"))
	if err != nil || statErr != nil || !os.SameFile(actual, expected) {
		return errors.New("Cortexium worker lease identity differs")
	}
	if err := lockOperator(lease); err != nil {
		return err
	}
	ctx := context.WithValue(context.Background(), leaseKey{}, lease)
	ctx = context.WithValue(ctx, deliveryDiagnosticKey{}, filepath.Dir(j.Worker))
	ctx = context.WithValue(ctx, cortexiumOutputKey{}, true)
	switch work.Kind {
	case cortexiumWorkKind:
		if file != j.Worker || work.Cleanup != nil {
			return errors.New("Cortexium source work specification differs")
		}
		if err := claimCortexiumWorker(filepath.Join(filepath.Dir(file), "started"), nil); err != nil {
			return err
		}
		bounded, cancel := context.WithDeadline(ctx, time.Unix(0, j.CortexiumDeadline))
		defer cancel()
		return b.runCortexiumWork(bounded, j)
	case cortexiumCleanupKind:
		if file != filepath.Join(filepath.Dir(j.Worker), "cleanup.json") {
			return errors.New("Cortexium cleanup specification path differs")
		}
		return b.runCortexiumCleanup(ctx, j)
	default:
		return errors.New("unsupported Cortexium worker specification")
	}
}

func (b *Live) runCortexiumCleanup(parent context.Context, j *Job) error {
	work, data, err := b.loadCortexiumCleanup(j)
	if err != nil {
		return err
	}
	if j.Failure != "" && j.Failure != work.Job.Failure {
		return errors.New("Cortexium cleanup primary failure changed before its claim")
	}
	if err := claimCortexiumWorker(filepath.Join(filepath.Dir(j.Worker), "cleanup-started"), []byte(Digest(data)+"\n")); err != nil {
		return err
	}
	*j = work.Job
	ctx, cancel := context.WithDeadline(context.WithoutCancel(parent), time.Unix(0, work.Cleanup.Deadline))
	defer cancel()
	var e saas.Evidence
	var files map[string][]byte
	input, inputErr := readFile(filepath.Join(filepath.Dir(j.Worker), "input.tar.gz"), saas.MaxBundle)
	if inputErr == nil && Digest(input) != j.Request.Cortexium.SHA256 {
		inputErr = errors.New("retained checked input hash differs during cleanup")
	}
	if inputErr == nil {
		e, files, inputErr = saas.DecodeCheckedBuild(input, j.Request.Source, j.Request.Cortexium.PatchSHA256, j.Request.Cortexium.AuthoritySHA256)
	}
	if inputErr == nil && e.Build.Runtime != b.Config.Cortexium.Runtime {
		inputErr = errors.New("retained checked runtime differs during cleanup")
	}
	if inputErr != nil {
		j.Failed, j.Outcome = true, "blocked"
		j.Reason = "retained checked input cannot be authenticated; owned cleanup continues"
		if j.Failure == "" {
			j.Failure = clipCleanupFailure(inputErr)
		}
	}
	if inputErr == nil && len(files) > 0 && j.Receipt.BrowserEvidenceSHA256 == "" {
		partialErr := b.captureCortexiumPartial(ctx, j, &e, files)
		_ = writeJSON(filepath.Join(filepath.Dir(j.Worker), "partial-export.json"), map[string]any{
			"captured":        j.Receipt.BrowserEvidenceSHA256 != "",
			"error":           fmt.Sprint(partialErr),
			"primary_failure": j.Failure,
		})
		if j.Receipt.BrowserEvidenceSHA256 == "" {
			e.Status, e.Reason = "incomplete", j.Failure
			readback, readErr := b.cortexiumReadbackReport(j)
			if readErr != nil {
				return readErr
			}
			report, _ := json.Marshal(map[string]any{"copy_readback": readback, "operator_failure": j.Failure, "partial_export": true})
			files["reports/browser.json"] = report
			e.Reports = append(e.Reports, saas.Report{Name: "reports/browser.json", SHA256: Digest(report), Bytes: len(report), Complete: true})
			if payload, packErr := saas.Pack(e, files); packErr == nil {
				if writeErr := writeExclusive(filepath.Join(filepath.Dir(j.Worker), "evidence.tar.gz"), payload); writeErr == nil {
					j.Receipt.BrowserEvidenceSHA256 = Digest(payload)
				}
			}
		}
	}
	cleanupErr := ctx.Err()
	if cleanupErr == nil {
		cleanupErr = b.settleCortexiumResources(ctx, j)
	}
	j.Done = cleanupErr == nil
	if j.Done {
		j.Phase = cortexiumCleanupSettled
	}
	if cleanupErr != nil {
		_ = cortexiumCleanupAttention(j, cleanupErr)
	} else if _, stopped := os.Stat(filepath.Join(filepath.Dir(j.Worker), "stop")); stopped == nil {
		j.Outcome, j.Reason = "blocked", "Cortexium cancellation observed and owned resources settled; original failure retained"
	}
	return writeJSON(filepath.Join(filepath.Dir(j.Worker), "result.json"), j)
}

func (b *Live) cortexiumCleanupClaim(j *Job) (bool, error) {
	work, data, err := b.loadCortexiumCleanup(j)
	if err != nil {
		return false, err
	}
	if j.CortexiumCleanupDeadline != work.Cleanup.Deadline || work.Job.Failure != "" && j.Failure != work.Job.Failure {
		return false, errors.New("Cortexium cleanup result changed its deadline or primary failure")
	}
	claim, err := readFile(filepath.Join(filepath.Dir(j.Worker), "cleanup-started"), 1024)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil || string(claim) != Digest(data)+"\n" {
		return false, errors.New("Cortexium cleanup claim identity differs")
	}
	return true, nil
}
