package workspace

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cortexium-io/runner/internal/runner/config"
	"github.com/cortexium-io/runner/internal/securefs"
)

// ValidateOutputs checks the actual source inventory on every execution. An
// approval never grants permission for newly tracked files below an output.
func ValidateOutputs(files map[string]string, outputs []config.CheckOutput) error {
	for _, o := range outputs {
		allowed := map[string]bool{}
		for _, p := range o.Placeholders {
			digest := files[p]
			if digest == "" || digest == "directory" || strings.HasPrefix(digest, "link:") {
				return fmt.Errorf("output placeholder %s must be an existing regular source file", p)
			}
			switch filepath.Base(p) {
			case "go.mod", "go.sum", "package.json", "package-lock.json", "npm-shrinkwrap.json":
				return errors.New("manifests cannot be output placeholders")
			}
			switch filepath.Ext(p) {
			case ".go", ".js", ".ts", ".tsx", ".jsx", ".mjs", ".cjs", ".mts", ".cts", ".vue", ".svelte", ".sh", ".py", ".rs", ".c", ".h", ".java":
				return errors.New("source code cannot be an output placeholder")
			}
			allowed[p] = true
		}
		for name, digest := range files {
			p := strings.TrimSuffix(name, "/")
			if p == o.Path || strings.HasPrefix(p, o.Path+"/") {
				if digest != "directory" && !allowed[p] {
					return fmt.Errorf("output %s covers unapproved source %s", o.Path, p)
				}
			} else if strings.HasPrefix(o.Path, p+"/") && strings.HasPrefix(digest, "link:") {
				return errors.New("output parent is a source link")
			}
		}
	}
	return nil
}

// PrepareOutputs leaves the source bytes in place. Docker overlays only these
// private directories; the source mount itself is read-only.
func PrepareOutputs(root string, files map[string]string, outputs []config.CheckOutput) error {
	if err := ValidateOutputs(files, outputs); err != nil {
		return err
	}
	for _, o := range outputs {
		backing := filepath.Join(root, ".runner-output", "outputs", o.Path)
		for _, dir := range []string{filepath.Join(root, o.Path), backing} {
			if err := config.NoSymlinks(dir); err != nil {
				return err
			}
			if err := os.MkdirAll(dir, 0700); err != nil {
				return err
			}
		}
		for _, p := range o.Placeholders {
			data, mode, _, err := securefs.ReadFile(filepath.Join(root, p), 64<<20)
			if err != nil {
				return err
			}
			dest := filepath.Join(root, ".runner-output", "outputs", p)
			if err = os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
				return err
			}
			if err = securefs.WriteFileExclusive(dest, data, mode.Perm()); err != nil {
				return err
			}
		}
	}
	return nil
}
