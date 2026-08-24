package main

import (
	"context"
	"path/filepath"
	"strings"
	"time"

	"github.com/cortexium-io/runner/internal/subprocess"
)

const defaultRunnerConfigPath = ".cortexium/runner.json"

// resolveRunnerConfigPath keeps an explicit operator choice intact. Otherwise
// it finds the repository root so every command agrees with init's default,
// including when invoked from a subdirectory.
func resolveRunnerConfigPath(explicitPath, startDirectory string) string {
	if path := strings.TrimSpace(explicitPath); path != "" {
		return filepath.Clean(path)
	}
	if strings.TrimSpace(startDirectory) == "" {
		startDirectory = "."
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result, err := subprocess.RunOSFailClosedInput(
		ctx, "git", []string{"-C", startDirectory, "rev-parse", "--show-toplevel"}, "", 10*time.Second, nil,
		subprocess.GitStdoutLimit, subprocess.DiagnosticStderrLimit,
	)
	if err == nil {
		root := strings.TrimSpace(result.Stdout)
		if root != "" && !strings.ContainsAny(root, "\r\n\x00") {
			return filepath.Join(root, filepath.FromSlash(defaultRunnerConfigPath))
		}
	}
	absolute, err := filepath.Abs(startDirectory)
	if err != nil {
		return filepath.FromSlash(defaultRunnerConfigPath)
	}
	return filepath.Join(absolute, filepath.FromSlash(defaultRunnerConfigPath))
}
