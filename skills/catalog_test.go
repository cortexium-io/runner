package skills

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"
)

type fixedCatalog struct {
	skills []Skill
}

func TestValidationRejectsChangedOrUnpinnedReferences(t *testing.T) {
	cases := []struct {
		name   string
		change func(*Skill)
	}{
		{"changed", func(s *Skill) { s.References[0].Content = []byte("unreviewed content") }},
		{"rehash", func(s *Skill) {
			s.References[0].Content = []byte("unreviewed content")
			s.References[0].SHA256 = fmt.Sprintf("%x", sha256.Sum256(s.References[0].Content))
		}},
		{"missing", func(s *Skill) { s.References = s.References[1:] }},
		{"duplicate", func(s *Skill) { s.References[1] = s.References[0] }},
		{"escape", func(s *Skill) { s.References[0].Path = "../outside.md" }},
		{"extra", func(s *Skill) {
			s.References = append(s.References, File{Path: "references/unreviewed.md", Content: []byte("extra")})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			listed := (EmbeddedCatalog{}).List()
			for i := range listed {
				if listed[i].ID != "runner-interaction-design" {
					continue
				}
				tc.change(&listed[i])
			}
			if _, err := Validate(fixedCatalog{skills: listed}); err == nil {
				t.Fatal("accepted invalid bundled reference")
			}
		})
	}
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
