package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/runner/config"
)

func TestMaintenanceExcludesCommandsAndInvalidatesBeforeConfigSave(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	db, err := Create(t.Context(), dir, config.Settings{})
	if err != nil {
		t.Fatal(err)
	}
	if g, err := LockGuard(dir, true, false); err == nil {
		g.Close()
		t.Fatal("maintenance admitted alongside writer")
	}
	db.Close()
	g, err := LockGuard(dir, true, false)
	if err != nil {
		t.Fatal(err)
	}
	token := strings.Repeat("a", 64)
	if err = g.Seal(token); err != nil {
		t.Fatal(err)
	}
	for _, ro := range []bool{true, false} {
		if db, err = Open(t.Context(), dir, ro); err == nil {
			db.Close()
			t.Fatal("command admitted during maintenance")
		}
	}
	g.Close()
	db, err = Open(t.Context(), dir, true)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(GuardPath(dir))
	if err != nil || string(b) != token {
		t.Fatal("inspection invalidated snapshot", err)
	}
	if err = db.AllowMutation(t.Context()); err != nil {
		t.Fatal(err)
	}
	b, err = os.ReadFile(GuardPath(dir))
	if err != nil || len(b) != 0 {
		t.Fatal("configuration save permission did not durably invalidate snapshot", err)
	}
	db.Close()
}
