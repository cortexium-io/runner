package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func boundCollectionFixture(t *testing.T) (string, string) {
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
import("encoding/json";"os";"path/filepath";"testing")
func BenchmarkBound(b *testing.B){
 dir:=os.Getenv("RUNNER_MEASURE_EVIDENCE")
 if info,err:=os.Stat(dir);err!=nil||!info.IsDir(){b.Fatal("EVIDENCE must be an existing directory")}
 stamp:=map[string]string{}
 for _,name:=range []string{"RUNNER_MEASURE_ROUND","RUNNER_MEASURE_SOURCE_IDENTITY","RUNNER_MEASURE_SOURCE_SHA256","RUNNER_MEASURE_SOURCE_DIGEST_METHOD"}{
  stamp[name]=os.Getenv(name);if stamp[name]==""{b.Fatal("missing helper binding",name)}
 }
 if _,err:=os.Stat("missing");os.IsNotExist(err){
  data,err:=json.Marshal(stamp);if err!=nil{b.Fatal(err)}
  if err=os.WriteFile(filepath.Join(dir,"binding.json"),data,0600);err!=nil{b.Fatal(err)}
 }
 if _,err:=os.Stat("fail");err==nil{b.Fatal("incomplete candidate producer")}
 b.ReportMetric(1,"samples/op")
}
`
	if err = os.WriteFile(filepath.Join(source, "sample_test.go"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return goBinary, source
}

func measureBound(t *testing.T, goBinary, source, previous string, mode measurementMode) (measurement, string, string) {
	t.Helper()
	scratch := t.TempDir()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stdout
	os.Stdout = writer
	err = run(goBinary, source, scratch, ".", "^BenchmarkBound$", previous, mode)
	os.Stdout = saved
	writer.Close()
	text, readErr := io.ReadAll(reader)
	reader.Close()
	if err != nil || readErr != nil {
		t.Fatal(err, readErr)
	}
	raw, err := os.ReadFile(filepath.Join(scratch, "measurements.json"))
	var m measurement
	if err != nil || json.Unmarshal(raw, &m) != nil {
		t.Fatal("missing diagnostic report", err)
	}
	raw, err = os.ReadFile(filepath.Join(scratch, "domain.json"))
	var domain struct{ Outcome string }
	if err != nil || json.Unmarshal(raw, &domain) != nil {
		t.Fatal("missing domain result", err)
	}
	return m, domain.Outcome, string(text)
}

func TestBoundMeasurementSuppliesAuthoritativeSourceAndRound(t *testing.T) {
	for _, name := range []string{
		"RUNNER_MEASURE_EVIDENCE", "RUNNER_MEASURE_ROUND", "RUNNER_MEASURE_SOURCE_IDENTITY", "RUNNER_MEASURE_SOURCE_SHA256", "RUNNER_MEASURE_SOURCE_DIGEST_METHOD",
	} {
		t.Setenv(name, "inherited spoof")
	}
	goBinary, source := boundCollectionFixture(t)
	mode := measurementMode{BindSource: true, RequireEvidence: true, RepairCollection: true}
	m, outcome, _ := measureBound(t, goBinary, source, "", mode)
	var evidence map[string]string
	if json.Unmarshal(m.Evidence["binding.json"], &evidence) != nil || m.Source == nil || !m.SourceVerified || m.Source.Files == 0 || m.Source.Bytes == 0 || m.Failed || m.CollectionFailed || outcome != "continue" || m.Round != 1 {
		t.Fatal("first complete candidate did not carry a verified helper binding", outcome, m.Error)
	}
	for key, want := range map[string]string{
		"RUNNER_MEASURE_ROUND":                strconv.Itoa(m.Round),
		"RUNNER_MEASURE_SOURCE_IDENTITY":      m.Source.Identity,
		"RUNNER_MEASURE_SOURCE_SHA256":        m.Source.SHA256,
		"RUNNER_MEASURE_SOURCE_DIGEST_METHOD": m.Source.DigestMethod,
	} {
		if evidence[key] != want || strings.Contains(evidence[key], "spoof") {
			t.Fatal("producer environment differed from authoritative outer metadata", key, evidence[key], want)
		}
	}
}

func TestStrictProducerRepairWaitsForFirstCompleteMeasurement(t *testing.T) {
	goBinary, source := boundCollectionFixture(t)
	missing := filepath.Join(source, "missing")
	if err := os.WriteFile(missing, nil, 0600); err != nil {
		t.Fatal(err)
	}
	mode := measurementMode{BindSource: true, RequireEvidence: true, RepairCollection: true}
	previous := ""
	for range 2 {
		m, outcome, text := measureBound(t, goBinary, source, previous, mode)
		if outcome != "changes_requested" || !m.CollectionFailed || !m.Failed || !m.OutputComplete || !m.SourceVerified || m.EvidenceStatus != "incomplete" || m.Round != 1 || m.PreviousCompleteRound != 0 {
			t.Fatal("missing producer files entered the before-copy path or widened execution retry", outcome, m.Error)
		}
		previous = "command exited 0\n" + text
		if round, err := selection(".", "^BenchmarkBound$", previous); err != nil || round != 1 {
			t.Fatal("producer correction advanced the first complete round", round, err)
		}
	}
	if err := os.Remove(missing); err != nil {
		t.Fatal(err)
	}
	m, outcome, text := measureBound(t, goBinary, source, previous, mode)
	if m.Round != 1 || m.CollectionFailed || m.Failed || !m.SourceVerified || m.EvidenceStatus != "provided" || outcome != "continue" {
		t.Fatal("corrected producer skipped the first valid before report", outcome, m.Error)
	}
	previous = text
	if err := os.WriteFile(missing, nil, 0600); err != nil {
		t.Fatal(err)
	}
	m, outcome, text = measureBound(t, goBinary, source, previous, mode)
	if m.Round != 2 || m.PreviousCompleteRound != 1 || !m.CollectionFailed || outcome != "changes_requested" {
		t.Fatal("later producer error forgot the last complete round", outcome, m.Error)
	}
	if err := os.Remove(missing); err != nil {
		t.Fatal(err)
	}
	m, outcome, _ = measureBound(t, goBinary, source, text, mode)
	if m.Round != 2 || m.CollectionFailed || m.Failed || !m.SourceVerified || outcome != "ready" {
		t.Fatal("producer repair repeated the first before snapshot", outcome, m.Error)
	}
}

func TestStrictFailedCandidateCannotBecomeBeforeEvidence(t *testing.T) {
	goBinary, source := boundCollectionFixture(t)
	for _, name := range []string{"missing", "fail"} {
		if err := os.WriteFile(filepath.Join(source, name), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	m, outcome, text := measureBound(t, goBinary, source, "", measurementMode{BindSource: true, RequireEvidence: true, RepairCollection: true})
	if outcome != "blocked" || !m.CollectionFailed || !m.Failed || m.EvidenceStatus != "incomplete" || !m.OutputComplete || !m.SourceVerified || strings.Contains(text, "runner-measure round=") {
		t.Fatal("failed candidate benchmark was accepted as first before evidence", outcome, m.Error)
	}
	// The original baseline remains a scoped control result. It may record a
	// failed control without promising the candidate's new JSON sidecars.
	m, outcome, _ = measureBound(t, goBinary, source, "", measurementMode{Baseline: true, BindSource: true})
	if outcome != "continue" || !m.Baseline || !m.Failed || m.CollectionFailed || !m.SourceVerified || m.EvidenceStatus != "incomplete" {
		t.Fatal("source binding changed the original baseline contract", outcome, m.Error)
	}
}

func TestRequiredEvidenceRefusesUnboundBaselineAndSkippedModes(t *testing.T) {
	goBinary, source := boundCollectionFixture(t)
	for _, mode := range []measurementMode{
		{RequireEvidence: true},
		{Baseline: true, BindSource: true, RequireEvidence: true},
	} {
		if err := run(goBinary, source, t.TempDir(), ".", "^BenchmarkBound$", "", mode); err == nil {
			t.Fatal("invalid strict collection mode was admitted", mode)
		}
	}
	if err := run(goBinary, source, t.TempDir(), ".", "none", "", measurementMode{BindSource: true, RequireEvidence: true}); err == nil {
		t.Fatal("strict collection accepted skipped work")
	}
}

func TestSourceDriftBlocksProducerCorrection(t *testing.T) {
	source := t.TempDir()
	file := filepath.Join(source, "source.go")
	if err := os.WriteFile(file, []byte("original source"), 0600); err != nil {
		t.Fatal(err)
	}
	program := filepath.Join(t.TempDir(), "go-fixture")
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
	body := fmt.Sprintf("#!/bin/sh\nprintf changed > %s\nprintf 'BenchmarkBound 1 1 ns/op\\n'\n", quote(file))
	if err := os.WriteFile(program, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	m, outcome, _ := measureBound(t, program, source, "", measurementMode{BindSource: true, RequireEvidence: true, RepairCollection: true})
	if outcome != "blocked" || !m.CollectionFailed || !m.Failed || m.SourceVerified || !strings.Contains(m.Error, "source verification failed") {
		t.Fatal("source drift was treated as a routine missing-sidecar repair", outcome, m.Error)
	}
}

func TestInvalidSourceBlocksBeforeBenchmarkDispatch(t *testing.T) {
	source := t.TempDir()
	if err := os.Symlink(filepath.Join(t.TempDir(), "foreign"), filepath.Join(source, "linked")); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "started")
	program := filepath.Join(t.TempDir(), "go-fixture")
	body := fmt.Sprintf("#!/bin/sh\ntouch '%s'\n", strings.ReplaceAll(marker, "'", "'\\''"))
	if err := os.WriteFile(program, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	m, outcome, _ := measureBound(t, program, source, "", measurementMode{BindSource: true, RequireEvidence: true, RepairCollection: true})
	if outcome != "blocked" || !m.CollectionFailed || m.Source != nil || m.OutputBytes != 0 || !strings.Contains(m.Error, "source binding failed") {
		t.Fatal("invalid source entered producer repair", outcome, m.Error)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("benchmark ran without a valid source binding", err)
	}
}
