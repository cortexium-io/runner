package workspace

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cortexium-io/runner/internal/securefs"
)

const gitControlFileLimit = 16 << 20

type pinnedControlFile struct {
	directory *securefs.Directory
	name      string
	state     securefs.FileState
	content   []byte
}

type gitControlSnapshot struct {
	root                  *securefs.Directory
	budget                *securefs.SnapshotBudget
	gitDirectory          *securefs.Directory
	commonDirectory       *securefs.Directory
	gitDirPath            string
	commonDirPath         string
	rootPath              string
	gitMarker             []byte
	gitMarkerFile         *pinnedControlFile
	commonLink            pinnedControlFile
	registrationLink      pinnedControlFile
	head                  pinnedControlFile
	headState             pinnedHEADState
	index                 *securefs.PinnedFile
	indexMissing          bool
	commonConfig          pinnedControlFile
	packedReplacementRefs pinnedControlFile
	worktreeConfigEnabled bool
	worktreeConfig        *pinnedControlFile
	protectedMetadata     []pinnedSnapshotPath
}

type pinnedHEADState struct {
	budget      *securefs.SnapshotBudget
	target      string
	objectID    string
	directories []*securefs.Directory
	loose       []pinnedReference
	packed      *pinnedControlFile
}

type pinnedReference struct {
	target string
	file   pinnedControlFile
}

type worktreeRegistration struct {
	path     string
	head     string
	branch   string
	detached bool
	bare     bool
	raw      string
}

func openGitControlSnapshot(rootDirectory *securefs.Directory, root string, budget *securefs.SnapshotBudget) (*gitControlSnapshot, error) {
	control := &gitControlSnapshot{
		root:     rootDirectory,
		rootPath: root,
		budget:   budget,
	}
	success := false
	defer func() {
		if !success {
			_ = control.Close()
		}
	}()

	var err error
	control.gitMarker, err = rootDirectory.HashPathWithBudget(".git", budget)
	if err != nil {
		return nil, fmt.Errorf("capture worktree Git administrative link: %w", err)
	}
	marker, markerErr := readPinnedControlFile(rootDirectory, ".git", budget)
	if markerErr == nil && marker.state.Exists {
		control.gitMarkerFile = &marker
		control.gitDirPath, err = parseGitFilePath(marker.content, root)
		if err != nil {
			return nil, err
		}
	} else {
		control.gitDirPath = filepath.Join(root, ".git")
	}
	control.gitDirectory, err = securefs.OpenDir(control.gitDirPath)
	if err != nil {
		if markerErr != nil {
			return nil, fmt.Errorf("open worktree Git directory without following links after administrative-file read failed: %w", err)
		}
		return nil, fmt.Errorf("open worktree Git directory without following links: %w", err)
	}

	control.commonLink, err = readPinnedControlFile(control.gitDirectory, "commondir", budget)
	if err != nil {
		return nil, fmt.Errorf("pin common Git directory link: %w", err)
	}
	control.commonDirPath = control.gitDirPath
	if control.commonLink.state.Exists {
		control.commonDirPath, err = parseGitAdminPath(control.commonLink.content, control.gitDirPath, "common Git directory link")
		if err != nil {
			return nil, err
		}
	}
	if err := validateGitDirectoryRelationship(control.gitDirPath, control.commonDirPath); err != nil {
		return nil, err
	}
	control.registrationLink, err = readPinnedControlFile(control.gitDirectory, "gitdir", budget)
	if err != nil {
		return nil, fmt.Errorf("pin worktree registration link: %w", err)
	}
	if control.gitDirPath != control.commonDirPath {
		if !control.registrationLink.state.Exists {
			return nil, errors.New("linked Git directory has no worktree registration link")
		}
		registeredRoot, parseErr := parseGitAdminPath(control.registrationLink.content, control.gitDirPath, "worktree registration link")
		if parseErr != nil {
			return nil, parseErr
		}
		if registeredRoot != filepath.Join(root, ".git") {
			return nil, fmt.Errorf("worktree registration link %q does not match current root %q", registeredRoot, root)
		}
	}
	if control.gitDirPath == control.commonDirPath {
		control.commonDirectory = control.gitDirectory
	} else {
		control.commonDirectory, err = securefs.OpenDir(control.commonDirPath)
		if err != nil {
			return nil, fmt.Errorf("open common Git directory without following links: %w", err)
		}
	}
	control.head, err = readPinnedControlFile(control.gitDirectory, "HEAD", budget)
	if err != nil {
		return nil, fmt.Errorf("pin worktree HEAD: %w", err)
	}
	control.headState, err = pinHEADState(control.head, control.commonDirectory, budget)
	if err != nil {
		return nil, fmt.Errorf("pin worktree HEAD state: %w", err)
	}
	control.commonConfig, err = readPinnedControlFile(control.commonDirectory, "config", budget)
	if err != nil {
		return nil, fmt.Errorf("pin common Git config: %w", err)
	}
	control.packedReplacementRefs, err = readPinnedControlFile(control.commonDirectory, "packed-refs", budget)
	if err != nil {
		return nil, fmt.Errorf("pin packed replacement references: %w", err)
	}
	control.index, err = control.gitDirectory.OpenFile("index")
	if errors.Is(err, os.ErrNotExist) {
		control.indexMissing = true
		control.index = nil
	} else if err != nil {
		return nil, fmt.Errorf("pin worktree Git index: %w", err)
	}
	if err := control.pinProtectedGitMetadata(); err != nil {
		return nil, err
	}

	success = true
	return control, nil
}

