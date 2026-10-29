//go:build !darwin && !linux

package sqlite

import (
	"errors"
	"os"
)

func lock(string) (*os.File, error) {
	return nil, errors.New("this first slice supports macOS and Linux only")
}
