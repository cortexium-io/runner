// runner-source-handoff materializes an authenticated retained source snapshot.
// Runner invokes this fixed command as the first writable continuation step.
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

const markerName = ".runner-source-handoff.json"

type sourceSpec struct {
	Repository  string `json:"repository"`
	Base        string `json:"base"`
	Tree        string `json:"tree"`
	Patch       string `json:"patch"`
	PatchSHA256 string `json:"patch_sha256"`
	Checks      string `json:"checks"`
	CheckSHA256 string `json:"check_sha256"`
	Destination string `json:"destination"`
	State       string `json:"coordinator_state"`
	Exclude     string `json:"exclude,omitempty"`
	Include     string `json:"include,omitempty"`
}

type sourceReceipt struct {
	Identity         string     `json:"identity"`
	Phase            string     `json:"phase"`
	Source           sourceSpec `json:"source"`
	Commit           string     `json:"initial_commit,omitempty"`
	MaterializedTree string     `json:"materialized_tree"`
	Files            int        `json:"files"`
	Bytes            int64      `json:"bytes"`
}

type sourceFile struct {
	name string
	mode fs.FileMode
	hash string
	size int64
}

func main() {
	var spec sourceSpec
	f := flag.NewFlagSet("runner-source-handoff", flag.ExitOnError)
	f.StringVar(&spec.Repository, "repository", "", "retained read-only repository")
	f.StringVar(&spec.Base, "base", "", "authenticated original base commit")
	f.StringVar(&spec.Tree, "tree", "", "authenticated reconstructed source tree")
	f.StringVar(&spec.Patch, "patch", "", "declared canonical patch input")
	f.StringVar(&spec.PatchSHA256, "patch-sha256", "", "authenticated patch digest")
	f.StringVar(&spec.Checks, "checks", "", "declared canonical passing check input")
	f.StringVar(&spec.CheckSHA256, "check-sha256", "", "authenticated passing check digest")
	f.StringVar(&spec.Destination, "destination", "", "owned external project workspace")
	f.StringVar(&spec.State, "state", "", "coordinator state excluded from materialization")
	f.StringVar(&spec.Exclude, "exclude", "", "comma-separated unchanged historical directories to retain only at origin")
	f.StringVar(&spec.Include, "include", "", "exact tracked fixture files needed from excluded directories")
	output := f.String("output", "", "owned command scratch directory")
	f.Parse(os.Args[1:])
	err := run(context.Background(), spec, *output)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, spec sourceSpec, output string) error {
	if !filepath.IsAbs(output) {
		return errors.New("handoff output requires an absolute owned scratch directory")
	}
	_, err := materialize(ctx, spec, output, 96<<20)
	return err
}

