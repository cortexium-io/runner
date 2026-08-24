package workspace

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/cortexium-io/runner/internal/securefs"
)

type pinnedSnapshotPath struct {
	directory *securefs.Directory
	scope     string
	path      string
	digest    []byte
	pinnedDir *securefs.Directory
}

type replacementReference struct {
	objectID string
	ref      string
}

var defaultGitHookNames = []string{
	"applypatch-msg",
	"commit-msg",
	"fsmonitor-watchman",
	"p4-changelist",
	"p4-post-changelist",
	"p4-prepare-changelist",
	"p4-pre-submit",
	"post-applypatch",
	"post-checkout",
	"post-commit",
	"post-index-change",
	"post-merge",
	"post-receive",
	"post-rewrite",
	"post-update",
	"pre-applypatch",
	"pre-auto-gc",
	"pre-commit",
	"pre-merge-commit",
	"pre-push",
	"pre-rebase",
	"pre-receive",
	"prepare-commit-msg",
	"proc-receive",
	"push-to-checkout",
	"reference-transaction",
	"sendemail-validate",
	"update",
}

func (s *gitControlSnapshot) pinProtectedGitMetadata() error {
	commonPaths := []string{
		"info/attributes",
		"info/exclude",
		"info/grafts",
		"objects/info/alternates",
		"objects/info/http-alternates",
	}
	for _, name := range defaultGitHookNames {
		commonPaths = append(commonPaths, "hooks/"+name)
	}
	for _, path := range commonPaths {
		if err := s.pinProtectedPath(s.commonDirectory, "common", path); err != nil {
			return fmt.Errorf("pin protected common Git metadata %q: %w", path, err)
		}
	}
	if err := s.pinProtectedDirectory(s.commonDirectory, "common", "refs/replace"); err != nil {
		return fmt.Errorf("pin protected common Git metadata %q: %w", "refs/replace", err)
	}
	if err := s.pinProtectedPath(s.gitDirectory, "worktree", "info/sparse-checkout"); err != nil {
		return fmt.Errorf("pin protected worktree Git metadata %q: %w", "info/sparse-checkout", err)
	}
	return nil
}

func (s *gitControlSnapshot) pinProtectedDirectory(directory *securefs.Directory, scope, path string) error {
	digest, err := directory.HashPathWithBudget(path, s.budget)
	if err != nil {
		return err
	}
	pinned, err := openRelativeSnapshotDirectory(directory, path)
	if errors.Is(err, os.ErrNotExist) {
		s.protectedMetadata = append(s.protectedMetadata, pinnedSnapshotPath{
			directory: directory,
			scope:     scope,
			path:      path,
			digest:    digest,
		})
		return nil
	}
	if err != nil {
		return err
	}
	s.protectedMetadata = append(s.protectedMetadata, pinnedSnapshotPath{
		directory: directory,
		scope:     scope,
		path:      path,
		digest:    digest,
		pinnedDir: pinned,
	})
	return nil
}

func (s *gitControlSnapshot) pinReplacementRefs(value string) error {
	refs, err := parseReplacementRefs(value)
	if err != nil {
		return err
	}
	for _, ref := range refs {
		if err := s.pinProtectedPath(s.commonDirectory, "common", ref); err != nil {
			return fmt.Errorf("pin replacement reference %q: %w", ref, err)
		}
	}
	return nil
}

func (s *gitControlSnapshot) finishReplacementRefs(value string) (string, error) {
	effective, err := canonicalReplacementRefs(value)
	if err != nil {
		return "", err
	}
	initialPacked, err := packedReplacementRefs(s.packedReplacementRefs)
	if err != nil {
		return "", fmt.Errorf("parse pinned packed replacement references: %w", err)
	}
	currentPacked, err := readPinnedControlFile(s.commonDirectory, "packed-refs", s.budget)
	if err != nil {
		return "", fmt.Errorf("verify packed replacement references: %w", err)
	}
	verifiedPacked, err := packedReplacementRefs(currentPacked)
	if err != nil {
		return "", fmt.Errorf("parse current packed replacement references: %w", err)
	}
	if initialPacked != verifiedPacked {
		return "", fmt.Errorf("%w while using packed replacement references", securefs.ErrChanged)
	}
	manifest := strings.Builder{}
	manifest.WriteString("effective\x00")
	manifest.WriteString(effective)
	manifest.WriteString("packed\x00")
	manifest.WriteString(initialPacked)
	return digestString([]byte(manifest.String())), nil
}

