package config

import (
	"fmt"
	"path"
	"strings"
	"unicode"
)

// ValidateReviewEvidencePaths accepts explicit files or subtrees, not patterns
// or host paths. The workspace boundary separately rejects filesystem links.
func ValidateReviewEvidencePaths(paths []string) error {
	for i, value := range paths {
		if value == "" || value == "." || path.IsAbs(value) || path.Clean(value) != value || strings.ContainsAny(value, `\:*?[]`) || strings.ContainsFunc(value, unicode.IsControl) {
			return fmt.Errorf("review_evidence_paths[%d] must be a clean, literal worktree-relative file or directory", i)
		}
		for _, component := range strings.Split(value, "/") {
			if component == ".." || strings.EqualFold(component, ".git") {
				return fmt.Errorf("review_evidence_paths[%d] cannot escape the worktree or expose Git administration", i)
			}
		}
		for _, previous := range paths[:i] {
			a, b := strings.ToLower(value), strings.ToLower(previous)
			if a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/") {
				return fmt.Errorf("review_evidence_paths[%d] overlaps another selected path", i)
			}
		}
	}
	return nil
}
