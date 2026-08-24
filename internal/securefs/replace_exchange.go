//go:build darwin || linux

package securefs

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func exchangeExpectedFile(directoryFD int, temporaryName, targetName string, expected FileState, exchange func() error) error {
	// The atomic exchange moves the commit-point target to temporaryName so its
	// identity can be checked. A mismatch is exchanged back before failing.
	if err := exchange(); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return fmt.Errorf("%w before replacing %s", ErrChanged, targetName)
		}
		return err
	}
	if replacementStateMatches(directoryFD, temporaryName, expected) {
		return nil
	}
	if err := exchange(); err != nil {
		return errors.Join(
			fmt.Errorf("%w before replacing %s", ErrChanged, targetName),
			fmt.Errorf("restore concurrently changed target: %w", err),
		)
	}
	return fmt.Errorf("%w before replacing %s", ErrChanged, targetName)
}

func replacementStateMatches(directoryFD int, name string, expected FileState) bool {
	var displaced unix.Stat_t
	if err := unix.Fstatat(directoryFD, name, &displaced, unix.AT_SYMLINK_NOFOLLOW); err != nil || !sameReplacementMetadata(stateFromStat(displaced), expected) || !expected.hasContent {
		return false
	}
	fd, err := unix.Openat(directoryFD, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return false
	}
	file := os.NewFile(uintptr(fd), filepath.Base(name))
	if file == nil {
		_ = unix.Close(fd)
		return false
	}
	content, readErr := io.ReadAll(io.LimitReader(file, expected.size+1))
	closeErr := file.Close()
	return readErr == nil && closeErr == nil && int64(len(content)) == expected.size && sha256.Sum256(content) == expected.contentDigest
}

func sameReplacementMetadata(current, expected FileState) bool {
	// The exchange itself may update ctime on some filesystems. Bind the
	// stable identity and content here; verifyState already checked ctime
	// immediately before the atomic exchange.
	return current.Exists == expected.Exists && current.dev == expected.dev && current.ino == expected.ino &&
		current.mode == expected.mode && current.size == expected.size && current.mtime == expected.mtime
}
