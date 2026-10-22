package execution

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/cortexium-io/runner/internal/runner/config"
	"github.com/cortexium-io/runner/internal/securefs"
)

type npmLock struct {
	LockfileVersion int `json:"lockfileVersion"`
	Packages        map[string]struct {
		Version              string            `json:"version"`
		Resolved             string            `json:"resolved"`
		Integrity            string            `json:"integrity"`
		Link                 bool              `json:"link"`
		Dependencies         map[string]string `json:"dependencies"`
		DevDependencies      map[string]string `json:"devDependencies"`
		OptionalDependencies map[string]string `json:"optionalDependencies"`
	} `json:"packages"`
}

// Only ordinary public-registry lockfile-v3 packages are supported. In
// particular npm configuration, git URLs and workspace links never reach fetch.
func npmInputs(manifest, lock []byte, playwright string) ([]string, error) {
	var pkg map[string]json.RawMessage
	var parsed npmLock
	if json.Unmarshal(manifest, &pkg) != nil || json.Unmarshal(lock, &parsed) != nil || parsed.LockfileVersion != 3 || len(parsed.Packages) == 0 {
		return nil, errors.New("npm requires package.json and a consistent package-lock.json v3")
	}
	for _, key := range []string{"workspaces", "overrides"} {
		if _, ok := pkg[key]; ok {
			return nil, fmt.Errorf("npm %s is not supported by the preparation recipe", key)
		}
	}
	root, ok := parsed.Packages[""]
	if !ok {
		return nil, errors.New("npm lock lacks its root package")
	}
	for key, deps := range map[string]map[string]string{"dependencies": root.Dependencies, "devDependencies": root.DevDependencies, "optionalDependencies": root.OptionalDependencies} {
		var declared map[string]string
		if data := pkg[key]; data != nil && json.Unmarshal(data, &declared) != nil {
			return nil, errors.New("invalid npm dependencies")
		}
		a, _ := json.Marshal(declared)
		b, _ := json.Marshal(deps)
		if !bytes.Equal(a, b) && !(len(declared) == 0 && len(deps) == 0) {
			return nil, errors.New("npm manifest and lock root dependencies differ")
		}
	}
	urls := map[string]bool{}
	for path, p := range parsed.Packages {
		if path == "" {
			continue
		}
		if !strings.HasPrefix(path, "node_modules/") || !config.LiteralPath(path) || p.Link {
			return nil, errors.New("npm local/workspace links are unsupported")
		}
		u, err := url.Parse(p.Resolved)
		if err != nil || u.Scheme != "https" || u.Host != "registry.npmjs.org" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || !strings.Contains(u.Path, "/-/") || strings.Contains(u.Path, "..") || !strings.HasPrefix(p.Integrity, "sha512-") || strings.ContainsAny(p.Integrity, " \n\t") || p.Version == "" {
			return nil, fmt.Errorf("npm package %s needs an integrity-pinned public registry tarball", path)
		}
		if strings.HasSuffix("/"+path, "/node_modules/playwright") || strings.HasSuffix("/"+path, "/node_modules/playwright-core") || strings.HasSuffix("/"+path, "/node_modules/@playwright/test") {
			if playwright != "*" && (playwright == "" || p.Version != playwright) {
				return nil, errors.New("Playwright packages require the matching declared Playwright version")
			}
		}
		urls[p.Resolved] = true
	}
	var result []string
	for u := range urls {
		result = append(result, u)
	}
	sort.Strings(result)
	return result, nil
}

func inputBytes(root, path string) ([]byte, error) {
	if !config.LiteralPath(path) {
		return nil, errors.New("invalid environment input path")
	}
	if err := config.NoSymlinks(filepath.Join(root, path)); err != nil {
		return nil, err
	}
	if _, err := os.Lstat(filepath.Join(root, path)); os.IsNotExist(err) && filepath.Base(path) == "go.sum" {
		return nil, nil
	}
	data, _, state, err := securefs.ReadFile(filepath.Join(root, path), 8<<20)
	if err == nil {
		err = securefs.ValidateOwnedRegularFile(state, uint32(os.Geteuid()))
	}
	return data, err
}

func environmentInputs(root string, spec config.CheckEnvironmentSpec) (map[string]string, error) {
	for _, dir := range spec.NPMPackages {
		if _, err := os.Lstat(filepath.Join(root, dir, "npm-shrinkwrap.json")); !os.IsNotExist(err) {
			return nil, fmt.Errorf("npm shrinkwrap in %s is unsupported by environment preparation", dir)
		}
	}
	inputs := map[string]string{}
	for _, path := range spec.InputPaths() {
		data, err := inputBytes(root, path)
		if err != nil {
			return nil, fmt.Errorf("environment input %s: %w", path, err)
		}
		inputs[path] = artifactHash(data)
		if _, err := os.Lstat(filepath.Join(root, path)); data == nil && os.IsNotExist(err) {
			inputs[path] = "absent"
		}
	}
	return inputs, nil
}

