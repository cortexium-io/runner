package execution

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
)

type sourcePreservationManifest struct {
	RunID         string                     `json:"run_id"`
	AttemptID     string                     `json:"attempt_id"`
	Source        runner.SourceResult        `json:"source"`
	PatchSHA256   string                     `json:"patch_sha256"`
	Path          string                     `json:"path"`
	Identity      workspaceGitIdentityRecord `json:"identity"`
	CleanupIntent json.RawMessage            `json:"cleanup_intent,omitempty"`
	Created       time.Time                  `json:"created"`
}

type sourcePreservation struct {
	Manifest    sourcePreservationManifest `json:"manifest"`
	ManifestSHA string                     `json:"manifest_sha256"`
	Complete    bool                       `json:"complete"`
}

func (s *Service) PrepareSourceRecovery(ctx context.Context, run runner.Run, source runner.SourceResult) (runner.SourcePreservationReceipt, error) {
	for _, attempt := range run.Attempts {
		if err := s.captureReleaseReady(attempt.ID); err != nil {
			return runner.SourcePreservationReceipt{}, err
		}
	}
	return s.workspaces.preserveRecoverySource(ctx, run, source)
}

// An explicit owner action preserves the whole authenticated checkout. Its
// contents remain unaccepted evidence; only the previously captured patch can
// restore source through ordinary Prepare. The move intent precedes all effects.
func (w *Workspaces) preserveRecoverySource(ctx context.Context, run runner.Run, source runner.SourceResult) (runner.SourcePreservationReceipt, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	var receipt runner.SourcePreservationReceipt
	if run.Status != "Failed" || run.CancelRequested || run.Current() == nil || !run.CleanupBlocked || run.CleanupError == "" {
		return receipt, errors.New("explicit preservation needs settled blocked source cleanup")
	}
	for _, attempt := range run.Attempts {
		if attempt.State != runner.Finished || !attempt.Released {
			return receipt, errors.New("source execution and attempt cleanup must settle before preservation")
		}
	}
	r, err := w.sourceRecoveryRecord(ctx, run, source)
	if err != nil {
		return receipt, err
	}
	var inheritedCleanupLease *os.File
	if r.CleanupIntent != nil {
		cleanupLease, err := w.lockCleanup(r)
		if err != nil {
			return receipt, err
		}
		defer cleanupLease.Close()
		inheritedCleanupLease = cleanupLease
	}
	quiescent, err := w.cleanupQuiescent(r)
	if err != nil {
		return receipt, err
	}
	defer quiescent()
	// The same private filesystem keeps the directory move atomic and avoids
	// copying or deleting unaccepted content within the managed storage ceiling.
	rel := filepath.Join("forensics", run.ID, run.Current().ID)
	if err := captureDirectory(w.disk.Root, "forensics", run.ID, run.Current().ID); err != nil {
		return receipt, err
	}
	root := filepath.Join(w.disk.Root, rel)
	path := filepath.Join(root, "source")
	p := r.SourcePreservation
	if p == nil {
		entries, err := os.ReadDir(root)
		if err != nil || len(entries) != 0 {
			return receipt, errors.New("unreferenced source forensics has unknown content; preserve it")
		}
		identity, err := workspaceGitIdentity(ctx, r, r.WorkDir)
		if err != nil || identity.Backlink != filepath.Join(r.WorkDir, ".git") {
			return receipt, errors.New("source worktree ownership is not proven; preserve it")
		}
		if err := workspaceSameFilesystem(r.WorkDir, root); err != nil {
			return receipt, err
		}
		intent, err := json.Marshal(r.CleanupIntent)
		if err != nil {
			return receipt, err
		}
		manifest := sourcePreservationManifest{
			RunID: run.ID, AttemptID: run.Current().ID, Source: source, PatchSHA256: r.Capture.PatchSHA,
			Path: path, Identity: identity, CleanupIntent: intent, Created: time.Now().UTC(),
		}
		data, err := json.Marshal(manifest)
		if err != nil {
			return receipt, err
		}
		p = &sourcePreservation{Manifest: manifest, ManifestSHA: captureDigest(data)}
		r.SourcePreservation = p
		if err := w.save(r); err != nil {
			return receipt, err
		}
	}
	m := p.Manifest
	data, err := json.Marshal(m)
	if err != nil || captureDigest(data) != p.ManifestSHA || m.RunID != run.ID || m.AttemptID != run.Current().ID || m.Path != path || m.PatchSHA256 != r.Capture.PatchSHA || !reflect.DeepEqual(m.Source, source) {
		return receipt, errors.New("source preservation intent identity differs; preserve it")
	}
	manifestPath := filepath.Join(rel, "manifest.json")
	if existing, err := w.disk.Read(manifestPath, w.disk.Policy.Database.MaxRecord); err == nil {
		if !bytes.Equal(existing, data) {
			return receipt, errors.New("source forensic manifest changed; preserve it")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return receipt, err
	} else if err := w.disk.Write(manifestPath, data, w.disk.Policy.Database.MaxRecord); err != nil {
		return receipt, err
	}
	moveLease, err := sourcePreservationLease(filepath.Join(root, "lease"))
	if err != nil {
		return receipt, err
	}
	defer moveLease.Close()
	gitLease := moveLease
	if inheritedCleanupLease != nil {
		gitLease = inheritedCleanupLease
	}
	ctx = context.WithValue(ctx, captureLeaseKey{}, gitLease)
	oldExists, err := sourcePreservationDirectory(r.WorkDir)
	if err != nil {
		return receipt, err
	}
	newExists, err := sourcePreservationDirectory(path)
	if err != nil {
		return receipt, err
	}
	if oldExists == newExists {
		return receipt, errors.New("source preservation has ambiguous old/new paths; preserve both")
	}
	if oldExists {
		if p.Complete {
			return receipt, errors.New("completed source preservation lost its destination; preserve it")
		}
		identity, err := workspaceGitIdentity(ctx, r, r.WorkDir)
		if err != nil || !reflect.DeepEqual(identity, m.Identity) {
			return receipt, errors.New("source worktree identity changed before preservation; preserve it")
		}
		if _, err := git(ctx, r.Repository, nil, nil, 4096, "worktree", "move", "--", r.WorkDir, path); err != nil {
			return receipt, fmt.Errorf("source preservation move is unresolved; rejoin the same intent: %w", err)
		}
	}
	identity, err := workspaceGitIdentity(ctx, r, path)
	if err != nil {
		return receipt, err
	}
	expected := m.Identity
	if identity.Backlink != filepath.Join(r.WorkDir, ".git") && identity.Backlink != filepath.Join(path, ".git") {
		return receipt, errors.New("preserved source has an unknown worktree backlink; preserve it")
	}
	expected.Backlink = identity.Backlink
	if !reflect.DeepEqual(identity, expected) {
		return receipt, errors.New("preserved source ownership differs from its move intent; preserve it")
	}
	if identity.Backlink != filepath.Join(path, ".git") {
		if _, err := git(ctx, r.Repository, nil, nil, 4096, "worktree", "repair", "--", path); err != nil {
			return receipt, fmt.Errorf("source preservation metadata is unresolved; rejoin the same intent: %w", err)
		}
		identity, err = workspaceGitIdentity(ctx, r, path)
		expected.Backlink = filepath.Join(path, ".git")
		if err != nil || !reflect.DeepEqual(identity, expected) {
			return receipt, errors.New("preserved source metadata repair is not proven; preserve it")
		}
	}
	p.Complete = true
	r.Released = true
	r.CleanupIntent = nil // The complete forensic manifest retains this old intent.
	if err := w.save(r); err != nil {
		return receipt, err
	}
	if err := w.disk.Refresh(); err != nil {
		return receipt, err
	}
	return runner.SourcePreservationReceipt{Path: path, ManifestSHA256: p.ManifestSHA, PatchSHA256: m.PatchSHA256}, nil
}

func sourcePreservationLease(path string) (*os.File, error) {
	info, err := os.Lstat(path)
	flags := os.O_RDWR
	if errors.Is(err, os.ErrNotExist) {
		flags |= os.O_CREATE | os.O_EXCL
	} else if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("source preservation lease ownership differs; preserve it")
	}
	lease, err := os.OpenFile(path, flags, 0600)
	if err != nil {
		return nil, err
	}
	owned, err := tryCaptureLease(lease)
	if err != nil || !owned {
		lease.Close()
		return nil, errors.New("source preservation move is still active or unknown; rejoin its intent")
	}
	return lease, nil
}

func sourcePreservationDirectory(path string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.IsDir() {
		return false, errors.New("source preservation path is not an owned directory; preserve it")
	}
	return true, nil
}
