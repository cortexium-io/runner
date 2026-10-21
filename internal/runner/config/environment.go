package config

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
)

// CheckEnvironmentSpec selects Runner's recipe, never a repository Dockerfile.
// Versions are exact releases; preparation resolves their local image identities.
type CheckEnvironmentSpec struct {
	Go             string   `json:"go,omitempty"`
	Node           string   `json:"node,omitempty"`
	Playwright     string   `json:"playwright,omitempty"`
	GoModules      []string `json:"go_modules,omitempty"`
	NPMPackages    []string `json:"npm_packages,omitempty"`
	GoTools        []GoTool `json:"go_tools,omitempty"`
	PrivateModules []string `json:"private_modules,omitempty"`
}

type GoTool struct {
	Module  string `json:"module"`
	Version string `json:"version"`
	Package string `json:"package"`
	Name    string `json:"name"`
	Sum     string `json:"sum"` // Explicit root archive checksum, including private tools.
}

type PreparedEnvironment struct {
	Recipe   string               `json:"recipe"`
	Spec     CheckEnvironmentSpec `json:"spec"`
	Inputs   map[string]string    `json:"inputs"`
	Images   map[string]string    `json:"images"`
	Tools    map[string]string    `json:"tools"`
	Platform string               `json:"platform"`
	Image    string               `json:"image"`
}

type CheckOutput struct {
	Path         string   `json:"path"`
	Placeholders []string `json:"placeholders,omitempty"`
}

var exactRelease = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)
var moduleName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]*$`)
var moduleVersion = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+[A-Za-z0-9.+-]*$`)
var moduleSum = regexp.MustCompile(`^h1:[A-Za-z0-9+/]{43}=$`)

func ModuleIdentity(module, version string) bool {
	return moduleName.MatchString(module) && !strings.Contains(module, "..") && !strings.HasSuffix(module, "/") && moduleVersion.MatchString(version)
}

func (e CheckEnvironmentSpec) Validate() error {
	if e.Go == "" && e.Node == "" {
		return errors.New("environment needs an exact Go or Node version")
	}
	for _, version := range []string{e.Go, e.Node, e.Playwright} {
		if version != "" && !exactRelease.MatchString(version) {
			return errors.New("environment tool versions must be exact major.minor.patch releases")
		}
	}
	if (len(e.GoModules) > 0 || len(e.GoTools) > 0 || len(e.PrivateModules) > 0) && e.Go == "" {
		return errors.New("Go modules and tools require a Go version")
	}
	if (len(e.NPMPackages) > 0 || e.Playwright != "") && e.Node == "" {
		return errors.New("npm and Playwright require a Node version")
	}
	for _, dirs := range [][]string{e.GoModules, e.NPMPackages} {
		seen := map[string]bool{}
		for _, dir := range dirs {
			if !LiteralPath(dir) || seen[dir] {
				return fmt.Errorf("invalid or duplicate package directory %q", dir)
			}
			seen[dir] = true
		}
	}
	seen := map[string]bool{}
	for _, t := range e.GoTools {
		switch t.Name {
		case "go", "gofmt", "node", "npm", "npx", "git", "sh", "env", "cp", "chmod", "mkdir":
			return errors.New("Go tool name shadows a Runner environment executable")
		}
		if !ModuleIdentity(t.Module, t.Version) || !moduleSum.MatchString(t.Sum) || !moduleName.MatchString(t.Package) || strings.Contains(t.Package, "..") || (t.Package != t.Module && !strings.HasPrefix(t.Package, t.Module+"/")) || !checkID.MatchString(t.Name) || seen[t.Name] {
			return errors.New("Go tools need unique executable names, module/version, contained package and h1 archive sum")
		}
		seen[t.Name] = true
	}
	seen = map[string]bool{}
	for _, m := range e.PrivateModules {
		if !moduleName.MatchString(m) || strings.Contains(m, "..") || strings.HasSuffix(m, "/") || seen[m] {
			return errors.New("private_modules must name unique literal module prefixes")
		}
		seen[m] = true
	}
	return nil
}

