package execution

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	bundledskills "github.com/cortexium-io/runner/skills"
)

// prepareSkillReferences uses embedded bytes only. This private subdirectory is
// outside the repository and sandbox-writable scratch/cache roots. Grant read
// access to it, never to its parent containing trusted MCP runtime state. It is
// removed with the owning profile workspace after the harness has exited.
func prepareSkillReferences(workspace *profileWorkspace, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	catalog := bundledskills.EmbeddedCatalog{}
	if _, err := bundledskills.Validate(catalog); err != nil {
		return err
	}
	for _, id := range ids {
		skill, ok := catalog.Get(strings.TrimSpace(id))
		if !ok {
			return fmt.Errorf("unrecognized pinned skill %q", id)
		}
		for _, file := range skill.References {
			if workspace.SkillReferenceRoot == "" {
				if !filepath.IsAbs(workspace.TrustedToolDir) {
					return fmt.Errorf("skill references require a private trusted runtime directory")
				}
				workspace.SkillReferenceRoot = filepath.Join(workspace.TrustedToolDir, "skill-references")
			}
			path := filepath.Join(workspace.SkillReferenceRoot, skill.ID, file.Path)
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				return fmt.Errorf("prepare skill reference directory: %w", err)
			}
			if err := os.WriteFile(path, file.Content, 0400); err != nil {
				return fmt.Errorf("prepare skill reference %s: %w", file.Path, err)
			}
		}
	}
	return nil
}
