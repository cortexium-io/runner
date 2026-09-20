package workspace

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/securefs"
)

type reviewEvidenceManifest struct {
	CandidateCommit string               `json:"candidate_commit"`
	CandidateTree   string               `json:"candidate_tree"`
	SourceRoot      string               `json:"source_root"`
	SelectedPaths   []string             `json:"selected_paths"`
	MissingPaths    []string             `json:"missing_paths"`
	Files           []reviewEvidenceFile `json:"files"`
}

type reviewEvidenceFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

// reviewEvidence holds only an ephemeral, operator-selected evidence snapshot.
// Its contents are untrusted claims; the manifest attests to captured bytes,
// not that those bytes prove a successful check on this candidate.
type reviewEvidence struct {
	root   *securefs.Directory
	paths  map[string][]byte
	limits SnapshotLimits
}

// PrepareEvidence never grants read access to the source worktree. Selection
// comes from operator configuration, not a card, report, or harness result.
func (w *ReviewWorkspace) PrepareEvidence(ctx context.Context, metadata Metadata, candidate Candidate, paths []string, limits SnapshotLimits) error {
	if len(paths) == 0 {
		return nil
	}
	if w.evidence != nil || w.parent == "" {
		return errors.New("review evidence requires a fresh private review workspace")
	}
	if candidate != w.candidate || metadata.WorktreePath != w.sourcePath {
		return errors.New("review evidence source or candidate does not match the private review workspace")
	}
	if err := validateRecordedIdentity(metadata); err != nil {
		return err
	}
	if err := config.ValidateReviewEvidencePaths(paths); err != nil {
		return err
	}
	destination := filepath.Join(w.parent, "evidence")
	evidence, err := copyReviewEvidence(ctx, metadata.WorktreePath, destination, candidate, paths, limits)
	if err != nil {
		return fmt.Errorf("capture selected review evidence: %w", err)
	}
	w.evidence, w.EvidencePath = evidence, destination
	return nil
}

func (w ReviewWorkspace) VerifyEvidence(ctx context.Context) error {
	if w.evidence == nil {
		return nil
	}
	return w.evidence.verify(ctx)
}

func (e *reviewEvidence) verify(ctx context.Context) error {
	budget, err := securefs.NewSnapshotBudget(e.limits)
	if err != nil {
		return err
	}
	for relative, before := range e.paths {
		if err := ctx.Err(); err != nil {
			return err
		}
		after, err := e.root.HashPathWithBudget(relative, budget)
		if err != nil {
			return err
		}
		if !bytes.Equal(before, after) {
			return fmt.Errorf("review evidence changed: %s", relative)
		}
	}
	return e.root.Verify()
}