func (s *gitControlSnapshot) pinWorktreeConfig(enabled bool) error {
	s.worktreeConfigEnabled = enabled
	if !enabled {
		return nil
	}
	config, err := readPinnedControlFile(s.gitDirectory, "config.worktree", s.budget)
	if err != nil {
		return fmt.Errorf("pin per-worktree Git config: %w", err)
	}
	s.worktreeConfig = &config
	return nil
}

func (s *gitControlSnapshot) Finish(head, registration, index, status, replacementRefs string) (map[string]string, error) {
	if status != "" && !strings.HasSuffix(status, "\x00") {
		return nil, errors.New("Git returned unterminated NUL-delimited status")
	}
	if err := s.head.verify(); err != nil {
		return nil, fmt.Errorf("verify worktree HEAD: %w", err)
	}
	if err := s.headState.verify(head); err != nil {
		return nil, fmt.Errorf("verify symbolic HEAD reference: %w", err)
	}
	if err := s.commonLink.verify(); err != nil {
		return nil, fmt.Errorf("verify common Git directory link: %w", err)
	}
	if err := s.registrationLink.verify(); err != nil {
		return nil, fmt.Errorf("verify worktree registration link: %w", err)
	}
	if s.indexMissing {
		if err := s.gitDirectory.VerifyFile("index", securefs.FileState{Exists: false}); err != nil {
			return nil, fmt.Errorf("verify missing worktree Git index: %w", err)
		}
	} else if err := s.index.Verify(); err != nil {
		return nil, fmt.Errorf("verify worktree Git index: %w", err)
	}
	if err := s.commonConfig.verify(); err != nil {
		return nil, fmt.Errorf("verify common Git config: %w", err)
	}
	if err := s.worktreeConfig.verify(); err != nil {
		return nil, fmt.Errorf("verify per-worktree Git config: %w", err)
	}
	if err := s.pinReplacementRefs(replacementRefs); err != nil {
		return nil, err
	}
	replacementState, err := s.finishReplacementRefs(replacementRefs)
	if err != nil {
		return nil, err
	}
	protectedMetadata, err := s.finishProtectedGitMetadata()
	if err != nil {
		return nil, err
	}
	if s.gitMarkerFile != nil {
		if err := s.gitMarkerFile.verify(); err != nil {
			return nil, fmt.Errorf("verify worktree Git administrative file: %w", err)
		}
	}
	marker, err := s.root.HashPathWithBudget(".git", s.budget)
	if err != nil {
		return nil, fmt.Errorf("verify worktree Git administrative link: %w", err)
	}
	if !bytes.Equal(marker, s.gitMarker) {
		return nil, fmt.Errorf("%w while using worktree Git administrative link", securefs.ErrChanged)
	}
	if s.gitDirectory != s.commonDirectory {
		if err := s.gitDirectory.Verify(); err != nil {
			return nil, fmt.Errorf("verify linked Git directory: %w", err)
		}
	}
	if err := s.commonDirectory.Verify(); err != nil {
		return nil, fmt.Errorf("verify common Git directory: %w", err)
	}

	identity := sha256.New()
	writeSnapshotPart(identity, "root", []byte(s.rootPath))
	writeSnapshotPart(identity, "git-dir", []byte(s.gitDirPath))
	writeSnapshotPart(identity, "common-dir", []byte(s.commonDirPath))
	writeSnapshotPart(identity, "registration", []byte(registration))
	writeSnapshotPart(identity, "registration-link", s.registrationLink.content)
	writeSnapshotPart(identity, "git-marker", s.gitMarker)
	writeSnapshotPart(identity, "HEAD-file", s.head.content)
	writeSnapshotPart(identity, "HEAD-target", []byte(s.headState.target))
	writeSnapshotPart(identity, "HEAD-object", []byte(s.headState.objectID))
	for _, reference := range s.headState.loose {
		writeSnapshotPart(identity, "HEAD-reference-target", []byte(reference.target))
		writeSnapshotPart(identity, "HEAD-reference-file", []byte(controlFileFingerprint(&reference.file)))
	}

	state := map[string]string{
		"common Git config":       controlFileFingerprint(&s.commonConfig),
		"Git index":               digestString([]byte(index)),
		"per-worktree Git config": worktreeConfigFingerprint(s.worktreeConfigEnabled, s.worktreeConfig),
		"protected Git metadata":  protectedMetadata,
		"replacement references":  replacementState,
		"worktree identity":       "sha256:" + hex.EncodeToString(identity.Sum(nil)),
		"worktree status":         digestString([]byte(status)),
	}
	return state, nil
}

