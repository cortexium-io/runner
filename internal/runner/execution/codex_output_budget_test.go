package execution

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
)

func TestNativeWholeJSONBudgetIsAuthoritativeWithoutNewFieldLimits(t *testing.T) {
	a := deadlinePayloadAssignment()
	a.Setup.Profile.MaxOutput = 512
	a.OutputTypes = map[string]string{"conclusion": "text", "summary": "text"}
	a.Outcomes = []string{"changes_requested", "blocked"}
	a.Inputs["max_output_bytes"] = runner.Text("unlimited")
	payload, rawSchema, err := nativePayloadAt(a, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var request struct {
		MaxOutput int            `json:"max_output_bytes"`
		Inputs    map[string]any `json:"inputs"`
	}
	if json.Unmarshal([]byte(payload), &request) != nil || request.MaxOutput != 512 || request.Inputs["max_output_bytes"] != "unlimited" {
		t.Fatal("task data shadowed the authoritative whole-result allowance", payload)
	}
	properties := rawSchema.(map[string]any)["properties"].(map[string]any)
	outputs := properties["outputs"].(map[string]any)["properties"].(map[string]any)
	for name, field := range outputs {
		if len(field.(map[string]any)) != 1 || field.(map[string]any)["type"] != "string" {
			t.Fatal("the existing output schema acquired an unverified field constraint", name, field)
		}
	}
	if len(properties["reason"].(map[string]any)) != 1 || !strings.Contains(reviewBase, "WHOLE final JSON") || !strings.Contains(workspaceBase, "UTF-8 bytes") || !strings.Contains(nativeBase, "max_output_bytes") {
		t.Fatal("the actual base instructions/schema omit the whole JSON contract")
	}
}

func TestNativeByteBoundaryRemainsStrictAndOversizedReviewIsNotAdopted(t *testing.T) {
	a := deadlinePayloadAssignment()
	a.Setup.Profile.MaxOutput = 512
	a.Outcomes = []string{"changes_requested"}
	encode := func(reason string) string {
		data, _ := json.Marshal(map[string]any{"outcome": "changes_requested", "outputs": map[string]string{"summary": "Evidence needs repair"}, "reason": reason})
		return string(data)
	}
	base := encode("")
	for _, size := range []int{512, 513, 868} {
		turn := goodTurn()
		turn.Items[0].Text = encode(strings.Repeat("r", size-len(base)))
		end := parseNativeResult(a, turn)
		if (end.Kind == "returned") != (size == 512) {
			t.Fatal("whole result was truncated, normalized or accepted outside its byte bound", size, end)
		}
	}
	turn := goodTurn()
	turn.Items[0].Text = encode(strings.Repeat("ø", (512-len(base))/2+1))
	if end := parseNativeResult(a, turn); end.Kind != "failed" {
		t.Fatal("character count replaced UTF-8 byte rejection", end)
	}
	turn.Items[0].Text = encode("")
	end := parseNativeResult(a, turn)
	if end.Kind != "returned" || end.Result.Outcome != "changes_requested" {
		t.Fatal("a new compliant review cannot enter configured correction", end)
	}
}
