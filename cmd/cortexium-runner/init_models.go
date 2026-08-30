package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/cortexium-io/runner/internal/config"
)

type initModelOption struct {
	Label       string
	Description string
	Value       string
	Native      bool
	Custom      bool
	Search      bool
}

func (p *initPrompter) model(ctx context.Context, label, harness string) (string, error) {
	options := initModelOptions(ctx, harness, "")
	for {
		menuOptions := make([]initMenuOption, 0, len(options))
		for _, option := range options {
			menuOptions = append(menuOptions, initMenuOption{
				Label: option.Label, Description: option.Description, Value: option.Value,
			})
		}
		index, err := p.selectMenu(label, menuOptions, 0)
		if err != nil {
			return "", err
		}
		selected := options[index]
		switch {
		case selected.Native:
			return "", nil
		case selected.Custom:
			return p.required("Custom model ID")
		case selected.Search:
			query, err := p.required("Search available Pi models")
			if err != nil {
				return "", err
			}
			searched := initModelOptions(ctx, harness, query)
			if countSelectableModels(searched) == 0 {
				fmt.Fprintf(p.output, "No available Pi models matched %q.\n", query)
				continue
			}
			options = searched
		default:
			return selected.Value, nil
		}
	}
}

func initModelOptions(ctx context.Context, harness, search string) []initModelOption {
	var options []initModelOption
	switch strings.TrimSpace(harness) {
	case config.HarnessClaudeCLI:
		options = claudeModelOptions()
	case config.HarnessCodexCLI:
		options = codexModelOptions()
	case config.HarnessPiCLI:
		options = piModelOptions(ctx, search)
	}
	if strings.TrimSpace(harness) == config.HarnessPiCLI {
		options = append(options, initModelOption{Label: "Search available models", Description: "Filter the models reported by Pi", Search: true})
	}
	return append(options,
		initModelOption{Label: "Use harness-native selection", Description: "Keep the model currently selected by the harness", Native: true},
		initModelOption{Label: "Enter a custom model ID", Description: "For pinned versions or provider-specific models", Custom: true},
	)
}

func claudeModelOptions() []initModelOption {
	return []initModelOption{
		{Label: "Opus", Description: "Latest Opus available to Claude Code; suited to complex agentic coding", Value: "opus"},
		{Label: "Sonnet", Description: "Latest Sonnet available to Claude Code; balanced speed and capability", Value: "sonnet"},
	}
}

func codexModelOptions() []initModelOption {
	root := strings.TrimSpace(os.Getenv("CODEX_HOME"))
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil
		}
		root = filepath.Join(home, ".codex")
	}
	content, err := os.ReadFile(filepath.Join(root, "models_cache.json"))
	if err != nil {
		return nil
	}
	var cache struct {
		Models []struct {
			Slug        string `json:"slug"`
			DisplayName string `json:"display_name"`
			Description string `json:"description"`
			Visibility  string `json:"visibility"`
		} `json:"models"`
	}
	if err := json.Unmarshal(content, &cache); err != nil {
		return nil
	}
	options := make([]initModelOption, 0, len(cache.Models))
	for _, model := range cache.Models {
		if strings.TrimSpace(model.Slug) == "" || strings.TrimSpace(model.Visibility) != "list" {
			continue
		}
		label := strings.TrimSpace(model.DisplayName)
		if label == "" {
			label = strings.TrimSpace(model.Slug)
		}
		options = append(options, initModelOption{
			Label: label, Description: truncateModelDescription(model.Description), Value: strings.TrimSpace(model.Slug),
		})
	}
	return options
}

func piModelOptions(ctx context.Context, search string) []initModelOption {
	args := []string{"--list-models"}
	if strings.TrimSpace(search) != "" {
		args = append(args, strings.TrimSpace(search))
	}
	output := commandOutput(ctx, 5*time.Second, "pi", args...)
	options := make([]initModelOption, 0)
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || strings.EqualFold(fields[0], "provider") {
			continue
		}
		provider, model := fields[0], fields[1]
		value := provider + "/" + model
		options = append(options, initModelOption{
			Label: model, Description: "Available through " + provider, Value: value,
		})
		if len(options) == 15 {
			break
		}
	}
	return options
}

func commandOutput(ctx context.Context, timeout time.Duration, command string, args ...string) string {
	path, err := exec.LookPath(command)
	if err != nil {
		return ""
	}
	callContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	output, err := exec.CommandContext(callContext, path, args...).CombinedOutput()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

func countSelectableModels(options []initModelOption) int {
	count := 0
	for _, option := range options {
		if !option.Native && !option.Custom && !option.Search {
			count++
		}
	}
	return count
}

func truncateModelDescription(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= 100 {
		return value
	}
	return strings.TrimSpace(value[:97]) + "..."
}