// LiteralPath is a portable repository path, never a glob, control or mount escape.
func LiteralPath(path string) bool {
	if !RelativePath(path) || strings.ContainsAny(path, "*?[]\\,:") || strings.ContainsFunc(path, unicode.IsControl) {
		return false
	}
	for _, part := range strings.Split(path, "/") {
		if strings.EqualFold(part, ".git") || strings.EqualFold(part, ".runner-output") {
			return false
		}
	}
	return true
}

func (s QualitySpec) validateEnvironment() error {
	if s.Preparation != nil {
		if s.CheckImage != "" {
			return errors.New("choose environment or check_image")
		}
		if err := s.Preparation.Validate(); err != nil {
			return err
		}
		for _, dir := range s.Preparation.NPMPackages {
			found := false
			for _, o := range s.Outputs {
				if o.Path == filepath.Join(dir, "node_modules") {
					found = true
				}
			}
			if !found {
				return fmt.Errorf("npm package %s requires its exact node_modules output directory", dir)
			}
		}
	}
	if len(s.Outputs) > 0 && s.CheckImage == "" && s.Preparation == nil {
		return errors.New("declared outputs require a container environment")
	}
	if len(s.Outputs) > 100 {
		return errors.New("at most 100 output directories are supported")
	}
	for i, output := range s.Outputs {
		if !LiteralPath(output.Path) || output.Path == "." {
			return fmt.Errorf("invalid output directory %q", output.Path)
		}
		for _, other := range s.Outputs[:i] {
			if under(output.Path, other.Path) || under(other.Path, output.Path) {
				return errors.New("output directories overlap")
			}
		}
		seen := map[string]bool{}
		for _, p := range output.Placeholders {
			if !LiteralPath(p) || p == output.Path || !under(p, output.Path) || seen[p] {
				return errors.New("placeholders must name exact unique files inside their output")
			}
			seen[p] = true
		}
		for _, source := range s.Sources {
			if under(source, output.Path) {
				return errors.New("output covers a quality source")
			}
		}
		if s.Preparation != nil {
			for _, input := range s.Preparation.InputPaths() {
				if under(input, output.Path) {
					return errors.New("output covers an environment manifest")
				}
			}
		}
	}
	return nil
}

func under(path, root string) bool { return path == root || strings.HasPrefix(path, root+"/") }

func (e CheckEnvironmentSpec) InputPaths() []string {
	var paths []string
	for _, dir := range e.GoModules {
		for _, name := range []string{"go.mod", "go.sum"} {
			paths = append(paths, filepath.Join(dir, name))
		}
	}
	for _, dir := range e.NPMPackages {
		for _, name := range []string{"package.json", "package-lock.json"} {
			paths = append(paths, filepath.Join(dir, name))
		}
	}
	return paths
}

// OutputArtifacts converts ordinary approved output selections to private backing paths.
func OutputArtifacts(paths []string, outputs []CheckOutput) []string {
	resolved := make([]string, len(paths))
	for i, p := range paths {
		resolved[i] = p
		for _, o := range outputs {
			if under(p, o.Path) {
				resolved[i] = filepath.Join(".runner-output", "outputs", p)
				break
			}
		}
	}
	return resolved
}

func ValidateOutputArtifacts(paths []string, outputs []CheckOutput) error {
	for _, p := range paths {
		if ArtifactPath(p) {
			continue
		}
		allowed := false
		for _, o := range outputs {
			if LiteralPath(p) && under(p, o.Path) {
				allowed = true
			}
		}
		if !allowed {
			return fmt.Errorf("artifact %q must be below .runner-output or a declared output", p)
		}
	}
	return ValidateArtifactPaths(OutputArtifacts(paths, outputs))
}
