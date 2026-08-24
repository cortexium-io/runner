package setup

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/cortexium-io/runner/internal/config"
	bundledskills "github.com/cortexium-io/runner/skills"
)

type SkillInstallResult struct {
	Harness string `json:"harness"`
	Skill   string `json:"skill"`
	Path    string `json:"path"`
	Status  string `json:"status"`
	Detail  string `json:"detail"`
}

type SkillInstaller struct {
	catalog bundledskills.Catalog
	homeDir func() (string, error)
}

var ErrDifferingSkill = errors.New("installed skill differs from the bundled Runner skill")

func NewSkillInstaller() *SkillInstaller {
	return &SkillInstaller{catalog: bundledskills.EmbeddedCatalog{}, homeDir: os.UserHomeDir}
}

func (i *SkillInstaller) InstallConfigured(cfg config.Config, force bool) ([]SkillInstallResult, error) {
	selected := map[string]map[string]struct{}{}
	for _, roleID := range cfg.ExecutionRoleIDs() {
		profile, ok := cfg.RoleProfile(roleID)
		if !ok {
			continue
		}
		kind := strings.TrimSpace(profile.Harness)
		if !config.ValidHarnessKind(kind) {
			return nil, fmt.Errorf("unsupported harness %q for role %s", kind, roleID)
		}
		for _, skillID := range profile.Skills {
			skillID = strings.TrimSpace(skillID)
			if _, bundled := i.catalog.Get(skillID); !bundled {
				continue
			}
			if selected[kind] == nil {
				selected[kind] = map[string]struct{}{}
			}
			selected[kind][skillID] = struct{}{}
		}
	}
	return i.installSelected(selected, force)
}

func (i *SkillInstaller) installSelected(selected map[string]map[string]struct{}, force bool) ([]SkillInstallResult, error) {
	home, err := i.homeDir()
	if err != nil {
		return nil, fmt.Errorf("resolve user home directory: %w", err)
	}
	descriptors := defaultHarnessDescriptors(home, nil)

	results := []SkillInstallResult{}
	for _, descriptor := range descriptors {
		skillIDs, ok := selected[descriptor.Kind]
		if !ok {
			continue
		}
		for _, skill := range i.catalog.List() {
			if _, ok := skillIDs[skill.ID]; !ok {
				continue
			}
			result, installErr := installBundledSkill(descriptor, skill, force)
			results = append(results, result)
			if installErr != nil {
				return results, installErr
			}
		}
	}
	sort.Slice(results, func(a, b int) bool {
		if results[a].Harness == results[b].Harness {
			return results[a].Skill < results[b].Skill
		}
		return results[a].Harness < results[b].Harness
	})
	return results, nil
}

func installBundledSkill(descriptor HarnessDescriptor, skill bundledskills.Skill, force bool) (SkillInstallResult, error) {
	path := filepath.Join(descriptor.SkillRoot, skill.ID, "SKILL.md")
	result := SkillInstallResult{Harness: descriptor.Kind, Skill: skill.ID, Path: path}
	replacing := false
	if err := validateSkillInstallPath(descriptor.SkillRoot, path); err != nil {
		result.Status = CapabilityBlocked
		result.Detail = err.Error()
		return result, err
	}
	if existing, err := os.ReadFile(path); err == nil {
		if string(existing) == string(skill.Content) {
			result.Status = "unchanged"
			result.Detail = "trusted bundled skill is already installed"
			return result, nil
		}
		if !force {
			result.Status = CapabilityBlocked
			result.Detail = "existing skill differs and was left unchanged; review it, then run cortexium-runner doctor --fix"
			return result, fmt.Errorf("%w: %s for %s", ErrDifferingSkill, skill.ID, descriptor.DisplayName)
		}
		replacing = true
	} else if !errors.Is(err, os.ErrNotExist) {
		result.Status = CapabilityBlocked
		result.Detail = "cannot inspect existing skill: " + err.Error()
		return result, fmt.Errorf("inspect skill %s: %w", skill.ID, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		result.Status = CapabilityBlocked
		result.Detail = "cannot create skill directory: " + err.Error()
		return result, fmt.Errorf("create skill directory: %w", err)
	}
	if err := validateSkillInstallPath(descriptor.SkillRoot, path); err != nil {
		result.Status = CapabilityBlocked
		result.Detail = err.Error()
		return result, err
	}
	if force {
		if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
			return result, errors.New("skill file must not be a symbolic link")
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return result, fmt.Errorf("replace skill: %w", err)
		}
	}
	if err := writeFileAtomically(path, skill.Content); err != nil {
		result.Status = CapabilityBlocked
		result.Detail = "cannot write skill: " + err.Error()
		return result, fmt.Errorf("write skill %s: %w", skill.ID, err)
	}
	result.Status = "installed"
	result.Detail = "installed trusted bundled skill"
	if replacing {
		result.Status = "replaced"
		result.Detail = "replaced differing skill with the trusted bundled version"
	}
	return result, nil
}

func validateSkillInstallPath(root, path string) error {
	if !pathInsideOrEqual(path, root) {
		return errors.New("skill path must stay inside the harness skill root")
	}
	current := filepath.Clean(root)
	for {
		info, err := os.Lstat(current)
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("skill install path must not contain symbolic link %s", current)
			}
			if !info.IsDir() && current != path {
				return fmt.Errorf("skill install ancestor must be a directory: %s", current)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect skill install path: %w", err)
		}
		if current == filepath.Clean(path) {
			break
		}
		next := filepath.Join(current, firstRelativePathComponent(current, path))
		if next == current {
			break
		}
		current = next
	}
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return errors.New("skill file must not be a symbolic link")
	}
	return nil
}

func firstRelativePathComponent(base, target string) string {
	relative, err := filepath.Rel(base, target)
	if err != nil || relative == "." {
		return ""
	}
	parts := strings.Split(relative, string(filepath.Separator))
	return parts[0]
}
