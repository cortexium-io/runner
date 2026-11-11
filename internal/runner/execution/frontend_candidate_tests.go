package execution

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/cortexium-io/runner/internal/runner"
)

// Resolve the selected tests against the prepared source before dispatch. Git
// metadata stays outside the command sandbox. New or renamed tests are selected
// by their current path, while deleted tests cannot become new selectors.
func frontendCandidateTests(ctx context.Context, w workspaceRecord, input runner.Value, patch []byte) (string, error) {
	const maxTests, maxSelectionBytes = 32, 4096
	var raw string
	var tests []string
	if json.Unmarshal(input.Inline, &raw) != nil || len(raw) > maxSelectionBytes ||
		json.Unmarshal([]byte(raw), &tests) != nil || len(tests) == 0 || len(tests) > maxTests {
		return "", errors.New("frontend check requires 1..32 explicit test files as a JSON array")
	}
	seen := make(map[string]bool, len(tests))
	for _, name := range tests {
		if seen[name] {
			return "", errors.New("frontend test selections must be unique")
		}
		seen[name] = true
	}

	// Prepare already authenticated these patch bytes against the owned source.
	// Captured trees can live in the private capture object store, so inspect
	// the retained diff without resolving its tree through the repository.
	changed, err := git(ctx, w.WorkDir, nil, patch, 1<<20, "apply", "--numstat", "-z", "--allow-empty", "--binary")
	if err != nil {
		return "", err
	}
	for _, entry := range strings.Split(strings.TrimSuffix(string(changed), "\x00"), "\x00") {
		if entry == "" {
			continue
		}
		fields := strings.SplitN(entry, "\t", 3)
		if len(fields) != 3 || !fs.ValidPath(fields[2]) {
			return "", errors.New("frontend patch file inventory is invalid")
		}
		path := fields[2]
		if !strings.HasPrefix(path, "frontend/src/") && !strings.HasPrefix(path, "frontend/scripts/") {
			continue
		}
		if !strings.HasSuffix(path, ".test.ts") && !strings.HasSuffix(path, ".test.js") {
			continue
		}
		info, err := os.Lstat(filepath.Join(w.WorkDir, filepath.FromSlash(path)))
		if errors.Is(err, os.ErrNotExist) {
			continue // Removed tests cannot be added to the explicit selection.
		}
		if err != nil || !info.Mode().IsRegular() {
			return "", errors.New("changed frontend test is unavailable or is not a regular file")
		}
		name := strings.TrimPrefix(path, "frontend/")
		if !seen[name] {
			tests = append(tests, name)
			seen[name] = true
		}
	}
	combined, err := json.Marshal(tests)
	if err != nil || len(tests) > maxTests || len(combined) > maxSelectionBytes {
		return "", errors.New("explicit and changed frontend tests exceed the configured selector bounds")
	}
	return string(combined), nil
}
