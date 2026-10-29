//go:build darwin || linux

package runner

import "golang.org/x/sys/unix"

func FreeBytes(path string) (int64, error) {
	var s unix.Statfs_t
	e := unix.Statfs(path, &s)
	return int64(s.Bavail) * int64(s.Bsize), e
}
