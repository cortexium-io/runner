package execution

import (
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/cortexium-io/runner/internal/runner"
)

func TestNativeStatePathUsesExistingSocketByteBoundary(t *testing.T) {
	suffix := string(filepath.Separator) + filepath.Join("jobs", "codex.sock")
	for _, size := range []int{99, 100, 101} {
		root := string(filepath.Separator) + strings.Repeat("x", size-len(suffix)-1)
		if got := len(filepath.Join(root, "jobs", "codex.sock")); got != size {
			t.Fatal("invalid path-boundary fixture", got, size)
		}
		err := ValidateNativeStatePath(root, &runner.CodexSettings{})
		if (err != nil) != (size > 100) {
			t.Fatal("native socket path boundary changed", size, err)
		}
	}
	root := string(filepath.Separator) + "ø" + strings.Repeat("x", 100-len(suffix)-2)
	socket := filepath.Join(root, "jobs", "codex.sock")
	if len(socket) != 101 || utf8.RuneCountInString(socket) != 100 {
		t.Fatal("invalid multibyte path fixture")
	}
	if err := ValidateNativeStatePath(root, &runner.CodexSettings{}); err == nil {
		t.Fatal("character count replaced the existing byte bound")
	}
	if err := ValidateNativeStatePath(strings.Repeat("x", 1000), nil); err != nil {
		t.Fatal("nonnative instances acquired a socket constraint", err)
	}
}

func TestNativePeerReusesStatePathCheckBeforeRuntimePreparation(t *testing.T) {
	c := &Codex{
		disk: &runner.Disk{Root: string(filepath.Separator) + strings.Repeat("x", 101)},
		cfg:  &runner.CodexSettings{Binary: "must-not-run"},
	}
	peer, err := c.openPeer(t.Context())
	if peer != nil || err == nil || !strings.Contains(err.Error(), "maximum path 100 bytes") {
		t.Fatal("native peer attempted runtime preparation before path validation", peer, err)
	}
}