func materialize(ctx context.Context, spec sourceSpec, output string, limit int64) (sourceReceipt, error) {
	var receipt sourceReceipt
	selection, err := projectionSelection(spec.Exclude, spec.Include)
	if err != nil {
		return receipt, err
	}
	for _, value := range []string{spec.Base, spec.Tree} {
		if !hexIdentity(value, 40) && !hexIdentity(value, 64) {
			return receipt, errors.New("source handoff requires exact base and tree identities")
		}
	}
	for _, path := range []*string{&spec.Repository, &spec.Destination, &spec.State} {
		if !filepath.IsAbs(*path) {
			return receipt, errors.New("source handoff paths must be absolute")
		}
		canonical, err := filepath.EvalSymlinks(*path)
		if err != nil {
			return receipt, err
		}
		*path = canonical
	}
	if overlapping(spec.Repository, spec.Destination) || overlapping(spec.State, spec.Destination) {
		return receipt, errors.New("source handoff workspace overlaps retained source or coordinator state")
	}
	patch, err := verifiedInput(spec.Patch, spec.PatchSHA256, 16<<20)
	if err != nil {
		return receipt, fmt.Errorf("retained patch: %w", err)
	}
	if _, err := verifiedInput(spec.Checks, spec.CheckSHA256, 16<<20); err != nil {
		return receipt, fmt.Errorf("retained check: %w", err)
	}
	encoded, err := json.Marshal(spec)
	if err != nil {
		return receipt, err
	}
	identity := digest(encoded)
	marker := filepath.Join(spec.Destination, markerName)
	if data, readErr := regularInput(marker, 16<<10); readErr == nil {
		if json.Unmarshal(data, &receipt) != nil || receipt.Identity != identity || receipt.Source != spec {
			return receipt, errors.New("workspace handoff ownership differs; preserve existing files")
		}
		if receipt.Phase == "ready" {
			// Later native writers own their edits. An infrastructure replay must
			// never restore the initial snapshot over those edits.
			return receipt, writeOutputs(output, receipt)
		}
		if receipt.Phase != "importing" {
			return receipt, errors.New("unknown source handoff phase; preserve existing files")
		}
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return receipt, readErr
	} else {
		entries, err := os.ReadDir(spec.Destination)
		if err != nil || len(entries) != 0 {
			return receipt, errors.New("source handoff requires an empty unclaimed workspace")
		}
		receipt = sourceReceipt{Identity: identity, Phase: "importing", Source: spec}
		if err := writeJSON(marker, receipt); err != nil {
			return receipt, err
		}
	}
	validation, err := os.MkdirTemp(output, "source-validation-")
	if err != nil {
		return receipt, err
	}
	defer os.RemoveAll(validation)
	objects := filepath.Join(validation, "objects")
	if err := os.Mkdir(objects, 0700); err != nil {
		return receipt, err
	}
	originalObjects, err := git(ctx, spec.Repository, nil, nil, 4096, "rev-parse", "--path-format=absolute", "--git-path", "objects")
	if err != nil {
		return receipt, err
	}
	env := []string{
		"GIT_INDEX_FILE=" + filepath.Join(validation, "index"),
		"GIT_OBJECT_DIRECTORY=" + objects,
		"GIT_ALTERNATE_OBJECT_DIRECTORIES=" + strings.TrimSpace(string(originalObjects)),
	}
	if _, err := git(ctx, spec.Repository, env, nil, 4096, "read-tree", spec.Base); err != nil {
		return receipt, err
	}
	if _, err := git(ctx, spec.Repository, env, patch, 4096, "apply", "--cached", "--binary"); err != nil {
		return receipt, err
	}
	tree, err := git(ctx, spec.Repository, env, nil, 256, "write-tree")
	if err != nil || strings.TrimSpace(string(tree)) != spec.Tree {
		return receipt, errors.New("retained patch does not reconstruct its authenticated tree")
	}
	changed, err := git(ctx, spec.Repository, env, nil, 2<<20, "diff", "--cached", "--name-only", "-z", "--no-ext-diff", "--no-textconv", spec.Base)
	if err != nil {
		return receipt, err
	}
	for _, name := range bytes.Split(changed, []byte{0}) {
		if !selection.keeps(string(name)) {
			return receipt, errors.New("historical exclusions would omit retained changes; preserve the full source")
		}
	}
	listing, err := git(ctx, spec.Repository, env, nil, 4<<20, "ls-tree", "-r", "-z", "--long", spec.Tree)
	if err != nil {
		return receipt, err
	}
	files, total, err := sourceFiles(listing, limit, selection)
	if err != nil {
		return receipt, err
	}
	materializedTree := spec.Tree
	if len(selection.exclude) > 0 {
		var selected bytes.Buffer
		for _, file := range files {
			mode := "100644"
			if file.mode == 0755 {
				mode = "100755"
			}
			fmt.Fprintf(&selected, "%s %s\t%s%c", mode, file.hash, file.name, 0)
		}
		if _, err := git(ctx, spec.Repository, env, nil, 256, "read-tree", "--empty"); err != nil {
			return receipt, err
		}
		if _, err := git(ctx, spec.Repository, env, selected.Bytes(), 4096, "update-index", "-z", "--index-info"); err != nil {
			return receipt, err
		}
		projected, err := git(ctx, spec.Repository, env, nil, 256, "write-tree")
		if err != nil {
			return receipt, err
		}
		materializedTree = strings.TrimSpace(string(projected))
	}
	if err := validatePartial(spec.Destination, files); err != nil {
		return receipt, err
	}
	gitDir := filepath.Join(spec.Destination, ".git")
	if info, err := os.Lstat(gitDir); err == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
		return receipt, errors.New("handoff repository metadata is not an owned directory")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return receipt, err
	}
	format := "sha1"
	if len(spec.Tree) == 64 {
		format = "sha256"
	}
	if _, err := git(ctx, spec.Destination, nil, nil, 4096, "init", "--quiet", "--object-format="+format); err != nil {
		return receipt, err
	}
	exclude := filepath.Join(gitDir, "info", "exclude")
	ignored, err := os.ReadFile(exclude)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return receipt, err
	}
	if !bytes.Contains(ignored, []byte(markerName)) {
		if err := os.WriteFile(exclude, append(ignored, []byte("\n/"+markerName+"\n")...), 0600); err != nil {
			return receipt, err
		}
	}
	parts := filepath.Join(gitDir, "runner-source-parts")
	if err := os.MkdirAll(parts, 0700); err != nil {
		return receipt, err
	}
	defer os.RemoveAll(parts)
	var hashes, names bytes.Buffer
	for _, file := range files {
		fmt.Fprintln(&hashes, file.hash)
		names.WriteString(file.name)
		names.WriteByte(0)
	}
	blobs, err := git(ctx, spec.Repository, env, hashes.Bytes(), limit+2<<20, "cat-file", "--batch")
	if err != nil {
		return receipt, err
	}
	reader := bufio.NewReader(bytes.NewReader(blobs))
	for _, file := range files {
		header, err := reader.ReadString('\n')
		fields := strings.Fields(header)
		if err != nil || len(fields) != 3 || fields[0] != file.hash || fields[1] != "blob" || fields[2] != strconv.FormatInt(file.size, 10) {
			return receipt, errors.New("retained source blob metadata differs")
		}
		data := make([]byte, file.size)
		if _, err := io.ReadFull(reader, data); err != nil {
			return receipt, err
		}
		if end, err := reader.ReadByte(); err != nil || end != '\n' {
			return receipt, errors.New("retained source blob framing differs")
		}
		if err := installSourceFile(spec.Destination, parts, file, data); err != nil {
			return receipt, err
		}
	}
	if _, err := git(ctx, spec.Destination, nil, names.Bytes(), 4096, "--literal-pathspecs", "add", "--force", "--pathspec-from-file=-", "--pathspec-file-nul"); err != nil {
		return receipt, err
	}
	actual, err := git(ctx, spec.Destination, nil, nil, 256, "write-tree")
	if err != nil || strings.TrimSpace(string(actual)) != materializedTree {
		return receipt, errors.New("materialized source tree differs; preserve owned workspace")
	}
	if headTree, err := git(ctx, spec.Destination, nil, nil, 256, "rev-parse", "--verify", "HEAD^{tree}"); err == nil {
		if strings.TrimSpace(string(headTree)) != materializedTree {
			return receipt, errors.New("partial handoff has an unrelated commit; preserve it")
		}
	} else if _, err := git(ctx, spec.Destination, nil, nil, 4096, "-c", "user.name=Cortexium Runner", "-c", "user.email=runner@localhost", "commit", "--quiet", "--allow-empty", "-m", "Retained source snapshot for verified preview"); err != nil {
		return receipt, err
	}
	commit, err := git(ctx, spec.Destination, nil, nil, 256, "rev-parse", "HEAD")
	if err != nil {
		return receipt, err
	}
	receipt.Phase, receipt.Commit, receipt.Files, receipt.Bytes = "ready", strings.TrimSpace(string(commit)), len(files), total
	receipt.MaterializedTree = materializedTree
	if err := writeJSON(marker, receipt); err != nil {
		return receipt, err
	}
	return receipt, writeOutputs(output, receipt)
}

