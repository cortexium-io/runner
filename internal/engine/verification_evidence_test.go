package engine

import (
	"os"
	"testing"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/github"
	"github.com/cortexium-io/runner/internal/workspace"
)

func TestVerificationEvidenceIsPrivateAndBoundToCandidateAndCriteria(t *testing.T) {
	service := reviewFeedbackTestEngine(t.TempDir())
	item := github.WorkItem{ID: "PVTI_evidence", Role: config.WorkRoleImplementer}
	content := github.DelegatedContentFor(github.WorkItem{ID: item.ID, Body: "Approved body"})
	metadata := workspace.Metadata{BranchName: "runner/evidence", Identity: workspace.Identity{Repository: "owner/repo"}}
	candidate := workspace.Candidate{CommitOID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", TreeOID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}
	criteria := []string{"focused test passes", "diff check passes"}
	evidence := []string{"go test ./focused passed", "git diff --check passed"}
	if err := service.saveVerificationEvidence(item, content, metadata, candidate, criteria, evidence); err != nil {
		t.Fatalf("save evidence: %v", err)
	}
	info, err := os.Stat(service.verificationEvidencePath(item.ID))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("evidence mode = %04o, want 0600", info.Mode().Perm())
	}
	loaded, err := service.loadVerificationEvidence(item, content, metadata, candidate, criteria)
	if err != nil || len(loaded) != 2 || loaded[0].Criterion != criteria[0] || loaded[0].Evidence != evidence[0] {
		t.Fatalf("loaded evidence = %#v, err=%v", loaded, err)
	}
	changed := candidate
	changed.TreeOID = "cccccccccccccccccccccccccccccccccccccccc"
	if loaded, err := service.loadVerificationEvidence(item, content, metadata, changed, criteria); err == nil || loaded != nil {
		t.Fatalf("candidate-mismatched evidence was accepted: %#v, %v", loaded, err)
	}
	if loaded, err := service.loadVerificationEvidence(item, content, metadata, candidate, []string{"different check", criteria[1]}); err == nil || loaded != nil {
		t.Fatalf("criterion-mismatched evidence was accepted: %#v, %v", loaded, err)
	}
}

func TestVerificationEvidenceRequiresOneEntryPerApprovedCheck(t *testing.T) {
	if _, err := verificationEvidenceEntries([]string{"one", "two"}, []string{"only one"}); err == nil {
		t.Fatal("mismatched verification evidence was accepted")
	}
}
