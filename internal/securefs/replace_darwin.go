//go:build darwin

package securefs

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

func conditionalReplaceAt(directoryFD int, temporaryName, targetName string, expected FileState) error {
	if !expected.Exists {
		if err := unix.RenameatxNp(directoryFD, temporaryName, directoryFD, targetName, unix.RENAME_EXCL); err != nil {
			if errors.Is(err, unix.EEXIST) {
				return fmt.Errorf("%w before replacing %s", ErrChanged, targetName)
			}
			return err
		}
		return nil
	}
	return exchangeExpectedFile(directoryFD, temporaryName, targetName, expected, func() error {
		return unix.RenameatxNp(directoryFD, temporaryName, directoryFD, targetName, unix.RENAME_SWAP)
	})
}
