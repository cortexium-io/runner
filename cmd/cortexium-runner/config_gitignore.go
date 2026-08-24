package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cortexium-io/runner/internal/securefs"
	"github.com/cortexium-io/runner/internal/subprocess"
)

const runnerConfigIgnoreComment = "# Cortexium Runner local configuration"
const maxGitignoreBytes = 16 * 1024 * 1024

func previewRunnerConfigGitignore(configPath, repositoryRoot string, output io.Writer) error {
	_, pattern, local, err := projectLocalConfigPath(configPath, repositoryRoot)
	if err != nil || !local {
		return err
	}
	fmt.Fprintf(output, "  Git ignore: ensure %s is ignored in %s (changes left uncommitted)\n", pattern, filepath.Join(repositoryRoot, ".gitignore"))
	return nil
}

func prepareRunnerConfigGitignore(ctx context.Context, configPath, repositoryRoot string, output io.Writer) error {
	relative, pattern, local, err := projectLocalConfigPath(configPath, repositoryRoot)
	if err != nil || !local {
		return err
	}
	ignored, err := initGitExitCode(ctx, repositoryRoot, "check-ignore", "--no-index", "--quiet", "--", relative)
	if err != nil {
		return fmt.Errorf("inspect Runner config ignore state: %w", err)
	}
	if ignored == 0 {
		return nil
	}
	if ignored != 1 {
		return fmt.Errorf("inspect Runner config ignore state: git exited %d", ignored)
	}
	ignoreFile := filepath.Join(repositoryRoot, ".gitignore")
	if err := writeRunnerConfigGitignore(ignoreFile, pattern); err != nil {
		return err
	}
	ignored, err = initGitExitCode(ctx, repositoryRoot, "check-ignore", "--no-index", "--quiet", "--", relative)
	if err != nil {
		return fmt.Errorf("verify Runner config ignore state: %w", err)
	}
	if ignored != 0 {
		return fmt.Errorf("%s does not ignore %s after adding %s; inspect overriding rules", ignoreFile, relative, pattern)
	}
	fmt.Fprintf(output, "Added %s to %s; the .gitignore change was not staged or committed\n", pattern, ignoreFile)
	return nil
}

func projectLocalConfigPath(configPath, repositoryRoot string) (string, string, bool, error) {
	configAbsolute, err := filepath.Abs(configPath)
	if err != nil {
		return "", "", false, fmt.Errorf("resolve Runner config path: %w", err)
	}
	repositoryAbsolute, err := filepath.Abs(repositoryRoot)
	if err != nil {
		return "", "", false, fmt.Errorf("resolve repository root: %w", err)
	}
	configAbsolute = canonicalInitPath(configAbsolute)
	repositoryAbsolute = canonicalInitPath(repositoryAbsolute)
	relative, err := filepath.Rel(repositoryAbsolute, configAbsolute)
	if err != nil {
		return "", "", false, fmt.Errorf("resolve Runner config path relative to repository: %w", err)
	}
	if relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) || filepath.IsAbs(relative) {
		return "", "", false, nil
	}
	relative = filepath.ToSlash(relative)
	pattern, err := exactGitignorePattern(relative)
	return relative, pattern, true, err
}

func canonicalInitPath(path string) string {
	current := filepath.Clean(path)
	suffix := []string{}
	for {
		if canonical, err := filepath.EvalSymlinks(current); err == nil {
			return filepath.Join(append([]string{canonical}, suffix...)...)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return filepath.Clean(path)
		}
		suffix = append([]string{filepath.Base(current)}, suffix...)
		current = parent
	}
}

func exactGitignorePattern(relativePath string) (string, error) {
	if relativePath == "" || relativePath == "." || strings.ContainsAny(relativePath, "\r\n") {
		return "", fmt.Errorf("Runner config has unsupported repository-relative path %q", relativePath)
	}
	var pattern strings.Builder
	pattern.WriteByte('/')
	for _, character := range filepath.ToSlash(relativePath) {
		if strings.ContainsRune(`\\*?[]!# `, character) {
			pattern.WriteByte('\\')
		}
		pattern.WriteRune(character)
	}
	return pattern.String(), nil
}

func writeRunnerConfigGitignore(path, pattern string) error {
	directory, err := securefs.OpenDir(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("open repository root: %w", err)
	}
	defer directory.Close()
	content, mode, initial, err := directory.ReadFile(filepath.Base(path), maxGitignoreBytes)
	if err != nil {
		return fmt.Errorf("read repository .gitignore: %w", err)
	}
	lineEnding := "\n"
	if strings.Contains(string(content), "\r\n") {
		lineEnding = "\r\n"
	}
	normalized := strings.ReplaceAll(string(content), "\r\n", "\n")
	for _, line := range strings.Split(normalized, "\n") {
		if line == pattern {
			return nil
		}
	}
	if normalized != "" && !strings.HasSuffix(normalized, "\n") {
		normalized += "\n"
	}
	if !strings.Contains("\n"+normalized, "\n"+runnerConfigIgnoreComment+"\n") {
		normalized += runnerConfigIgnoreComment + "\n"
	}
	normalized += pattern + "\n"
	if lineEnding == "\r\n" {
		normalized = strings.ReplaceAll(normalized, "\n", lineEnding)
	}
	if err := directory.ReplaceFile(filepath.Base(path), []byte(normalized), mode, initial); err != nil {
		return fmt.Errorf("write repository .gitignore: %w", err)
	}
	return nil
}

func initGitExitCode(ctx context.Context, directory string, args ...string) (int, error) {
	result, err := subprocess.RunOSFailClosedInput(ctx, "git", append([]string{"-C", directory}, args...), "", 30*time.Second, nil, subprocess.GitStdoutLimit, subprocess.DiagnosticStderrLimit)
	if err == nil {
		return 0, nil
	}
	var limitError *subprocess.CaptureLimitError
	if errors.As(err, &limitError) || result.ExitCode < 0 {
		return -1, err
	}
	return result.ExitCode, nil
}
