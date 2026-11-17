package execution

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/cortexium-io/runner/internal/runner"
)

const harnessInstructions = `Complete this one role assignment against the original goal and acceptance criteria. Use the permitted harness capabilities when useful.
Runner owns delegation, dependencies and revisions: never spawn agents or start another Runner. Return the declared JSON result.
Text outputs contain the requested deliverable: complete writing when requested, or a concise evidence-rich handoff for source work. JSON outputs contain JSON encoded as a string. Artifact outputs name a regular file relative to output_dir, or forward one exact artifact ID already supplied in inputs or task_context results. Never invent artifact IDs.
Only declared artifact outputs and Runner's audit manifest are retained. Other files in output_dir and scratch_dir are removed after collection; do not link them or claim them as retained evidence. Return evidence needed later through a declared artifact output.
Explain consequential decisions and evidence in the deliverable; do not disclose private reasoning. Avoid repeating verification of unchanged inputs. After recurring failures, reassess the design rather than layering another special case.
The sandbox covers the project workspace: write_paths are cooperative ownership claims, not per-file security isolation. Preserve other tasks' edits and stay within your assigned paths. Skills and tools follow the explicit native profile. MCP side effects are governed by the harness, not its shell filesystem sandbox.`

const harnessTaskInstructions = `Use task_action=null when finished. Delegate or revise only through task_action; task inputs are [{name,kind,value}], with kind=text, json (JSON encoded as a string), or artifact (an ID already supplied by Runner). A control action has empty outputs.
Select result_inputs [{id,run_id}] from your supplied Results when a delegated role needs an existing settled result, including a failed/stopped check and its diagnostic artifact. These are read-only evidence, not successful prerequisites. depends_on remains success-only. Do not copy failure reports into briefs or invent references.
Task write_paths are literal paths relative to the product source, or "." for the whole project. Never use absolute paths or claim runtime/cache directories. Planning, writing without source edits, and read-only roles normally use []. Configured runtime permissions already exist and are not task write claims or permission requests.`

func objectSchema(properties map[string]any) map[string]any {
	required := make([]string, 0, len(properties))
	for name := range properties {
		required = append(required, name)
	}
	sort.Strings(required)
	return map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}
}

