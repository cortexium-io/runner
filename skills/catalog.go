package skills

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
)

// BundledVersion identifies the skill bundle shipped in this Runner build.
const BundledVersion = "1.10.1"

var bundledSkillIDs = []string{
	"runner-planner",
	"runner-implementer",
	"runner-reviewer",
	"runner-interaction-design",
}

var bundledSkillSHA256 = map[string]string{
	"runner-implementer":        "a2295551cbdc6ab1540cf2543cae1794b4af98029c91bc24ffa94a053ebd5099",
	"runner-planner":            "42db8ce13005014b536c143b68ee35ef475f912454ea1d02b7244df745eae063",
	"runner-reviewer":           "07b447ef4f8a3af5c2794d71cfec16ddf8f8315594285299ab9c4f971d82be6c",
	"runner-interaction-design": "1391ca8c754aca160a58f0436de30c914f1b95a45ddd59d8bf481c4193d79816",
}

// Reference files are a reviewed, finite Markdown allowlist, not a mechanism
// for loading arbitrary files from installed skills or the target repository.
var bundledReferenceSHA256 = map[string]map[string]string{
	"runner-interaction-design": {
		"references/interaction-models.md":      "181ca6cf0890af5fd3d324efea3b5a814c98f8767c6215d904bce6cce352a9e6",
		"references/visual-structure.md":        "c7e2d334ab79fef48f9b5dc52e7a7d244a5238eef3623e0c73c4fc2120ef0369",
		"references/accessible-interactions.md": "f3d7f4d3ca9cff89a7cee2754fd61e423acb62e790d6582220acd1386dc5183c",
		"references/evaluation.md":              "6bd005cd2981a4a622a6ae4c0359a4f1caf559dcf065c0b3f27f87611f815929",
	},
}

func ValidID(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 64 || strings.HasPrefix(value, "-") || strings.HasSuffix(value, "-") || strings.Contains(value, "--") {
		return false
	}
	for _, character := range value {
		if character != '-' && (character < 'a' || character > 'z') && (character < '0' || character > '9') {
			return false
		}
	}
	return true
}

//go:embed */SKILL.md */references/*.md
var bundledSkills embed.FS

type Skill struct {
	ID         string `json:"id"`
	Version    string `json:"version"`
	SHA256     string `json:"sha256"`
	Content    []byte `json:"-"`
	References []File `json:"references,omitempty"`
}

type File struct {
	Path    string `json:"path"`
	SHA256  string `json:"sha256"`
	Content []byte `json:"-"`
}

// Files returns the entrypoint and the explicitly bundled references in stable
// order. Callers never discover additional files from an installed directory.
func (s Skill) Files() []File {
	return append([]File{{Path: "SKILL.md", SHA256: s.SHA256, Content: s.Content}}, s.References...)
}

type Catalog interface {
	List() []Skill
	Get(id string) (Skill, bool)
}

type EmbeddedCatalog struct{}

func (EmbeddedCatalog) List() []Skill {
	result := make([]Skill, 0, len(bundledSkillIDs))
	for _, id := range bundledSkillIDs {
		skill, ok := EmbeddedCatalog{}.Get(id)
		if ok {
			result = append(result, skill)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func (EmbeddedCatalog) Get(id string) (Skill, bool) {
	allowed := false
	for _, candidate := range bundledSkillIDs {
		if candidate == id {
			allowed = true
			break
		}
	}
	if !allowed {
		return Skill{}, false
	}
	content, err := bundledSkills.ReadFile(fmt.Sprintf("%s/SKILL.md", id))
	if err != nil {
		return Skill{}, false
	}
	digest := sha256.Sum256(content)
	skill := Skill{
		ID: id, Version: BundledVersion, SHA256: hex.EncodeToString(digest[:]), Content: content,
	}
	for name := range bundledReferenceSHA256[id] {
		content, err := bundledSkills.ReadFile(id + "/" + name)
		if err != nil {
			return Skill{}, false
		}
		digest := sha256.Sum256(content)
		skill.References = append(skill.References, File{Path: name, SHA256: hex.EncodeToString(digest[:]), Content: content})
	}
	sort.Slice(skill.References, func(i, j int) bool { return skill.References[i].Path < skill.References[j].Path })
	return skill, true
}

func Validate(catalog Catalog) ([]Skill, error) {
	listed := catalog.List()
	if len(listed) != len(bundledSkillIDs) {
		return nil, fmt.Errorf("bundled skill catalog has %d entries, want %d", len(listed), len(bundledSkillIDs))
	}
	seen := map[string]struct{}{}
	for _, skill := range listed {
		if _, exists := seen[skill.ID]; exists {
			return nil, fmt.Errorf("bundled skill catalog contains duplicate %q", skill.ID)
		}
		seen[skill.ID] = struct{}{}
		expected, ok := bundledSkillSHA256[skill.ID]
		if !ok {
			return nil, fmt.Errorf("bundled skill %q has no pinned hash", skill.ID)
		}
		digest := sha256.Sum256(skill.Content)
		actual := hex.EncodeToString(digest[:])
		if skill.SHA256 != actual || actual != expected {
			return nil, fmt.Errorf("bundled skill %q does not match its pinned hash", skill.ID)
		}
		references := bundledReferenceSHA256[skill.ID]
		if len(skill.References) != len(references) {
			return nil, fmt.Errorf("bundled skill %q has an incorrect reference count", skill.ID)
		}
		seenReferences := map[string]bool{}
		for _, reference := range skill.References {
			expected, pinned := references[reference.Path]
			if !pinned || seenReferences[reference.Path] || path.Dir(reference.Path) != "references" ||
				path.Ext(reference.Path) != ".md" || !ValidID(strings.TrimSuffix(path.Base(reference.Path), ".md")) {
				return nil, fmt.Errorf("bundled skill %q has invalid reference %q", skill.ID, reference.Path)
			}
			seenReferences[reference.Path] = true
			digest := sha256.Sum256(reference.Content)
			actual := hex.EncodeToString(digest[:])
			if reference.SHA256 != actual || actual != expected {
				return nil, fmt.Errorf("bundled skill %q reference %q does not match its pinned hash", skill.ID, reference.Path)
			}
		}
		name, description, err := parseManifest(skill.Content)
		if err != nil {
			return nil, fmt.Errorf("bundled skill %q: %w", skill.ID, err)
		}
		if name != skill.ID {
			return nil, fmt.Errorf("bundled skill %q manifest name is %q", skill.ID, name)
		}
		if strings.TrimSpace(description) == "" {
			return nil, fmt.Errorf("bundled skill %q manifest description is empty", skill.ID)
		}
	}
	for _, id := range bundledSkillIDs {
		if _, exists := seen[id]; !exists {
			return nil, fmt.Errorf("bundled skill %q is missing", id)
		}
	}
	return listed, nil
}

func parseManifest(content []byte) (string, string, error) {
	lines := strings.Split(strings.ReplaceAll(string(content), "\r\n", "\n"), "\n")
	if len(lines) < 4 || strings.TrimSpace(lines[0]) != "---" {
		return "", "", errors.New("manifest must start with YAML frontmatter")
	}
	name := ""
	description := ""
	closed := false
	for _, line := range lines[1:] {
		if strings.TrimSpace(line) == "---" {
			closed = true
			break
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch strings.TrimSpace(key) {
		case "name":
			name = strings.TrimSpace(value)
		case "description":
			description = strings.TrimSpace(value)
		}
	}
	if !closed || name == "" || description == "" {
		return "", "", errors.New("manifest requires closed frontmatter with name and description")
	}
	return name, description, nil
}
