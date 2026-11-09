//go:build darwin || linux

package execution

import (
	"fmt"
	"os"
	"syscall"
)

func workspaceFileIdentity(info os.FileInfo) (string, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", fmt.Errorf("workspace filesystem identity is unavailable")
	}
	return fmt.Sprintf("%d:%d", stat.Dev, stat.Ino), nil
}

func workspaceSameFilesystem(old, parent string) error {
	a, err := os.Lstat(old)
	if err != nil {
		return err
	}
	b, err := os.Lstat(parent)
	if err != nil {
		return err
	}
	if !a.IsDir() || !b.IsDir() || a.Sys().(*syscall.Stat_t).Dev != b.Sys().(*syscall.Stat_t).Dev {
		return fmt.Errorf("workspace preservation requires regular directories on the same filesystem")
	}
	return nil
}
