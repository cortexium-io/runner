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

func TestBundledSkillsOwnReusableRoleWorkflow(t *testing.T) {
	checks := map[string][]string{
		"runner-planner": {
			"what evidence must establish",
			"The implementer owns that choice",
			"emergency loop protection",
			"configured timeout only as a safety bound",
			"Never assume a browser",
		},
		"runner-implementer": {
			"smallest reliable method",
			"Add or update durable test code when it is the simplest reliable protection",
			"Run a broad or complete suite only",
			"Do not assume a browser",
			"Never run `git add`, `git rm`, `git update-index`, or `git commit`",
		},
		"runner-reviewer": {
			"Complete one focused static pass",
			"Evaluate every Runner-owned proof obligation exactly once",
			"The implementer owns how proof is produced",
			"test files, rewrite tests",
			"Do not assume a browser",
			"In an evidence-audit stage",
			"In a focused-verification stage",
		},
	}
	for skillID, required := range checks {
		skill, ok := (EmbeddedCatalog{}).Get(skillID)
		if !ok {
			t.Fatalf("bundled skill %q is missing", skillID)
		}
		for _, instruction := range required {
			if !strings.Contains(string(skill.Content), instruction) {
				t.Fatalf("bundled skill %q must own workflow instruction %q", skillID, instruction)
			}
		}
	}
}

func TestBundledSkillsDoNotRetainObsoleteReviewOrTestChoreography(t *testing.T) {
	for skillID, forbidden := range map[string][]string{
		"runner-planner":     {"exact existing command", "half that timeout"},
		"runner-implementer": {"only when the approved body"},
		"runner-reviewer":    {"static stage", "criterion stage", "At most one"},
	} {
		skill, _ := (EmbeddedCatalog{}).Get(skillID)
		for _, value := range forbidden {
			if strings.Contains(string(skill.Content), value) {
				t.Fatalf("bundled skill %q retained obsolete instruction %q", skillID, value)
			}
		}
	}
}
