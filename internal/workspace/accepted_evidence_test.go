package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/subprocess"
)

func memberEvidenceFixture(t *testing.T, id string) (Metadata, PublicationRecord, ReviewWorkspace) {
	t.Helper()
	root := t.TempDir()
	source := filepath.Join(root, id)
	if err := os.MkdirAll(filepath.Join(source, "reports"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"baseline.json", "final.json"} {
		if err := os.WriteFile(filepath.Join(source, "reports", name), []byte(id+":"+name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(source, "reports", "empty"), 0700); err != nil {
		t.Fatal(err)
	}
	metadata := Metadata{WorktreePath: source, BaseRef: "origin/plan", BaseRevision: strings.Repeat("a", 40), Identity: Identity{ItemID: id, DelegatedContentDigest: "content-" + id, Repository: "owner/repo", WorktreePath: source}}
	record := PublicationRecord{ItemID: id, DelegatedContentDigest: metadata.Identity.DelegatedContentDigest, Repository: "owner/repo", PlanRevision: "v1:" + strings.Repeat("c", 64), ApprovedBaseRef: metadata.BaseRef, ApprovedBaseOID: metadata.BaseRevision, CommitOID: strings.Repeat("b", 40), TreeOID: strings.Repeat("d", 40)}
	destination := filepath.Join(t.TempDir(), "evidence")
	e, err := copyReviewEvidence(t.Context(), source, destination, Candidate{record.CommitOID, record.TreeOID}, []string{"reports"}, DefaultSnapshotLimits(), MemberEvidenceProvenance("parent", record.PlanRevision, metadata))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.root.Close() })
	return metadata, record, ReviewWorkspace{EvidencePath: destination, evidence: e}
}

func TestAcceptedEvidencePreservesReviewedBytesAndResumesExclusiveCapture(t *testing.T) {
	m, record, review := memberEvidenceFixture(t, "member")
	// The producer changes after capture; acceptance must retain reviewed bytes.
	if err := os.WriteFile(filepath.Join(m.WorktreePath, "reports", "final.json"), []byte("later output"), 0600); err != nil {
		t.Fatal(err)
	}
	digest, err := review.PreserveEvidence(t.Context(), m)
	if err != nil {
		t.Fatal(err)
	}
	if again, err := review.PreserveEvidence(t.Context(), m); err != nil || again != digest {
		t.Fatalf("immutable capture replay: %s %v", again, err)
	}
	record.ReviewEvidenceDigest = digest
	snapshot, found, err := LoadAcceptedEvidence(t.Context(), m, record, "parent", []string{"reports"}, DefaultSnapshotLimits())
	if err != nil || !found {
		t.Fatalf("load: %v %v", found, err)
	}
	data, err := os.ReadFile(filepath.Join(snapshot.root, "files", "reports", "final.json"))
	if err != nil || string(data) != "member:final.json" {
		t.Fatalf("recaptured changed output: %q %v", data, err)
	}
	if snapshot.Manifest.Provenance.Kind != EvidenceAccepted || snapshot.Manifest.Provenance.AcceptanceDigest != "" {
		t.Fatal("wrong original provenance")
	}
	// Manifest-last interruption: existing verified payload is reusable, not
	// replaced; the absent manifest itself conveys no accepted authority.
	if err := os.Remove(filepath.Join(snapshot.root, "manifest.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := review.PreserveEvidence(t.Context(), m); err != nil {
		t.Fatalf("resume partial snapshot: %v", err)
	}
}

func TestAcceptedEvidenceRefusesChangedProvenanceOrBytes(t *testing.T) {
	for _, mutation := range []string{"payload", "extra", "symlink", "directory type", "missing directory", "member", "plan", "parent", "selection"} {
		t.Run(mutation, func(t *testing.T) {
			m, record, review := memberEvidenceFixture(t, "member")
			digest, err := review.PreserveEvidence(t.Context(), m)
			if err != nil {
				t.Fatal(err)
			}
			record.ReviewEvidenceDigest = digest
			root := filepath.Join(evidenceStore(m), digest)
			target := filepath.Join(root, "files", "reports", "final.json")
			parent := "parent"
			paths := []string{"reports"}
			switch mutation {
			case "payload":
				err = os.WriteFile(target, []byte("tampered"), 0600)
			case "extra":
				err = os.WriteFile(filepath.Join(root, "files", "reports", "extra"), []byte("unselected"), 0600)
			case "symlink":
				if err = os.Remove(target); err == nil {
					err = os.Symlink(filepath.Join(m.WorktreePath, "reports", "final.json"), target)
				}
			case "directory type", "missing directory":
				dir := filepath.Join(root, "files", "reports", "empty")
				err = os.Remove(dir)
				if err == nil && mutation == "directory type" {
					err = os.WriteFile(dir, nil, 0600)
				}
			case "member":
				record.ItemID = "other"
			case "plan":
				record.PlanRevision = "other"
			case "parent":
				parent = "other"
			case "selection":
				paths = []string{"reports/final.json"}
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := LoadAcceptedEvidence(t.Context(), m, record, parent, paths, DefaultSnapshotLimits()); err == nil {
				t.Fatal("changed evidence was accepted")
			}
		})
	}
}

func TestAcceptedEvidenceFollowsOnlyExplicitUnaffectedAmendmentCarry(t *testing.T) {
	for _, kind := range []string{EvidenceAccepted, EvidenceRecovered} {
		t.Run(kind, func(t *testing.T) { testEvidenceAmendmentCarry(t, kind) })
	}
}

func testEvidenceAmendmentCarry(t *testing.T, kind string) {
	t.Helper()
	repo := initGitRepo(t)
	provider := NewGitProvider(subprocess.OSRunner{})
	m, err := provider.Prepare(t.Context(), boundRequest(Request{WorkingDir: repo,
		WorktreeRoot: filepath.Join(t.TempDir(), "worktrees"), WorkID: "carried_evidence", BranchPrefix: "runner", BaseRef: "HEAD"}))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(m.WorktreePath, "receipt.json"), []byte(`{"original_execution":"synthetic"}`), 0600); err != nil {
		t.Fatal(err)
	}
	candidate, err := provider.ConstructCandidate(t.Context(), m, "Candidate with selected receipt")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := captureDefaultCheckoutSnapshotState(t.Context(), subprocess.OSRunner{}, m.WorktreePath, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	revision := "v1:" + strings.Repeat("a", 64)
	destination := filepath.Join(t.TempDir(), "evidence")
	e, err := copyReviewEvidence(t.Context(), m.WorktreePath, destination, candidate, []string{"receipt.json"}, DefaultSnapshotLimits(), MemberEvidenceProvenance("parent", revision, m))
	if err != nil {
		t.Fatal(err)
	}
	defer e.root.Close()
	review := ReviewWorkspace{EvidencePath: destination, evidence: e}
	digest, err := review.PreserveEvidence(t.Context(), m)
	if err != nil {
		t.Fatal(err)
	}
	recordDigest := digest
	if kind == EvidenceRecovered {
		recordDigest = ""
	}
	original, err := provider.RecordPublicationAcceptance(t.Context(), m, snapshot, "Accepted original execution.", "Accepted.", PublicationEvidence{PlanRevision: revision, ReviewEvidenceDigest: recordDigest})
	if err != nil {
		t.Fatal(err)
	}
	if kind == EvidenceRecovered {
		recovered, cleanup, err := CaptureRecoveryEvidence(t.Context(), m, original, "parent", []string{"receipt.json"}, DefaultSnapshotLimits())
		if err != nil {
			t.Fatal(err)
		}
		defer cleanup()
		if err := recovered.PreserveRecovered(t.Context(), m, original, DefaultSnapshotLimits()); err != nil {
			t.Fatal(err)
		}
		digest = recovered.Digest
	}
	record := original
	for _, next := range []string{"b", "c"} {
		record, err = provider.CarryPlanAcceptance(t.Context(), m, snapshot, record, "v1:"+strings.Repeat(next, 64), "v1:"+strings.Repeat("d", 64))
		if err != nil {
			t.Fatal(err)
		}
		loaded, found, err := LoadAcceptedEvidence(t.Context(), m, record, "parent", []string{"receipt.json"}, DefaultSnapshotLimits())
		if err != nil || !found || loaded.Digest != digest || loaded.Manifest.Provenance.PlanRevision != original.PlanRevision || loaded.Manifest.Provenance.Kind != kind {
			t.Fatalf("carry lost original evidence provenance: %v %v %+v", found, err, loaded)
		}
	}
	changed := record
	changed.DelegatedContentDigest = "changed-approved-content"
	if _, _, err := LoadAcceptedEvidence(t.Context(), m, changed, "parent", []string{"receipt.json"}, DefaultSnapshotLimits()); err == nil {
		t.Fatal("changed acceptance retained old evidence authority")
	}
	originalPath, err := publicationAcceptancePath(filepath.Dir(m.Identity.WorktreePath), original)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(originalPath); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadAcceptedEvidence(t.Context(), m, record, "parent", []string{"receipt.json"}, DefaultSnapshotLimits()); err == nil {
		t.Fatal("missing original carry authority was accepted")
	}
}

func TestCombinedEvidenceNamespacesReceiptsAndUsesAggregateLimit(t *testing.T) {
	var snapshots []EvidenceSnapshot
	for _, id := range []string{"first", "second"} {
		m, record, review := memberEvidenceFixture(t, id)
		digest, err := review.PreserveEvidence(t.Context(), m)
		if err != nil {
			t.Fatal(err)
		}
		record.ReviewEvidenceDigest = digest
		snapshot, _, err := LoadAcceptedEvidence(t.Context(), m, record, "parent", []string{"reports"}, DefaultSnapshotLimits())
		if err != nil {
			t.Fatal(err)
		}
		snapshots = append(snapshots, snapshot)
	}
	_, _, parent := memberEvidenceFixture(t, "parent")
	if err := parent.AddAcceptedEvidence(t.Context(), snapshots, DefaultSnapshotLimits()); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"first", "second"} {
		data, err := os.ReadFile(filepath.Join(parent.EvidencePath, "members", evidenceDigest([]byte(id)), "files", "reports", "baseline.json"))
		if err != nil || string(data) != id+":baseline.json" {
			t.Fatalf("member collision or missing receipt: %s %q %v", id, data, err)
		}
	}
	_, _, limited := memberEvidenceFixture(t, "parent")
	limits := DefaultSnapshotLimits()
	limits.MaxFileBytes = 1800
	limits.MaxTotalBytes = 2200
	if err := limited.AddAcceptedEvidence(t.Context(), snapshots, limits); err == nil {
		t.Fatal("collection exceeded the aggregate limit")
	}
}

func TestRecoveredEvidenceDoesNotRewriteOrImpersonateAcceptance(t *testing.T) {
	m, record, _ := memberEvidenceFixture(t, "member")
	original := record
	snapshot, cleanup, err := CaptureRecoveryEvidence(t.Context(), m, record, "parent", []string{"reports"}, DefaultSnapshotLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if snapshot.Manifest.Provenance.Kind != EvidenceRecovered || snapshot.Manifest.Provenance.AcceptanceDigest != PublicationAcceptanceDigest(record) {
		t.Fatal("recovery pretends earlier QA saw these bytes")
	}
	if err := snapshot.PreserveRecovered(t.Context(), m, record, DefaultSnapshotLimits()); err != nil {
		t.Fatal(err)
	}
	loaded, found, err := LoadAcceptedEvidence(t.Context(), m, record, "parent", []string{"reports"}, DefaultSnapshotLimits())
	if err != nil || !found || loaded.Digest != snapshot.Digest || record != original {
		t.Fatalf("recovery changed original acceptance or lost evidence: %v", err)
	}
	if err := os.WriteFile(filepath.Join(m.WorktreePath, "reports", "final.json"), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	changed, cleanup2, err := CaptureRecoveryEvidence(t.Context(), m, record, "parent", []string{"reports"}, DefaultSnapshotLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup2()
	if err := changed.PreserveRecovered(t.Context(), m, record, DefaultSnapshotLimits()); err == nil {
		t.Fatal("recovery replaced immutable historical bytes")
	}
}
