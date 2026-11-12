package guidesmedia

import (
	"errors"
	"strings"
)

// ValidatePropagation relates full original Git-source and compiled static
// inventories. Inventory success is byte custody, never player/browser proof.
func ValidatePropagation(source, compiled map[string]string) error {
	m, err := Approved()
	if err != nil {
		return err
	}
	expectedSource, expectedCompiled := map[string]bool{}, map[string]bool{}
	for _, g := range m.Guides {
		for _, f := range g.Files {
			src := Target(g, f)
			dst := "frontend_dist/" + strings.TrimPrefix(src, "frontend/public/")
			if source[src] != f.SHA256 || compiled[dst] != f.SHA256 {
				return errors.New("guide source/compiled inventory lost or changed immutable media")
			}
			expectedSource[src] = true
			expectedCompiled[dst] = true
		}
	}
	for name := range source {
		if strings.HasPrefix(name, "frontend/public/assets/learning/guides/") && !expectedSource[name] {
			return errors.New("unlisted media in guide source inventory")
		}
	}
	for name := range compiled {
		if strings.HasPrefix(name, "frontend_dist/assets/learning/guides/") && !expectedCompiled[name] {
			return errors.New("unlisted media in guide compiled inventory")
		}
	}
	return nil
}
