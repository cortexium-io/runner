package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/securefs"
)

func TestReadPinnedControlFileAllowsSiblingReferenceUpdates(t *testing.T) {
	for _, operation := range []string{"create", "replace", "delete"} {
		t.Run(operation, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "refs", "heads", "runner")
			if err := os.MkdirAll(root, 0o700); err != nil {
				t.Fatal(err)
			}
			content := strings.Repeat("a", 40) + "\n"
			if err := os.WriteFile(filepath.Join(root, "current"), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			sibling := filepath.Join(root, "sibling")
			if operation != "create" {
				if err := os.WriteFile(sibling, []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			directory, err := securefs.OpenDir(root)
			if err != nil {
				t.Fatal(err)
			}
			defer directory.Close()

			// Reproduce the parallel assignment's interleaving deterministically:
			// pinLooseReference has opened the shared parent, but has not read its
			// own ref yet. Git publishes a sibling by renaming its lock file.
			if operation == "delete" {
				if err := os.Remove(sibling); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.WriteFile(sibling+".lock", []byte(strings.Repeat("b", 40)+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(sibling+".lock", sibling); err != nil {
					t.Fatal(err)
				}
			}

			pinned, err := readPinnedControlFile(directory, "current", nil)
			if err != nil {
				t.Fatalf("sibling %s blocked initial reference pinning: %v", operation, err)
			}
			if !pinned.state.Exists || string(pinned.content) != content {
				t.Fatalf("wrong pinned reference: %#v", pinned)
			}
			if err := pinned.verify(); err != nil {
				t.Fatalf("verify unchanged current reference: %v", err)
			}
		})
	}
}
