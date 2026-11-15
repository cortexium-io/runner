package runner

import (
	"errors"
	"strings"
)

// CommandArtifactInput selects the canonical file of a declared artifact input.
// It never interpolates artifact contents, inline text or executable arguments.
// The whole-token restriction also keeps configured shell bodies literal.
func CommandArtifactInput(value string) (string, error) {
	const prefix = "{artifact."
	if !strings.Contains(value, prefix) {
		return "", nil
	}
	if !strings.HasPrefix(value, prefix) || !strings.HasSuffix(value, "}") {
		return "", errors.New("artifact file binding must occupy one whole argument or environment value")
	}
	name := value[len(prefix) : len(value)-1]
	if len(name) == 0 || len(name) > 64 {
		return "", errors.New("artifact file binding needs a declared input name")
	}
	for _, char := range name {
		if !(char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '_' || char == '-') {
			return "", errors.New("artifact file binding has an invalid input name")
		}
	}
	return name, nil
}

// CommandDependencyInput selects an artifact output already exposed in this
// task's completed results. Core still owns which results the task may receive.
func CommandDependencyInput(value string) (task, output string, err error) {
	const prefix = "{dependency."
	if !strings.Contains(value, prefix) {
		return "", "", nil
	}
	if !strings.HasPrefix(value, prefix) || !strings.HasSuffix(value, "}") {
		return "", "", errors.New("dependency file binding must occupy one whole argument or environment value")
	}
	parts := strings.Split(value[len(prefix):len(value)-1], ".")
	if len(parts) != 2 {
		return "", "", errors.New("dependency binding needs a task ID and artifact output name")
	}
	for _, part := range parts {
		if _, err := CommandArtifactInput("{artifact." + part + "}"); err != nil {
			return "", "", err
		}
	}
	return parts[0], parts[1], nil
}
