//go:build !darwin && !linux

package sourcesnapshot

import "os"

func unlinked(os.FileInfo) bool { return false }
