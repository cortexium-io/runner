package execution

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
)

type nativePayloadRequest struct {
	Task                  string         `json:"task"`
	Inputs                map[string]any `json:"inputs"`
	DeadlineUTC           string         `json:"deadline_utc,omitempty"`
	RemainingSeconds      *int64         `json:"remaining_seconds,omitempty"`
	MaxOutputBytes        runner.Bytes   `json:"max_output_bytes"`
	InputFileKeys         []string       `json:"input_file_keys,omitempty"`
	InputFileInstructions string         `json:"input_file_instructions,omitempty"`
}

type nativeInputFile struct {
	Path     string `json:"runner_input_file"`
	Encoding string `json:"encoding"`
	JSONType string `json:"json_type"`
}

const nativeInputFileInstructions = "Only inputs named in input_file_keys refer to complete typed JSON in read-only files. Read their runner_input_file paths and decode each JSON value before using it. Other input values keep their existing meaning. No input evidence was truncated."

// The plan depends only on the saved assignment, including its original deadline.
// Reserve the widest remaining-seconds representation so observing or releasing
// the same attempt cannot change which files it owns as time passes.
func renderNativeInputs(a runner.Assignment, request nativePayloadRequest, overhead int) ([]byte, map[string][]byte, error) {
	tooLarge := errors.New("resolved input/instructions/schema exceed max_input")
	if a.Setup.Profile.Access == "input-only" || a.Limits.MaxArtifact <= 0 {
		data, _ := json.Marshal(request)
		if int64(len(data)+overhead) > int64(a.Setup.Profile.MaxInput) {
			return nil, nil, tooLarge
		}
		return data, nil, nil
	}
	fits := func() bool {
		budget := request
		if budget.RemainingSeconds != nil {
			// time.Time.Sub saturates at the duration bound before seconds
			// are derived in nativePayloadPlan.
			widest := int64(time.Duration(1<<63-1) / time.Second)
			budget.RemainingSeconds = &widest
		}
		data, _ := json.Marshal(budget)
		return int64(len(data)+overhead) <= int64(a.Setup.Profile.MaxInput)
	}
	if fits() {
		data, _ := json.Marshal(request)
		return data, nil, nil
	}
	type candidate struct {
		name string
		file nativeInputFile
		gain int
	}
	var candidates []candidate
	for name, value := range a.Inputs {
		if value.ArtifactID != "" || int64(len(value.Inline)) > int64(a.Limits.MaxArtifact) {
			continue
		}
		if !validStagedInputName(name) || !validStagedInputName(a.AttemptID) {
			return nil, nil, errors.New("invalid staged inline input identity")
		}
		file := nativeInputFile{
			Path:     filepath.Join(".runner-tmp", "inputs", a.AttemptID, name),
			Encoding: "json",
			JSONType: nativeJSONType(value.Inline),
		}
		original, _ := json.Marshal(request.Inputs[name])
		reference, _ := json.Marshal(file)
		key, _ := json.Marshal(name)
		if gain := len(original) - len(reference) - len(key) - 1; gain > 0 {
			candidates = append(candidates, candidate{name, file, gain})
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].gain != candidates[j].gain {
			return candidates[i].gain > candidates[j].gain
		}
		return candidates[i].name < candidates[j].name
	})
	files := map[string][]byte{}
	request.InputFileInstructions = nativeInputFileInstructions
	for _, c := range candidates {
		request.Inputs[c.name] = c.file
		request.InputFileKeys = append(request.InputFileKeys, c.name)
		sort.Strings(request.InputFileKeys)
		files[c.name] = a.Inputs[c.name].Inline
		if fits() {
			data, _ := json.Marshal(request)
			return data, files, nil
		}
	}
	return nil, nil, tooLarge
}

func validStagedInputName(name string) bool {
	return name != "." && fs.ValidPath(name) && !strings.Contains(name, "/")
}

func nativeJSONType(data []byte) string {
	switch bytes.TrimSpace(data)[0] {
	case '"':
		return "string"
	case '{':
		return "object"
	case '[':
		return "array"
	case 't', 'f':
		return "boolean"
	case 'n':
		return "null"
	default:
		return "number"
	}
}
