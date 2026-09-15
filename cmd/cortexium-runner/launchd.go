package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/cortexium-io/runner/internal/github"
	"github.com/cortexium-io/runner/internal/subprocess"
)

func launchctl(ctx context.Context, args ...string) (string, error) {
	result, err := subprocess.RunFailClosed(ctx, subprocess.OSRunner{}, "/bin/launchctl", args, "", 15*time.Second, 256*1024, 4096)
	if err != nil {
		return "", fmt.Errorf("launchctl %s failed: %w", args[0], err)
	}
	return result.Stdout, nil
}

func launchdTarget(label string) (string, error) {
	if label == "" || len(label) > 200 {
		return "", errors.New("invalid launchd service label")
	}
	for _, c := range label {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '-' || c == '_') {
			return "", errors.New("invalid launchd service label")
		}
	}
	return fmt.Sprintf("gui/%d/%s", os.Geteuid(), label), nil
}

func launchdValue(output, key string) string {
	// Top-level launchctl print fields have exactly one indentation level;
	// never mistake environment or child-service values for the job identity.
	prefix := "\t" + key + " = "
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix))
		}
	}
	return ""
}

func currentLaunchdService(ctx context.Context, executable string) (string, error) {
	if runtime.GOOS != "darwin" {
		return "", nil
	}
	label := os.Getenv("XPC_SERVICE_NAME")
	if label == "" || label == "0" {
		return "", nil
	}
	// Interactive shells can inherit another application's service environment.
	// Only a direct launchd child can identify itself as a managed Runner.
	if os.Getppid() != 1 {
		return "", nil
	}
	target, err := launchdTarget(label)
	if err != nil {
		return "", err
	}
	output, err := launchctl(ctx, "print", target)
	if err != nil {
		return "", err
	}
	pid, _ := strconv.Atoi(launchdValue(output, "pid"))
	program, err := filepath.EvalSymlinks(launchdValue(output, "program"))
	if err != nil || pid != os.Getpid() || program != executable {
		return "", errors.New("launchd job does not match this Runner process and executable")
	}
	return target, nil
}

func validateLaunchdTarget(target string) error {
	parts := strings.Split(target, "/")
	if len(parts) != 3 {
		return errors.New("invalid launchd target")
	}
	expected, err := launchdTarget(parts[2])
	if err != nil || target != expected {
		return errors.New("launchd target is not a service owned by this user")
	}
	return nil
}

// Unloading happens only after the coordinator has drained and its resources
// have been released. Unlike stopping the process, bootout prevents KeepAlive
// from respawning it. No plist or persistent disabled setting is changed.
func unloadDrainedLaunchdService(ctx context.Context, target string, pid int) error {
	if target == "" {
		return nil
	}
	if err := validateLaunchdTarget(target); err != nil {
		return err
	}
	output, err := launchctl(ctx, "print", target)
	if err != nil {
		return err
	}
	currentPID, _ := strconv.Atoi(launchdValue(output, "pid"))
	if currentPID != pid {
		return errors.New("refusing to unload a replacement launchd process")
	}
	_, err = launchctl(ctx, "bootout", target)
	return err
}

func launchdStopped(ctx context.Context, process github.RuntimeState) (bool, error) {
	if process.LaunchdService == "" {
		return true, nil
	}
	if err := validateLaunchdTarget(process.LaunchdService); err != nil {
		return false, err
	}
	result, err := subprocess.RunFailClosed(ctx, subprocess.OSRunner{}, "/bin/launchctl", []string{"print", process.LaunchdService}, "", 15*time.Second, 256*1024, 4096)
	if err == nil {
		return false, nil
	}
	// Permission, timeout and inspection failures are not proof of shutdown.
	if ctx.Err() == nil && result.ExitCode == 113 && strings.Contains(result.Stderr, "Could not find service") {
		return true, nil
	}
	return false, fmt.Errorf("could not verify launchd shutdown: %w", err)
}
