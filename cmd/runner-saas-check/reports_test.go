package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/saas"
)

func TestCommandOutputReadFromCannotBypassTheFixedBound(t *testing.T) {
	var out boundedOutput
	data := strings.Repeat("x", 100<<10)
	n, err := io.Copy(&out, bytes.NewBufferString(data))
	if err != nil || n != int64(len(data)) || out.total != n || out.Len() != 64<<10 {
		t.Fatal("command output escaped bound/count", n, out.total, out.Len(), err)
	}
	var read boundedOutput
	n, err = read.ReadFrom(strings.NewReader(data))
	if err != nil || read.total != n || read.Len() != 64<<10 {
		t.Fatal("ReadFrom bypassed bounded Write", n, read.total, read.Len(), err)
	}
}

func TestFailureReportsPreserveCompleteDataAndExplicitOverflow(t *testing.T) {
	e := saas.Evidence{Status: "incomplete"}
	p := proofFiles{e: &e, files: map[string][]byte{}}
	dir := t.TempDir()
	file := filepath.Join(dir, "failure.json")
	data, _ := json.Marshal(map[string]any{"success": false, "diagnostic": strings.Repeat("meaningful failing assertion; ", 1000)})
	if err := os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := p.jsonReport("reports/vue.json", file); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(p.files["reports/vue.json"], data) || !e.Reports[0].Complete || e.Reports[0].Truncated {
		t.Fatal("correction lost the full bounded failure report")
	}
	oversized, _ := json.Marshal(map[string]string{"diagnostic": strings.Repeat("x", 3<<20)})
	if err := os.WriteFile(file, oversized, 0600); err != nil {
		t.Fatal(err)
	}
	if err := p.jsonReport("reports/vue.json", file); err == nil {
		t.Fatal("oversized report treated as complete")
	}
	if e.Reports[0].Complete || !e.Reports[0].Truncated || len(p.files["reports/vue.json"]) > 2<<20 {
		t.Fatal("overflow flags/bounds lost")
	}
	if _, err := boundedFile(file, 1<<20); err == nil {
		t.Fatal("unbounded regular input read")
	}
}

func TestSourceCopyRefusesLinkedAndPrivateInput(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, ".env"), []byte("dummy"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := sourceCopy(source, filepath.Join(root, "private-copy")); err == nil {
		t.Fatal("runtime/private state copied")
	}
	if err := os.Remove(filepath.Join(source, ".env")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(root, filepath.Join(source, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := sourceCopy(source, filepath.Join(root, "linked-copy")); err == nil {
		t.Fatal("foreign linked source copied")
	}
}