func (s *gitControlSnapshot) Close() error {
	var first error
	if s == nil {
		return nil
	}
	if s.index != nil {
		first = s.index.Close()
		s.index = nil
	}
	for index := len(s.protectedMetadata) - 1; index >= 0; index-- {
		if s.protectedMetadata[index].pinnedDir != nil {
			if err := s.protectedMetadata[index].pinnedDir.Close(); first == nil {
				first = err
			}
			s.protectedMetadata[index].pinnedDir = nil
		}
	}
	for index := len(s.headState.directories) - 1; index >= 0; index-- {
		if err := s.headState.directories[index].Close(); first == nil {
			first = err
		}
	}
	s.headState.directories = nil
	if s.gitDirectory != nil && s.gitDirectory != s.commonDirectory {
		if err := s.gitDirectory.Close(); first == nil {
			first = err
		}
	}
	if s.commonDirectory != nil {
		if err := s.commonDirectory.Close(); first == nil {
			first = err
		}
	}
	return first
}

func pinHEADState(head pinnedControlFile, commonDirectory *securefs.Directory, budget *securefs.SnapshotBudget) (pinnedHEADState, error) {
	if !head.state.Exists {
		return pinnedHEADState{}, errors.New("worktree HEAD is missing")
	}
	const symbolicPrefix = "ref: "
	if !bytes.HasPrefix(head.content, []byte(symbolicPrefix)) {
		objectID, err := parseGitObjectID(head.content, "detached worktree HEAD")
		return pinnedHEADState{objectID: objectID, budget: budget}, err
	}

	target, err := parseSymbolicReference(head.content, "symbolic worktree HEAD")
	if err != nil {
		return pinnedHEADState{}, err
	}
	state := pinnedHEADState{target: target, budget: budget}
	seen := map[string]struct{}{}
	for range 16 {
		if _, duplicate := seen[target]; duplicate {
			state.close()
			return pinnedHEADState{}, fmt.Errorf("symbolic reference chain contains a cycle at %q", target)
		}
		seen[target] = struct{}{}

		loose, pinErr := pinLooseReference(&state, commonDirectory, target)
		if pinErr != nil {
			state.close()
			return pinnedHEADState{}, pinErr
		}
		if !loose.state.Exists {
			return pinPackedHEADState(state, commonDirectory, target)
		}
		if !bytes.HasPrefix(loose.content, []byte(symbolicPrefix)) {
			state.objectID, err = parseGitObjectID(loose.content, "loose symbolic reference")
			if err != nil {
				state.close()
				return pinnedHEADState{}, err
			}
			return state, nil
		}
		target, err = parseSymbolicReference(loose.content, "loose symbolic reference")
		if err != nil {
			state.close()
			return pinnedHEADState{}, err
		}
	}
	state.close()
	return pinnedHEADState{}, errors.New("symbolic reference chain exceeds 16 targets")
}

