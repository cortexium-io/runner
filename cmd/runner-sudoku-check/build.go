package main

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// These are build inputs, not a second archived source checkout. Imported JSON
// fixtures are added explicitly; historical evidence and dependencies stay out.
var sourceRoots = []string{"cmd", "internal", "server", "migrations", ".genny", "frontend", "go.mod", "go.sum"}

func skipSource(name string) bool {
	base := filepath.Base(name)
	return base == "node_modules" || base == ".git" || base == ".DS_Store" || base == ".qualification" || base == "coverage" ||
		name == "internal/app/http/frontend_dist" || name == "frontend/.genny/static-pages" || name == "frontend/dist" ||
		strings.HasSuffix(base, ".log") || strings.HasSuffix(base, ".tsbuildinfo")
}

func inventory(root string) ([]fileProof, string, error) {
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, "", err
	}
	names := map[string]bool{}
	var total int64
	for _, prefix := range sourceRoots {
		if _, err := os.Lstat(filepath.Join(root, prefix)); errors.Is(err, os.ErrNotExist) && (prefix == "server" || prefix == "migrations" || prefix == ".genny") {
			continue
		}
		err := filepath.WalkDir(filepath.Join(root, prefix), func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			name, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			name = filepath.ToSlash(name)
			if skipSource(name) {
				if entry.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if entry.IsDir() {
				return nil
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return fmt.Errorf("source input must be a regular file, not a symlink: %s", name)
			}
			if strings.HasPrefix(entry.Name(), ".env") || strings.HasSuffix(name, ".pem") || strings.HasSuffix(name, ".key") {
				return errors.New("credential/environment source files are not build inputs")
			}
			total += info.Size()
			if total > 128<<20 || len(names) >= 16000 {
				return errors.New("minimal Sudoku build input exceeds 128MiB/16000 files")
			}
			names[name] = true
			return nil
		})
		if err != nil {
			return nil, "", err
		}
	}
	imports := regexp.MustCompile(`(?:from\s*|import\s*\()['"]([^'"\n]+\.json)['"]`)
	for name := range names {
		if !strings.HasPrefix(name, "frontend/") || !(strings.HasSuffix(name, ".ts") || strings.HasSuffix(name, ".vue") || strings.HasSuffix(name, ".mjs") || strings.HasSuffix(name, ".js")) {
			continue
		}
		data, err := readBounded(filepath.Join(root, filepath.FromSlash(name)), 4<<20)
		if err != nil {
			return nil, "", err
		}
		for _, match := range imports.FindAllSubmatch(data, -1) {
			ref := string(match[1])
			if !strings.HasPrefix(ref, ".") {
				continue
			}
			target := filepath.Join(filepath.Dir(name), filepath.FromSlash(ref))
			if !fs.ValidPath(filepath.ToSlash(target)) {
				return nil, "", errors.New("source JSON import escapes the candidate")
			}
			if !strings.HasPrefix(target, "docs/") && !strings.HasPrefix(target, "testdata/") {
				continue
			}
			canonical, err := filepath.EvalSymlinks(filepath.Join(root, target))
			if err != nil || !nested(canonicalRoot, canonical) {
				return nil, "", fmt.Errorf("imported source JSON is not an owned candidate file: %s", target)
			}
			names[filepath.ToSlash(target)] = true
		}
	}
	ordered := make([]string, 0, len(names))
	for name := range names {
		ordered = append(ordered, name)
	}
	sort.Strings(ordered)
	files := make([]fileProof, 0, len(ordered))
	for _, name := range ordered {
		path := filepath.Join(root, filepath.FromSlash(name))
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() {
			return nil, "", errors.New("source input changed during inventory")
		}
		hash, err := hashFile(path, 16<<20)
		if err != nil {
			return nil, "", err
		}
		files = append(files, fileProof{Name: name, SHA256: hash, Bytes: info.Size()})
	}
	data, _ := json.Marshal(files)
	return files, digest(data), nil
}