func copyReviewEvidence(ctx context.Context, source, destination string, candidate Candidate, paths []string, limits SnapshotLimits) (*reviewEvidence, error) {
	if err := config.ValidateReviewEvidencePaths(paths); err != nil {
		return nil, err
	}
	budget, err := securefs.NewSnapshotBudget(limits)
	if err != nil {
		return nil, err
	}
	root, err := securefs.OpenDir(source)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if err := os.Mkdir(destination, 0o700); err != nil {
		return nil, err
	}
	manifest := reviewEvidenceManifest{
		CandidateCommit: candidate.CommitOID, CandidateTree: candidate.TreeOID,
		SourceRoot: source, SelectedPaths: append([]string(nil), paths...),
		MissingPaths: []string{}, Files: []reviewEvidenceFile{},
	}
	sourcePins := map[string][]byte{}
	destinationPaths := map[string][]byte{}
	var total int64
	pinSource := func(relative string) error {
		if _, exists := sourcePins[relative]; exists {
			return nil
		}
		digest, err := root.HashPathWithBudget(relative, budget)
		if err == nil {
			sourcePins[relative] = digest
		}
		return err
	}
	var copyPath func(string, bool) error
	copyPath = func(relative string, selected bool) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if strings.EqualFold(path.Base(relative), ".git") {
			return fmt.Errorf("review evidence cannot include Git administration: %s", relative)
		}
		if err := pinSource(relative); err != nil {
			return err
		}
		absolute := filepath.Join(source, filepath.FromSlash(relative))
		info, err := os.Lstat(absolute)
		if selected && errors.Is(err, os.ErrNotExist) {
			manifest.MissingPaths = append(manifest.MissingPaths, relative)
			return nil
		}
		if err != nil {
			return err
		}
		targetRelative := "files/" + relative
		target := filepath.Join(destination, filepath.FromSlash(targetRelative))
		switch {
		case info.IsDir():
			directory, err := securefs.OpenDir(absolute)
			if err != nil {
				return err
			}
			defer directory.Close()
			names, err := directory.ReadDirNamesWithBudget(budget)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}
			for _, name := range names {
				if err := copyPath(relative+"/"+name, false); err != nil {
					return err
				}
			}
			if err := directory.Verify(); err != nil {
				return err
			}
		case info.Mode().IsRegular():
			content, _, state, err := securefs.ReadFile(absolute, min(limits.MaxFileBytes, limits.MaxTotalBytes-total))
			if err != nil {
				return err
			}
			if !state.Exists {
				return fmt.Errorf("review evidence disappeared: %s", relative)
			}
			if err := securefs.ValidateOwnedRegularFile(state, uint32(os.Geteuid())); err != nil {
				return fmt.Errorf("unsafe review evidence %s: %w", relative, err)
			}
			total += int64(len(content))
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				return err
			}
			if err := os.WriteFile(target, content, 0o600); err != nil {
				return err
			}
			digest := sha256.Sum256(content)
			manifest.Files = append(manifest.Files, reviewEvidenceFile{Path: relative, SHA256: hex.EncodeToString(digest[:]), Bytes: int64(len(content))})
		default:
			return fmt.Errorf("review evidence must contain only regular files and directories, not links or special files: %s", relative)
		}
		for entry := targetRelative; entry != "."; entry = path.Dir(entry) {
			destinationPaths[entry] = nil
		}
		return nil
	}
	for _, relative := range paths {
		// Pin ancestors of selected nested paths as well as the subtree itself.
		for parent := path.Dir(relative); parent != "."; parent = path.Dir(parent) {
			if err := pinSource(parent); err != nil {
				return nil, err
			}
		}
		if err := copyPath(relative, true); err != nil {
			return nil, err
		}
	}
	// Reject a concurrent replacement, changed payload, or newly added file.
	for relative, before := range sourcePins {
		after, err := root.HashPathWithBudget(relative, budget)
		if err != nil {
			return nil, err
		}
		if !bytes.Equal(before, after) {
			return nil, fmt.Errorf("review evidence changed during capture: %s", relative)
		}
	}
	if err := root.Verify(); err != nil {
		return nil, err
	}
	sort.Slice(manifest.Files, func(i, j int) bool { return manifest.Files[i].Path < manifest.Files[j].Path })
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > min(limits.MaxFileBytes, limits.MaxTotalBytes-total) {
		return nil, errors.New("review evidence manifest exceeds remaining snapshot byte limits")
	}
	if err := os.WriteFile(filepath.Join(destination, "manifest.json"), data, 0o600); err != nil {
		return nil, err
	}
	destinationPaths["manifest.json"] = nil
	sealed, err := securefs.OpenDir(destination)
	if err != nil {
		return nil, err
	}
	evidence := &reviewEvidence{root: sealed, paths: destinationPaths, limits: limits}
	sealBudget, err := securefs.NewSnapshotBudget(limits)
	if err != nil {
		_ = sealed.Close()
		return nil, err
	}
	for relative := range destinationPaths {
		digest, err := sealed.HashPathWithBudget(relative, sealBudget)
		if err != nil {
			_ = sealed.Close()
			return nil, err
		}
		destinationPaths[relative] = digest
	}
	return evidence, nil
}
