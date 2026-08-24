//go:build darwin || linux

package securefs

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestArtifactSetCreatesPrivatePinnedInvocationFiles(t *testing.T) {
	artifacts, err := NewArtifactSet("runner-artifact-test", []ArtifactFile{
		{Name: "result.json", Mutable: true},
		{Name: "schema.json", Content: []byte(`{"type":"object"}`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer artifacts.Close()
	for _, path := range []string{filepath.Dir(artifacts.Path("result.json")), artifacts.Path("result.json"), artifacts.Path("schema.json")} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		want := os.FileMode(0o600)
		if info.IsDir() {
			want = 0o700
		}
		if info.Mode().Perm() != want {
			t.Fatalf("%s mode = %04o, want %04o", path, info.Mode().Perm(), want)
		}
	}
	if err := os.WriteFile(artifacts.Path("result.json"), []byte(`{"ok":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	content, err := artifacts.ReadMutable("result.json", 1024)
	if err != nil || string(content) != `{"ok":true}` {
		t.Fatalf("read pinned mutable result: %q, %v", content, err)
	}
	if err := artifacts.VerifyImmutable("schema.json"); err != nil {
		t.Fatalf("verify immutable schema: %v", err)
	}
}

func TestArtifactSetRejectsResultSubstitutionAndUnsafeMetadata(t *testing.T) {
	tests := map[string]func(string) error{
		"symlink": func(path string) error {
			if err := os.Remove(path); err != nil {
				return err
			}
			target := filepath.Join(t.TempDir(), "target")
			if err := os.WriteFile(target, []byte(`{"substituted":true}`), 0o600); err != nil {
				return err
			}
			return os.Symlink(target, path)
		},
		"rename replacement": func(path string) error {
			if err := os.Rename(path, path+".moved"); err != nil {
				return err
			}
			return os.WriteFile(path, []byte(`{"substituted":true}`), 0o600)
		},
		"non regular": func(path string) error {
			if err := os.Remove(path); err != nil {
				return err
			}
			return os.Mkdir(path, 0o700)
		},
		"permissive mode": func(path string) error { return os.Chmod(path, 0o644) },
	}
	for name, attack := range tests {
		t.Run(name, func(t *testing.T) {
			artifacts, err := NewArtifactSet("runner-artifact-attack", []ArtifactFile{{Name: "result.json", Mutable: true}})
			if err != nil {
				t.Fatal(err)
			}
			defer artifacts.Close()
			if err := attack(artifacts.Path("result.json")); err != nil {
				t.Fatal(err)
			}
			if content, err := artifacts.ReadMutable("result.json", 1024); err == nil {
				t.Fatalf("substitution was accepted: %s", content)
			}
		})
	}
}

func TestArtifactSetRejectsImmutableContentChanges(t *testing.T) {
	artifacts, err := NewArtifactSet("runner-artifact-schema", []ArtifactFile{{Name: "schema.json", Content: []byte(`{"type":"object"}`)}})
	if err != nil {
		t.Fatal(err)
	}
	defer artifacts.Close()
	if err := os.WriteFile(artifacts.Path("schema.json"), []byte(`{"type":"string"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := artifacts.VerifyImmutable("schema.json"); err == nil {
		t.Fatal("modified immutable schema was accepted")
	}
}

func TestArtifactFileValidationRejectsWrongOwnerAndMode(t *testing.T) {
	valid := unix.Stat_t{Mode: unix.S_IFREG | 0o600, Uid: uint32(os.Geteuid()), Nlink: 1}
	if err := validateArtifactFile(valid); err != nil {
		t.Fatalf("valid artifact metadata was rejected: %v", err)
	}
	wrongOwner := valid
	wrongOwner.Uid++
	if err := validateArtifactFile(wrongOwner); err == nil {
		t.Fatal("wrong-owner artifact metadata was accepted")
	}
	permissive := valid
	permissive.Mode = unix.S_IFREG | 0o640
	if err := validateArtifactFile(permissive); err == nil {
		t.Fatal("permissive artifact metadata was accepted")
	}
}

func TestOwnedRegularFileValidationRejectsUnsafeConfigProvenance(t *testing.T) {
	valid := stateFromStat(unix.Stat_t{Mode: unix.S_IFREG | 0o600, Uid: uint32(os.Geteuid()), Nlink: 1})
	if err := ValidateOwnedRegularFile(valid, uint32(os.Geteuid())); err != nil {
		t.Fatalf("valid owned file metadata was rejected: %v", err)
	}
	wrongOwner := valid
	wrongOwner.uid++
	if err := ValidateOwnedRegularFile(wrongOwner, uint32(os.Geteuid())); err == nil {
		t.Fatal("wrong-owner file metadata was accepted")
	}
	otherWritable := valid
	otherWritable.mode |= 0o002
	if err := ValidateOwnedRegularFile(otherWritable, uint32(os.Geteuid())); err == nil {
		t.Fatal("other-writable file metadata was accepted")
	}
	multipleLinks := valid
	multipleLinks.nlink = 2
	if err := ValidateOwnedRegularFile(multipleLinks, uint32(os.Geteuid())); err == nil {
		t.Fatal("multiply linked file metadata was accepted")
	}
}

func TestArtifactSetCleanupDoesNotFollowSubstitutedDirectory(t *testing.T) {
	artifacts, err := NewArtifactSet("runner-artifact-cleanup", []ArtifactFile{{Name: "result.json", Mutable: true}})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Dir(artifacts.Path("result.json"))
	moved := path + ".moved"
	if err := os.Rename(path, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(path, "do-not-remove")
	if err := os.WriteFile(marker, []byte("outside pinned directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := artifacts.Close(); err == nil {
		t.Fatal("substituted invocation directory was not detected")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("cleanup touched substituted directory: %v", err)
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(moved); err != nil {
		t.Fatal(err)
	}
}

func TestArtifactSetCleanupDoesNotRemoveExternalRenameTarget(t *testing.T) {
	artifacts, err := NewArtifactSet("runner-artifact-external-rename", []ArtifactFile{{Name: "result.json", Mutable: true}})
	if err != nil {
		t.Fatal(err)
	}
	external := filepath.Join(t.TempDir(), "renamed-result.json")
	if err := os.Rename(artifacts.Path("result.json"), external); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(artifacts.Path("result.json"), []byte(`{"substituted":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := artifacts.Close(); err != nil {
		t.Fatalf("confined cleanup failed: %v", err)
	}
	content, err := os.ReadFile(external)
	if err != nil {
		t.Fatalf("cleanup removed external rename target: %v", err)
	}
	if len(content) != 0 {
		t.Fatalf("cleanup left artifact content reachable outside its directory: %q", content)
	}
}
