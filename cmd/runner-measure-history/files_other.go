//go:build !darwin && !linux

package main

import "os"

func unlinked(os.FileInfo) bool { return false }