func (s *gitControlSnapshot) pinProtectedPath(directory *securefs.Directory, scope, path string) error {
	digest, err := directory.HashPathWithBudget(path, s.budget)
	if err != nil {
		return err
	}
	s.protectedMetadata = append(s.protectedMetadata, pinnedSnapshotPath{
		directory: directory,
		scope:     scope,
		path:      path,
		digest:    digest,
	})
	return nil
}

func (s *gitControlSnapshot) finishProtectedGitMetadata() (string, error) {
	paths := append([]pinnedSnapshotPath(nil), s.protectedMetadata...)
	sort.Slice(paths, func(left, right int) bool {
		if paths[left].scope == paths[right].scope {
			return paths[left].path < paths[right].path
		}
		return paths[left].scope < paths[right].scope
	})
	manifest := strings.Builder{}
	for _, path := range paths {
		current, err := path.directory.HashPathWithBudget(path.path, s.budget)
		if err != nil {
			return "", fmt.Errorf("verify protected %s Git metadata %q: %w", path.scope, path.path, err)
		}
		if !bytes.Equal(current, path.digest) {
			return "", fmt.Errorf("%w while using protected %s Git metadata %q", securefs.ErrChanged, path.scope, path.path)
		}
		if path.pinnedDir != nil {
			if err := path.pinnedDir.Verify(); err != nil {
				return "", fmt.Errorf("verify protected %s Git metadata directory %q: %w", path.scope, path.path, err)
			}
		}
		manifest.WriteString(path.scope)
		manifest.WriteByte(0)
		manifest.WriteString(path.path)
		manifest.WriteByte(0)
		manifest.WriteString(digestString(path.digest))
		manifest.WriteByte(0)
	}
	return digestString([]byte(manifest.String())), nil
}

func parseReplacementRefs(value string) ([]string, error) {
	records, err := parseReplacementRefRecords(value)
	if err != nil {
		return nil, err
	}
	refs := make([]string, 0, len(records))
	for _, record := range records {
		refs = append(refs, record.ref)
	}
	return refs, nil
}

func canonicalReplacementRefs(value string) (string, error) {
	records, err := parseReplacementRefRecords(value)
	if err != nil {
		return "", err
	}
	var canonical strings.Builder
	for _, record := range records {
		canonical.WriteString(record.objectID)
		canonical.WriteByte(' ')
		canonical.WriteString(record.ref)
		canonical.WriteByte('\n')
	}
	return canonical.String(), nil
}

func parseReplacementRefRecords(value string) ([]replacementReference, error) {
	if value == "" {
		return nil, nil
	}
	if !strings.HasSuffix(value, "\n") {
		return nil, errors.New("Git returned unterminated replacement references")
	}
	lines := strings.Split(strings.TrimSuffix(value, "\n"), "\n")
	refs := make([]replacementReference, 0, len(lines))
	seen := map[string]struct{}{}
	for _, record := range lines {
		separator := strings.IndexByte(record, ' ')
		if separator < 0 {
			return nil, errors.New("Git returned an invalid replacement reference")
		}
		objectID, err := parseGitObjectID([]byte(record[:separator]+"\n"), "replacement reference object")
		if err != nil {
			return nil, err
		}
		ref := record[separator+1:]
		if !strings.HasPrefix(ref, "refs/replace/") || strings.ContainsAny(ref, "\x00\r") {
			return nil, errors.New("Git returned an invalid replacement reference")
		}
		components := strings.Split(ref, "/")
		for _, component := range components {
			if component == "" || component == "." || component == ".." {
				return nil, errors.New("Git returned an invalid replacement reference")
			}
		}
		if _, duplicate := seen[ref]; duplicate {
			return nil, errors.New("Git returned a duplicate replacement reference")
		}
		seen[ref] = struct{}{}
		refs = append(refs, replacementReference{objectID: objectID, ref: ref})
	}
	sort.Slice(refs, func(left, right int) bool { return refs[left].ref < refs[right].ref })
	return refs, nil
}

func packedReplacementRefs(file pinnedControlFile) (string, error) {
	if !file.state.Exists {
		return "", nil
	}
	if !bytes.HasSuffix(file.content, []byte("\n")) {
		return "", errors.New("packed references are not newline terminated")
	}
	var records strings.Builder
	for _, line := range bytes.Split(bytes.TrimSuffix(file.content, []byte("\n")), []byte("\n")) {
		if len(line) == 0 || line[0] == '#' || line[0] == '^' {
			continue
		}
		separator := bytes.IndexByte(line, ' ')
		if separator < 0 {
			return "", errors.New("packed references contain an invalid record")
		}
		if !bytes.HasPrefix(line[separator+1:], []byte("refs/replace/")) {
			continue
		}
		records.Write(line)
		records.WriteByte('\n')
	}
	return canonicalReplacementRefs(records.String())
}
