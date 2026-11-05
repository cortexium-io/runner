//go:build darwin || linux

package execution

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	a := runner.Assignment{
		Task:        role.Purpose,
		Setup:       runner.ExecutionSetup{Profile: *role.Execution},
		OutputTypes: role.Outputs,
		Inputs: map[string]runner.Value{
			"request":        runner.Text(strings.Repeat("r", 2000)),
			"baseline":       runner.Text(strings.Repeat("b", 1400)),
			"check":          runner.Text(strings.Repeat("c", 256)),
			"frontend_check": runner.Text(strings.Repeat("f", 512)),
			"measurement":    runner.Text(strings.Repeat("m", 1024)),
			"review":         runner.Text(strings.Repeat("v", 240)),
			"qualification":  runner.Value{Inline: []byte(`{"key":"retained_qualification","head":"` + strings.Repeat("a", 40) + `"}`)},
		},
	}
	if _, _, err = nativePayload(a); err != nil {
		t.Fatal("combined correction exceeds the unchanged profile", err)
	}
}
