//go:build darwin || linux

package securefs

import (
	"errors"
	"fmt"
	"os"
	"runtime"

	"golang.org/x/sys/unix"
)

type runtimeAncestor struct {
	path  string
	state FileState
}

func openExternalRuntimeDir(path string) (*Directory, error) {
	return openAbsoluteDirForRead(path, false, false, true)
}

func validateDirectoryForRead(stat unix.Stat_t, private, runtimeReadOnly, ancestor bool) error {
	if !runtimeReadOnly {
		return validateDirectory(stat, private)
	}
	if private {
		return errors.New("runtime observation cannot authorize a private directory")
	}
	return validateRuntimeReadPermissions(stat, ancestor)
}

func validateRuntimeReadPermissions(stat unix.Stat_t, ancestor bool) error {
	if stat.Uid != 0 && stat.Uid != uint32(os.Geteuid()) {
		return fmt.Errorf("runtime owner uid %d is neither root nor the effective user", stat.Uid)
	}
	// Preserve strict traversal's existing sticky temporary ancestor support;
	// the selected runtime and its descendants may never be world-writable.
	if ancestor && stat.Mode&unix.S_IFMT == unix.S_IFDIR && stat.Mode&unix.S_ISVTX != 0 {
		return validateDirectory(stat, false)
	}
	if stat.Mode&0o002 != 0 {
		return errors.New("runtime object is world-writable")
	}
	if stat.Mode&0o020 != 0 && (runtime.GOOS != "darwin" || stat.Gid != 80) {
		return fmt.Errorf("runtime object is writable by unapproved group %d", stat.Gid)
	}
	return nil
}

func (d *Directory) verifyRuntimeAncestors(named *Directory) error {
	if !d.runtimeReadOnly {
		return nil
	}
	if len(d.runtimeAncestors) != len(named.runtimeAncestors) {
		return fmt.Errorf("%w: runtime ancestor chain changed", ErrChanged)
	}
	for index, before := range d.runtimeAncestors {
		after := named.runtimeAncestors[index]
		a, b := before.state, after.state
		// Sibling installations may change timestamps; ancestor identity and the
		// exact owner/group/mode must remain pinned. Selected directories also
		// retain the normal full-metadata/listing stability check in Verify.
		if before.path != after.path || a.dev != b.dev || a.ino != b.ino || a.uid != b.uid || a.gid != b.gid || a.mode != b.mode {
			return fmt.Errorf("%w: runtime ancestor identity or permissions changed", ErrChanged)
		}
	}
	return nil
}
