//go:build !darwin && !linux

package main

import "os"

func evidenceUnlinked(os.FileInfo) bool { return false }
