package execution

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/cortexium-io/runner/internal/runner"
)

// Inventory size is not checkout size. Keep one bounded NUL-delimited record
// while summing content against the existing managed-storage allowance.
type treeInventory struct {
	entry       []byte
	total       int64
	limit       runner.Bytes
	verifyPaths bool
	err         error
}

func (v *treeInventory) Write(data []byte) (int, error) {
	if v.err != nil {
		return 0, v.err
	}
	written := 0
	for len(data) != 0 {
		end := bytes.IndexByte(data, 0)
		n := end
		if n < 0 {
			n = len(data)
		}
		if n > (1<<20)-len(v.entry) {
			v.err = fmt.Errorf("%w: source tree record exceeds 1MiB", runner.ErrCapacity)
			return written, v.err
		}
		v.entry = append(v.entry, data[:n]...)
		written += n
		data = data[n:]
		if end < 0 {
			break
		}
		written++
		data = data[1:]
		if len(v.entry) != 0 {
			v.err = v.addEntry()
			if v.err != nil {
				return written, v.err
			}
			v.entry = v.entry[:0]
		}
	}
	return written, nil
}

func (v *treeInventory) addEntry() error {
	parts := bytes.SplitN(v.entry, []byte{'\t'}, 2)
	if len(parts) != 2 {
		return errors.New("invalid source tree entry")
	}
	if v.verifyPaths {
		path := string(parts[1])
		if !(bytes.HasPrefix(parts[0], []byte("100644 ")) || bytes.HasPrefix(parts[0], []byte("100755 "))) || filepath.Base(path) == ".gitattributes" || filepath.Base(path) == ".gitmodules" || path == ".runner-tmp" || strings.HasPrefix(path, ".runner-tmp/") || strings.HasPrefix(path, ".codex/") {
			return errors.New("first writable slice supports regular files without Git attributes/submodules")
		}
	}
	size, err := treeEntrySize(parts[0])
	if err != nil || size > int64(v.limit)-v.total {
		return fmt.Errorf("%w: source tree content exceeds managed-storage allowance", runner.ErrCapacity)
	}
	v.total += size
	return nil
}

func sourceTreeBytes(ctx context.Context, repository string, environment []string, tree string, limit runner.Bytes, verifyPaths bool) (int64, error) {
	cmd := gitCommand(ctx, repository, environment, nil, "ls-tree", "-rlz", tree)
	inventory := &treeInventory{limit: limit, verifyPaths: verifyPaths}
	diagnostic := &diagnosticTail{limit: 1024}
	cmd.Stdout, cmd.Stderr = inventory, diagnostic
	err := cmd.Run()
	if inventory.err != nil {
		return 0, inventory.err
	}
	if err != nil {
		return 0, fmt.Errorf("git source inventory: %w: %s", err, runner.Clip(string(diagnostic.bytes())))
	}
	if len(inventory.entry) != 0 {
		return 0, errors.New("incomplete source tree record")
	}
	return inventory.total, nil
}
