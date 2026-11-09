package execution

import (
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/cortexium-io/runner/internal/runner"
)

const maxRootAttributes = 64 << 10

// Only built-in rules needed by ordinary text-normalized repositories are
// supported. Patterns and comments are retained verbatim, never rewritten.
func validateRootAttributes(data []byte) error {
	if len(data) > maxRootAttributes || !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return errors.New("root Git attributes exceed the supported text contract")
	}
	for _, b := range data {
		if b < 0x20 && b != '\n' && b != '\r' && b != '\t' {
			return errors.New("root Git attributes contain unsupported control bytes")
		}
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 || strings.HasPrefix(fields[0], "!") || strings.HasPrefix(strings.ToLower(fields[0]), "[attr]") || strings.ContainsAny(fields[0], "\\\"\r\t") {
			return errors.New("root Git attributes require an unquoted pattern and supported built-in rules")
		}
		for _, attribute := range fields[1:] {
			if attribute != "text=auto" && attribute != "-whitespace" {
				return errors.New("root Git attributes contain unsupported filters, drivers or transformations")
			}
		}
	}
	return nil
}

// Global/system attributes are disabled in gitCommand. Repository-local
// overrides are private Git metadata, not part of the pinned source tree.
func validateAttributeSources(ctx context.Context, repository string) error {
	_, err := git(ctx, repository, nil, nil, 4096, "config", "--get", "attr.tree")
	if err == nil {
		return &runner.CleanupBlockedError{Reason: "alternate Git attribute trees are unsupported; preserve the owning repository"}
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 {
		return err
	}
	paths, err := git(ctx, repository, nil, nil, 8192, "rev-parse", "--path-format=absolute", "--git-dir", "--git-common-dir")
	if err != nil {
		return err
	}
	for _, root := range strings.Split(strings.TrimSpace(string(paths)), "\n") {
		if !filepath.IsAbs(root) {
			return errors.New("Git attribute metadata root is not absolute")
		}
		infoDir := filepath.Join(root, "info")
		if info, err := os.Lstat(infoDir); err == nil && !info.IsDir() {
			return &runner.CleanupBlockedError{Reason: "Git info directory is not a regular directory; preserve it"}
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		file := filepath.Join(infoDir, "attributes")
		if info, err := os.Lstat(file); err == nil {
			if !info.Mode().IsRegular() || info.Size() != 0 {
				return &runner.CleanupBlockedError{Reason: "repository info attributes are unsupported; preserve the owning repository"}
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func rootAttributes(ctx context.Context, repository string, env []string, tree string) ([]byte, bool, error) {
	raw, err := git(ctx, repository, env, nil, 8192, "ls-tree", "-z", tree, "--", ".gitattributes")
	if err != nil || len(raw) == 0 {
		return nil, false, err
	}
	parts := bytes.SplitN(bytes.TrimSuffix(raw, []byte{0}), []byte{'\t'}, 2)
	if len(parts) != 2 || string(parts[1]) != ".gitattributes" {
		return nil, false, errors.New("root Git attribute identity is invalid")
	}
	meta := strings.Fields(string(parts[0]))
	if len(meta) != 3 || meta[0] != "100644" || meta[1] != "blob" {
		return nil, false, errors.New("root Git attributes must be a regular tracked file")
	}
	data, err := git(ctx, repository, env, nil, maxRootAttributes, "cat-file", "blob", meta[2])
	if err == nil {
		err = validateRootAttributes(data)
	}
	return data, true, err
}

// Check names without asking Git to interpret their attributes. This also finds
// ignored/untracked nested attributes before any attribute-aware diff executes.
func workspaceAttributes(ctx context.Context, r workspaceRecord) (bool, error) {
	if err := validateAttributeSources(ctx, r.WorkDir); err != nil {
		return false, err
	}
	expected, present, err := rootAttributes(ctx, r.Repository, nil, r.Base)
	if err != nil {
		return false, err
	}
	paths, err := git(ctx, r.WorkDir, nil, nil, 1<<20, "ls-files", "--cached", "--others", "-z", "--", ":(icase).gitattributes", ":(icase,glob)**/.gitattributes", ":(exclude).runner-tmp")
	if err != nil {
		return false, err
	}
	for _, raw := range bytes.Split(paths, []byte{0}) {
		if len(raw) == 0 {
			continue
		}
		if string(raw) != ".gitattributes" || !present {
			return false, &runner.CleanupBlockedError{Reason: "workspace adds or changes unsupported Git attribute paths; preserve it"}
		}
	}
	info, err := os.Lstat(filepath.Join(r.WorkDir, ".gitattributes"))
	if errors.Is(err, os.ErrNotExist) {
		if present {
			return false, &runner.CleanupBlockedError{Reason: "workspace root Git attributes are missing; preserve it"}
		}
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !present || !info.Mode().IsRegular() || info.Size() > maxRootAttributes || info.Mode()&0111 != 0 {
		return false, &runner.CleanupBlockedError{Reason: "workspace root Git attribute identity changed; preserve it"}
	}
	data, mode, err := captureSourceFile(r.WorkDir, ".gitattributes", maxRootAttributes)
	if err != nil {
		return false, err
	}
	if mode != "100644" || !bytes.Equal(data, expected) {
		return false, &runner.CleanupBlockedError{Reason: "workspace root Git attributes differ from immutable source; preserve it"}
	}
	return present, nil
}

type literalSourceReader struct {
	ctx  context.Context
	file *os.File
}

func (r literalSourceReader) Read(data []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.file.Read(data)
}

func literalSourceBlob(ctx context.Context, root *os.Root, name string, width int, expectedSize int64, buffer []byte) (string, string, error) {
	parent := ""
	parts := strings.Split(name, "/")
	for _, part := range parts[:len(parts)-1] {
		parent = filepath.Join(parent, part)
		info, err := root.Lstat(parent)
		if errors.Is(err, os.ErrNotExist) {
			return "0", "", nil
		}
		if err != nil {
			return "", "", err
		}
		if !info.IsDir() {
			return "", "", errors.New("literal source parent is not a regular directory")
		}
	}
	info, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return "0", "", nil
	}
	if err != nil {
		return "", "", err
	}
	if !info.Mode().IsRegular() {
		return "", "", errors.New("literal source must contain regular files")
	}
	mode := "100644"
	if info.Mode()&0111 != 0 {
		mode = "100755"
	}
	if info.Size() != expectedSize {
		return mode, "", nil // Different size already proves different bytes.
	}
	file, err := root.Open(name)
	if err != nil {
		return "", "", err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return "", "", errors.New("literal source changed while opening")
	}
	digest := sha1.New()
	if width == 64 {
		digest = sha256.New()
	}
	fmt.Fprintf(digest, "blob %d%c", info.Size(), 0)
	n, err := io.CopyBuffer(digest, io.LimitReader(literalSourceReader{ctx, file}, info.Size()+1), buffer)
	if err != nil {
		return "", "", err
	}
	after, err := file.Stat()
	if err != nil || n != info.Size() || !os.SameFile(info, after) || info.Mode() != after.Mode() || info.Size() != after.Size() || !info.ModTime().Equal(after.ModTime()) {
		return "", "", errors.New("literal source changed while hashing")
	}
	return mode, hex.EncodeToString(digest.Sum(nil)), nil
}

// A normalized Git diff can declare CRLF/LF bytes equivalent. Attribute-bearing
// workspaces therefore compare literal blob identities against the expected
// tree, including unchanged files larger than the per-artifact patch ceiling.
func literalAttributeChanges(ctx context.Context, r workspaceRecord, env []string, tree string) ([]string, error) {
	raw, err := git(ctx, r.Repository, env, nil, 8<<20, "ls-tree", "-rlz", tree)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(r.WorkDir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	buffer := make([]byte, 64<<10)
	var changed []string
	for _, entry := range bytes.Split(raw, []byte{0}) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if len(entry) == 0 {
			continue
		}
		parts := bytes.SplitN(entry, []byte{'\t'}, 2)
		if len(parts) != 2 || !fs.ValidPath(string(parts[1])) {
			return nil, errors.New("literal source tree path is invalid")
		}
		meta := strings.Fields(string(parts[0]))
		if len(meta) != 4 || (meta[0] != "100644" && meta[0] != "100755") || meta[1] != "blob" || len(meta[2]) != len(r.Base) {
			return nil, errors.New("literal source tree mode or object is unsupported")
		}
		size, err := treeEntrySize(parts[0])
		if err != nil {
			return nil, err
		}
		name := string(parts[1])
		mode, object, err := literalSourceBlob(ctx, root, name, len(r.Base), size, buffer)
		if err != nil {
			return nil, err
		}
		if mode != meta[0] || object != meta[2] {
			changed = append(changed, name)
		}
	}
	return changed, nil
}
