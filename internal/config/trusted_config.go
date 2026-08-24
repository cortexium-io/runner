package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const maxTrustedConfigBytes = 4 * 1024 * 1024

// LoadTrustedConfig is the only config loader for commands that can launch a
// model or change role policy. It validates provenance before decoding any
// operator-selected execution settings.
func LoadTrustedConfig(path string) (Config, error) {
	cfg, _, err := CheckTrustedConfig(path)
	return cfg, err
}

func CheckTrustedConfig(path string) (Config, StaticCheckReport, error) {
	report := StaticCheckReport{Checks: []StaticCheckResult{}}
	if strings.TrimSpace(path) == "" {
		err := errors.New("an explicit --config path is required for privileged Runner commands")
		report.Checks = append(report.Checks, StaticCheckResult{ID: "config.provenance", Status: StaticCheckFailed, Detail: err.Error()})
		return Config{}, report, err
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return Config{}, report, fmt.Errorf("resolve trusted config path: %w", err)
	}
	data, err := readTrustedConfigFile(filepath.Clean(absolute), maxTrustedConfigBytes)
	if err != nil {
		report.Checks = append(report.Checks, StaticCheckResult{ID: "config.provenance", Status: StaticCheckFailed, Detail: err.Error()})
		return Config{}, report, err
	}
	cfg, err := decodeConfig(data)
	if err != nil {
		report.Checks = append(report.Checks, StaticCheckResult{ID: "config.load", Status: StaticCheckFailed, Detail: err.Error()})
		return Config{}, report, err
	}
	projectLocal, err := validateConfigLocation(absolute, cfg)
	if err != nil {
		report.Checks = append(report.Checks, StaticCheckResult{ID: "config.provenance", Status: StaticCheckFailed, Detail: err.Error()})
		return Config{}, report, err
	}
	detail := "operator config is a private regular file outside repository and worktree roots"
	if projectLocal {
		detail = "operator config is a private regular file in the project repository"
	}
	report.Checks = append(report.Checks,
		StaticCheckResult{ID: "config.provenance", Status: StaticCheckPassed, Detail: detail},
		StaticCheckResult{ID: "config.load", Status: StaticCheckPassed, Detail: "configuration is valid JSON with no unknown fields"},
	)
	return checkDecodedConfig(cfg, report)
}

func ValidateTrustedConfigDestination(path string, workspaceRoots ...string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("an explicit --config path is required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	for _, root := range workspaceRoots {
		if strings.TrimSpace(root) == "" {
			continue
		}
		if withinPath(root, absolute) {
			return fmt.Errorf("operator config %s must be outside implementation-workspace root %s", absolute, root)
		}
	}
	return nil
}

func validateConfigLocation(path string, cfg Config) (bool, error) {
	workspaceRoots := make([]string, 0, len(cfg.Harnesses))
	for _, harness := range cfg.Harnesses {
		workspaceRoots = append(workspaceRoots, harness.WorkspaceWriteRoot)
	}
	if err := ValidateTrustedConfigDestination(path, workspaceRoots...); err != nil {
		return false, err
	}
	if !withinPath(cfg.ProjectDir, path) {
		return false, nil
	}
	return true, nil
}

func withinPath(root, candidate string) bool {
	rootAbsolute, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	candidateAbsolute, err := filepath.Abs(candidate)
	if err != nil {
		return false
	}
	rootAbsolute = canonicalPath(rootAbsolute)
	candidateAbsolute = canonicalPath(candidateAbsolute)
	relative, err := filepath.Rel(filepath.Clean(rootAbsolute), filepath.Clean(candidateAbsolute))
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(os.PathSeparator))
}

// canonicalPath resolves every existing ancestor and then reapplies any
// not-yet-created suffix. This closes the init case where an external-looking
// path traverses a symlink into the repository before the config file exists.
func canonicalPath(path string) string {
	current := filepath.Clean(path)
	suffix := []string{}
	for {
		if canonical, err := filepath.EvalSymlinks(current); err == nil {
			parts := append([]string{canonical}, suffix...)
			return filepath.Join(parts...)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return filepath.Clean(path)
		}
		suffix = append([]string{filepath.Base(current)}, suffix...)
		current = parent
	}
}
