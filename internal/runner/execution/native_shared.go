package execution

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
)

func sharedCommand(a runner.Assignment) bool {
	return a.Setup.Profile.Harness == "command" && a.Setup.Profile.Mode == "shared"
}

func sharedAssignment(a runner.Assignment) (runner.Assignment, error) {
	p := a.Setup.Profile
	if p.Command == nil || len(p.Command.Args) == 0 || p.Command.Directory != "" || p.Access != "workspace-read" || a.TaskContext == nil {
		return a, errors.New("shared command needs configured argv and a general read-only workspace")
	}
	if err := runner.ValidateRuntimeWritePaths(p.Command.RuntimeWritePaths); err != nil {
		return a, err
	}
	// A fixed command does not consume a model context/catalog. Keep its frozen
	// ownership and purpose without repeating every child's report in its job.
	g := a.TaskContext
	a.TaskContext = &runner.TaskContext{RootRunID: g.RootRunID, ParentRunID: g.ParentRunID, TaskID: g.TaskID, Brief: runner.Clip(g.Brief), WritePaths: runner.Clone(g.WritePaths)}
	return a, nil
}

func executionRuntimePaths(p runner.Profile) []string {
	if p.Native != nil {
		return p.Native.RuntimeWritePaths
	}
	if p.Command != nil {
		return p.Command.RuntimeWritePaths
	}
	return nil
}

func sharedValue(value string, spec harnessSpec) string {
	return strings.NewReplacer("{workspace}", spec.Directory, "{source}", spec.Directory, "{scratch}", spec.ScratchDir).Replace(value)
}

func sharedProcess(ctx context.Context, spec harnessSpec) (*exec.Cmd, error) {
	args := runner.Clone(spec.Assignment.Setup.Profile.Command.Args)
	for i := range args {
		args[i] = sharedValue(args[i], spec)
	}
	args[0] = spec.Binary
	var cmd *exec.Cmd
	if runtime.GOOS == "darwin" {
		policy := sharedSeatbeltPolicy(spec)
		file := filepath.Join(spec.Control, "shared.sbpl")
		if err := workerWrite(file, []byte(policy)); err != nil {
			return nil, err
		}
		cmd = exec.CommandContext(ctx, "/usr/bin/sandbox-exec", append([]string{"-f", file, "--"}, args...)...)
	} else if runtime.GOOS == "linux" {
		binary, err := exec.LookPath("codex")
		if err != nil {
			return nil, err
		}
		copy := spec
		copy.Assignment.Setup.Profile.Native = &runner.NativeProfile{Network: spec.Assignment.Setup.Profile.Command.Network, RuntimeWritePaths: spec.Assignment.Setup.Profile.Command.RuntimeWritePaths}
		profile := []string{"-c", "permissions.runner-shared.filesystem=" + harnessFilesystem(copy), "-c", "permissions.runner-shared.network.enabled=" + strconv.FormatBool(spec.Assignment.Setup.Profile.Command.Network), "sandbox", "-P", "runner-shared", "-C", spec.Directory, "--"}
		cmd = exec.CommandContext(ctx, binary, append(profile, args...)...)
	} else {
		return nil, errors.New("shared command sandbox is unsupported on this platform")
	}
	cmd.Dir = spec.Directory
	cmd.Env = os.Environ()
	for key, value := range spec.Assignment.Setup.Profile.Command.Env {
		cmd.Env = append(cmd.Env, key+"="+sharedValue(value, spec))
	}
	cmd.Env = append(cmd.Env,
		"TMPDIR="+spec.ScratchDir,
		"MAC_CHROMIUM_TMPDIR="+spec.ScratchDir,
		"CFFIXED_USER_HOME="+filepath.Join(spec.ScratchDir, "browser-home"),
		nativeOwnerVariable+"="+spec.Owner,
	)
	return cmd, nil
}

