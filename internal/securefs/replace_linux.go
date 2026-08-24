//go:build linux

package securefs

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

func conditionalReplaceAt(directoryFD int, temporaryName, targetName string, expected FileState) error {
	if !expected.Exists {
		if err := unix.Renameat2(directoryFD, temporaryName, directoryFD, targetName, unix.RENAME_NOREPLACE); err != nil {
			if errors.Is(err, unix.EEXIST) {
				return fmt.Errorf("%w before replacing %s", ErrChanged, targetName)
			}
			return err
		}
		return nil
	}
	return exchangeExpectedFile(directoryFD, temporaryName, targetName, expected, func() error {
		return unix.Renameat2(directoryFD, temporaryName, directoryFD, targetName, unix.RENAME_EXCHANGE)
	})
}
