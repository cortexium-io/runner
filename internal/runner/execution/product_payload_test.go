//go:build darwin || linux

package execution

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	request := `Continue the retained mobile/Nonogram candidate using actual initial checks and candidate measurement before repair. Continue from restored files; avoid redesign or repeated broad exploration. Preserve themes, choices/identities, difficulty/uniqueness proofs, saves, desktop behavior, public validation, cancellation/work limits, generated files and dependencies.
Mobile: at 320/360/390 short phone viewports, primary/expanded boards and input/commit/Undo controls must fit initially and be legible, or offer accessible Zoom in/Fit board. Hidden overflow is insufficient. Support Shikaku finger drawing without page pan, two-corner input, reachable commit/cancel/Undo and edit/save/reload/reopen. New/resume from scrolled home enters the board without resetting play scroll. Hashi 15 selects a valid preset or exposes Regular/Dense. Preserve mouse/pen/keyboard. Native Chromium/WebKit qualification must pass; no physical-device claim.
The supplied original baseline is genuine but incomplete: no sidecars, truncated stdout, and the lost HTTP seed is not reproduced by passing controls. Initial measurement is of this retained candidate, not a replacement baseline. For ngm2 25 Hard require complete synthetic request/selection, initial/advanced seeds, attempts, phase/work/nodes/allocations, failures and cold comparisons supporting the cause/fix under the unchanged single 10-second request deadline. During measurement write complete flat JSON to RUNNER_MEASURE_EVIDENCE (<=64 files/8MiB total); no production/player logging, convenient seed replacement or relaxed ceilings. If a safe fix remains unsupported within limits, return blocked with precise unresolved evidence.
Frontend files are supplied explicitly as frontend_tests; extend regressions within that selection. Configured Go package ... and tests ^Test(Next.*|RunnerNonogram25Hard.*|V2(LineOracle|TwentyFiveCellLines|ExactAgainstTinyIndependentOracle|VariationReplayAndAuthority|AggregateBoundsAndCancellation|LateAndWarmCancellation|DFAKnownLineVisitsOnlyReachableStates|PlayerPublicationContract))$; benchmark ^BenchmarkRunnerNonogram25Hard$. No additional Hashi prototype investigation or v3 unlock. Runner owns checks/measurement/repair/review/qualification. Merge, production and restoration need separate approvals.`
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
		Deadline:    time.Date(2026, 11, 5, 12, 20, 0, 123456789, time.UTC),
		Setup:       runner.ExecutionSetup{Profile: *role.Execution},
		OutputTypes: role.Outputs,
		Outcomes:    []string{"blocked"},
		Inputs: map[string]runner.Value{
			"request":        runner.Text(request),
			"baseline":       {ArtifactID: "attempt_baseline/result"},
			"check":          feedback,
			"frontend_check": feedback,
			"frontend_tests": runner.Text(`["src/components/player-shell.test.ts","src/pages/home/setup-dialog.test.ts","src/pages/shikaku/ShikakuWorkspace.test.ts","src/pages/nonogram/NonogramWorkspace.test.ts","src/pages/kakuro/KakuroWorkspace.test.ts","src/pages/hashi/HashiWorkspace.test.ts","src/pages/hashi/v3-workspace.test.ts","src/components/BoardViewport.test.ts","src/pages/binary/BinaryWorkspace.test.ts","src/pages/calcudoku/CalcudokuWorkspace.test.ts","src/pages/futoshiki/FutoshikiWorkspace.test.ts"]`),
			"measurement":    {ArtifactID: "attempt_measure/result"},
			"review":         runner.Text(strings.Repeat("v", 240)),
			"qualification":  runner.Value{Inline: qualification},
		},
	}
	if len(a.Inputs) != 8 || runner.ValidateValues(a.Inputs, role.Inputs) != nil {
		t.Fatal("real correction must include the selected frontend files within the existing eight-input contract")
	}
	payload, schema, err := nativePayloadAt(a, a.Deadline.Add(-20*time.Minute))
	if err != nil {
		t.Fatal("combined correction exceeds the unchanged input limit", err)
	}
	var deadline struct {
		UTC       string `json:"deadline_utc"`
		Remaining int64  `json:"remaining_seconds"`
	}
	if json.Unmarshal([]byte(payload), &deadline) != nil || deadline.UTC != "2026-11-05T12:20:00.123456789Z" || deadline.Remaining != 1200 || role.Execution.MaxInput != 8<<10 {
		t.Fatal("realistic correction omitted the authoritative deadline or changed its input allowance")
	}
	encoded, _ := json.Marshal(schema)
	bytes := len(payload) + len(workspaceBase) + len(role.Execution.Instructions) + len(encoded)
	t.Logf("actual request %d bytes; qualification %d bytes; resolved correction %d/%d bytes", len(request), len(qualification), bytes, role.Execution.MaxInput)
}
