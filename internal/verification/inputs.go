package verification

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/execution"
	"github.com/cortexium-io/runner/internal/securefs"
	"github.com/cortexium-io/runner/internal/subprocess"
	"github.com/cortexium-io/runner/internal/workspace"
)

// ObserveCandidate collects content-based applicability from reviewed explicit
// roots. It independently snapshots the full Git candidate before and after;
// excluding an evidence path from executable inputs never excludes it from
// candidate integrity. No historical/model digest is an input to this method.
func ObserveCandidate(ctx context.Context, directory, baseOID, approvedRequirements string, entry config.VerificationEntrypoint) (Observation, error) {
	var observation Observation
	decoded, err := hex.DecodeString(baseOID)
	if err != nil || len(decoded) != 20 && len(decoded) != 32 || approvedRequirements == "" || len(entry.InputPaths) == 0 {
		return observation, errors.New("verification needs exact base, approved requirements and explicit inputs")
	}
	allPaths := append(append([]string{}, entry.InputPaths...), entry.DependencyPaths...)
	for _, name := range allPaths {
		if err := validateInputPath(name); err != nil {
			return observation, err
		}
	}
	limits := workspace.DefaultSnapshotLimits()
	runner := subprocess.OSRunner{}
	before, err := workspace.CaptureCheckoutSnapshotStateWithLimits(ctx, runner, directory, 30*time.Second, limits)
	if err != nil {
		return observation, err
	}
	if !before.Clean {
		return observation, errors.New("supported verification requires a clean committed candidate")
	}
	executable, err := collectInputs(ctx, directory, entry.InputPaths, nil)
	if err != nil {
		return observation, err
	}
	dependencies, err := collectInputs(ctx, directory, entry.DependencyPaths, entry.DependencyExcludePaths)
	if err != nil {
		return observation, err
	}
	baseArgs := append([]string{"ls-tree", "-rz", "--full-tree", baseOID, "--"}, entry.InputPaths...)
	base, err := subprocess.RunGit(ctx, runner, baseArgs, directory, 30*time.Second)
	if err != nil || base.ExitCode != 0 {
		return observation, errors.New("cannot resolve selected verification base inputs")
	}
	environment, err := observeEnvironment(ctx, entry)
	if err != nil {
		return observation, err
	}
	after, err := workspace.CaptureCheckoutSnapshotStateWithLimits(ctx, runner, directory, 30*time.Second, limits)
	if err != nil {
		return observation, err
	}
	if before.Fingerprint != after.Fingerprint {
		return observation, errors.New("candidate changed while collecting verification inputs")
	}
	sort.Strings(allPaths)
	observation = Observation{CommitOID: before.Head, TreeOID: before.Tree, BaseOID: baseOID, Integrity: before.Fingerprint,
		Inputs: execution.VerificationInputs{
			Selection:    execution.VerificationInputSelection{Policy: "explicit-content-roots-v1", Paths: allPaths},
			Requirements: hash([]byte(approvedRequirements)), Executable: executable, Dependencies: dependencies,
			Configuration: strings.TrimPrefix(entry.Digest(), "v1:"), Environment: environment, Base: hash([]byte(base.Stdout)),
		},
	}
	return observation, nil
}

type inputEntry struct {
	Path           string `json:"path"`
	Kind           string `json:"kind"`
	Mode           uint32 `json:"mode,omitempty"`
	Digest         string `json:"digest,omitempty"`
	Target         string `json:"target,omitempty"`
	ResolvedTarget string `json:"resolved_target,omitempty"`
}

