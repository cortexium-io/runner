package setup

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/cortexium-io/runner/internal/config"
)

type HarnessDescriptor struct {
	Kind        string
	DisplayName string
	Command     string
	VersionArgs []string
	SkillRoot   string
}

// AvailableHarnesses reports supported harness executables currently available
// on PATH. It does not inspect credentials or install state.
func AvailableHarnesses() []HarnessDescriptor {
	available := []HarnessDescriptor{}
	for _, descriptor := range defaultHarnessDescriptors("", nil) {
		if _, err := exec.LookPath(descriptor.Command); err == nil {
			available = append(available, descriptor)
		}
	}
	return available
}

func HarnessCommand(kind string) (string, bool) {
	for _, descriptor := range defaultHarnessDescriptors("", nil) {
		if descriptor.Kind == strings.TrimSpace(kind) {
			return descriptor.Command, true
		}
	}
	return "", false
}

func defaultHarnessDescriptors(home string, configured []config.HarnessConfig) []HarnessDescriptor {
	descriptors := []HarnessDescriptor{
		{Kind: config.HarnessCodexCLI, DisplayName: "Codex CLI", Command: "codex", VersionArgs: []string{"--version"}, SkillRoot: filepath.Join(home, ".codex", "skills")},
		{Kind: config.HarnessClaudeCLI, DisplayName: "Claude Code", Command: "claude", VersionArgs: []string{"--version"}, SkillRoot: filepath.Join(home, ".claude", "skills")},
		{Kind: config.HarnessPiCLI, DisplayName: "Pi CLI", Command: "pi", VersionArgs: []string{"--version"}, SkillRoot: filepath.Join(home, ".pi", "agent", "skills")},
	}
	for _, override := range configured {
		for index := range descriptors {
			if descriptors[index].Kind != strings.TrimSpace(override.Kind) {
				continue
			}
			if strings.TrimSpace(override.Command) != "" {
				descriptors[index].Command = strings.TrimSpace(override.Command)
			}
		}
	}
	return descriptors
}

func harnessEnabled(kind string, configured []config.HarnessConfig) bool {
	for _, harness := range configured {
		if strings.TrimSpace(harness.Kind) != kind {
			continue
		}
		return harness.Enabled == nil || *harness.Enabled
	}
	return true
}

func harnessSkillCapabilityID(kind, skillID string) string {
	return fmt.Sprintf("%s/%s", kind, skillID)
}
