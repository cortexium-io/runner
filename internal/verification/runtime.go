package verification

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cortexium-io/runner/internal/securefs"
)

// Bounds support whole browser installations without reading a framework into
// memory. These are refusal ceilings, not discovered runtime completeness.
var runtimeLimits = securefs.SnapshotLimits{MaxEntries: 100000, MaxFileBytes: 2 << 30, MaxTotalBytes: 8 << 30}

// ObserveRuntimePaths binds only operator-declared runtime closures. Doctor uses
// the same collector: it never executes the runtimes. Symlink roots, external
// framework links, missing files, special objects and unstable reads fail closed.
func ObserveRuntimePaths(ctx context.Context, paths []string) (string, error) {
	var identities [][2]string
	budget, _ := securefs.NewSnapshotBudget(runtimeLimits)
	for _, selected := range paths {
		if !filepath.IsAbs(selected) || filepath.Clean(selected) != selected || strings.ContainsAny(selected, "\x00\r\n") {
			return "", errors.New("runtime selection must be a literal canonical absolute path")
		}
		selected, err := securefs.AbsolutePath(selected)
		if err != nil {
			return "", err
		}
		info, err := os.Lstat(selected)
		if err != nil {
			return "", fmt.Errorf("runtime root unavailable: %w", err)
		}
		root, selections, whole := filepath.Dir(selected), []string{filepath.Base(selected)}, false
		switch {
		case info.IsDir():
			root, selections, whole = selected, []string{"."}, true
		case info.Mode().IsRegular():
		default:
			return "", errors.New("runtime root must be a regular file or no-follow directory")
		}
		digest, err := collectContentWithBudget(ctx, root, selections, nil, budget, whole)
		if err != nil {
			return "", fmt.Errorf("observe runtime %s: %w", selected, err)
		}
		identities = append(identities, [2]string{selected, digest})
	}
	encoded, _ := json.Marshal(identities)
	return hash(encoded), nil
}
