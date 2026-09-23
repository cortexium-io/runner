package execution

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/subprocess"
)

func TestReviewEvidenceGrantsAreReadOnlyAndReviewerOnly(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	profile, err := ProfileForRole(RoleReviewer)
	if err != nil {
		t.Fatal(err)
	}
	w, err := prepareExecutionWorkspace(t.Context(), subprocess.OSRunner{}, profile, t.TempDir(), config.ExecutionConfig{ReviewEvidenceRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	defer w.cleanup()
	if w.ReviewEvidenceRoot != root || pathInsideOrEqual(root, w.Dir) || !slices.Contains(repositoryReadRoots(profile, w), root) {
		t.Fatalf("evidence escaped the isolated read-only scope: %#v", w)
	}
	args := codexInvocationArgs(profile, w, false, config.HarnessConfigModeIsolated, "codex")
	quoted, _ := json.Marshal(root)
	if !strings.Contains(strings.Join(args, " "), string(quoted)+`="read"`) || strings.Contains(strings.Join(args, " "), string(quoted)+`="write"`) {
		t.Fatalf("Codex lacks an explicit read-only evidence grant: %q", args)
	}
	var claude map[string]any
	if err := json.Unmarshal([]byte(claudeSandboxSettingsForConfig(profile, w, false, config.HarnessConfigModeIsolated)), &claude); err != nil {
		t.Fatal(err)
	}
	filesystem := claude["sandbox"].(map[string]any)["filesystem"].(map[string]any)
	for _, key := range []string{"allowRead", "denyWrite"} {
		found := false
		for _, path := range filesystem[key].([]any) {
			found = found || path == root
		}
		if !found {
			t.Fatalf("Claude %s omitted evidence root: %#v", key, filesystem)
		}
	}
	for _, required := range []string{"manifest.json", "members/*/manifest.json", "provenance.member_id", "within that member's tree only", "recovered_historical_requires_parent_review", "combined candidate", "not mean member evidence is absent"} {
		if !strings.Contains(profileRepositoryInstruction(w), required) {
			t.Fatalf("evidence mapping or provenance boundary unavailable in harness prompt: %s", required)
		}
	}
	for _, role := range []RoleContract{RoleImplementer, RolePlanner, RoleSynthesis, RoleProbe} {
		profile, err := ProfileForRole(role)
		if err != nil {
			t.Fatal(err)
		}
		if launch, err := prepareExecutionWorkspace(t.Context(), subprocess.OSRunner{}, profile, t.TempDir(), config.ExecutionConfig{ReviewEvidenceRoot: root}); err == nil {
			launch.cleanup()
			t.Fatalf("%s acquired review evidence", role)
		}
	}
	if _, err := prepareExecutionWorkspace(t.Context(), subprocess.OSRunner{}, profile, t.TempDir(), config.ExecutionConfig{ReviewEvidenceRoot: filepath.Join(root, "missing")}); err == nil {
		t.Fatal("missing evidence root accepted")
	}
}
