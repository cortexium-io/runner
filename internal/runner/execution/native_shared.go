package execution

import (
	"bytes"
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
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
)

func sharedCommand(a runner.Assignment) bool {
	return a.Setup.Profile.Harness == "command" && a.Setup.Profile.Mode == "shared"
}

func sourceSharedCommand(a runner.Assignment) bool {
	return sharedCommand(a) && a.TaskContext == nil
}

func sharedAssignment(a runner.Assignment) (runner.Assignment, error) {
	p := a.Setup.Profile
	if p.Command == nil || len(p.Command.Args) == 0 || p.Command.Directory != "" || (p.Access != "workspace-read" && p.Access != "workspace-write") {
		return a, errors.New("shared command needs configured argv and explicit workspace access")
	}
	if sourceSharedCommand(a) && (p.Access != "workspace-read" || a.Source == nil || a.Source.Ref == "" || !filepath.IsAbs(a.Repository) || a.Inputs["patch"].ArtifactID == "" || a.Inputs["patch"].Validate() != nil) {
		return a, errors.New("source-owned shared command needs read-only access, explicit source and its candidate patch")
	}
	if p.Access == "workspace-write" && (a.TaskContext == nil || len(a.TaskContext.WritePaths) == 0) {
		return a, errors.New("writable shared command needs declared task write paths")
	}
	values := append([]string{}, p.Command.Args...)
	for _, value := range p.Command.Env {
		values = append(values, value)
	}
	selected := map[string]runner.RoleTaskResult{}
	needsGoal := false
	for _, value := range values {
		if sourceSharedCommand(a) && (strings.Contains(value, "{goal}") || strings.Contains(value, "{dependency.")) {
			return a, errors.New("goal and dependency command bindings require general task ownership")
		}
		if strings.Contains(value, "{goal}") {
			if value != "{goal}" {
				return a, errors.New("goal file binding must occupy one whole argument or environment value")
			}
			needsGoal = true
		}
		name, err := runner.CommandArtifactInput(value)
		if err != nil {
			return a, err
		}
		if name != "" {
			input := a.Inputs[name]
			if input.ArtifactID == "" || input.Validate() != nil {
				return a, errors.New("command artifact binding lacks its declared retained input")
			}
		}
		task, output, err := runner.CommandDependencyInput(value)
		if err != nil {
			return a, err
		}
		if task != "" {
			result, retained, err := sharedDependency(a, task, output)
			if err != nil {
				return a, err
			}
			entry, exists := selected[task]
			if !exists {
				entry = runner.RoleTaskResult{ID: result.ID, RunID: result.RunID, Role: result.Role, Status: result.Status, Outputs: map[string]runner.Value{}}
			}
			entry.Outputs[output] = retained
			selected[task] = entry
		}
	}
	if err := runner.ValidateRuntimeWritePaths(p.Command.RuntimeWritePaths); err != nil {
		return a, err
	}
	if sourceSharedCommand(a) {
		return a, nil // Source/candidate identity is authenticated during owned preparation.
	}
	// A fixed command does not consume a model context/catalog. Keep its frozen
	// ownership and purpose without repeating every child's report in its job.
	g := a.TaskContext
	a.TaskContext = &runner.TaskContext{RootRunID: g.RootRunID, ParentRunID: g.ParentRunID, TaskID: g.TaskID, Brief: runner.Clip(g.Brief), WritePaths: runner.Clone(g.WritePaths)}
	if needsGoal {
		if g.Goal == "" || int64(len(g.Goal)) > int64(a.Limits.MaxRecord) {
			return a, errors.New("goal file binding needs the complete authoritative goal within its record limit")
		}
		a.TaskContext.Goal = g.Goal
	}
	ids := make([]string, 0, len(selected))
	for id := range selected {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		a.TaskContext.Results = append(a.TaskContext.Results, selected[id])
	}
	return a, nil
}

func sharedDependency(a runner.Assignment, task, output string) (runner.RoleTaskResult, runner.Value, error) {
	var selected *runner.RoleTaskResult
	for i := range a.TaskContext.Results {
		result := &a.TaskContext.Results[i]
		if result.ID != task {
			continue
		}
		if selected != nil {
			return runner.RoleTaskResult{}, runner.Value{}, errors.New("configured command dependency ID is ambiguous")
		}
		selected = result
	}
	if selected == nil || selected.Status != "Completed" {
		return runner.RoleTaskResult{}, runner.Value{}, errors.New("configured command requires its completed task result")
	}
	value := selected.Outputs[output]
	if value.ArtifactID == "" || value.Validate() != nil {
		return runner.RoleTaskResult{}, runner.Value{}, errors.New("configured command dependency output is not a retained artifact")
	}
	return *selected, value, nil
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
	if value == "{goal}" {
		return sharedGoalPath(spec.Assignment, spec.Control)
	}
	if name, err := runner.CommandArtifactInput(value); err == nil && name != "" {
		return harnessArtifactFiles(spec.Assignment)[spec.Assignment.Inputs[name].ArtifactID]
	}
	if task, output, err := runner.CommandDependencyInput(value); err == nil && task != "" {
		if _, retained, err := sharedDependency(spec.Assignment, task, output); err == nil {
			return harnessArtifactFiles(spec.Assignment)[retained.ArtifactID]
		}
	}
	return strings.NewReplacer("{workspace}", spec.Directory, "{source}", spec.Directory, "{scratch}", spec.ScratchDir).Replace(value)
}