func sourceFiles(listing []byte, limit int64, selections ...sourceSelection) ([]sourceFile, int64, error) {
	var selection sourceSelection
	if len(selections) > 0 {
		selection = selections[0]
	}
	files := []sourceFile{}
	seen := map[string]bool{}
	present := map[string]bool{}
	var total int64
	for _, entry := range bytes.Split(listing, []byte{0}) {
		if len(entry) == 0 {
			continue
		}
		meta, name, ok := strings.Cut(string(entry), "\t")
		fields := strings.Fields(meta)
		if !ok || !fs.ValidPath(name) || strings.Contains(name, "\\") || len(fields) != 4 {
			return nil, 0, errors.New("source snapshot has an unsafe path or metadata")
		}
		if !selection.keeps(name) {
			continue
		}
		if fields[1] != "blob" || (fields[0] != "100644" && fields[0] != "100755") {
			return nil, 0, errors.New("source snapshot has an unsafe path, symlink or unsupported file mode")
		}
		for _, part := range strings.Split(name, "/") {
			if strings.EqualFold(part, ".git") || strings.EqualFold(part, markerName) {
				return nil, 0, errors.New("source snapshot overlaps handoff metadata")
			}
		}
		key := name
		if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
			key = strings.ToLower(key)
		}
		if seen[key] {
			return nil, 0, errors.New("source snapshot paths collide on this host")
		}
		seen[key] = true
		present[name] = true
		size, err := strconv.ParseInt(fields[3], 10, 64)
		if err != nil || size < 0 || size > 16<<20 || total+size > limit {
			return nil, 0, errors.New("source snapshot exceeds its bounded materialization size")
		}
		total += size
		mode := fs.FileMode(0644)
		if fields[0] == "100755" {
			mode = 0755
		}
		files = append(files, sourceFile{name: name, mode: mode, hash: fields[2], size: size})
	}
	for name := range selection.include {
		if !present[name] {
			return nil, 0, errors.New("required fixture is absent from the authenticated source tree")
		}
	}
	return files, total, nil
}

