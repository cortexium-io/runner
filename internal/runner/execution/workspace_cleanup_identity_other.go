//go:build !darwin && !linux

package execution

import (
	"errors"
	"os"
)

func workspaceFileIdentity(os.FileInfo) (string, error) {
	return "", errors.New("workspace filesystem identity requires macOS or Linux")
}

func workspaceSameFilesystem(string, string) error {
	return errors.New("workspace preservation requires macOS or Linux")
}