// A configured process may need macOS browser IPC that the Codex shell profile
// intentionally excludes. Keep filesystem writes narrowly granted; this is not
// an unsandboxed command or a widening of the native AI shell's permissions.
func sharedSeatbeltPolicy(spec harnessSpec) string {
	writes := append([]string{spec.ScratchDir, filepath.Join(spec.Control, "tmp"), spec.Assignment.OutputDir}, spec.Assignment.Setup.Profile.Command.RuntimeWritePaths...)
	var exceptions []string
	for _, path := range writes {
		exceptions = append(exceptions, "(require-not (subpath "+strconv.Quote(path)+"))")
	}
	exceptions = append(exceptions, `(require-not (literal "/dev/null"))`, `(require-not (subpath "/dev/fd"))`)
	policy := "(version 1)\n(allow default)\n(deny file-write* (require-all " + strings.Join(exceptions, " ") + "))\n"
	readExceptions := []string{"(subpath " + strconv.Quote(spec.Root) + ")", "(require-not (subpath " + strconv.Quote(spec.Assignment.OutputDir) + "))", "(require-not (subpath " + strconv.Quote(filepath.Join(spec.Control, "tmp")) + "))"}
	for _, path := range harnessArtifactFiles(spec.Assignment) {
		readExceptions = append(readExceptions, "(require-not (literal "+strconv.Quote(path)+"))")
	}
	// SQLite's Go VFS inspects ancestor metadata while resolving its own tmp
	// path. Keep private contents/listing denied while permitting traversal.
	policy += "(deny file-read-data (require-all " + strings.Join(readExceptions, " ") + "))\n"
	policy += "(deny file-write* (literal " + strconv.Quote(filepath.Join(spec.Assignment.OutputDir, "provenance.json")) + "))\n"
	if !spec.Assignment.Setup.Profile.Command.Network {
		policy += "(deny network*)\n"
	}
	return policy
}

func sharedProvenance(a runner.Assignment, binary string) runner.NativeProvenance {
	host, _ := os.Hostname()
	request, _ := json.Marshal(a)
	digest := sha256.Sum256(request)
	return runner.NativeProvenance{
		Machine: host, Platform: runtime.GOOS + "/" + runtime.GOARCH,
		Harness: "command/shared", Binary: binary, Access: "workspace-read",
		StartedAt:     time.Now().UTC(),
		Permitted:     runner.NativeProfile{Harness: "command/shared", Tools: runner.CapabilitySelection{Mode: "selected", Names: []string{filepath.Base(binary)}}, Skills: runner.CapabilitySelection{Mode: "none"}, Network: a.Setup.Profile.Command.Network, RuntimeWritePaths: runner.Clone(a.Setup.Profile.Command.RuntimeWritePaths)},
		RequestDigest: hex.EncodeToString(digest[:]), CleanupAssurance: "cooperative-known-processes",
		Gaps: []string{"Configured argv only; no model or native harness account is invoked.", "The shared-command sandbox permits host process/browser IPC and host reads outside coordinator state while denying source writes. Only owned managed temporary/output and configured runtime roots are writable.", "Free-space monitoring is a soft recovery reserve, not an OS disk quota.", "CLI subprocess cleanup follows an inherited ownership marker and stable process identities. Deliberately clearing ownership before spawning is unsupported.", "Darwin can hide system-process environments; a fast detached fork through an unseen exited parent may be unobservable. Unrelated ambiguous host processes are preserved."},
	}
}

func sharedCommandFinal(a runner.Assignment, runErr error, diagnostic []byte) []byte {
	code := 0
	if runErr != nil {
		code = 1
		var exit *exec.ExitError
		if errors.As(runErr, &exit) {
			code = exit.ExitCode()
		}
	}
	result, _ := json.Marshal(commandResult{AttemptID: a.AttemptID, Kind: "returned", ExitCode: code, Diagnostic: runner.Clip(string(diagnostic))})
	return result
}

func sharedCommandEvents(a runner.Assignment, events harnessEvents, runErr error, diagnostic []byte) harnessEvents {
	events.Final = sharedCommandFinal(a, runErr, diagnostic)
	zero := int64(0)
	events.Usage = runner.Usage{InputTokens: &zero, OutputTokens: &zero, CachedInputTokens: &zero}
	return events
}

func removeSharedScratch(path string) error {
	if path == "" {
		return nil
	}
	if !filepath.IsAbs(path) || !strings.HasPrefix(filepath.Base(path), "runner-shared-") {
		return fmt.Errorf("shared scratch ownership differs")
	}
	return os.RemoveAll(path)
}
