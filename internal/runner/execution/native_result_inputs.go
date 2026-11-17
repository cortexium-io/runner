package execution

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"

	"github.com/cortexium-io/runner/internal/runner"
)

type harnessPayloadOptions struct {
	Scratch            string
	Results            *runner.NativeInputFile
	RequestByReference bool
}

var errHarnessInputBudget = errors.New("native instructions, task context and inputs exceed max_input; nothing was truncated")

// Move only an oversized result aggregate to one private immutable file. Keep
// exact artifact IDs in the assignment for normal output-authority validation.
func planHarnessAssignment(a runner.Assignment, control string, assignmentLimit runner.Bytes) (runner.Assignment, *runner.NativeInputFile, []byte, error) {
	options := harnessPayloadOptions{Scratch: filepath.Join(control, "tmp")}
	_, _, payloadErr := harnessPayload(a, options)
	if payloadErr != nil && !errors.Is(payloadErr, errHarnessInputBudget) {
		return a, nil, nil, payloadErr
	}
	data, err := json.Marshal(a)
	if err != nil {
		return a, nil, nil, err
	}
	if payloadErr == nil && int64(len(data)) <= int64(assignmentLimit) {
		return a, nil, nil, nil
	}
	if a.TaskContext == nil || len(a.TaskContext.Results) == 0 {
		return a, nil, nil, errHarnessInputBudget
	}
	p := a.Setup.Profile.Native
	if p.Harness == "claude" && (p.Tools.Mode != "selected" || !slices.Contains(p.Tools.Names, "Read")) {
		return a, nil, nil, errors.New("oversized complete task results need a file-reading capability; this Claude profile has no Read tool")
	}
	results, err := json.Marshal(a.TaskContext.Results)
	if err != nil {
		return a, nil, nil, err
	}
	if int64(len(results)) > int64(a.Limits.MaxArtifact) {
		return a, nil, nil, errors.New("complete task results exceed max_artifact; nothing was truncated")
	}
	a = runner.Clone(a)
	for i := range a.TaskContext.Results {
		result := &a.TaskContext.Results[i]
		result.Diagnostic = ""
		for name, value := range result.Outputs {
			if value.ArtifactID == "" {
				delete(result.Outputs, name)
			}
		}
	}
	digest := sha256.Sum256(results)
	file := &runner.NativeInputFile{
		Path: filepath.Join(control, "results.json"), SHA256: hex.EncodeToString(digest[:]),
		Bytes: int64(len(results)), Items: len(a.TaskContext.Results),
	}
	options.Results = file
	if _, _, err = harnessPayload(a, options); err != nil {
		return a, nil, nil, err
	}
	return a, file, results, nil
}

func harnessResultsReadPath(a runner.Assignment, file *runner.NativeInputFile) string {
	if a.Setup.Profile.Native != nil && a.Setup.Profile.Native.Harness == "claude" {
		return filepath.Join(filepath.Dir(file.Path), "evidence", "results.json")
	}
	return file.Path
}

func validateHarnessResults(a runner.Assignment, control string, file *runner.NativeInputFile) error {
	if file == nil {
		return nil
	}
	if file.Path != filepath.Join(control, "results.json") || file.Bytes < 1 || file.Bytes > int64(a.Limits.MaxArtifact) || file.Items < 1 || len(file.SHA256) != 64 || a.TaskContext == nil || len(a.TaskContext.Results) != file.Items {
		return errors.New("native task-results input identity or bound differs")
	}
	info, err := os.Lstat(file.Path)
	if err != nil || !info.Mode().IsRegular() || info.Size() != file.Bytes {
		return errors.New("native complete task-results file is absent or changed")
	}
	f, err := os.Open(file.Path)
	if err != nil {
		return err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, file.Bytes+1))
	if err != nil || int64(len(data)) != file.Bytes {
		return errors.New("native complete task-results file size differs")
	}
	digest := sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) != file.SHA256 {
		return errors.New("native complete task-results content digest differs")
	}
	var results []runner.RoleTaskResult
	if err := json.Unmarshal(data, &results); err != nil || len(results) != file.Items {
		return errors.New("native complete task-results array differs")
	}
	// The file may contain complete text, but cannot introduce artifact IDs
	// omitted from the compact, Core-authorized assignment.
	allowed := harnessArtifactFiles(a)
	for i, result := range results {
		if !reflect.DeepEqual(result.DiagnosticArtifact, a.TaskContext.Results[i].DiagnosticArtifact) {
			return errors.New("native task-results diagnostic artifact authority differs")
		}
		if result.Diagnostic != runner.DiagnosticTail(result.Diagnostic) || a.TaskContext.Results[i].Diagnostic != "" {
			return errors.New("native task-results diagnostic bound or compact context differs")
		}
		for _, value := range result.Outputs {
			if value.ArtifactID != "" && allowed[value.ArtifactID] == "" {
				return errors.New("native task-results artifact authority differs")
			}
		}
	}
	return nil
}