func pinLooseReference(state *pinnedHEADState, commonDirectory *securefs.Directory, target string) (pinnedControlFile, error) {
	components := strings.Split(target, "/")
	current := commonDirectory
	for _, component := range components[:len(components)-1] {
		next, err := current.OpenDir(component)
		if err != nil {
			if !os.IsNotExist(err) {
				return pinnedControlFile{}, fmt.Errorf("open symbolic reference directory %q: %w", component, err)
			}
			loose, err := readPinnedControlFile(current, component, state.budget)
			if err != nil {
				return pinnedControlFile{}, fmt.Errorf("pin missing symbolic reference component %q: %w", component, err)
			}
			state.loose = append(state.loose, pinnedReference{target: target, file: loose})
			return loose, nil
		}
		state.directories = append(state.directories, next)
		current = next
	}

	loose, err := readPinnedControlFile(current, components[len(components)-1], state.budget)
	if err != nil {
		return pinnedControlFile{}, fmt.Errorf("pin loose symbolic reference: %w", err)
	}
	state.loose = append(state.loose, pinnedReference{target: target, file: loose})
	return loose, nil
}

func parseSymbolicReference(content []byte, label string) (string, error) {
	const prefix = "ref: "
	if !bytes.HasPrefix(content, []byte(prefix)) || !bytes.HasSuffix(content, []byte("\n")) {
		return "", fmt.Errorf("%s is not a complete symbolic reference", label)
	}
	target := string(bytes.TrimSuffix(bytes.TrimPrefix(content, []byte(prefix)), []byte("\n")))
	if !strings.HasPrefix(target, "refs/") || strings.ContainsAny(target, "\x00\r\n") {
		return "", fmt.Errorf("%s has an invalid reference target", label)
	}
	return target, nil
}

func pinPackedHEADState(state pinnedHEADState, commonDirectory *securefs.Directory, target string) (pinnedHEADState, error) {
	packed, err := readPinnedControlFile(commonDirectory, "packed-refs", state.budget)
	if err != nil {
		state.close()
		return pinnedHEADState{}, fmt.Errorf("pin packed references: %w", err)
	}
	state.packed = &packed
	state.objectID, err = packedReferenceObjectID(packed, target)
	if err != nil {
		state.close()
		return pinnedHEADState{}, err
	}
	return state, nil
}

