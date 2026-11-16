package execution

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
)

type nativeOutputIdentity struct {
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
}

// Three fixed-format fields fit this reserve even with full RFC3339Nano times.
// Normal worker facts leave this headroom for mandatory recovery evidence.
const nativeOutputRecoveryReserve runner.Bytes = 256

// A failed checkpoint leaves the original reader alive. Once it is closed,
// even a failed final advertisement leaves a durable terminal checkpoint for
// the next observer to recover under the lease.
func handoffNativeOutput(reader io.Closer, drained <-chan error, save func(bool) error) (bool, error) {
	if err := save(false); err != nil {
		return false, err
	}
	_ = reader.Close()
	_ = <-drained
	return true, save(true)
}

func nativeOutputLease(control string) (*os.File, bool, error) {
	path := filepath.Join(control, "output.lease")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, false, errors.New("native output observer lease is unavailable")
	}
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return nil, false, err
	}
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		file.Close()
		return nil, false, errors.New("native output observer lease changed")
	}
	locked, err := tryCaptureLease(file)
	if err != nil || !locked {
		file.Close()
		return nil, false, err
	}
	return file, true, nil
}

// Called only with the original reader's lease. The durable checkpoint is
// terminal, so remaining stream bytes cannot replace its final model result.
func recoverNativeOutput(spec harnessSpec, receipt *harnessReceipt) (bool, error) {
	if spec.Output == nil || receipt.Output == nil {
		return false, errors.New("saved anonymous output has no recoverable EOF witness")
	}
	if *spec.Output != *receipt.Output {
		return false, errors.New("saved output checkpoint and transport identity differ")
	}
	if !receipt.OutputFinalized || receipt.ExecutionEndedAt.IsZero() || receipt.ExecutionEndedAt.Before(receipt.Since) || receipt.ExecutionEndedAt.After(time.Now()) {
		return false, errors.New("saved output checkpoint is not terminal; retain the original observation")
	}
	closed, tail, err := drainNativeOutput(spec.Control, spec.Output)
	if err != nil {
		return false, err
	}
	if spec.Shared && len(tail) > 0 {
		var result commandResult
		if json.Unmarshal(receipt.Final, &result) != nil || result.AttemptID != receipt.AttemptID {
			return false, errors.New("saved shared-command output identity differs")
		}
		log := &diagnosticTail{limit: int(spec.Assignment.Limits.MaxLog)}
		_, _ = log.Write([]byte(result.Diagnostic))
		_, _ = log.Write(tail)
		result.Diagnostic = runner.DiagnosticTail(string(log.bytes()))
		receipt.Final, err = json.Marshal(result)
		if err != nil {
			return false, err
		}
	}
	if closed {
		receipt.OutputWritersPending, receipt.OutputObserverExpired = false, false
		receipt.OutputRecoveredAt = time.Now().UTC()
	}
	return closed, nil
}

func (n *Native) recordOutputRecovery(spec harnessSpec, receipt harnessReceipt) error {
	id := receipt.AttemptID + "/provenance.json"
	limit := min(spec.Assignment.Limits.MaxLog, 64<<10)
	data, err := n.disk.Read("artifacts/"+id, limit)
	if err != nil {
		return err
	}
	var proof runner.NativeProvenance
	if err := json.Unmarshal(data, &proof); err != nil {
		return err
	}
	proof.OutputRecovery = &runner.NativeOutputRecovery{
		Transport:        "fifo",
		ExecutionEndedAt: receipt.ExecutionEndedAt,
		EOFAt:            receipt.OutputRecoveredAt,
	}
	if receipt.ChildrenSettled {
		proof.CompletedAt = time.Now().UTC()
		if !proof.LaunchedAt.IsZero() {
			proof.ExecutionMillis = proof.CompletedAt.Sub(proof.LaunchedAt).Milliseconds()
		}
	}
	data, err = boundedHarnessProof(proof, limit)
	if err != nil {
		return err
	}
	return n.disk.Write("artifacts/"+id, data, limit)
}
