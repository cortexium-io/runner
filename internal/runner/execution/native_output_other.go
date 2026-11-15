//go:build !darwin && !linux

package execution

import (
	"errors"
	"io"
	"os"
)

func createNativeOutput(string) (*nativeOutputIdentity, error) {
	return nil, errors.New("recoverable native output requires macOS or Linux")
}

func openNativeOutput(string, *nativeOutputIdentity) (io.ReadCloser, *os.File, error) {
	return nil, nil, errors.New("recoverable native output requires macOS or Linux")
}

func drainNativeOutput(string, *nativeOutputIdentity) (bool, []byte, error) {
	return false, nil, errors.New("recoverable native output requires macOS or Linux")
}