func packedReferenceObjectID(packed pinnedControlFile, target string) (string, error) {
	if !packed.state.Exists {
		return "", fmt.Errorf("symbolic reference %q is neither loose nor packed", target)
	}
	if !bytes.HasSuffix(packed.content, []byte("\n")) {
		return "", errors.New("packed references are not newline terminated")
	}
	for _, line := range bytes.Split(bytes.TrimSuffix(packed.content, []byte("\n")), []byte("\n")) {
		if len(line) == 0 || line[0] == '#' || line[0] == '^' {
			continue
		}
		separator := bytes.IndexByte(line, ' ')
		if separator < 0 || string(line[separator+1:]) != target {
			continue
		}
		return parseGitObjectID(append(bytes.Clone(line[:separator]), '\n'), "packed symbolic reference")
	}
	return "", fmt.Errorf("symbolic reference %q is absent from packed references", target)
}

func parseGitObjectID(content []byte, label string) (string, error) {
	if !bytes.HasSuffix(content, []byte("\n")) {
		return "", fmt.Errorf("%s is not newline terminated", label)
	}
	value := bytes.TrimSuffix(content, []byte("\n"))
	decoded, err := hex.DecodeString(string(value))
	if err != nil || (len(decoded) != 20 && len(decoded) != 32) {
		return "", fmt.Errorf("%s does not contain a complete Git object ID", label)
	}
	return string(value), nil
}

func (s *pinnedHEADState) verify(head string) error {
	if head != s.objectID {
		return fmt.Errorf("Git reported HEAD %q instead of pinned object %q", head, s.objectID)
	}
	if s.target == "" {
		return nil
	}
	for _, reference := range s.loose {
		if err := reference.file.verify(); err != nil {
			return fmt.Errorf("verify loose reference path %q: %w", reference.target, err)
		}
	}
	if s.packed != nil {
		if err := s.packed.verify(); err != nil {
			return fmt.Errorf("verify packed-reference fallback: %w", err)
		}
	}
	for index := len(s.directories) - 1; index >= 0; index-- {
		// Parallel assignment branches share refs/heads/runner. A sibling ref may
		// legitimately change the containing directory while this snapshot is in
		// progress. The exact HEAD reference was verified above; only require its
		// parent directories to retain their identity and safe permissions.
		if err := s.directories[index].VerifyIdentity(); err != nil {
			return fmt.Errorf("verify symbolic reference directory: %w", err)
		}
	}
	return nil
}

func (s *pinnedHEADState) close() {
	for index := len(s.directories) - 1; index >= 0; index-- {
		_ = s.directories[index].Close()
	}
	s.directories = nil
}

func readPinnedControlFile(directory *securefs.Directory, name string, budget *securefs.SnapshotBudget) (pinnedControlFile, error) {
	if _, err := directory.HashPathWithBudget(name, budget); err != nil {
		return pinnedControlFile{}, err
	}
	content, _, state, err := directory.ReadFile(name, gitControlFileLimit)
	if err != nil {
		return pinnedControlFile{}, err
	}
	if _, err := directory.HashPathWithBudget(name, budget); err != nil {
		return pinnedControlFile{}, err
	}
	return pinnedControlFile{directory: directory, name: name, state: state, content: content}, nil
}

func (f *pinnedControlFile) verify() error {
	if f == nil {
		return nil
	}
	return f.directory.VerifyFile(f.name, f.state)
}

func controlFileFingerprint(file *pinnedControlFile) string {
	if file == nil || !file.state.Exists {
		return "missing"
	}
	return digestString(file.content)
}

func worktreeConfigFingerprint(enabled bool, file *pinnedControlFile) string {
	if !enabled {
		return "disabled"
	}
	return "enabled:" + controlFileFingerprint(file)
}

