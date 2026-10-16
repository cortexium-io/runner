//go:build darwin

package backup

import "golang.org/x/sys/unix"

func install(from, to string) error { return unix.RenamexNp(from, to, unix.RENAME_EXCL) }
