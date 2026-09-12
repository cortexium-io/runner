package skills

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
)

const bundledSkillVersion = "1.8.10"

var bundledSkillIDs = []string{
	"runner-planner",
	"runner-implementer",
	"runner-reviewer",
}

var bundledSkillSHA256 = map[string]string{
	"runner-implementer": "d9516fd6192d6c963dc85637d348b3cce7d5e4a512096fc03457fb3701ec9823",
	"runner-planner":     "d1149c09965e1cedc7e36970a760105e2b83fa6b0bcbb91417fc36fe75c5041e",
	"runner-reviewer":    "6af580b824ec9508f24fb4488a867d494650c9630ad1506cf65f5358673f3ce7",
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

//go:embed */SKILL.md
var bundledSkills embed.FS

type Skill struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
	Content []byte `json:"-"`
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
	return Skill{
		ID: id, Version: bundledSkillVersion, SHA256: hex.EncodeToString(digest[:]), Content: content,
	}, true
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
