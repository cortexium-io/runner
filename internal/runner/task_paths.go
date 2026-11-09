package runner

import (
	"errors"
	"io/fs"
	"strings"
)

// ValidateTaskPaths is shared by plan admission and source enforcement. Scope
// data narrows existing write authority; it never grants metadata access.
func ValidateTaskPaths(paths []string) error {
	if len(paths) < 1 || len(paths) > 32 {
		return errors.New("task needs 1–32 narrowing write paths")
	}
	seen := map[string]bool{}
	for _, name := range paths {
		if name == "." || !fs.ValidPath(name) || strings.ContainsAny(name, "\\*?[]") || seen[name] {
			return errors.New("task write paths are invalid or duplicated")
		}
		for _, part := range strings.Split(name, "/") {
			switch strings.ToLower(part) {
			case ".git", ".runner-tmp", ".codex", ".agents", ".aws":
				return errors.New("task write path names protected metadata")
			}
		}
		seen[name] = true
	}
	return nil
}