type sourceSelection struct {
	exclude []string
	include map[string]bool
}

func projectionSelection(excluded, included string) (sourceSelection, error) {
	paths, err := exclusionPaths(excluded)
	selection := sourceSelection{exclude: paths, include: map[string]bool{}}
	if err != nil || included == "" {
		return selection, err
	}
	files := strings.Split(included, ",")
	if len(files) > 64 {
		return selection, errors.New("too many explicit fixture files in source projection")
	}
	for _, name := range files {
		if !fs.ValidPath(name) || !excludedSource(name, paths) || selection.include[name] {
			return selection, errors.New("fixture exceptions must be unique exact files inside excluded history")
		}
		selection.include[name] = true
	}
	return selection, nil
}

func (s sourceSelection) keeps(name string) bool {
	return s.include[name] || !excludedSource(name, s.exclude)
}

func exclusionPaths(value string) ([]string, error) {
	if value == "" {
		return nil, nil
	}
	paths := strings.Split(value, ",")
	if len(paths) > 16 {
		return nil, errors.New("too many source projection exclusions")
	}
	for i, path := range paths {
		if !fs.ValidPath(path) || path == "." || strings.Contains(path, "\\") || strings.HasPrefix(path, ".git") {
			return nil, errors.New("source exclusions need literal relative directories")
		}
		for _, previous := range paths[:i] {
			if excludedSource(path, []string{previous}) || excludedSource(previous, []string{path}) {
				return nil, errors.New("source exclusions overlap")
			}
		}
	}
	return paths, nil
}

func excludedSource(name string, paths []string) bool {
	for _, path := range paths {
		if name == path || strings.HasPrefix(name, path+"/") {
			return true
		}
	}
	return false
}

func validatePartial(root string, files []sourceFile) error {
	known := map[string]bool{markerName: true}
	for _, file := range files {
		known[file.name] = true
		for dir := filepath.ToSlash(filepath.Dir(file.name)); dir != "."; dir = filepath.ToSlash(filepath.Dir(dir)) {
			known[dir] = true
		}
	}
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || path == root {
			return err
		}
		name, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		name = filepath.ToSlash(name)
		if name == ".git" && entry.IsDir() && entry.Type()&os.ModeSymlink == 0 {
			return filepath.SkipDir
		}
		if !known[name] || entry.Type()&os.ModeSymlink != 0 {
			return errors.New("partial handoff contains unrelated files; preserve them")
		}
		return nil
	})
}

