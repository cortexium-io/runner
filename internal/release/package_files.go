package release

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// Candidate build output must not turn the privileged collector into a symlink
// reader outside its owned directory. Runtime/configuration paths are separate.
func readPackageFile(root, name string, limit int64) ([]byte, error) {
	path, err := packagePath(root, name)
	if err != nil {
		return nil, err
	}
	return readFile(path, limit)
}

func packagePath(root, name string) (string, error) {
	if name == "" || filepath.IsAbs(name) || filepath.Clean(name) != name || strings.HasPrefix(name, "../") {
		return "", errors.New("invalid package file path")
	}
	path := root
	for _, part := range append([]string{""}, strings.Split(name, string(filepath.Separator))...) {
		if part != "" {
			path = filepath.Join(path, part)
		}
		info, err := os.Lstat(path)
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("package contains a symlink")
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("package file is not regular")
	}
	return path, nil
}

type sourceFailure struct{ reason string }

func (e *sourceFailure) Error() string { return e.reason }

func packageFailure(root string, original error) error {
	if !knownFailure(original) {
		return original
	}
	data, err := readPackageFile(root, "commands.json", 128<<10)
	if err != nil {
		return original
	}
	var commands []struct {
		Command []string `json:"command"`
		Exit    int      `json:"exit"`
		Log     string   `json:"log"`
	}
	if json.Unmarshal(data, &commands) != nil {
		return original
	}
	for _, command := range commands {
		if command.Exit == 0 || len(command.Command) < 2 {
			continue
		}
		program := filepath.Base(command.Command[0])
		if program == "npm" && command.Command[1] == "ci" {
			return original
		}
		if program != "go" && program != "node" && !(program == "npm" && command.Command[1] == "run") {
			return original
		}
		log, err := readPackageFile(root, command.Log, 256<<10)
		if err != nil {
			return original
		}
		if len(log) > 1000 {
			log = log[len(log)-1000:]
		}
		return &sourceFailure{reason: "candidate build/check failed: " + string(log)}
	}
	return original
}
