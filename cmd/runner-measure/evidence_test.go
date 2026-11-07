package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestStructuredEvidenceRejectsLinksTraversalInvalidJSONAndAggregateOverflow(t *testing.T) {
	for _, kind := range []string{"symlink", "directory", "hardlink", "invalid JSON", "not JSON", "aggregate overflow"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			outside := filepath.Join(t.TempDir(), "private.json")
			if err := os.WriteFile(outside, []byte(`{"private":true}`), 0600); err != nil {
				t.Fatal(err)
			}
			var err error
			switch kind {
			case "symlink":
				err = os.Symlink(outside, filepath.Join(dir, "escape.json"))
			case "hardlink":
				err = os.Link(outside, filepath.Join(dir, "alias.json"))
			case "directory":
				err = os.Mkdir(filepath.Join(dir, "nested.json"), 0700)
			case "invalid JSON":
				err = os.WriteFile(filepath.Join(dir, "invalid.json"), []byte("not JSON"), 0600)
			case "not JSON":
				err = os.WriteFile(filepath.Join(dir, "data.txt"), []byte(`{}`), 0600)
			case "aggregate overflow":
				for _, name := range []string{"first.json", "second.json"} {
					err = os.WriteFile(filepath.Join(dir, name), []byte(`"`+strings.Repeat("x", maxEvidenceBytes/2)+`"`), 0600)
					if err != nil {
						break
					}
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = collectEvidence(dir); err == nil {
				t.Fatal("unsafe structured evidence accepted")
			}
		})
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "request.json"), []byte(`{"seed":131,"complete":false}`), 0600); err != nil {
		t.Fatal(err)
	}
	files, err := collectEvidence(dir)
	if err != nil || len(files) != 1 || string(files["request.json"]) != `{"seed":131,"complete":false}` {
		t.Fatal("valid retained evidence was altered or labelled complete", files, err)
	}
}

func TestMeasurementRetainsVerboseLogsAndStructuredProof(t *testing.T) {
	goBinary, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	source, scratch := t.TempDir(), t.TempDir()
	if err = os.WriteFile(filepath.Join(source, "go.mod"), []byte("module sample\n\ngo 1.26.0\n"), 0600); err != nil {
		t.Fatal(err)
	}
	proof := `{"request":{"game":"nonogram","size":25,"band":"hard"},"seeds":[1,131,3489660928],"phases":[` + strings.Repeat(`{"work":123},`, 99) + `{"work":123}]}`
	body := fmt.Sprintf(`package sample
import("os";"path/filepath";"testing")
func BenchmarkEvidence(b *testing.B){
 if err:=os.WriteFile(filepath.Join(os.Getenv("RUNNER_MEASURE_EVIDENCE"),"request.json"),[]byte(%q),0600);err!=nil{b.Fatal(err)}
 for i:=0;i<100;i++{b.Logf("verbose phase diagnostic %%d",i)}
}`, proof)
	if err = os.WriteFile(filepath.Join(source, "sample_test.go"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	if err = run(goBinary, source, scratch, ".", "^BenchmarkEvidence$", "", true, false); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(scratch, "measurements.json"))
	var m measurement
	if err != nil || json.Unmarshal(data, &m) != nil {
		t.Fatal("retained measurement unavailable", err)
	}
	if m.EvidenceStatus != "provided" || string(m.Evidence["request.json"]) != proof || strings.Contains(m.Output, "output truncated") || strings.Contains(m.EvidenceNote, "complete evidence") {
		t.Fatal("structured proof or verbose stdout was lost or labelled complete", m)
	}
	for i := range 100 {
		if !strings.Contains(m.Output, fmt.Sprintf("verbose phase diagnostic %d\n", i)) {
			t.Fatalf("benchmark log %d was omitted from the full artifact", i)
		}
	}
}