func harnessPayload(a runner.Assignment, options ...harnessPayloadOptions) (string, []byte, error) {
	p := a.Setup.Profile
	if p.Native == nil || p.Native.Validate() != nil || strings.TrimSpace(a.Task) == "" || p.Model == "" || p.MaxInput < 128 || p.MaxOutput < 128 {
		return "", nil, errors.New("incomplete native profile or task")
	}
	if !slices.Contains([]string{"input-only", "workspace-read", "workspace-write"}, p.Access) {
		return "", nil, errors.New("unsupported native access")
	}
	if p.Native.Harness == "claude" && p.Access == "workspace-write" {
		return "", nil, errors.New("Claude native executor currently supports input-only or read-only work")
	}
	if p.Native.Harness == "claude" && p.Access == "input-only" && p.Native.Tools.Mode != "none" {
		return "", nil, errors.New("input-only Claude cannot expose file or web tools; use workspace-read")
	}
	inputs := map[string]any{}
	evidenceFiles := harnessArtifactFiles(a)
	for name, v := range a.Inputs {
		if !validStagedInputName(name) || v.Validate() != nil {
			return "", nil, errors.New("invalid native input")
		}
		if v.ArtifactID != "" {
			inputs[name] = map[string]string{"artifact_id": v.ArtifactID, "path": evidenceFiles[v.ArtifactID]}
		} else {
			inputs[name] = json.RawMessage(v.Inline)
		}
	}
	scratch := filepath.Join(a.WorkDir, "harness", "tmp")
	var resultsFile *runner.NativeInputFile
	if len(options) > 0 {
		if options[0].Scratch != "" {
			scratch = options[0].Scratch
		}
		resultsFile = options[0].Results
	}
	instructions := harnessInstructions + "\n" + resultBudgetBase
	type taskContext struct {
		*runner.TaskContext
		BriefRef    string                  `json:"brief_ref,omitempty"`
		ResultsFile *runner.NativeInputFile `json:"results_file,omitempty"`
	}
	var context *taskContext
	task, taskRef := a.Task, ""
	var inputRefs map[string]string
	if a.TaskContext != nil {
		instructions += "\n" + harnessTaskInstructions
		context = &taskContext{TaskContext: runner.Clone(a.TaskContext), ResultsFile: runner.Clone(resultsFile)}
		if resultsFile != nil {
			context.ResultsFile.Path = harnessResultsReadPath(a, resultsFile)
			instructions += "\nTask result entries retain identities, reasons and exact artifact IDs. Read task_context.results_file for complete child/dependency outputs before judging them; complete text was moved, never truncated. The file is a read-only Results array with size and SHA256, and is not an artifact output ID."
		}
		if context.Goal != "" && context.Brief == context.Goal {
			context.Brief, context.BriefRef = "", "task_context.goal"
		}
		reference := func(text string) string {
			if text != "" && text == a.TaskContext.Goal {
				return "task_context.goal"
			}
			if text != "" && text == a.TaskContext.Brief {
				return "task_context.brief"
			}
			return ""
		}
		if taskRef = reference(task); taskRef != "" {
			task = ""
		}
		for name, value := range a.Inputs {
			var text string
			if value.ArtifactID == "" && json.Unmarshal(value.Inline, &text) == nil {
				if ref := reference(text); ref != "" {
					if inputRefs == nil {
						inputRefs = map[string]string{}
					}
					inputRefs[name] = ref
					delete(inputs, name)
				}
			}
		}
		if taskRef != "" || len(inputRefs) > 0 || context.BriefRef != "" {
			instructions += "\nA task_ref, brief_ref or input_references value names the path holding that complete text once. Resolve these references as the task, brief or named input; no value is missing or truncated."
		}
	}
	request := struct {
		WorkPolicy      string            `json:"work_policy,omitempty"`
		Task            string            `json:"task,omitempty"`
		TaskRef         string            `json:"task_ref,omitempty"`
		Inputs          map[string]any    `json:"inputs"`
		InputReferences map[string]string `json:"input_references,omitempty"`
		Context         *taskContext      `json:"task_context,omitempty"`
		MaxOutput       runner.Bytes      `json:"max_output_bytes"`
		OutputDir       string            `json:"output_dir"`
		ScratchDir      string            `json:"scratch_dir"`
		Evidence        map[string]string `json:"evidence_files,omitempty"`
		Instructions    string            `json:"instructions"`
	}{
		Task:            task,
		TaskRef:         taskRef,
		Inputs:          inputs,
		InputReferences: inputRefs,
		Context:         context,
		MaxOutput:       p.MaxOutput,
		OutputDir:       a.OutputDir,
		ScratchDir:      scratch,
		Evidence:        harnessArtifactFiles(a),
		Instructions:    instructions + "\n" + p.Instructions + "\nWrite retained files into output_dir; artifact output paths are relative to output_dir. Use scratch_dir for temporary checks. Source access and read-only verification are enforced separately from these two writable roots.",
	}
	if a.UnlimitedWork {
		request.WorkPolicy = unlimitedWorkPolicy
	}
	props := map[string]any{}
	for name, kind := range a.OutputTypes {
		if name == "patch" && p.Access == "workspace-write" && a.TaskContext == nil {
			continue // The owning source collector creates the patch.
		}
		if !slices.Contains([]string{"text", "json", "artifact"}, kind) {
			return "", nil, fmt.Errorf("unsupported native output type %q", kind)
		}
		props[name] = map[string]any{"type": "string", "description": "Declared " + kind + " output"}
	}
	outcomes := append([]string{"ready"}, a.Outcomes...)
	if a.AllowHelp {
		outcomes = append(outcomes, "needs_help")
	}
	slices.Sort(outcomes)
	outcomes = slices.Compact(outcomes)
	var outputSchema any = objectSchema(props)
	if len(props) > 0 && (a.AllowHelp || a.TaskContext != nil) {
		outputSchema = map[string]any{"anyOf": []any{outputSchema, objectSchema(map[string]any{})}}
	}
	resultProps := map[string]any{
		"outcome": map[string]any{"type": "string", "enum": outcomes},
		"outputs": outputSchema,
		"reason":  map[string]any{"type": "string"},
	}
	if a.TaskContext != nil {
		str := map[string]any{"type": "string"}
		stringsArray := map[string]any{"type": "array", "items": str}
		binding := objectSchema(map[string]any{
			"name":  str,
			"kind":  map[string]any{"type": "string", "enum": []string{"text", "json", "artifact"}},
			"value": str,
		})
		task := objectSchema(map[string]any{
			"id":            str,
			"role":          str,
			"brief":         str,
			"inputs":        map[string]any{"type": "array", "items": binding},
			"acceptance":    stringsArray,
			"depends_on":    stringsArray,
			"result_inputs": map[string]any{"type": "array", "items": objectSchema(map[string]any{"id": str, "run_id": str})},
			"write_paths":   stringsArray,
		})
		action := objectSchema(map[string]any{
			"kind":     map[string]any{"type": "string", "enum": []string{"delegate", "revise", "ask_human"}},
			"reason":   str,
			"tasks":    map[string]any{"type": "array", "items": task},
			"replace":  stringsArray,
			"question": str,
		})
		resultProps["task_action"] = map[string]any{"anyOf": []any{action, map[string]any{"type": "null"}}}
	}
	schema, _ := json.Marshal(objectSchema(resultProps))
	prompt, err := json.Marshal(request)
	if err != nil {
		return "", nil, err
	}
	if int64(len(prompt)+len(schema)) > int64(p.MaxInput) {
		return "", nil, errHarnessInputBudget
	}
	return string(prompt), schema, nil
}

