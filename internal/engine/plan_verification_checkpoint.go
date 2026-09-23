package engine

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/execution"
	"github.com/cortexium-io/runner/internal/github"
	"github.com/cortexium-io/runner/internal/metrics"
	"github.com/cortexium-io/runner/internal/securefs"
	"github.com/cortexium-io/runner/internal/verification"
	"github.com/cortexium-io/runner/internal/workspace"
)

// This progress lives in the existing protected parent feedback record. It is
// NOT publication authority. Only RecordPublicationAcceptance creates that,
// after both accepted QA and a passing complete verification envelope.
type planVerificationProgress struct {
	Assignment               execution.Assignment            `json:"assignment"`
	Metadata                 workspace.Metadata              `json:"metadata"`
	Candidate                workspace.Snapshot              `json:"candidate"`
	AttemptID                string                          `json:"attempt_id"`
	SettingsDigest           string                          `json:"settings_digest"`
	ReviewerRole             string                          `json:"reviewer_role"`
	QAFailures               int                             `json:"qa_failures"`
	Accepted                 execution.Output                `json:"accepted"`
	Report                   string                          `json:"report"`
	Comment                  string                          `json:"comment"`
	Gate                     *verification.Result            `json:"gate,omitempty"`
	EnvelopeDigest           string                          `json:"envelope_digest,omitempty"`
	Failure                  *planVerificationFailure        `json:"failure,omitempty"`
	Classification           *planVerificationClassification `json:"classification,omitempty"`
	Publication              *workspace.PublicationRecord    `json:"publication,omitempty"`
	EvidenceCollectionDigest string                          `json:"evidence_collection_digest,omitempty"`
}

type planVerificationFailure struct {
	Phase       string `json:"phase"`
	ExecutionID string `json:"execution_id"`
	ExitCode    int    `json:"exit_code"`
}

type planVerificationClassification struct {
	AttemptID     string            `json:"attempt_id"`
	WorkspacePath string            `json:"workspace_path"`
	Deadline      time.Time         `json:"deadline"`
	FailureDigest string            `json:"failure_digest"`
	Result        *execution.Output `json:"result,omitempty"`
	Failed        bool              `json:"failed"`
}

func (p *planVerificationProgress) classificationPending() bool {
	return p != nil && p.Classification != nil && p.Classification.Result == nil
}

func planProgressDigest(value any) string {
	b, _ := json.Marshal(value)
	return fmt.Sprintf("%x", sha256.Sum256(b))
}

func (p *planVerificationProgress) validate() error {
	if p == nil || p.AttemptID == "" || p.SettingsDigest == "" || p.ReviewerRole == "" || p.QAFailures < 0 || !p.Candidate.Clean || !reviewObjectID(p.Candidate.Head) || !reviewObjectID(p.Candidate.Tree) || p.Candidate.Fingerprint == "" || p.Assignment.Spec.PlanContext == nil || p.Assignment.Spec.ReviewScope != execution.ReviewScopePlan || p.Assignment.Spec.ReviewCandidateOID != p.Candidate.Head || p.Assignment.Spec.ReviewBaseOID != p.Metadata.BaseRevision || p.Assignment.Spec.ItemID != p.Metadata.Identity.ItemID {
		return errors.New("parent verification progress has incomplete candidate or assignment identity")
	}
	if p.EvidenceCollectionDigest != "" && (len(p.EvidenceCollectionDigest) != 64 || !reviewObjectID(p.EvidenceCollectionDigest)) {
		return errors.New("parent review evidence collection digest is invalid")
	}
	if p.Accepted.Outcome != execution.OutcomeSucceeded || p.Accepted.ReviewAssessment == nil || p.Accepted.ReviewAssessment.Verdict != "accept" {
		return errors.New("parent verification progress lacks accepted QA")
	}
	if err := execution.ValidateReviewOutput(p.Assignment, p.Accepted); err != nil {
		return err
	}
	if err := metrics.ValidateUsage(p.Accepted.Usage); err != nil {
		return err
	}
	if p.Report != formatQAReport(*p.Accepted.ReviewAssessment, p.Accepted.Verification, p.Accepted.Usage) || p.Comment != formatQAComment(*p.Accepted.ReviewAssessment) {
		return errors.New("parent QA report no longer matches the retained assessment")
	}
	if p.Gate != nil && (p.Gate.Receipt != nil || p.Gate.CurrentCandidateCheck != nil) && p.EnvelopeDigest == "" {
		return errors.New("observed gate receipt has no protected envelope digest")
	}
	if p.Gate != nil && p.EnvelopeDigest != "" {
		if err := execution.AuthenticateVerificationEnvelope(p.Gate.Evidence(), p.EnvelopeDigest); err != nil {
			return err
		}
	} else if p.EnvelopeDigest != "" || p.Failure != nil {
		return errors.New("parent gate lost observed evidence")
	}
	if p.Failure != nil {
		if _, err := p.failureDiagnostics(); err != nil {
			return err
		}
	}
	if p.Classification != nil {
		if p.Failure == nil || p.Classification.AttemptID == "" || !filepath.IsAbs(p.Classification.WorkspacePath) || filepath.Clean(p.Classification.WorkspacePath) != p.Classification.WorkspacePath || p.Classification.Deadline.IsZero() || p.Classification.FailureDigest != p.failureDigest() {
			return errors.New("parent classifier is not bound to the observed failure")
		}
		if p.Classification.Result != nil {
			if err := metrics.ValidateUsage(p.Classification.Result.Usage); err != nil {
				return err
			}
		}
	}
	return nil
}

