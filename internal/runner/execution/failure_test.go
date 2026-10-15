package execution

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/cortexium-io/runner/internal/runner/core"
)

func TestOnlyTerminalProviderFailuresAreTransient(t *testing.T) {
	for _, tc := range []struct {
		name, event string
		want        core.FailureKind
	}{
		{"provider 503", `{"type":"turn.failed","error":{"message":"unexpected status 503 Service Unavailable: upstream failure"}}`, core.Transient},
		{"rate limit", `{"type":"turn.failed","error":{"message":"unexpected status 429 Too Many Requests"}}`, core.Transient},
		{"bad configuration", `{"type":"turn.failed","error":{"message":"unexpected status 404 Not Found"}}`, ""},
		{"unknown failure", `{"type":"turn.failed","error":{"message":"something failed: 503"}}`, ""},
		{"nonterminal", `{"type":"error","message":"unexpected status 503 Service Unavailable"}`, ""},
		{"tool result", `{"type":"item.completed","item":{"aggregated_output":"unexpected status 503 Service Unavailable"}}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "events.jsonl")
			if err := os.WriteFile(path, []byte(tc.event+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if got := codexFailure(path); (got != "") != (tc.want == core.Transient) {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}
