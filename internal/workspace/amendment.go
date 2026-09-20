package workspace

import (
	"context"
	"errors"
	"path/filepath"
	"time"
)

// RebindRetainedContent is an explicit operator boundary, never automatic
// mismatch recovery. It changes only the content binding of the exact clean
// candidate that was previewed, leaving all Git data and evidence untouched.
func (p GitProvider) RebindRetainedContent(ctx context.Context, request Request, expected Identity, snapshot Snapshot, digest string) error {
	if digest == "" || digest == expected.DelegatedContentDigest {
		return errors.New("amendment requires a new content identity")
	}
	recorded, err := p.ValidateRetainedIdentity(ctx, request)
	if err != nil {
		return err
	}
	if recorded != expected {
		return errors.New("workspace identity changed after amendment preview")
	}
	current, err := CaptureSnapshotStateWithLimits(ctx, p.run, recorded.WorktreePath, 30*time.Second, p.limits)
	if err != nil {
		return err
	}
	if !snapshot.Clean || !current.Clean || current.Fingerprint != snapshot.Fingerprint || current.Head != snapshot.Head || current.Tree != snapshot.Tree || current.Branch != expected.Branch {
		return errors.New("retained candidate changed after amendment preview")
	}
	replacement := expected
	replacement.DelegatedContentDigest = digest
	root := filepath.Dir(expected.WorktreePath)
	return replaceIdentity(activeIdentityPath(root, filepath.Base(expected.WorktreePath)), expected, replacement)
}
