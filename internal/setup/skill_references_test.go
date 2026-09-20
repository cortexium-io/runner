package setup

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/cortexium-io/runner/internal/config"
	bundledskills "github.com/cortexium-io/runner/skills"
)

func TestUnconfiguredDoctorDoesNotRequireOptionalDesignSkill(t *testing.T) {
	inspector := NewInspector(config.Config{}, inspectorCommandRunner{})
	for _, kind := range []string{config.HarnessCodexCLI, config.HarnessClaudeCLI, config.HarnessPiCLI} {
		skills := inspector.requiredBundledSkills(kind)
		if len(skills) != 3 {
			t.Fatalf("unexpected default requirements: %#v", skills)
		}
		for _, skill := range skills {
			if skill.ID == "runner-interaction-design" {
				t.Fatal("optional skill became a default requirement")
			}
		}
	}
}

func TestSkillReferencesInstallAndReadiness(t *testing.T) {
	for _, descriptor := range defaultHarnessDescriptors(t.TempDir(), nil) {
		t.Run(descriptor.Kind, func(t *testing.T) {
			skill, _ := (bundledskills.EmbeddedCatalog{}).Get("runner-interaction-design")
			if _, err := installBundledSkill(descriptor, skill, false); err != nil {
				t.Fatal(err)
			}
			for _, file := range skill.Files() {
				got, err := os.ReadFile(filepath.Join(descriptor.SkillRoot, skill.ID, file.Path))
				if err != nil || string(got) != string(file.Content) {
					t.Fatalf("missing %s: %v", file.Path, err)
				}
			}
			if state := inspectHarnessSkill(descriptor, skill, nil); state.Status != CapabilityAvailable {
				t.Fatalf("installed skill unavailable: %#v", state)
			}
			result, err := installBundledSkill(descriptor, skill, false)
			if err != nil || result.Status != "unchanged" {
				t.Fatalf("not idempotent: %#v %v", result, err)
			}
			ref := filepath.Join(descriptor.SkillRoot, skill.ID, skill.References[0].Path)
			if err := os.Remove(ref); err != nil {
				t.Fatal(err)
			}
			if state := inspectHarnessSkill(descriptor, skill, nil); state.Status != CapabilityMissing {
				t.Fatalf("missing reference reported ready: %#v", state)
			}
			if _, err := installBundledSkill(descriptor, skill, false); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(ref, []byte("operator edit"), 0600); err != nil {
				t.Fatal(err)
			}
			if state := inspectHarnessSkill(descriptor, skill, nil); state.Status != CapabilityBlocked {
				t.Fatalf("modified reference reported ready: %#v", state)
			}
			entry := filepath.Join(descriptor.SkillRoot, skill.ID, "SKILL.md")
			if err := os.Remove(entry); err != nil {
				t.Fatal(err)
			}
			if _, err := installBundledSkill(descriptor, skill, false); !errors.Is(err, ErrDifferingSkill) {
				t.Fatalf("overwrote modified reference: %v", err)
			}
			if _, err := os.Stat(entry); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("wrote entrypoint before checking all references")
			}
			got, _ := os.ReadFile(ref)
			if string(got) != "operator edit" {
				t.Fatal("operator edit lost")
			}
			if _, err := installBundledSkill(descriptor, skill, true); err != nil {
				t.Fatal(err)
			}
			if state := inspectHarnessSkill(descriptor, skill, nil); state.Status != CapabilityAvailable {
				t.Fatalf("repair incomplete: %#v", state)
			}
		})
	}
}

func TestSkillReferenceSymlinkIsNeverFollowed(t *testing.T) {
	descriptor := defaultHarnessDescriptors(t.TempDir(), nil)[0]
	skill, _ := (bundledskills.EmbeddedCatalog{}).Get("runner-interaction-design")
	if _, err := installBundledSkill(descriptor, skill, false); err != nil {
		t.Fatal(err)
	}
	ref := filepath.Join(descriptor.SkillRoot, skill.ID, skill.References[0].Path)
	external := filepath.Join(t.TempDir(), "outside.md")
	if err := os.WriteFile(external, skill.References[0].Content, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(ref); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, ref); err != nil {
		t.Fatal(err)
	}
	if state := inspectHarnessSkill(descriptor, skill, nil); state.Status != CapabilityBlocked {
		t.Fatalf("symlink accepted: %#v", state)
	}
	if _, err := installBundledSkill(descriptor, skill, true); err == nil {
		t.Fatal("force accepted symlink")
	}
	got, _ := os.ReadFile(external)
	if string(got) != string(skill.References[0].Content) {
		t.Fatal("external target changed")
	}
}
