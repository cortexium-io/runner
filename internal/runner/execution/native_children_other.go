//go:build !darwin && !linux

package execution

import (
	"errors"
	"time"
)

func ownedNativeProcesses(string, time.Time, map[string]bool) ([]nativeChild, error) {
	return nil, errors.New("native owned-process inventory is unsupported on this platform")
}

func killOwnedNativeProcess(nativeChild, string, map[string]bool) error {
	return errors.New("native owned-process termination is unsupported on this platform")
}

func rememberNativeRoot(int, map[string]bool) error {
	return errors.New("native process identity is unsupported on this platform")
}

func nativeChildIdentity(int) (nativeChild, error) {
	return nativeChild{}, errors.New("native process identity is unsupported on this platform")
}