func (p *planVerificationProgress) failureDigest() string {
	return planProgressDigest(struct {
		Envelope string
		Failure  *planVerificationFailure
	}{p.EnvelopeDigest, p.Failure})
}

// Verify the exact bounded output bytes against the supervisor's receipt. A
// model's explanation, exception string or an exit-code guess cannot enter here.
func (p *planVerificationProgress) failureDiagnostics() ([]byte, error) {
	if p.Gate == nil || p.Failure == nil || !p.Gate.Invocation.CleanupResolved || p.Gate.Invocation.Outcome != "failed" || p.Failure.ExitCode <= 0 {
		return nil, errors.New("gate failure is not an observed resolved command exit")
	}
	f := p.Failure
	var reportDigest string
	var output any
	switch f.Phase {
	case "heavy":
		r := p.Gate.Receipt
		if r == nil || p.Gate.Historical || r.ExecutionID != f.ExecutionID || r.ExecutionID != p.Gate.Invocation.ExecutionID+"/heavy" || r.Outcome != "failed" || !r.CleanupResolved || r.ExitCode == nil || *r.ExitCode != f.ExitCode {
			return nil, errors.New("heavy failure identity changed")
		}
		reportDigest, output = r.ReportDigest, p.Gate.Output
	case "current_candidate_check":
		r := p.Gate.CurrentCandidateCheck
		if r == nil || r.ExecutionID != f.ExecutionID || r.ExecutionID != p.Gate.Invocation.ExecutionID+"/current" || r.Outcome != "failed" || !r.CleanupResolved || r.ExitCode == nil || *r.ExitCode != f.ExitCode || p.Gate.CurrentCandidateOutput == nil {
			return nil, errors.New("current-candidate failure identity changed")
		}
		reportDigest, output = r.ReportDigest, p.Gate.CurrentCandidateOutput
	default:
		return nil, errors.New("only supported check failures can be classified")
	}
	data, err := json.Marshal(output)
	if err != nil || len(data) > 256*1024 || planProgressDigest(output) != reportDigest {
		return nil, errors.Join(errors.New("gate diagnostics are unavailable, oversized or changed"), err)
	}
	return data, nil
}

func (s *Engine) planReviewSettings(role, directory string) string {
	return planProgressDigest(s.executionConfig(role, s.roleHarness(role), directory))
}

