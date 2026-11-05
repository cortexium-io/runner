//go:build darwin || linux

package execution

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/release"
	"github.com/cortexium-io/runner/internal/runner"
)

// A real correction supplies both domains' feedback; the union role must not
// require another model-input allowance when it receives that evidence.
func TestCombinedProductCorrectionFitsExistingInputLimit(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "examples", "jacks-break", "roles", "product-owner.json"))
	var role runner.Role
	if err != nil || json.Unmarshal(data, &role) != nil {
		t.Fatal("combined owner unavailable", err)
	}
	request := `Complete mobile repair and Nonogram performance as one candidate. Preserve themes, puzzle choices/identities, difficulty/uniqueness proofs, saves, desktop behavior, public validation, work/cancellation limits, generated files and dependencies.
Mobile: at 320/360/390 short phone viewports, primary/expanded boards and input/commit/Undo controls must fit initially and be legible, or offer accessible Zoom in/Fit board. Hidden overflow is insufficient. Support Shikaku finger drawing without page pan, two-corner selection, reachable commit/cancel/Undo and edit/save/reload/reopen. New/resume from scrolled home must enter the board without resetting play scroll. Hashi 15 must choose a valid preset or expose Regular/Dense. Preserve mouse/pen/keyboard. Native Chromium/WebKit qualification must pass; no physical-device claim.
Nonogram ngm2 25 Hard: measure committed scaffold first. Capture complete synthetic request/selection, initial/advanced seeds, attempts, phases/work/nodes/allocations, failures and cold time. During measurement, write complete JSON evidence to RUNNER_MEASURE_EVIDENCE (flat *.json, <=64 files/8MiB total); truncated stdout is incomplete. The old HTTP seed is lost: passing three controls is not reproduction/resolution. Establish supported cause/fix, regression and bounded cold comparisons under the unchanged single 10-second request deadline. No convenient seed substitution, relaxed ceilings or production/player logging. If unresolved within limits, retain precise evidence/status. Name Go checks TestRunnerNonogram25Hard*; prove cancellation/HTTP boundaries if affected. Scaffold: internal/puzzlegen/nonogram_runner_test.go.
Hashi prototype failure is already triaged; no new native Hashi investigation or v3 unlock. Runner owns checks/measurement/repair/review/qualification. Merge, production and restoration need separate approvals.`
	qualification, err := json.Marshal(release.Receipt{
		Key:      "a_01a182cfe25c7d529fe03d7ce9b7ef1d",
		RunID:    "r_01a182cfe25c7d529fe03d7ce9b7ef1d",
		Action:   "qualify",
		Policy:   strings.Repeat("a", 64),
		Source:   "fffff55553904ed4dcfdd538e96058a80155e160",
		Recovery: "source_correction_required",
		Note:     strings.Repeat("Board/control geometry failed; retained source requires repair. ", 10)[:600],
	})
	if err != nil {
		t.Fatal(err)
	}
	// Checks retain the bounded diagnostic tail; full baseline and candidate
	// proof is staged as explicitly declared read-only artifact inputs.
	feedback := runner.Text("command exited 0\n" + strings.Repeat("source assertion failed; details retained in artifact.\n", 10)[:480])
	a := runner.Assignment{
		Task:        role.Purpose,
		Setup:       runner.ExecutionSetup{Profile: *role.Execution},
		OutputTypes: role.Outputs,
		Outcomes:    []string{"blocked"},
		Inputs: map[string]runner.Value{
			"request":        runner.Text(request),
			"baseline":       {ArtifactID: "attempt_baseline/result"},
			"check":          feedback,
			"frontend_check": feedback,
			"measurement":    {ArtifactID: "attempt_measure/result"},
			"review":         runner.Text(strings.Repeat("v", 240)),
			"qualification":  runner.Value{Inline: qualification},
		},
	}
	payload, schema, err := nativePayload(a)
	if err != nil {
		t.Fatal("combined correction exceeds the unchanged input limit", err)
	}
	encoded, _ := json.Marshal(schema)
	bytes := len(payload) + len(workspaceBase) + len(role.Execution.Instructions) + len(encoded)
	t.Logf("actual request %d bytes; qualification %d bytes; resolved correction %d/%d bytes", len(request), len(qualification), bytes, role.Execution.MaxInput)
}