func ValidateEnvironmentInputs(root string, prepared config.PreparedEnvironment) error {
	if prepared.Recipe != environmentRecipe {
		return errors.New("environment recipe changed; prepare and approve the new quality revision")
	}
	inputs, err := environmentInputs(root, prepared.Spec)
	if err != nil {
		return err
	}
	before, _ := json.Marshal(prepared.Inputs)
	after, _ := json.Marshal(inputs)
	if !bytes.Equal(before, after) {
		return errors.New("environment inputs changed; prepare and approve the new quality revision")
	}
	return nil
}

func validateGoMod(data []byte) error {
	// Local replacements/workspaces could read outside the declared input graph.
	// Remote replacements are deliberately deferred too, rather than guessed.
	for line := range strings.SplitSeq(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && (fields[0] == "replace" || fields[0] == "exclude") {
			return errors.New("Go replace/exclude directives are not supported by preparation")
		}
	}
	return nil
}

type goModule struct {
	Path, Version string
	Zip           bool
}

func sumModules(data []byte) ([]goModule, error) {
	seen := map[string]goModule{}
	for line := range strings.SplitSeq(string(data), "\n") {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		if len(f) != 3 || !config.ModuleIdentity(f[0], strings.TrimSuffix(f[1], "/go.mod")) || !strings.HasPrefix(f[2], "h1:") {
			return nil, errors.New("invalid pinned go.sum")
		}
		version := strings.TrimSuffix(f[1], "/go.mod")
		key := f[0] + "@" + version
		m := seen[key]
		m.Path, m.Version = f[0], version
		m.Zip = m.Zip || !strings.HasSuffix(f[1], "/go.mod")
		seen[key] = m
	}
	var result []goModule
	for _, m := range seen {
		result = append(result, m)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Path+"@"+result[i].Version < result[j].Path+"@"+result[j].Version
	})
	return result, nil
}

func escapedModule(m string) string {
	var b strings.Builder
	for _, c := range m {
		if c >= 'A' && c <= 'Z' {
			b.WriteByte('!')
			c += 'a' - 'A'
		}
		b.WriteRune(c)
	}
	return b.String()
}

func privateModule(path string, prefixes []string) bool {
	for _, p := range prefixes {
		if path == p || strings.HasPrefix(path, p+"/") {
			return true
		}
	}
	return false
}

// Import only exact cache/download archives named by pinned input checksums.
// Do not import ziphash: Go must calculate and validate it against go.sum.
func importModules(cache, destination string, modules []goModule, prefixes []string) error {
	for _, m := range modules {
		if !privateModule(m.Path, prefixes) {
			continue
		}
		if cache == "" {
			return fmt.Errorf("private module %s@%s needs --module-cache with verified archives", m.Path, m.Version)
		}
		exts := []string{".mod"}
		if m.Zip {
			exts = append(exts, ".info", ".zip")
		}
		for _, ext := range exts {
			rel := filepath.Join("cache", "download", escapedModule(m.Path), "@v", escapedModule(m.Version)+ext)
			path := filepath.Join(cache, rel)
			if err := config.NoSymlinks(path); err != nil {
				return err
			}
			data, _, state, err := securefs.ReadFile(path, 128<<20)
			if err == nil {
				err = securefs.ValidateOwnedRegularFile(state, uint32(os.Geteuid()))
			}
			if err != nil {
				return fmt.Errorf("private archive %s: %w", rel, err)
			}
			out := filepath.Join(destination, rel)
			if err = os.MkdirAll(filepath.Dir(out), 0700); err != nil {
				return err
			}
			if existing, err := os.ReadFile(out); err == nil {
				if !bytes.Equal(existing, data) {
					return errors.New("conflicting private module archives")
				}
				continue
			}
			if err = securefs.WriteFileExclusive(out, data, 0600); err != nil {
				return err
			}
		}
		if m.Zip {
			files, err := moduleArchiveInputs(destination, m.Path, m.Version)
			if err != nil {
				return err
			}
			mod, err := os.ReadFile(filepath.Join(destination, "cache/download", escapedModule(m.Path), "@v", escapedModule(m.Version)+".mod"))
			if err != nil {
				return err
			}
			if !bytes.Equal(mod, files["go.mod"]) {
				return errors.New("imported module metadata differs from its archive")
			}
		}
	}
	return nil
}

func toolInputs(cache string, tool config.GoTool) (map[string][]byte, error) {
	result, err := moduleArchiveInputs(cache, tool.Module, tool.Version)
	if err == nil {
		err = validateGoMod(result["go.mod"])
	}
	return result, err
}

func moduleArchiveInputs(cache, module, version string) (map[string][]byte, error) {
	path := filepath.Join(cache, "cache", "download", escapedModule(module), "@v", escapedModule(version)+".zip")
	reader, err := zip.OpenReader(path)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	result := map[string][]byte{}
	prefix := module + "@" + version + "/"
	for _, f := range reader.File {
		name := strings.TrimPrefix(f.Name, prefix)
		if f.Name != prefix+name || (name != "go.mod" && name != "go.sum") {
			continue
		}
		if f.UncompressedSize64 > 8<<20 {
			return nil, errors.New("tool manifest exceeds limit")
		}
		r, err := f.Open()
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(io.LimitReader(r, 8<<20+1))
		r.Close()
		if err != nil {
			return nil, err
		}
		result[name] = data
	}
	if len(result["go.mod"]) == 0 {
		return nil, errors.New("tool archive lacks go.mod")
	}
	return result, nil
}
