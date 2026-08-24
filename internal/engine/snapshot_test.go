package engine

import (
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/subprocess"
	"github.com/cortexium-io/runner/internal/workspace"
)

func TestSnapshotChangeErrorIdentifiesControlStateCategory(t *testing.T) {
	repo, _ := createPublicationRepository(t)
	before, err := workspace.CaptureSnapshotStateWithLimits(t.Context(), subprocess.OSRunner{}, repo, 30*time.Second, workspace.DefaultSnapshotLimits())
	if err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repo, "config", "snapshot.diagnostic", "changed")
	after, err := workspace.CaptureSnapshotStateWithLimits(t.Context(), subprocess.OSRunner{}, repo, 30*time.Second, workspace.DefaultSnapshotLimits())
	if err != nil {
		t.Fatal(err)
	}
	diagnostic := snapshotChangeError("snapshot changed", before, after).Error()
	if !strings.Contains(diagnostic, "Changed Git control state:\n- common Git config") {
		t.Fatalf("control-state category missing from diagnostic: %s", diagnostic)
	}
	if strings.Contains(diagnostic, "without an identifiable category") || strings.Contains(diagnostic, "Changed paths:") {
		t.Fatalf("metadata-only drift was presented as unexplained path drift: %s", diagnostic)
	}
}