// Candidate identity covers all materialized source/documents, including new
// ordinary untracked edits; it is broader than the temporary build staging.
func candidateInventory(ctx context.Context, root string) ([]fileProof, string, error) {
	data, err := protectedGit(ctx, root, "ls-files", "--cached", "--others", "--exclude-standard", "-z")
	if err != nil {
		return nil, "", fmt.Errorf("candidate file inventory: %w", err)
	}
	names := map[string]bool{}
	for _, name := range strings.Split(string(data), "\x00") {
		if name == "" || name == ".runner-source-handoff.json" {
			continue
		}
		if !fs.ValidPath(name) {
			return nil, "", errors.New("candidate inventory contains an invalid relative path")
		}
		names[name] = true
	}
	ordered := make([]string, 0, len(names))
	for name := range names {
		ordered = append(ordered, name)
	}
	sort.Strings(ordered)
	var files []fileProof
	bound := map[string]fileProof{}
	var total int64
	for _, name := range ordered {
		path := filepath.Join(root, filepath.FromSlash(name))
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() {
			return nil, "", errors.New("candidate identity requires existing regular source files")
		}
		total += info.Size()
		if total > 128<<20 || len(files) >= 16000 {
			return nil, "", errors.New("candidate source identity exceeds 128MiB/16000 files")
		}
		file, err := sourceFile(path, name, info)
		if err != nil {
			return nil, "", err
		}
		files = append(files, file)
		bound[name] = file
	}
	if len(files) == 0 {
		return nil, "", errors.New("candidate source identity is empty")
	}
	// The build copier walks actual files, while Git omits ignored untracked
	// paths. An ignored consumed input must never escape source identity or
	// authorize reuse of the unchanged candidate's frontend proof.
	buildFiles, _, err := inventory(root)
	if err != nil {
		return nil, "", err
	}
	for _, file := range buildFiles {
		candidate, ok := bound[file.Name]
		if !ok {
			return nil, "", fmt.Errorf("ignored build input is outside candidate identity: %s", file.Name)
		}
		if candidate.SHA256 != file.SHA256 || candidate.Bytes != file.Bytes {
			return nil, "", errors.New("candidate changed while binding its build inputs")
		}
	}
	encoded, _ := json.Marshal(files)
	return files, digest(encoded), nil
}

func sourceFile(path, name string, info os.FileInfo) (fileProof, error) {
	if info.Size() > 16<<20 {
		return fileProof{}, errors.New("candidate source file exceeds 16MiB")
	}
	f, err := os.Open(path)
	if err != nil {
		return fileProof{}, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return fileProof{}, errors.New("candidate source changed while opening")
	}
	strong, git := sha256.New(), sha1.New()
	fmt.Fprintf(git, "blob %d\x00", info.Size())
	n, err := io.Copy(io.MultiWriter(strong, git), io.LimitReader(f, info.Size()+1))
	if err != nil || n != info.Size() {
		return fileProof{}, errors.New("candidate source changed while hashing")
	}
	return fileProof{Name: name, SHA256: hex.EncodeToString(strong.Sum(nil)), GitBlob: hex.EncodeToString(git.Sum(nil)), Bytes: info.Size(), Executable: info.Mode().Perm()&0111 != 0}, nil
}

func stageSource(source, destination string, files []fileProof) error {
	if err := os.Mkdir(destination, 0700); err != nil {
		return err
	}
	for _, file := range files {
		from, to := filepath.Join(source, filepath.FromSlash(file.Name)), filepath.Join(destination, filepath.FromSlash(file.Name))
		if err := os.MkdirAll(filepath.Dir(to), 0700); err != nil {
			return err
		}
		if err := copyVerified(from, to, file.SHA256, file.Bytes); err != nil {
			return err
		}
	}
	return nil
}

func copyVerified(source, destination, expected string, size int64) error {
	info, err := os.Lstat(source)
	if err != nil || !info.Mode().IsRegular() || info.Size() != size {
		return errors.New("copy input identity is invalid")
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	opened, err := input.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return errors.New("copy input changed while opening")
	}
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	n, copyErr := io.Copy(output, io.LimitReader(input, size+1))
	if err = errors.Join(copyErr, output.Close()); err != nil {
		return err
	}
	hash, err := hashFile(destination, size)
	if err != nil || n != size || hash != expected {
		return errors.New("staged input differs from its source inventory")
	}
	return nil
}