// Content hashes deliberately exclude inode/device identities: those identities
// guard each read, but a private QA copy of identical executable bytes remains
// applicable. Directory listings are pinned until their children are checked.
func collectInputs(ctx context.Context, root string, paths, excluded []string) (string, error) {
	root, err := securefs.AbsolutePath(root)
	if err != nil {
		return "", err
	}
	directory, err := securefs.OpenDir(root)
	if err != nil {
		return "", err
	}
	defer directory.Close()
	budget, err := securefs.NewSnapshotBudget(workspace.DefaultSnapshotLimits())
	if err != nil {
		return "", err
	}
	remaining := int64(workspace.DefaultSnapshotMaxTotalBytes)
	var entries []inputEntry
	exclusions := map[string]bool{}
	for _, name := range excluded {
		if err := validateInputPath(name); err != nil {
			return "", err
		}
		exclusions[name] = true
	}
	var visit func(*securefs.Directory, string, string) error
	visit = func(parent *securefs.Directory, parentPath, name string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		relative := path.Join(parentPath, name)
		if exclusions[relative] {
			return nil
		}
		if err := budget.AddEntry(relative); err != nil {
			return err
		}
		absolute := filepath.Join(root, filepath.FromSlash(relative))
		info, err := os.Lstat(absolute)
		if errors.Is(err, os.ErrNotExist) {
			entries = append(entries, inputEntry{Path: relative, Kind: "missing"})
			return parent.Verify()
		}
		if err != nil {
			return err
		}
		switch {
		case info.IsDir():
			child, err := parent.OpenDir(name)
			if err != nil {
				return err
			}
			defer child.Close()
			names, err := child.ReadDirNamesWithBudget(budget)
			if err != nil {
				return err
			}
			entries = append(entries, inputEntry{Path: relative, Kind: "directory", Mode: uint32(info.Mode().Perm())})
			for _, leaf := range names {
				if leaf == ".git" {
					return errors.New("Git administrative paths cannot be verification inputs")
				}
				if err := visit(child, relative, leaf); err != nil {
					return err
				}
			}
			return child.Verify()
		case info.Mode().IsRegular():
			limit := int64(workspace.DefaultSnapshotMaxFileBytes)
			if remaining < limit {
				limit = remaining
			}
			if limit <= 0 {
				return errors.New("verification input byte budget exceeded")
			}
			data, mode, state, err := parent.ReadFile(name, limit)
			if err != nil {
				return err
			}
			if !state.Exists {
				return errors.New("verification input disappeared")
			}
			remaining -= int64(len(data))
			entries = append(entries, inputEntry{Path: relative, Kind: "file", Mode: uint32(mode.Perm()), Digest: hash(data)})
			return parent.VerifyFile(name, state)
		case info.Mode()&os.ModeSymlink != 0:
			// Links are not traversed. Only targets already covered by selected
			// roots are allowed; an external package-store link needs an explicit
			// supported input policy rather than a misleading lockfile digest.
			target, err := os.Readlink(absolute)
			if err != nil {
				return err
			}
			lexicalTarget := target
			if !filepath.IsAbs(lexicalTarget) {
				lexicalTarget = filepath.Join(filepath.Dir(absolute), target)
			}
			lexicalRelative, err := filepath.Rel(root, lexicalTarget)
			if err != nil {
				return err
			}
			if !coveredInput(filepath.ToSlash(lexicalRelative), paths, exclusions) {
				return fmt.Errorf("verification symlink %s traverses unobserved inputs", relative)
			}
			resolved, err := filepath.EvalSymlinks(absolute)
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(root, resolved)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			if !coveredInput(rel, paths, exclusions) {
				return fmt.Errorf("verification symlink %s leaves the selected executable inputs", relative)
			}
			after, err := os.Lstat(absolute)
			if err != nil {
				return err
			}
			if !os.SameFile(info, after) || info.ModTime() != after.ModTime() {
				return errors.New("verification input link changed")
			}
			entries = append(entries, inputEntry{Path: relative, Kind: "symlink", Target: target, ResolvedTarget: rel})
			return parent.Verify()
		default:
			return fmt.Errorf("unsupported verification input type at %s", relative)
		}
	}
	for _, selected := range paths {
		if err := validateInputPath(selected); err != nil {
			return "", err
		}
		parts := strings.Split(selected, "/")
		parent := directory
		var opened []*securefs.Directory
		parentPath := ""
		missing := false
		for _, component := range parts[:len(parts)-1] {
			child, openErr := parent.OpenDir(component)
			if errors.Is(openErr, os.ErrNotExist) {
				entries = append(entries, inputEntry{Path: selected, Kind: "missing"})
				missing = true
				break
			}
			if openErr != nil {
				for _, d := range opened {
					_ = d.Close()
				}
				return "", openErr
			}
			opened = append(opened, child)
			parent = child
			parentPath = path.Join(parentPath, component)
		}
		if !missing {
			err = visit(parent, parentPath, parts[len(parts)-1])
		}
		for _, d := range opened {
			if err == nil {
				err = d.Verify()
			}
			_ = d.Close()
		}
		if err != nil {
			return "", err
		}
	}
	if err := directory.Verify(); err != nil {
		return "", err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	encoded, err := json.Marshal(entries)
	if err != nil {
		return "", err
	}
	return hash(encoded), nil
}

func coveredInput(name string, paths []string, excluded map[string]bool) bool {
	covered := false
	for _, selected := range paths {
		if name == selected || strings.HasPrefix(name, selected+"/") {
			covered = true
		}
	}
	for skip := range excluded {
		if name == skip || strings.HasPrefix(name, skip+"/") {
			return false
		}
	}
	return covered
}

func validateInputPath(name string) error {
	if name == "" || name == "." || path.IsAbs(name) || path.Clean(name) != name || strings.HasPrefix(name, "../") || name == ".." || strings.ContainsAny(name, "\\\x00\r\n") {
		return fmt.Errorf("invalid verification input path %q", name)
	}
	for _, component := range strings.Split(name, "/") {
		if component == ".git" {
			return errors.New("Git administration cannot be selected as executable input")
		}
	}
	return nil
}

func observeEnvironment(ctx context.Context, entry config.VerificationEntrypoint) (string, error) {
	if len(entry.ToolchainCommands) == 0 {
		return "", errors.New("verification toolchain identity is not configured")
	}
	var tools [][3]string
	for _, command := range append([]string{entry.Command}, entry.ToolchainCommands...) {
		if strings.ContainsAny(command, "/\\") && !filepath.IsAbs(command) {
			return "", errors.New("relative executable paths are not supported; use configured PATH names or absolute executables")
		}
		resolved, err := exec.LookPath(command)
		if err != nil {
			return "", err
		}
		resolved, err = filepath.EvalSymlinks(resolved)
		if err != nil {
			return "", err
		}
		data, _, state, err := securefs.ReadFile(resolved, 128*1024*1024)
		if err != nil {
			return "", err
		}
		if !state.Exists {
			return "", errors.New("verification toolchain executable disappeared")
		}
		tools = append(tools, [3]string{command, resolved, hash(data)})
	}
	var env []string
	for _, value := range os.Environ() {
		key, _, _ := strings.Cut(value, "=")
		switch key {
		case subprocess.OwnershipEnvironmentVariable, subprocess.HeavyOwnershipEnvironmentVariable, "PWD", "OLDPWD":
			continue
		}
		env = append(env, value)
	}
	sort.Strings(env)
	// Environment values, including credentials, are hashed in memory only;
	// neither receipt nor metrics receives the values themselves.
	platform, err := subprocess.RunFailClosed(ctx, subprocess.OSRunner{}, "uname", []string{"-srvm"}, "", 5*time.Second, 4096, 4096)
	if err != nil || platform.ExitCode != 0 {
		return "", errors.New("verification platform identity unavailable")
	}
	encoded, _ := json.Marshal([]any{runtime.GOOS, runtime.GOARCH, platform.Stdout, tools, env})
	return hash(encoded), nil
}