// A fixed command can read the immutable root goal without having a model copy
// it into another task input or select a caller-owned filesystem path.
func sharedGoalPath(a runner.Assignment, control string) string {
	command := a.Setup.Profile.Command
	if !sharedCommand(a) || command == nil {
		return ""
	}
	for _, value := range command.Args {
		if value == "{goal}" {
			return filepath.Join(control, "goal.txt")
		}
	}
	for _, value := range command.Env {
		if value == "{goal}" {
			return filepath.Join(control, "goal.txt")
		}
	}
	return ""
}

func (n *Native) stageSharedGoal(a runner.Assignment, control string) error {
	path := sharedGoalPath(a, filepath.Join(n.disk.Root, control))
	if path == "" {
		return nil
	}
	if a.TaskContext == nil || a.TaskContext.Goal == "" {
		return errors.New("shared goal binding lost its authoritative task goal")
	}
	name := control + "/goal.txt"
	data, err := n.disk.Read(name, n.disk.Policy.Database.MaxRecord)
	if errors.Is(err, os.ErrNotExist) {
		if err = n.disk.Write(name, []byte(a.TaskContext.Goal), n.disk.Policy.Database.MaxRecord); err != nil {
			return err
		}
	} else if err != nil || !bytes.Equal(data, []byte(a.TaskContext.Goal)) {
		return errors.New("staged shared goal differs from its frozen assignment")
	}
	return os.Chmod(path, 0400)
}

func validateSharedGoal(spec harnessSpec) error {
	path := sharedGoalPath(spec.Assignment, spec.Control)
	if path == "" {
		return nil
	}
	goal := spec.Assignment.TaskContext
	info, err := os.Lstat(path)
	if goal == nil || goal.Goal == "" || err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0222 != 0 || info.Size() != int64(len(goal.Goal)) || info.Size() > int64(spec.Storage.Database.MaxRecord) {
		return errors.New("shared goal input identity, permissions or bound differs")
	}
	data, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(data, []byte(goal.Goal)) {
		return errors.New("shared goal input changed from its frozen assignment")
	}
	return nil
}

func sharedProcess(ctx context.Context, spec harnessSpec) (*exec.Cmd, error) {
	if sourceSharedCommand(spec.Assignment) && spec.Directory != filepath.Join(spec.Root, "work", spec.Assignment.RunID, "source") {
		return nil, errors.New("source-owned command directory differs from its owned workspace")
	}
	if err := validateSharedGoal(spec); err != nil {
		return nil, err
	}
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
	if spec.Assignment.Setup.Profile.Access == "workspace-write" {
		writes = append(writes, spec.Directory)
	}
	var exceptions []string
	for _, path := range writes {
		exceptions = append(exceptions, "(require-not (subpath "+strconv.Quote(path)+"))")
	}
	exceptions = append(exceptions, `(require-not (literal "/dev/null"))`, `(require-not (subpath "/dev/fd"))`)
	policy := "(version 1)\n(allow default)\n(deny file-write* (require-all " + strings.Join(exceptions, " ") + "))\n"
	readExceptions := []string{"(subpath " + strconv.Quote(spec.Root) + ")", "(require-not (subpath " + strconv.Quote(spec.Assignment.OutputDir) + "))", "(require-not (subpath " + strconv.Quote(filepath.Join(spec.Control, "tmp")) + "))"}
	if sourceSharedCommand(spec.Assignment) && spec.Directory == filepath.Join(spec.Root, "work", spec.Assignment.RunID, "source") {
		readExceptions = append(readExceptions, "(require-not (subpath "+strconv.Quote(spec.Directory)+"))")
	}
	for _, path := range harnessArtifactFiles(spec.Assignment) {
		readExceptions = append(readExceptions, "(require-not (literal "+strconv.Quote(path)+"))")
	}
	if path := sharedGoalPath(spec.Assignment, spec.Control); path != "" {
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
		Harness: "command/shared", Binary: binary, Access: a.Setup.Profile.Access,
		UsageScope:    "none",
		StartedAt:     time.Now().UTC(),
		Permitted:     runner.NativeProfile{Harness: "command/shared", Tools: runner.CapabilitySelection{Mode: "selected", Names: []string{filepath.Base(binary)}}, Skills: runner.CapabilitySelection{Mode: "none"}, Network: a.Setup.Profile.Command.Network, RuntimeWritePaths: runner.Clone(a.Setup.Profile.Command.RuntimeWritePaths)},
		RequestDigest: hex.EncodeToString(digest[:]), CleanupAssurance: "cooperative-known-processes",
		Gaps: []string{"Configured argv only; no model or native harness account is invoked.", "The sandbox permits host process/browser IPC and host reads outside coordinator state. Project writes require explicit workspace-write access and task write claims; the enforced filesystem boundary is the project, as for native writers. Only owned temporary/output, configured runtime roots and that explicitly writable project are writable.", "Free-space monitoring is a soft recovery reserve, not an OS disk quota.", "CLI subprocess cleanup follows an inherited ownership marker and stable process identities. Deliberately clearing ownership before spawning is unsupported.", "Darwin can hide system-process environments; a fast detached fork through an unseen exited parent may be unobservable. Unrelated ambiguous host processes are preserved."},
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
	result, _ := json.Marshal(commandResult{AttemptID: a.AttemptID, Kind: "returned", ExitCode: code, Diagnostic: runner.DiagnosticTail(string(diagnostic))})
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
