//go:build darwin || linux

package release

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"github.com/cortexium-io/runner/internal/saas"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func readbackFixture(t *testing.T) (*Live, Job, cortexiumReadback) {
	t.Helper()
	o, _ := cortexiumPolicy(t)
	b := o.Backend.(*Live)
	j := cleanupSavedJob(t, o, "run_copy", "attempt_copy")
	j.Failed, j.Failure = false, ""
	if err := writeJSON(j.Worker, deliveryWork{Kind: cortexiumWorkKind, Config: b.Config, Job: j}); err != nil {
		t.Fatal(err)
	}
	source, _ := os.ReadFile(j.Worker)
	body := bytes.Repeat([]byte("x"), 4<<20)
	record := cortexiumReadback{
		Version:    1,
		State:      "recorded",
		WorkSHA256: Digest(source),
		Image:      "sha256:" + strings.Repeat("c", 64),
		Container:  strings.Repeat("d", 64),
		Files: []cortexiumCopyFact{
			{Name: "server", Size: int64(len(body)), SHA256: Digest(body), Mode: 0555, State: "pending"},
			{Name: "manifest.json", Size: int64(len(body)), SHA256: Digest(body), Mode: 0444, State: "pending"},
		},
	}
	if err := writeJSON(readbackFile(&j), record); err != nil {
		t.Fatal(err)
	}
	for role, id := range map[string]string{"image": record.Image, "verify": record.Container} {
		if err := os.WriteFile(filepath.Join(filepath.Dir(j.Worker), "owned-"+role), []byte(id), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return b, j, record
}

func TestCortexiumReadbackDurablyDistinguishesReceivedAndMissingFacts(t *testing.T) {
	for _, mode := range []string{"valid", "complete", "missing", "malformed", "partial-failure", "crash-before-outcome"} {
		t.Run(mode, func(t *testing.T) {
			b, j, record := readbackFixture(t)
			readbackPipeFixture(t, mode, b, &j, &record)
			var err error
			if mode != "crash-before-outcome" {
				err = b.verifyCortexiumCopy(t.Context(), &j, &record, 0)
				if (err == nil) != (mode == "valid") {
					t.Fatal(mode, err)
				}
				if err != nil {
					j.Failed, j.Failure = true, clipCleanupFailure(err)
				}
				if mode == "valid" {
					if err := b.verifyCortexiumCopy(t.Context(), &j, &record, 1); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err := b.sealCortexiumReadback(&j); err != nil {
				t.Fatal(err)
			}
			saved, data, err := b.loadCortexiumReadback(&j)
			if err != nil {
				t.Fatal(err)
			}
			f := saved.Files[0]
			if mode == "valid" {
				if j.Failed || f.State != "verified" || f.Header == nil || !f.EOF || !f.ProcessSettled || f.BodySHA256 != f.SHA256 {
					t.Fatal("valid facts lost", f)
				}
			} else {
				if !j.Failed || f.State == "verified" {
					t.Fatal("missing/refused proof became success", f, j)
				}
				if mode == "missing" || mode == "malformed" || mode == "crash-before-outcome" {
					if f.Header != nil {
						t.Fatal("invented header", f)
					}
				}
				if mode == "complete" && (f.Header == nil || f.Header.Mode != 0666 || !f.EOF || !f.ProcessSettled) {
					t.Fatal("actual refused header/settlement lost", f)
				}
				if mode == "partial-failure" && (f.BodyBytes != 512 || f.BodySHA256 == "" || !f.ProcessSettled) {
					t.Fatal("partial received bytes lost", f)
				}
			}
			if len(data) > 32<<10 || j.Receipt.CortexiumReadbackSHA256 != Digest(data) {
				t.Fatal("unbounded/unbound record")
			}
			before := append([]byte(nil), data...)
			if err := b.sealCortexiumReadback(&j); err != nil {
				t.Fatal(err)
			}
			after, _ := os.ReadFile(readbackFile(&j))
			if !bytes.Equal(before, after) {
				t.Fatal("rejoin rewrote sealed facts")
			}
			if err := b.verifyCortexiumCopy(t.Context(), &j, &saved, 0); err == nil {
				t.Fatal("sealed copy was replayed")
			}
		})
	}
}

func TestCortexiumReadbackRefusesDriftAndFalseSuccessBeforeDispatch(t *testing.T) {
	for _, fault := range []string{"missing", "malformed", "foreign-work", "resource", "shape", "false-verified", "digest", "false-success", "foreign-valid-resource", "unknown-field"} {
		t.Run(fault, func(t *testing.T) {
			b, j, record := readbackFixture(t)
			switch fault {
			case "missing":
				if err := os.Remove(readbackFile(&j)); err != nil {
					t.Fatal(err)
				}
			case "malformed":
				if err := os.WriteFile(readbackFile(&j), []byte("{"), 0600); err != nil {
					t.Fatal(err)
				}
			case "foreign-work":
				record.WorkSHA256 = strings.Repeat("e", 64)
			case "resource":
				record.Container = "foreign"
			case "foreign-valid-resource":
				record.Container = strings.Repeat("f", 64)
			case "shape":
				record.Files[0].Name = "../private"
			case "false-verified":
				record.Files[0].State = "verified"
			case "digest":
				j.Receipt.CortexiumReadbackSHA256 = strings.Repeat("e", 64)
			case "false-success":
				if err := b.sealCortexiumReadback(&j); err != nil {
					t.Fatal(err)
				}
				j.Failed = false
			}
			if fault != "missing" && fault != "malformed" && fault != "false-success" {
				if err := writeJSON(readbackFile(&j), record); err != nil {
					t.Fatal(err)
				}
			}
			if fault == "unknown-field" {
				raw, _ := json.Marshal(record)
				raw = append(raw[:len(raw)-1], []byte(`,"foreign_private":"must not export"}`)...)
				if err := os.WriteFile(readbackFile(&j), raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			calls := 0
			b.runCommand = func(context.Context, []byte, string, ...string) ([]byte, error) { calls++; return nil, nil }
			if fault == "false-success" {
				if _, err := b.cortexiumReadbackReport(&j); err == nil {
					t.Fatal("false completed proof accepted")
				}
			} else if err := b.verifyCortexiumCopy(t.Context(), &j, &record, 0); err == nil {
				t.Fatal("foreign/missing intent accepted")
			}
			if calls != 0 {
				t.Fatal("refusal dispatched copy")
			}
		})
	}
}

func TestCortexiumReadbackFailureExportRetainsFactsWithoutQualifyingCandidate(t *testing.T) {
	b, j, record := readbackFixture(t)
	payload := cortexiumPayload(t, &j.Request, b.Config.Cortexium.Runtime)
	j.Receipt.BuildSHA256 = j.Request.Cortexium.SHA256
	if err := writeJSON(j.Worker, deliveryWork{Kind: cortexiumWorkKind, Config: b.Config, Job: j}); err != nil {
		t.Fatal(err)
	}
	source, _ := os.ReadFile(j.Worker)
	record.WorkSHA256 = Digest(source)
	if err := writeJSON(readbackFile(&j), record); err != nil {
		t.Fatal(err)
	}
	body := bytes.Repeat([]byte("x"), 4<<20)
	refused := copyFixtureTar(t, tar.Header{Name: "server", Size: int64(len(body)), Mode: 0666, Typeflag: tar.TypeReg}, body, false)
	b.runCommand = func(_ context.Context, _ []byte, _ string, args ...string) ([]byte, error) {
		if len(args) == 5 && args[2] == "cp" {
			return refused, nil
		}
		return nil, nil // Fixed empty simulated resource discovery, no real engine.
	}
	err := b.verifyCortexiumCopy(t.Context(), &j, &record, 0)
	if err == nil {
		t.Fatal("bad copied mode accepted")
	}
	j.Failed, j.Failure, j.Outcome = true, clipCleanupFailure(err), "blocked"
	originalFailure := j.Failure
	if err := os.WriteFile(filepath.Join(filepath.Dir(j.Worker), "input.tar.gz"), payload, 0600); err != nil {
		t.Fatal(err)
	}
	// This fixture never creates a resource; remove its helper's synthetic intent.
	if err := os.Remove(filepath.Join(filepath.Dir(j.Worker), "resources-intent")); err != nil {
		t.Fatal(err)
	}
	if _, err := b.prepareCortexiumCleanup(&j); err != nil {
		t.Fatal(err)
	}
	if err := b.runCortexiumCleanup(t.Context(), &j); err != nil {
		t.Fatal(err)
	}
	if !j.Done || !j.Failed || j.Outcome != "blocked" || j.Failure != originalFailure || j.Receipt.BrowserEvidenceSHA256 == "" {
		t.Fatal("failure lost or false qualification", j)
	}
	packed, err := os.ReadFile(filepath.Join(filepath.Dir(j.Worker), "evidence.tar.gz"))
	if err != nil {
		t.Fatal(err)
	}
	if Digest(packed) != j.Receipt.BrowserEvidenceSHA256 || len(packed) > saas.MaxBundle {
		t.Fatal("unbound/oversized failure artifact")
	}
	if _, _, err := saas.Decode(packed, j.Request.Source, j.Request.Cortexium.PatchSHA256, j.Request.Cortexium.AuthoritySHA256); err == nil {
		t.Fatal("failure proof admitted as qualified candidate")
	}
	z, err := gzip.NewReader(bytes.NewReader(packed))
	if err != nil {
		t.Fatal(err)
	}
	defer z.Close()
	reader := tar.NewReader(z)
	found := false
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if header.Name == "reports/browser.json" {
			data, err := io.ReadAll(reader)
			if err != nil {
				t.Fatal(err)
			}
			var report struct {
				Readback json.RawMessage `json:"copy_readback"`
				Failure  string          `json:"operator_failure"`
			}
			if json.Unmarshal(data, &report) != nil || Digest(report.Readback) != j.Receipt.CortexiumReadbackSHA256 || report.Failure != originalFailure || !bytes.Contains(report.Readback, []byte(`"mode":438`)) {
				t.Fatal("export lost exact refused facts", string(data))
			}
			found = true
		}
	}
	if !found {
		t.Fatal("failure artifact omitted durable copy observation")
	}
}

func TestCortexiumReadbackV1CleanupCompatibilityDoesNotInventFacts(t *testing.T) {
	b, j, _ := readbackFixture(t)
	if err := os.Remove(readbackFile(&j)); err != nil {
		t.Fatal(err)
	}
	j.Failed, j.Failure = true, "preserved v1 failure"
	if err := writeJSON(j.Worker, deliveryWork{Kind: cortexiumLegacyWorkKind, Config: b.Config, Job: j}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.prepareCortexiumCleanup(&j); err != nil {
		t.Fatal(err)
	}
	if _, err := b.cortexiumCleanupClaim(&j); err != nil {
		t.Fatal(err)
	}
	if j.Receipt.CortexiumReadbackSHA256 != "" {
		t.Fatal("historical operation gained invented readback")
	}
	if _, err := os.Stat(readbackFile(&j)); !os.IsNotExist(err) {
		t.Fatal("historical operation migrated", err)
	}
}

func TestCortexiumReadbackE7ReaderRefusesV2BeforeClaim(t *testing.T) {
	old := os.Getenv("RUNNER_CORTEXIUM_E7_OPERATOR")
	if old == "" {
		t.Skip("explicit preserved e7fc030 operator proof")
	}
	data, err := os.ReadFile(old)
	if err != nil || Digest(data) != "f87397bcefd0c195935b26ceee881ea96b82e7942adf2bd4a61fd6d3ac454286" && Digest(data) != "c7d74786fffa287755b5e0b00703d1b37d7311fae014222c25f3f314c16a5f71" {
		t.Fatal("unbound prior reader", err)
	}
	_, j, _ := readbackFixture(t)
	// No inherited lease is supplied: old code must reject the work kind BEFORE
	// lease/claim/native execution. This launches only the preserved Go reader.
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, old, "--delivery-worker", j.Worker).CombinedOutput()
	if err == nil || !bytes.Contains(output, []byte("Cortexium saved work ownership differs")) {
		t.Fatal("old reader did not refuse new contract", string(output), err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(j.Worker), "cleanup-started")); !os.IsNotExist(err) {
		t.Fatal("old reader claimed cleanup", err)
	}
}

func readbackPipeFixture(t *testing.T, mode string, b *Live, j *Job, record *cortexiumReadback) {
	t.Helper()
	// A fixed local process fixture, not Docker/browser/App source.
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	program := filepath.Join(t.TempDir(), "copy-fixture")
	if err := os.WriteFile(program, []byte("#!/bin/sh\nexec "+quote(binary)+" -test.run=^TestCortexiumCopyPipeWorker$ -test.timeout=20s -- \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	// Rebind the disposable work to this exact fixture policy BEFORE effects.
	b.Config.Docker = program
	config, _ := json.Marshal(b.Config)
	j.Receipt.Policy = Digest(config)
	j.Request.Cortexium.PolicySHA256 = j.Receipt.Policy
	if err := writeJSON(j.Worker, deliveryWork{Kind: cortexiumWorkKind, Config: b.Config, Job: *j}); err != nil {
		t.Fatal(err)
	}
	source, _ := os.ReadFile(j.Worker)
	record.WorkSHA256 = Digest(source)
	if err := writeJSON(readbackFile(j), record); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RUNNER_CORTEXIUM_COPY_PIPE", mode)
	t.Setenv("RUNNER_CORTEXIUM_COPY_PIPE_ROOT", t.TempDir())
}

func TestCortexiumReadbackHeaderIsDurableBeforeShortObservationReturns(t *testing.T) {
	b, j, record := readbackFixture(t)
	readbackPipeFixture(t, "cancel", b, &j, &record)
	lease, err := os.OpenFile(filepath.Join(filepath.Dir(j.Worker), "worker.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	if err := lockOperator(lease); err != nil {
		t.Fatal(err)
	}
	defer unlockOperator(lease)
	ctx, cancel := context.WithCancel(context.WithValue(t.Context(), leaseKey{}, lease))
	defer cancel()
	done := make(chan error, 1)
	settled := false
	go func() { done <- b.verifyCortexiumCopy(ctx, &j, &record, 0) }()
	// Every exit waits for this owned local worker; never leave a stalled pipe.
	defer func() {
		cancel()
		if settled {
			return
		}
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("owned copy fixture did not settle")
		}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		pending, _, e := b.loadCortexiumReadback(&j)
		if e == nil && pending.Files[0].Header != nil {
			if pending.Files[0].State != "pending" || pending.Files[0].Header.Mode != 0666 || pending.Files[0].ProcessSettled {
				t.Fatal("pending observation invented final outcome", pending)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("actual header was not durable before drain", e)
		}
		time.Sleep(time.Millisecond)
	}
	b.launchDeliveryWorker = func(string, *os.File) error {
		t.Error("short observation duplicated worker")
		return errors.New("unexpected dispatch")
	}
	observer := j
	// The existing delivery boundary never waits on the active pipe.
	started := time.Now()
	if err := b.cortexiumDelivery(&observer, func() error { return nil }); err != nil || time.Since(started) > 500*time.Millisecond || observer.Done {
		t.Fatal("observation blocked or falsely completed", err, observer)
	}
	cancel()
	err = <-done
	settled = true
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatal("copy cancellation was not retained", err)
	}
	if err := b.sealCortexiumReadback(&j); err != nil {
		t.Fatal(err)
	}
	final, _, err := b.loadCortexiumReadback(&j)
	if err != nil || final.Files[0].State != "refused" || final.Files[0].Header == nil || !final.Files[0].ProcessSettled || !j.Failed {
		t.Fatal("cancel/failure facts lost", err, final)
	}
}
