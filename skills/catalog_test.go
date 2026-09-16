package skills

import (
	"strings"
	"testing"
)

type fixedCatalog struct {
	skills []Skill
}

func (c fixedCatalog) List() []Skill { return append([]Skill(nil), c.skills...) }

func (c fixedCatalog) Get(id string) (Skill, bool) {
	for _, skill := range c.skills {
		if skill.ID == id {
			return skill, true
		}
	}
	return Skill{}, false
}

func TestEmbeddedCatalogMatchesPinnedSkills(t *testing.T) {
	listed, err := Validate(EmbeddedCatalog{})
	if err != nil || len(listed) != len(bundledSkillIDs) {
		t.Fatalf("validate embedded skills: skills=%#v error=%v", listed, err)
	}
}

func TestValidationRejectsContentOutsidePinnedHash(t *testing.T) {
	listed := (EmbeddedCatalog{}).List()
	listed[0].Content = append(append([]byte{}, listed[0].Content...), []byte("\nunreviewed change\n")...)
	if _, err := Validate(fixedCatalog{skills: listed}); err == nil || !strings.Contains(err.Error(), "pinned hash") {
		t.Fatalf("changed bundled skill was accepted: %v", err)
	}
}