// Keep installed packages shared and read-only while giving build tools their
// own node_modules cache namespace. Linking the whole directory redirects
// Vite's default config bundle cache into the immutable installed dependencies.
func stageModules(modules, destination string) error {
	canonical, err := filepath.EvalSymlinks(modules)
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(canonical)
	if err != nil {
		return err
	}
	if err := os.Mkdir(destination, 0700); err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") && name != ".bin" && name != ".package-lock.json" {
			continue // Existing shared .vite, .vite-temp and other caches stay out.
		}
		if err := os.Symlink(filepath.Join(canonical, name), filepath.Join(destination, name)); err != nil {
			return err
		}
	}
	return nil
}

func validateModules(source, modules string, facts map[string]string) error {
	candidate, err := readBounded(filepath.Join(source, "frontend/package-lock.json"), 4<<20)
	if err != nil {
		return err
	}
	installed, err := readBounded(filepath.Join(filepath.Dir(modules), "package-lock.json"), 4<<20)
	if err != nil || digest(candidate) != digest(installed) {
		return errors.New("configured installed dependencies do not have the candidate's exact package-lock.json")
	}
	hidden, err := readBounded(filepath.Join(modules, ".package-lock.json"), 4<<20)
	if err != nil {
		return err
	}
	type locked struct{ Version, Integrity string }
	var wanted, actual struct{ Packages map[string]locked }
	if json.Unmarshal(candidate, &wanted) != nil || json.Unmarshal(hidden, &actual) != nil || len(actual.Packages) == 0 {
		return errors.New("installed dependency lock inventory is invalid")
	}
	for name, got := range actual.Packages {
		expected, ok := wanted.Packages[name]
		if !ok || expected != got {
			return fmt.Errorf("installed dependency differs from the candidate lock: %s", name)
		}
	}
	for _, name := range []string{"vite", "vue", "vue-tsc", "typescript", "@vitejs/plugin-vue", "@tailwindcss/vite"} {
		data, err := readBounded(filepath.Join(modules, name, "package.json"), 128<<10)
		var pkg struct{ Version string }
		if err != nil || json.Unmarshal(data, &pkg) != nil || pkg.Version != wanted.Packages["node_modules/"+name].Version {
			return fmt.Errorf("required installed build dependency is missing or mismatched: %s", name)
		}
	}
	canonical, err := filepath.EvalSymlinks(modules)
	if err != nil {
		return err
	}
	facts["frontend_lock_sha256"], facts["installed_lock_sha256"], facts["frontend_modules"] = digest(candidate), digest(hidden), canonical
	return nil
}

func build(ctx context.Context, o options, source, server, identity string) ([]commandProof, error) {
	frontend := filepath.Join(source, "frontend")
	env := map[string]string{"GOTOOLCHAIN": "local", "GOWORK": "off", "CGO_ENABLED": "0", "GOFLAGS": "-mod=readonly", "GOOS": "", "GOARCH": "", "GOEXPERIMENT": ""}
	type invocation struct {
		name, directory, binary string
		args                    []string
	}
	commands := []invocation{
		{"frontend client build", frontend, o.node, []string{filepath.Join(o.modules, "vite/bin/vite.js"), "build", "--configLoader", "runner"}},
		{"frontend static SSR build", frontend, o.node, []string{filepath.Join(o.modules, "vite/bin/vite.js"), "build", "--configLoader", "runner", "--config", "vite.static-pages.ssr.config.gny.ts"}},
		{"frontend prerender", frontend, o.node, []string{filepath.Join(frontend, ".genny/static-pages/prerender.gny.mjs")}},
		{"native SSR graph audit", frontend, o.node, []string{filepath.Join(frontend, "scripts/audit-native-ssr.mjs")}},
		{"native preview build", source, o.goBinary, []string{"build", "-mod=readonly", "-trimpath", "-buildvcs=false", "-ldflags", "-s -w -buildid= -X main.buildIdentity=source-sha256:" + identity, "-o", server, "./cmd/server"}},
	}
	var proof []commandProof
	for _, command := range commands {
		started := time.Now()
		output, err := execute(ctx, command.directory, env, command.binary, command.args...)
		row := commandProof{Name: command.name, DurationMS: time.Since(started).Milliseconds(), Passed: err == nil}
		if err != nil {
			row.Diagnostic = clip(string(output), 4000)
		}
		proof = append(proof, row)
		if err != nil {
			return proof, fmt.Errorf("%s failed: %v; %s", command.name, err, row.Diagnostic)
		}
	}
	return proof, nil
}