// Refer to canonical retained evidence rather than copying every child artifact
// into every subsequent workspace. Core decides which IDs enter this context.
func harnessArtifactFiles(a runner.Assignment) map[string]string {
	files := map[string]string{}
	add := func(v runner.Value) {
		if v.ArtifactID != "" && fs.ValidPath(v.ArtifactID) && v.ArtifactID != "." {
			path := filepath.Join(filepath.Dir(a.OutputDir), filepath.FromSlash(v.ArtifactID))
			if a.Setup.Profile.Native != nil && a.Setup.Profile.Native.Harness == "claude" {
				root := filepath.Dir(filepath.Dir(a.OutputDir))
				path = filepath.Join(root, harnessControl(a.AttemptID), "evidence", fmt.Sprintf("%x", sha256.Sum256([]byte(v.ArtifactID))))
			}
			files[v.ArtifactID] = path
		}
	}
	for _, v := range a.Inputs {
		add(v)
	}
	if a.TaskContext != nil {
		for _, result := range a.TaskContext.Results {
			if result.DiagnosticArtifact != nil {
				add(runner.Value{ArtifactID: result.DiagnosticArtifact.ID})
			}
			for _, v := range result.Outputs {
				add(v)
			}
		}
	}
	return files
}

type harnessTaskInput struct {
	Name  string `json:"name"`
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

type harnessTask struct {
	ID           string                 `json:"id"`
	Role         string                 `json:"role"`
	Brief        string                 `json:"brief"`
	Inputs       []harnessTaskInput     `json:"inputs"`
	Acceptance   []string               `json:"acceptance"`
	DependsOn    []string               `json:"depends_on"`
	ResultInputs []runner.TaskResultRef `json:"result_inputs"`
	WritePaths   []string               `json:"write_paths"`
}

func nativeResultSizeReason(size int, limit runner.Bytes) string {
	return fmt.Sprintf("native final JSON is %d UTF-8 bytes; max_output_bytes=%d. Shrink the whole JSON including escaping, outputs, reason and task_action, or use declared artifact outputs for larger evidence", size, limit)
}

func harnessResult(a runner.Assignment, text []byte, work string, store func(string, []byte) error) (*runner.ExecutionEnd, error) {
	fail := func(reason string) (*runner.ExecutionEnd, error) {
		return &runner.ExecutionEnd{Kind: "failed", Reason: reason}, nil
	}
	if len(text) == 0 {
		return fail("native result is missing")
	}
	if int64(len(text)) > int64(a.Setup.Profile.MaxOutput) {
		return fail(nativeResultSizeReason(len(text), a.Setup.Profile.MaxOutput))
	}
	var wire struct {
		Outcome string            `json:"outcome"`
		Outputs map[string]string `json:"outputs"`
		Reason  string            `json:"reason"`
		Action  *struct {
			Kind     string        `json:"kind"`
			Reason   string        `json:"reason"`
			Tasks    []harnessTask `json:"tasks"`
			Replace  []string      `json:"replace"`
			Question string        `json:"question"`
		} `json:"task_action"`
	}
	dec := json.NewDecoder(bytes.NewReader(text))
	dec.DisallowUnknownFields()
	if dec.Decode(&wire) != nil || dec.Decode(new(any)) != io.EOF {
		return fail("native result is not the declared JSON object")
	}
	r := &runner.StepResult{Outcome: wire.Outcome, Outputs: map[string]runner.Value{}, Reason: wire.Reason}
	if wire.Action != nil {
		if a.TaskContext == nil || len(wire.Outputs) != 0 {
			return fail("native task action needs task context and empty outputs")
		}
		action := &runner.TaskAction{Kind: wire.Action.Kind, Reason: wire.Action.Reason, Replace: wire.Action.Replace, Question: wire.Action.Question}
		for _, t := range wire.Action.Tasks {
			child := runner.RoleTask{ID: t.ID, Role: t.Role, Brief: t.Brief, Inputs: map[string]runner.Value{}, Acceptance: t.Acceptance, DependsOn: t.DependsOn, ResultInputs: t.ResultInputs, WritePaths: t.WritePaths}
			for _, v := range t.Inputs {
				if _, exists := child.Inputs[v.Name]; exists {
					return fail("duplicate delegated input")
				}
				switch v.Kind {
				case "text":
					child.Inputs[v.Name] = runner.Text(v.Value)
				case "json":
					if !json.Valid([]byte(v.Value)) {
						return fail("delegated JSON input is invalid")
					}
					child.Inputs[v.Name] = runner.Value{Inline: json.RawMessage(v.Value)}
				case "artifact":
					child.Inputs[v.Name] = runner.Value{ArtifactID: v.Value}
				default:
					return fail("undeclared delegated input kind")
				}
			}
			action.Tasks = append(action.Tasks, child)
		}
		r.TaskAction = action
		return &runner.ExecutionEnd{Kind: "returned", Result: r}, nil
	}
	if wire.Outcome == "needs_help" && a.AllowHelp && wire.Reason != "" && len(wire.Outputs) == 0 {
		return &runner.ExecutionEnd{Kind: "returned", Result: r}, nil
	}
	if wire.Outcome != "ready" && !slices.Contains(a.Outcomes, wire.Outcome) {
		return fail("native result has an undeclared outcome")
	}
	contract := runner.Clone(a.OutputTypes)
	if a.Setup.Profile.Access == "workspace-write" && a.TaskContext == nil {
		delete(contract, "patch")
	}
	for name, value := range wire.Outputs {
		switch contract[name] {
		case "text":
			r.Outputs[name] = runner.Text(value)
		case "json":
			r.Outputs[name] = runner.Value{Inline: json.RawMessage(value)}
		case "artifact":
			if _, supplied := harnessArtifactFiles(a)[value]; supplied {
				// Forward one exact retained input/result reference. Core verifies
				// its owning project and task before accepting the handoff.
				r.Outputs[name] = runner.Value{ArtifactID: value}
				continue
			}
			if !validStagedInputName(name) || !filepath.IsLocal(value) {
				return fail("native artifact must be a relative owned file")
			}
			data, err := commandArtifact(work, value, a.Limits.MaxArtifact)
			if err != nil {
				return fail("native artifact: " + runner.Clip(err.Error()))
			}
			id := a.AttemptID + "/" + name
			if err = store(id, data); err != nil {
				return nil, err
			}
			r.Outputs[name] = runner.Value{ArtifactID: id}
		default:
			return fail("native result has an undeclared output")
		}
	}
	if err := runner.ValidateValues(r.Outputs, contract); err != nil {
		return fail(runner.Clip(err.Error()))
	}
	return &runner.ExecutionEnd{Kind: "returned", Result: r}, nil
}
