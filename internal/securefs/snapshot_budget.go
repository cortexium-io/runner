package securefs

import (
	"fmt"
	"path/filepath"
)

const maxSnapshotDiagnosticPathBytes = 512

func snapshotDiagnosticPath(path string) string {
	if len(path) <= maxSnapshotDiagnosticPathBytes {
		return path
	}
	return path[:maxSnapshotDiagnosticPathBytes-3] + "..."
}

// SnapshotLimits bounds one logical repository snapshot. Repeated verification
// reads of the same pinned path are checked again but charged only for the
// largest observed payload, so race detection does not double-count content.
type SnapshotLimits struct {
	MaxEntries    int
	MaxFileBytes  int64
	MaxTotalBytes int64
}

type SnapshotBudget struct {
	limits  SnapshotLimits
	entries map[string]struct{}
	charged map[string]int64
	total   int64
}

func NewSnapshotBudget(limits SnapshotLimits) (*SnapshotBudget, error) {
	if limits.MaxEntries <= 0 {
		return nil, fmt.Errorf("snapshot maximum entries must be positive; got %d", limits.MaxEntries)
	}
	if limits.MaxFileBytes <= 0 {
		return nil, fmt.Errorf("snapshot maximum individual bytes must be positive; got %d", limits.MaxFileBytes)
	}
	if limits.MaxTotalBytes <= 0 {
		return nil, fmt.Errorf("snapshot maximum aggregate bytes must be positive; got %d", limits.MaxTotalBytes)
	}
	if limits.MaxTotalBytes < limits.MaxFileBytes {
		return nil, fmt.Errorf("snapshot maximum aggregate bytes %d must be at least maximum individual bytes %d", limits.MaxTotalBytes, limits.MaxFileBytes)
	}
	return &SnapshotBudget{limits: limits, entries: map[string]struct{}{}, charged: map[string]int64{}}, nil
}

func (b *SnapshotBudget) addEntry(directory, name string) error {
	return b.AddEntry(filepath.Join(directory, name))
}

// AddEntry charges one logical snapshot path before its representation is
// appended to a collection.
func (b *SnapshotBudget) AddEntry(path string) error {
	if b == nil {
		return nil
	}
	path = filepath.Clean(path)
	if _, exists := b.entries[path]; exists {
		return nil
	}
	if len(b.entries) >= b.limits.MaxEntries {
		return fmt.Errorf("snapshot maximum entries limit %d exceeded before path %q (next count %d)", b.limits.MaxEntries, snapshotDiagnosticPath(path), len(b.entries)+1)
	}
	b.entries[path] = struct{}{}
	return nil
}

// payloadAllowance validates the observed size before allocation or reading
// and returns the largest payload that may be read for this logical path.
func (b *SnapshotBudget) payloadAllowance(path string, observed int64) (int64, error) {
	if b == nil {
		return int64(^uint64(0)>>1) - 1, nil
	}
	if observed < 0 {
		return 0, fmt.Errorf("snapshot path %q has invalid payload size %d", snapshotDiagnosticPath(path), observed)
	}
	if observed > b.limits.MaxFileBytes {
		return 0, fmt.Errorf("snapshot maximum individual bytes limit %d exceeded by path %q (observed %d)", b.limits.MaxFileBytes, snapshotDiagnosticPath(path), observed)
	}
	previous := b.charged[path]
	allowance := b.limits.MaxTotalBytes - b.total + previous
	if allowance > b.limits.MaxFileBytes {
		allowance = b.limits.MaxFileBytes
	}
	if observed > allowance {
		return 0, fmt.Errorf("snapshot maximum aggregate bytes limit %d exceeded by path %q (charged %d, observed %d)", b.limits.MaxTotalBytes, snapshotDiagnosticPath(path), b.total, observed)
	}
	return allowance, nil
}

func (b *SnapshotBudget) chargePayload(path string, size int64) error {
	if b == nil {
		return nil
	}
	if size > b.limits.MaxFileBytes {
		return fmt.Errorf("snapshot maximum individual bytes limit %d exceeded while reading path %q (read at least %d)", b.limits.MaxFileBytes, snapshotDiagnosticPath(path), size)
	}
	previous := b.charged[path]
	if size <= previous {
		return nil
	}
	if b.total > b.limits.MaxTotalBytes-(size-previous) {
		return fmt.Errorf("snapshot maximum aggregate bytes limit %d exceeded while reading path %q (charged %d, read at least %d)", b.limits.MaxTotalBytes, snapshotDiagnosticPath(path), b.total, size)
	}
	b.total += size - previous
	b.charged[path] = size
	return nil
}

func (b *SnapshotBudget) overflowError(path string, read int64) error {
	if b == nil {
		return fmt.Errorf("snapshot path %q exceeded its read allowance", snapshotDiagnosticPath(path))
	}
	if read > b.limits.MaxFileBytes {
		return fmt.Errorf("snapshot maximum individual bytes limit %d exceeded while reading path %q (read at least %d)", b.limits.MaxFileBytes, snapshotDiagnosticPath(path), read)
	}
	return fmt.Errorf("snapshot maximum aggregate bytes limit %d exceeded while reading path %q (charged %d, read at least %d)", b.limits.MaxTotalBytes, snapshotDiagnosticPath(path), b.total, read)
}
