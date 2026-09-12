package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/engine"
)

const maxSavedPlanBytes = 2 * 1024 * 1024

// The generated plan remains the engine's existing representation. This JSON
// envelope carries the context needed to replay staging, never approval.
type projectPlanDocument struct {
	engine.ProjectPlan
	SourceContext string            `json:"source_context"`
	Target        projectPlanTarget `json:"target"`
}

type projectPlanTarget struct {
	Owner       string `json:"owner"`
	Number      int    `json:"number"`
	Repository  string `json:"repository"`
	BaseBranch  string `json:"base_branch"`
	Destination string `json:"destination"`
}

func newProjectPlanDocument(cfg config.Config, plan engine.ProjectPlan) projectPlanDocument {
	project := cfg.ResolveProject()
	workflow := cfg.EffectiveWorkflow()
	planner := workflow.Lanes[workflow.PlanLane]
	return projectPlanDocument{
		ProjectPlan: plan, SourceContext: plan.SourceContext,
		Target: projectPlanTarget{
			Owner: project.Owner, Number: project.Number, Repository: project.IntakeRepository,
			BaseBranch: project.BaseBranch, Destination: workflow.Lanes[planner.CreatesIn].Name,
		},
	}
}

func readProjectPlanFile(path string, cfg config.Config) (engine.ProjectPlan, error) {
	info, err := os.Stat(path)
	if err != nil {
		return engine.ProjectPlan{}, fmt.Errorf("inspect saved plan: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > maxSavedPlanBytes {
		return engine.ProjectPlan{}, errors.New("saved plan must be a regular JSON file no larger than 2 MiB")
	}
	file, err := os.Open(path)
	if err != nil {
		return engine.ProjectPlan{}, fmt.Errorf("open saved plan: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxSavedPlanBytes+1))
	if err != nil {
		return engine.ProjectPlan{}, fmt.Errorf("read saved plan: %w", err)
	}
	if len(data) > maxSavedPlanBytes {
		return engine.ProjectPlan{}, errors.New("saved plan exceeds 2 MiB")
	}
	// Accept either preview JSON or the plan inside a staging result/error.
	// Receipt fields are discarded, not imported into the approval path.
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(data, &envelope); err != nil {
		return engine.ProjectPlan{}, fmt.Errorf("decode saved plan: %w", err)
	}
	if plan, wrapped := envelope["plan"]; wrapped {
		for key := range envelope {
			switch key {
			case "plan", "staged", "released", "error":
			default:
				return engine.ProjectPlan{}, fmt.Errorf("saved plan result contains unknown field %q", key)
			}
		}
		data = plan
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var document projectPlanDocument
	if err := decoder.Decode(&document); err != nil {
		return engine.ProjectPlan{}, fmt.Errorf("decode saved plan: %w", err)
	}
	if document.Target != newProjectPlanDocument(cfg, engine.ProjectPlan{}).Target {
		return engine.ProjectPlan{}, errors.New("saved plan target is missing or does not match the configured Project, repository, base branch and destination; use the original config or generate a new plan for this target")
	}
	if strings.TrimSpace(document.SourceContext) == "" {
		return engine.ProjectPlan{}, errors.New("saved plan is missing source_context; retain JSON from a planning command that includes the original request")
	}
	document.ProjectPlan.SourceContext = document.SourceContext
	return document.ProjectPlan, nil
}