func (s *Engine) savePlanVerification(item github.WorkItem, content github.DelegatedContent, progress *planVerificationProgress) error {
	if err := progress.validate(); err != nil {
		return err
	}
	record, err := s.readReviewFeedbackRecord(item)
	if err != nil {
		return err
	}
	if record == nil {
		record = &reviewFeedbackRecord{Version: reviewFeedbackVersion, ItemID: item.ID, DelegatedContentDigest: content.Digest, Items: []string{}}
	}
	if record.DelegatedContentDigest != content.Digest {
		return errors.New("parent progress cannot overwrite a different approved contract")
	}
	if prior := record.PlanVerification; prior != nil && (prior.AttemptID != progress.AttemptID || prior.Candidate.Head != progress.Candidate.Head || prior.Assignment.Spec.PlanContext.Revision != progress.Assignment.Spec.PlanContext.Revision || prior.Gate != nil && progress.Gate != nil && prior.Gate.Invocation.ExecutionID != progress.Gate.Invocation.ExecutionID) {
		if prior.classificationPending() {
			return errors.New("uncertain parent classification cannot be superseded")
		}
		data, err := json.Marshal(record)
		if err != nil {
			return err
		}
		archive := s.reviewFeedbackPath(item.ID) + ".verification-" + planProgressDigest(record)
		if err := securefs.WriteFileExclusive(archive, data, 0o600); err != nil {
			stored, readErr := readAmendmentEvidence(archive)
			if readErr != nil || string(stored) != string(data) {
				return errors.Join(err, readErr)
			}
		}
	}
	record.PlanVerification = progress
	return s.writeReviewFeedback(*record)
}

func (s *Engine) revalidatePlanProgress(ctx context.Context, action github.AuthorizedAction, p *planVerificationProgress) (github.AuthorizedAction, error) {
	if err := p.validate(); err != nil {
		return action, err
	}
	fresh, err := s.source.Authorize(ctx, github.WorkItem{ID: action.Item.ID})
	if err != nil {
		return action, err
	}
	content, err := fresh.DelegatedContent()
	if err != nil {
		return action, err
	}
	publication := s.cfg.LaneIDForStatus(fresh.Item.Status) == s.cfg.PublicationLaneID()
	if publication {
		qaLane, ok := s.cfg.Lane(s.cfg.LaneIDForStatus(s.cfg.GitHubProject.QAStatus))
		if !ok || qaLane.Role != p.ReviewerRole || s.cfg.RoleContract(qaLane.Role) != config.WorkRoleReviewer || p.Publication == nil {
			return action, errors.New("publication lost its original approved reviewer profile")
		}
	} else if fresh.Role != p.ReviewerRole {
		return action, errors.New("parent reviewer profile changed")
	}
	if fresh.Item.ID != p.Assignment.Spec.ItemID || content.Digest != p.Assignment.Spec.DelegatedContentDigest || content.BodySnapshot != p.Assignment.Spec.ApprovedBodySnapshot || fresh.Item.Branch != p.Metadata.BranchName || p.SettingsDigest != s.planReviewSettings(p.ReviewerRole, p.Metadata.WorktreePath) {
		return action, errors.New("parent acceptance authority, destination or reviewer settings changed")
	}
	if _, _, err := s.planGateForReviewer(ctx, fresh, p.ReviewerRole); err != nil {
		return action, err
	}
	// Reconstruct only the retained review context for equality validation. The
	// signed publication action is never rewritten or used to launch a reviewer.
	reviewItem := fresh.Item
	if publication {
		reviewItem.Role = p.ReviewerRole
	}
	if err := s.revalidateDeliveryAssignment(ctx, reviewItem, p.Assignment); err != nil {
		return action, err
	}
	comments, err := s.source.ItemComments(ctx, fresh.Item)
	if err != nil {
		return action, err
	}
	// Ignore only our exact, authenticated-actor publication comment, whose
	// bytes derive from this retained acceptance, if it was added after QA.
	// A renewed review can already include that historical comment; stripping
	// it then would manufacture a context change. Other changes require QA.
	comment := p.Comment
	if p.Publication != nil {
		// Publication reuses the immutable record's original text even when
		// renewed QA reached acceptance with a different explanation.
		comment = p.Publication.AcceptanceComment
	}
	publicationComment := qaCommentMarker(fresh.Item.ID, p.Candidate.Head, comment) + "\n\n" + comment
	if !slices.Equal(humanCommentContext(comments), p.Assignment.Spec.ReviewCommentContext) {
		comments = slices.DeleteFunc(comments, func(c github.ItemComment) bool { return strings.TrimSpace(c.Body) == publicationComment })
		if !slices.Equal(humanCommentContext(comments), p.Assignment.Spec.ReviewCommentContext) {
			return action, errors.New("parent comment context changed; QA applicability needs renewed assessment")
		}
	}
	return fresh, nil
}