func installSourceFile(root, parts string, file sourceFile, data []byte) error {
	path := filepath.Join(root, filepath.FromSlash(file.name))
	if old, err := os.ReadFile(path); err == nil {
		info, statErr := os.Lstat(path)
		if statErr != nil || !info.Mode().IsRegular() || !bytes.Equal(old, data) || info.Mode().Perm() != file.mode {
			return errors.New("partial handoff source was edited; preserve it")
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(parts, "source-")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if _, err = tmp.Write(data); err == nil {
		err = tmp.Chmod(file.mode)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if err == nil {
		err = tmp.Close()
	}
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	return err
}

func verifiedInput(path, want string, limit int64) ([]byte, error) {
	if !filepath.IsAbs(path) || !hexIdentity(want, 64) {
		return nil, errors.New("input needs an absolute canonical path and exact SHA256")
	}
	data, err := regularInput(path, limit)
	if err != nil {
		return nil, err
	}
	if digest(data) != want {
		return nil, errors.New("input differs from authenticated artifact digest")
	}
	return data, nil
}

func regularInput(path string, limit int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return nil, errors.New("input is not a bounded regular artifact")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, errors.New("input identity changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, errors.New("input could not be read within its artifact bound")
	}
	return data, nil
}

type boundedBuffer struct {
	bytes.Buffer
	limit int64
}

func (b *boundedBuffer) Write(data []byte) (int, error) {
	if int64(b.Len()+len(data)) > b.limit {
		return 0, errors.New("source operation output exceeds its bound")
	}
	return b.Buffer.Write(data)
}

func git(ctx context.Context, repo string, extra []string, input []byte, limit int64, args ...string) ([]byte, error) {
	argv := append([]string{"-C", repo, "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "core.autocrlf=false"}, args...)
	command := exec.CommandContext(ctx, "/usr/bin/git", argv...)
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "GIT_") {
			command.Env = append(command.Env, value)
		}
	}
	command.Env = append(command.Env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0", "GIT_NO_LAZY_FETCH=1", "GIT_ATTR_NOSYSTEM=1")
	command.Env = append(command.Env, extra...)
	command.Stdin = bytes.NewReader(input)
	out, diagnostic := &boundedBuffer{limit: limit}, &boundedBuffer{limit: 4096}
	command.Stdout, command.Stderr = out, diagnostic
	if err := command.Run(); err != nil {
		return nil, fmt.Errorf("source git %s failed: %w: %s", args[0], err, diagnostic.String())
	}
	return out.Bytes(), nil
}

func writeOutputs(output string, receipt sourceReceipt) error {
	if err := writeJSON(filepath.Join(output, "handoff.json"), receipt); err != nil {
		return err
	}
	return writeJSON(filepath.Join(output, "domain.json"), map[string]string{"outcome": "ready", "reason": "Authenticated retained source materialized; prior checks remain bound to the initial tree, not later edits"})
}

func writeJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".handoff-write-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if err = file.Chmod(0600); err == nil {
		_, err = file.Write(append(data, '\n'))
	}
	if err == nil {
		err = file.Sync()
	}
	if err == nil {
		err = file.Close()
	}
	if err == nil {
		err = os.Rename(file.Name(), path)
	}
	if err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func overlapping(a, b string) bool {
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		a, b = strings.ToLower(a), strings.ToLower(b)
	}
	return a == b || strings.HasPrefix(a, b+string(filepath.Separator)) || strings.HasPrefix(b, a+string(filepath.Separator))
}

func hexIdentity(value string, length int) bool {
	_, err := hex.DecodeString(value)
	return err == nil && len(value) == length && value == strings.ToLower(value)
}

func digest(data []byte) string { value := sha256.Sum256(data); return hex.EncodeToString(value[:]) }
