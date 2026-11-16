//go:build darwin || linux

package execution

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/runner"
)

func TestHarnessWholeResultBudgetIsExplicitInCurrentPayload(t *testing.T) {
	a := harnessAssignment(t)
	a.Setup.Profile.MaxOutput = 8192
	a.Inputs["max_output_bytes"] = runner.Text("unlimited")
	a.TaskContext = &runner.TaskContext{
		Goal:            "Complete the original goal",
		PreviousFailure: "Prior final exceeded the whole-result byte bound",
	}
	prompt, schema, err := harnessPayload(a)
	if err != nil {
		t.Fatal(err)
	}
	var request struct {
		MaxOutput    int                `json:"max_output_bytes"`
		Inputs       map[string]string  `json:"inputs"`
		Instructions string             `json:"instructions"`
		Context      runner.TaskContext `json:"task_context"`
	}
	if err := json.Unmarshal([]byte(prompt), &request); err != nil || request.MaxOutput != 8192 || request.Inputs["max_output_bytes"] != "unlimited" || request.Context.PreviousFailure != a.TaskContext.PreviousFailure {
		t.Fatal("native payload lost authoritative limit or retry feedback", request, err)
	}
	if !strings.Contains(request.Instructions, resultBudgetBase) || strings.Contains(string(schema), "maxLength") {
		t.Fatal("current native harness omitted the shared whole-JSON guidance or substituted character/field limits")
	}
}

func TestHarnessCompactSummaryCanRetainLargerDeclaredEvidenceArtifact(t *testing.T) {
	a := harnessAssignment(t)
	a.Setup.Profile.MaxOutput = 8192
	a.OutputTypes = map[string]string{"summary": "text", "evidence": "artifact"}
	output, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	evidence := strings.Repeat("Detailed retained evidence.\n", 1024)
	if err := os.WriteFile(filepath.Join(output, "evidence.txt"), []byte(evidence), 0600); err != nil {
		t.Fatal(err)
	}
	wire := []byte(`{"outcome":"ready","outputs":{"summary":"Evidence is retained once.","evidence":"evidence.txt"},"reason":""}`)
	stored := false
	end, err := harnessResult(a, wire, output, func(id string, data []byte) error {
		stored = id == a.AttemptID+"/evidence" && string(data) == evidence
		return nil
	})
	if err != nil || end.Kind != "returned" || !stored || len(evidence) <= int(a.Setup.Profile.MaxOutput) || end.Result.Outputs["evidence"].ArtifactID != a.AttemptID+"/evidence" {
		t.Fatal("declared file evidence was charged to the inline result budget or failed retention", end, err)
	}
}

func TestHarnessOverflowWithoutMeasurementsDoesNotInventTransportCause(t *testing.T) {
	// A retained/recovered overflow can predate exact byte accounting. Missing
	// observations are unknown, not evidence of a zero-byte stream limit.
	receipt := completedHarnessReceipt(runner.Assignment{}, harnessEvents{FinalOverflow: true, Limit: 8192}, false, nil, "")
	if receipt.Kind != "failed" || !strings.Contains(receipt.Reason, "unavailable") || strings.Contains(receipt.Reason, "stream event exceeded") || strings.Contains(receipt.Reason, "0-byte") {
		t.Fatal("missing measurements were misrepresented as observed transport loss", receipt)
	}
}

func TestHarnessWholeJSONByteBoundaryRetainsExactNonASCIIAndEscapedSize(t *testing.T) {
	a := harnessAssignment(t)
	a.Setup.Profile.MaxOutput = 8192
	a.OutputTypes = map[string]string{"report": "text"}
	a.TaskContext = &runner.TaskContext{Goal: "Deliver compact structured research"}
	encode := func(padding string) []byte {
		report, _ := json.Marshal(map[string]string{
			"quoted":  "\"\\\n",
			"city":    "København",
			"padding": padding,
		})
		wire, _ := json.Marshal(map[string]any{
			"outcome":     "ready",
			"outputs":     map[string]string{"report": string(report)},
			"reason":      "",
			"task_action": nil,
		})
		return wire
	}
	padding := strings.Repeat("x", 8192-len(encode("")))
	for _, tc := range []struct {
		name, suffix string
		size         int
	}{
		{name: "exact whole byte boundary", size: 8192},
		{name: "one extra ASCII byte", suffix: "x", size: 8193},
		{name: "one extra non-ASCII character", suffix: "ø", size: 8194},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wire := encode(padding + tc.suffix)
			if len(wire) != tc.size {
				t.Fatal("fixture missed UTF-8/outer-escaping boundary", len(wire))
			}
			events := harnessEvents{Limit: 8192}
			events.final(wire)
			receipt := completedHarnessReceipt(a, events, false, nil, "")
			end, err := harnessResult(a, wire, t.TempDir(), nil)
			if err != nil || (receipt.Kind == "returned") != (tc.size == 8192) || (end.Kind == "returned") != (tc.size == 8192) {
				t.Fatal("native result was truncated, normalized or accepted above its whole-result limit", receipt.Kind, end, err)
			}
			// This is the durable bounded projection, not discarded final text.
			encoded, _ := json.Marshal(events.Proof)
			var proof runner.NativeProvenance
			if json.Unmarshal(encoded, &proof) != nil || proof.FinalResultBytes == nil || *proof.FinalResultBytes != tc.size || proof.MaxOutputBytes != 8192 || proof.OversizedEvents != 0 {
				t.Fatal("exact final-byte facts were lost or confused with transport loss", proof)
			}
			if tc.size > 8192 && (len(events.Final) != 0 || receipt.Reason != end.Reason || !strings.Contains(receipt.Reason, strconv.Itoa(tc.size)+" UTF-8 bytes; max_output_bytes=8192")) {
				t.Fatal("oversized native output survived or retry lacks its measured byte feedback", receipt)
			}
		})
	}
}
