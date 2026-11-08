package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func collectionFixture(t *testing.T) (string, string) {
	t.Helper()
	goBinary, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	source := t.TempDir()
	if err = os.WriteFile(filepath.Join(source, "go.mod"), []byte("module sample\n\ngo 1.26.0\n"), 0600); err != nil {
		t.Fatal(err)
	}
	body := `package sample
import("os";"path/filepath";"strings";"testing")
func BenchmarkCollection(b *testing.B){
 if _,err:=os.Stat("verbose");err==nil {b.Log("private-raw-prefix:"+strings.Repeat("x",400<<10))}
 dir:=os.Getenv("RUNNER_MEASURE_EVIDENCE")
 if _,err:=os.Stat("unsafe");err==nil {
  if err=os.Symlink(filepath.Join(dir,"..","outside.json"),filepath.Join(dir,"samples.json"));err!=nil{b.Fatal(err)}
 }else {
  proof:=[]byte("{\"samples\":[{\"phase\":\"after-output\",\"work\":1}]}")
  if _,err:=os.Stat("large");err==nil {proof=[]byte("{\"value\":\""+strings.Repeat("<",3<<20)+"\"}")}
  if err=os.WriteFile(filepath.Join(dir,"samples.json"),proof,0600);err!=nil{b.Fatal(err)}
 }
 b.Log("collector finished")
 if _,err:=os.Stat("fail");err==nil {b.Fatal("additional benchmark failure")}
}
`
	if err = os.WriteFile(filepath.Join(source, "sample_test.go"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return goBinary, source
}

func measureCollection(t *testing.T, goBinary, source, previous string, repair bool) (measurement, map[string]string, string) {
	t.Helper()
	scratch := t.TempDir()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout := os.Stdout
	os.Stdout = writer
	err = run(goBinary, source, scratch, ".", "^BenchmarkCollection$", previous, measurementMode{RepairCollection: repair})
	os.Stdout = stdout
	writer.Close()
	output, readErr := io.ReadAll(reader)
	reader.Close()
	if err != nil || readErr != nil {
		t.Fatal(err, readErr)
	}
	data, err := os.ReadFile(filepath.Join(scratch, "measurements.json"))
	var m measurement
	if err != nil || json.Unmarshal(data, &m) != nil || len(data) > 16<<20 {
		t.Fatal("diagnostic report missing or outside the unchanged artifact bound", err)
	}
	data, err = os.ReadFile(filepath.Join(scratch, "domain.json"))
	var domain map[string]string
	if err != nil || json.Unmarshal(data, &domain) != nil {
		t.Fatal(err)
	}
	return m, domain, string(output)
}

func TestCollectionOverflowRetainsBoundedDiagnosticsAndNeverAdvancesRound(t *testing.T) {
	goBinary, source := collectionFixture(t)
	verbose := filepath.Join(source, "verbose")
	if err := os.WriteFile(verbose, nil, 0600); err != nil {
		t.Fatal(err)
	}
	previous := ""
	for range 2 {
		m, domain, stdout := measureCollection(t, goBinary, source, previous, true)
		if domain["outcome"] != "changes_requested" || !m.CollectionFailed || !m.Failed || m.OutputComplete || m.Round != 1 || m.PreviousCompleteRound != 0 || len(m.Output) != 256<<10 || m.OutputBytes <= 400<<10 || len(m.OutputSHA256) != 64 || m.EvidenceStatus != "provided" || !strings.Contains(string(m.Evidence["samples.json"]), "after-output") {
			t.Fatal("known overflow lost bounded data, safe completed sidecars or the original logical round", domain, m.Round, m.OutputBytes)
		}
		prefix := sha256.Sum256([]byte(m.Output))
		if m.OutputSHA256 == hex.EncodeToString(prefix[:]) || strings.Contains(stdout, "private-raw-prefix") || strings.Contains(stdout, "runner-measure round=") {
			t.Fatal("overflow hash covered only the prefix or incomplete output was echoed as a completed round")
		}
		previous = "command exited 0\n" + stdout
		if next, err := selection(".", "^BenchmarkCollection$", previous); err != nil || next != 1 {
			t.Fatal("repeated collection error advanced the first complete measurement", next, err)
		}
	}
	if err := os.Remove(verbose); err != nil {
		t.Fatal(err)
	}
	first, domain, stdout := measureCollection(t, goBinary, source, previous, true)
	if first.Round != 1 || first.CollectionFailed || first.Failed || !first.OutputComplete || domain["outcome"] != "continue" {
		t.Fatal("source correction skipped the first valid before-copy measurement", first.Round, domain)
	}
	previous = "command exited 0\n" + stdout
	if err := os.WriteFile(verbose, nil, 0600); err != nil {
		t.Fatal(err)
	}
	failed, domain, stdout := measureCollection(t, goBinary, source, previous, true)
	if failed.Round != 2 || failed.PreviousCompleteRound != 1 || domain["outcome"] != "changes_requested" {
		t.Fatal("later overflow forgot the last complete measurement", failed.Round, domain)
	}
	if err := os.Remove(verbose); err != nil {
		t.Fatal(err)
	}
	repaired, domain, _ := measureCollection(t, goBinary, source, "command exited 0\n"+stdout, true)
	if repaired.Round != 2 || repaired.CollectionFailed || !repaired.OutputComplete || domain["outcome"] != "ready" {
		t.Fatal("repaired second measurement skipped or repeated the before-copy stage", repaired.Round, domain)
	}
}

func TestCollectionRepairRequiresKnownOverflowAndSafeEvidence(t *testing.T) {
	goBinary, source := collectionFixture(t)
	for _, mode := range []string{"legacy overflow", "unsafe evidence", "unsafe overflow", "failed child overflow"} {
		t.Run(mode, func(t *testing.T) {
			for _, name := range []string{"verbose", "unsafe", "fail"} {
				_ = os.Remove(filepath.Join(source, name))
			}
			if mode != "unsafe evidence" {
				if err := os.WriteFile(filepath.Join(source, "verbose"), nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "unsafe evidence" || mode == "unsafe overflow" {
				if err := os.WriteFile(filepath.Join(source, "unsafe"), nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "failed child overflow" {
				if err := os.WriteFile(filepath.Join(source, "fail"), nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			m, domain, _ := measureCollection(t, goBinary, source, "", mode != "legacy overflow")
			unsafe := mode == "unsafe evidence" || mode == "unsafe overflow"
			if domain["outcome"] != "blocked" || !m.CollectionFailed || !m.Failed || m.PreviousCompleteRound != 0 || unsafe && m.EvidenceStatus != "invalid" {
				t.Fatal("legacy or unsafe collection acquired routine source correction", mode, domain)
			}
		})
	}
	for _, tc := range []struct {
		selector string
		baseline bool
	}{
		{"^BenchmarkCollection$", true},
		{"none", false},
	} {
		if err := run(goBinary, source, t.TempDir(), ".", tc.selector, "", measurementMode{Baseline: tc.baseline, RepairCollection: true}); err == nil {
			t.Fatal("collection repair granted to baseline or skipped work")
		}
	}
}

func TestCollectionOverflowWithAbnormalChildRemainsBlocked(t *testing.T) {
	source := t.TempDir()
	program := filepath.Join(source, "go-fixture")
	if err := os.WriteFile(program, []byte("#!/bin/sh\nhead -c 400000 /dev/zero\nkill -TERM $$\n"), 0700); err != nil {
		t.Fatal(err)
	}
	m, domain, _ := measureCollection(t, program, source, "", true)
	if domain["outcome"] != "blocked" || !m.CollectionFailed || m.OutputComplete || m.OutputBytes != 400000 {
		t.Fatal("abnormal child exit acquired source correction", domain, m.OutputBytes)
	}
}

func TestCollectionOverflowRetainsAdmittedLargeJSONWithoutEncodingExpansion(t *testing.T) {
	goBinary, source := collectionFixture(t)
	for _, name := range []string{"verbose", "large"} {
		if err := os.WriteFile(filepath.Join(source, name), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	m, domain, _ := measureCollection(t, goBinary, source, "", true)
	if !m.CollectionFailed || m.OutputComplete || domain["outcome"] != "changes_requested" || m.EvidenceStatus != "provided" || strings.Count(string(m.Evidence["samples.json"]), "<") != 3<<20 {
		t.Fatal("admitted sidecar was expanded beyond the report ceiling or lost from overflow diagnostics", domain, m.EvidenceStatus)
	}
}

func TestMeasurementDoesNotPublishDomainBeforeRetainingItsReport(t *testing.T) {
	goBinary, source := collectionFixture(t)
	scratch := t.TempDir()
	if err := os.Mkdir(filepath.Join(scratch, "measurements.json"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := run(goBinary, source, scratch, ".", "^BenchmarkCollection$", "", measurementMode{RepairCollection: true}); err == nil {
		t.Fatal("report write failure was ignored")
	}
	if _, err := os.Stat(filepath.Join(scratch, "domain.json")); !os.IsNotExist(err) {
		t.Fatal("domain result preceded retained measurement evidence", err)
	}
}
