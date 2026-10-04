package execution

import (
	"errors"
	"slices"
	"strings"

	"github.com/cortexium-io/runner/internal/securefs"
)

const piCodemodeExtensionName = "codemode-extension.ts"

const piCodemodeSystemPrompt = "Use Codemode to batch independent tool calls when useful. Parse, filter, calculate and sort large results inside the script; return only compact evidence needed for the assignment. Submit the final Runner result through its direct result or finalize tool after the script completes."

// Codemode uses only tools admitted by the invocation. Its models namespace is
// disabled: the pilot must not introduce separate provider calls or credentials.
func createPiCodemodeExtension() (*securefs.ArtifactSet, error) {
	return securefs.NewArtifactSet("cortexium-runner-pi-codemode", []securefs.ArtifactFile{{
		Name: piCodemodeExtensionName, Content: []byte(`import { createCodemodeExtension } from "@earendil-works/pi-coding-agent";
export default function (pi) {
  createCodemodeExtension({ mode: "on", models: false })(pi);
  pi.on("session_start", () => pi.setActiveTools([...new Set([...pi.getActiveTools(), "codemode"])]));
}
`),
	}})
}

func piInvocationAllowsCodemode(args []string, harnessConfigMode string) bool {
	// A formatter's explicit no-tools stage overrides inherited configuration.
	if slices.Contains(args, "--no-tools") {
		return false
	}
	for index := 0; index+1 < len(args); index++ {
		if args[index] == "--tools" {
			for _, name := range strings.Split(args[index+1], ",") {
				switch strings.TrimSpace(name) {
				case "read", "grep", "find", "ls", "bash", "write", "edit":
					return true
				}
			}
			return false
		}
	}
	return inheritsHarnessConfiguration(harnessConfigMode)
}

func addPiCodemodeExtension(args []string, path, harnessConfigMode string) ([]string, error) {
	if strings.TrimSpace(path) == "" || !piInvocationAllowsCodemode(args, harnessConfigMode) {
		return nil, errors.New("Pi Codemode requires an extension path and repository tools")
	}
	result := append([]string(nil), args...)
	for index := 0; index+1 < len(result); index++ {
		if result[index] == "--tools" && !containsCSVValue(result[index+1], "codemode") {
			result[index+1] += ",codemode"
			break
		}
	}
	for index := 0; index+1 < len(result); index++ {
		if result[index] == "--append-system-prompt" {
			result[index+1] += "\n\n" + piCodemodeSystemPrompt
			return append(result, "--extension", path), nil
		}
	}
	return append(result, "--extension", path, "--append-system-prompt", piCodemodeSystemPrompt), nil
}
