package workspace

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// PlanAcceptanceDelta describes the originally integrated change for a
// retirement preview. It does not claim later members left every line intact,
// and retirement never reverts this change.
func (p GitProvider) PlanAcceptanceDelta(ctx context.Context, metadata Metadata, snapshot Snapshot, record PublicationRecord) (string, error) {
	verified, found, err := p.LoadPublicationAcceptance(ctx, metadata, snapshot, PublicationEvidence{PlanRevision: record.PlanRevision})
	if err != nil || !found || verified != record {
		return "", errors.Join(errors.New("retirement preview requires exact retained acceptance"), err)
	}
	profile, err := derivePrivilegedGitProfile(metadata.WorktreePath)
	if err != nil {
		return "", err
	}
	result, err := p.privilegedGit(ctx, profile, "diff", "--no-ext-diff", "--no-textconv", "--no-renames", "--stat=120,80,80", record.ApprovedBaseOID, record.CommitOID, "--")
	if err != nil {
		return "", fmt.Errorf("inspect original accepted delta: %w", commandError(err, result))
	}
	return strings.TrimSpace(result.Stdout), nil
}

// PublicationAcceptanceDigest identifies the original immutable proof bytes in
// protected amendment intent. It is not a model-supplied applicability claim.
func PublicationAcceptanceDigest(record PublicationRecord) string {
	data, _ := json.Marshal(record)
	return fmt.Sprintf("v1:%x", sha256.Sum256(data))
}

// CarryPlanAcceptance preserves an unchanged member's original report and
// candidate under an explicitly approved amendment. The engine must first prove
// the member is outside the old/new affected closure. This validates its private
// old proof and live checkout again; it never presents carry-forward as fresh QA.
func (p GitProvider) CarryPlanAcceptance(ctx context.Context, metadata Metadata, snapshot Snapshot, original PublicationRecord, revision, amendment string) (PublicationRecord, error) {
	if original.PlanRevision == "" || revision == original.PlanRevision || len(revision) != 67 || len(amendment) != 67 {
		return PublicationRecord{}, errors.New("carry-forward requires distinct approved revisions and protected amendment identity")
	}
	verified, found, err := p.LoadPublicationAcceptance(ctx, metadata, snapshot, PublicationEvidence{PlanRevision: original.PlanRevision})
	if err != nil || !found || verified != original {
		return PublicationRecord{}, errors.Join(errors.New("original member acceptance is missing or changed"), err)
	}
	return p.RecordPublicationAcceptance(ctx, metadata, snapshot, original.AcceptanceReport, original.AcceptanceComment, PublicationEvidence{
		PlanRevision: revision, VerificationDigest: original.VerificationDigest, VerificationReceipt: original.VerificationReceipt,
		ReviewEvidenceDigest: original.ReviewEvidenceDigest,
		CarriedFromRevision:  original.PlanRevision, CarriedAcceptanceDigest: PublicationAcceptanceDigest(original), AmendmentDigest: amendment,
	})
}
