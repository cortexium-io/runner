//go:build !darwin && !linux

package runner

import "errors"

func FreeBytes(string) (int64, error) {
	return 0, errors.New("this first slice supports macOS and Linux only")
}