func digestString(value []byte) string {
	digest := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func parseGitFilePath(content []byte, root string) (string, error) {
	const prefix = "gitdir: "
	value := string(content)
	if !strings.HasPrefix(value, prefix) {
		return "", errors.New("worktree .git file does not contain a gitdir record")
	}
	return parseGitAdminPath([]byte(strings.TrimPrefix(value, prefix)), root, "worktree Git directory link")
}

func parseGitAdminPath(content []byte, base, label string) (string, error) {
	value := string(content)
	if !strings.HasSuffix(value, "\n") {
		return "", fmt.Errorf("%s is not newline terminated", label)
	}
	value = strings.TrimSuffix(value, "\n")
	if value == "" || strings.ContainsRune(value, '\x00') {
		return "", fmt.Errorf("%s is empty or ambiguous", label)
	}
	if !filepath.IsAbs(value) {
		value = filepath.Join(base, value)
	}
	absolute, err := securefs.AbsolutePath(value)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", label, err)
	}
	return absolute, nil
}

func validateGitDirectoryRelationship(gitDir, commonDir string) error {
	if gitDir == commonDir {
		return nil
	}
	relative, err := filepath.Rel(commonDir, gitDir)
	if err != nil {
		return fmt.Errorf("resolve linked Git directory relationship: %w", err)
	}
	parts := strings.Split(relative, string(os.PathSeparator))
	if len(parts) != 2 || parts[0] != "worktrees" || parts[1] == "" || parts[1] == "." || parts[1] == ".." {
		return fmt.Errorf("Git directory %q is not a registered linked-worktree directory under %q", gitDir, commonDir)
	}
	return nil
}

func currentWorktreeRegistration(value, root, head, branchRef string) (string, error) {
	records, err := parseWorktreeRegistrations(value)
	if err != nil {
		return "", err
	}
	var match string
	for _, record := range records {
		if record.path != root {
			continue
		}
		if match != "" {
			return "", errors.New("Git returned duplicate registrations for the current worktree")
		}
		if record.head != head {
			return "", errors.New("current worktree registration does not match HEAD")
		}
		if branchRef == "HEAD" {
			if !record.detached || record.branch != "" {
				return "", errors.New("current worktree registration does not record detached HEAD")
			}
		} else if record.detached || record.branch != branchRef {
			return "", errors.New("current worktree registration does not match the current branch")
		}
		match = record.raw
	}
	if match == "" {
		return "", fmt.Errorf("current worktree root %q has no matching worktree registration", root)
	}
	return match, nil
}

func parseWorktreeRegistrations(value string) ([]worktreeRegistration, error) {
	if value == "" || !strings.HasSuffix(value, "\x00\x00") {
		return nil, errors.New("Git returned unterminated NUL-delimited worktree records")
	}
	blocks := strings.Split(strings.TrimSuffix(value, "\x00\x00"), "\x00\x00")
	records := make([]worktreeRegistration, 0, len(blocks))
	for _, block := range blocks {
		if block == "" {
			return nil, errors.New("Git returned an empty worktree record")
		}
		fields := strings.Split(block, "\x00")
		if len(fields) < 2 || !strings.HasPrefix(fields[0], "worktree ") {
			return nil, errors.New("Git returned an invalid worktree record")
		}
		record := worktreeRegistration{path: strings.TrimPrefix(fields[0], "worktree "), raw: block}
		for _, field := range fields[1:] {
			switch {
			case strings.HasPrefix(field, "HEAD "):
				if record.head != "" {
					return nil, errors.New("Git returned a duplicate worktree HEAD")
				}
				record.head = strings.TrimPrefix(field, "HEAD ")
			case strings.HasPrefix(field, "branch "):
				if record.branch != "" {
					return nil, errors.New("Git returned a duplicate worktree branch")
				}
				record.branch = strings.TrimPrefix(field, "branch ")
			case field == "detached":
				record.detached = true
			case field == "bare":
				record.bare = true
			}
		}
		if record.path == "" || (!record.bare && record.head == "") || (record.detached && record.branch != "") {
			return nil, errors.New("Git returned an incomplete worktree record")
		}
		records = append(records, record)
	}
	return records, nil
}
