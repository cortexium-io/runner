package setup

import (
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strings"

	"github.com/cortexium-io/runner/internal/config"
)

type ToolInstallStep struct {
	Tool        string `json:"tool"`
	Instruction string `json:"instruction"`
}

type PrerequisiteInstaller struct {
	lookPath func(string) (string, error)
}

func NewPrerequisiteInstaller() *PrerequisiteInstaller {
	return &PrerequisiteInstaller{lookPath: exec.LookPath}
}

func (i *PrerequisiteInstaller) Plan(tools []string) ([]ToolInstallStep, error) {
	wanted := map[string]bool{}
	for _, tool := range tools {
		tool = strings.TrimSpace(tool)
		switch tool {
		case "git", "gh", "codex", "claude", "pi":
			wanted[tool] = true
		case "":
		default:
			return nil, fmt.Errorf("manual prerequisite guidance does not allow unknown tool %q", tool)
		}
	}
	order := []string{"git", "gh", "codex", "claude", "pi"}
	steps := []ToolInstallStep{}
	for _, tool := range order {
		if !wanted[tool] {
			continue
		}
		if _, err := i.lookPath(tool); err == nil {
			continue
		}
		steps = append(steps, ToolInstallStep{Tool: tool, Instruction: manualInstallInstruction(tool)})
	}
	return steps, nil
}

func manualInstallInstruction(tool string) string {
	switch tool {
	case "git":
		return "Install Git from https://git-scm.com/downloads and ensure it is in PATH."
	case "gh":
		return "Install GitHub CLI from https://github.com/cli/cli#installation and ensure it is in PATH."
	case "codex":
		return "Install Codex CLI using OpenAI's supported installation method, then authenticate it with its native setup flow."
	default:
		return "Install " + tool + " using its official distribution, then rerun init."
	}
}

func RequiredToolsForHarnesses(harnesses []string) ([]string, error) {
	tools := map[string]bool{"git": true, "gh": true}
	for _, harness := range harnesses {
		switch strings.TrimSpace(harness) {
		case config.HarnessCodexCLI:
			tools["codex"] = true
		case config.HarnessClaudeCLI:
			tools["claude"] = true
		case config.HarnessPiCLI:
			tools["pi"] = true
		default:
			return nil, errors.New("unsupported harness " + harness)
		}
	}
	result := make([]string, 0, len(tools))
	for tool := range tools {
		result = append(result, tool)
	}
	sort.Strings(result)
	return result, nil
}
