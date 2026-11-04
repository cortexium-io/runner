package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestMeasurementRejectsUnsafeOrUnmeasuredSelections(t *testing.T) {
	for _, test := range []struct{ pkg, selector, previous string }{
		{"../private", "^BenchmarkRun$", ""},
		{"/absolute", "^BenchmarkRun$", ""},
		{"internal/foo", "", ""},
		{"internal/foo", "^TestFoo$", ""},
		{"internal/foo", "^Benchmark[", ""},
		{"internal/foo", "^BenchmarkFoo$", "unrecognized feedback"},
	} {
		if _, err := selection(test.pkg, test.selector, test.previous); err == nil {
			t.Fatal("invalid measurement accepted", test)
		}
	}
	for _, test := range []struct {
		selector, previous string
		round              int
	}{
		{"none", "", 1},
		{"^BenchmarkFoo$", "", 1},
		{"^BenchmarkFoo$", "command exited 0\nrunner-measure round=1 selector=^BenchmarkFoo$ skipped=false", 2},
	} {
		if n, err := selection("internal/foo", test.selector, test.previous); err != nil || n != test.round {
			t.Fatal(n, err)
		}
	}
}

func TestLongMeasurementKeepsRoundInCommandDiagnosticTail(t *testing.T) {
	goBinary, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	source := t.TempDir()
	os.WriteFile(filepath.Join(source, "go.mod"), []byte("module sample\n\ngo 1.26.0\n"), 0600)
	var body strings.Builder
	body.WriteString("package sample\nimport \"testing\"\n")
	for i := range 12 {
		fmt.Fprintf(&body, "func BenchmarkCohort%02d%s(b *testing.B){for i:=0;i<b.N;i++{_ = i+1}}\n", i, strings.Repeat("LongName", 8))
	}
	os.WriteFile(filepath.Join(source, "sample_test.go"), []byte(body.String()), 0600)
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	previous := os.Stdout
	os.Stdout = writer
	err = run(goBinary, source, t.TempDir(), ".", "^BenchmarkCohort", "", false)
	os.Stdout = previous
	writer.Close()
	output, readErr := io.ReadAll(reader)
	reader.Close()
	if err != nil || readErr != nil || len(output) <= 480 {
		t.Fatal("long measurement fixture failed", err, readErr, string(output))
	}
	// This is the command worker's retained diagnostic bound.
	tail := string(output[len(output)-480:])
	if round, err := selection(".", "^BenchmarkCohort", tail); err != nil || round != 2 {
		t.Fatal("round metadata was lost before the next configured step", round, err, tail)
	}
}

func TestBaselineRetainsFailureWithoutClaimingSuccess(t *testing.T) {
	goBinary, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	source := t.TempDir()
	os.WriteFile(filepath.Join(source, "go.mod"), []byte("module sample\n\ngo 1.26.0\n"), 0600)
	os.WriteFile(filepath.Join(source, "sample_test.go"), []byte("package sample\nimport \"testing\"\nfunc BenchmarkFailure(b *testing.B){b.Fatal(\"observed baseline failure\")}\n"), 0600)
	for _, selector := range []string{"^BenchmarkFailure$", "^BenchmarkAbsent$"} {
		scratch := t.TempDir()
		if err = run(goBinary, source, scratch, ".", selector, "", true); err != nil {
			t.Fatal(err)
		}
		data, _ := os.ReadFile(filepath.Join(scratch, "measurements.json"))
		var m measurement
		if json.Unmarshal(data, &m) != nil || !m.Baseline || !m.Failed || m.Error == "" || m.Skipped {
			t.Fatal("unsuccessful baseline was reported as success", string(data))
		}
		if selector == "^BenchmarkFailure$" && !strings.Contains(m.Output, "observed baseline failure") {
			t.Fatal("failure evidence was lost", m)
		}
		data, _ = os.ReadFile(filepath.Join(scratch, "domain.json"))
		var domain map[string]string
		json.Unmarshal(data, &domain)
		if domain["outcome"] != "continue" || domain["reason"] != m.Error {
			t.Fatal("baseline did not hand its real failure to the investigator", domain)
		}
	}
	for _, previous := range []string{"", "runner-measure round=1 selector=^BenchmarkFailure$ skipped=false"} {
		scratch := t.TempDir()
		if err = run(goBinary, source, scratch, ".", "^BenchmarkFailure$", previous, false); err != nil {
			t.Fatal(err)
		}
		data, _ := os.ReadFile(filepath.Join(scratch, "domain.json"))
		var domain map[string]string
		json.Unmarshal(data, &domain)
		if domain["outcome"] != "continue" {
			t.Fatal("failed candidate measurement advanced to review", domain)
		}
	}
	if err = run(goBinary, source, t.TempDir(), ".", "none", "", true); err == nil {
		t.Fatal("baseline skipped the measurement")
	}
	if err = run(filepath.Join(source, "absent-go"), source, t.TempDir(), ".", "^BenchmarkFailure$", "", true); err == nil {
		t.Fatal("missing runtime was sent to source repair")
	}
}

func TestMeasurementRunsActualBenchmarkAndDoesNotInventMissingSamples(t *testing.T) {
	goBinary, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	source := filepath.Join(root, "source")
	os.Mkdir(source, 0700)
	os.WriteFile(filepath.Join(source, "go.mod"), []byte("module sample\n\ngo 1.26.0\n"), 0600)
	os.WriteFile(filepath.Join(source, "sample_test.go"), []byte("package sample\nimport \"testing\"\nfunc BenchmarkValue(b *testing.B){for i:=0;i<b.N;i++{_ = i+1}}\n"), 0600)
	for round := 1; round <= 3; round++ {
		previous := ""
		if round > 1 {
			previous = fmt.Sprintf("runner-measure round=%d selector=^BenchmarkValue$ skipped=false", round-1)
		}
		scratch := filepath.Join(root, fmt.Sprint(round))
		if err = run(goBinary, source, scratch, ".", "^BenchmarkValue$", previous, false); err != nil {
			t.Fatal(err)
		}
		data, _ := os.ReadFile(filepath.Join(scratch, "domain.json"))
		var domain map[string]string
		json.Unmarshal(data, &domain)
		want := "ready"
		if round == 1 {
			want = "continue"
		}
		if domain["outcome"] != want {
			t.Fatal("unexpected measurement route", domain)
		}
	}
	scratch := filepath.Join(root, "missing")
	if err = run(goBinary, source, scratch, ".", "^BenchmarkAbsent$", "", false); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(scratch, "measurements.json"))
	var missing measurement
	if json.Unmarshal(data, &missing) != nil || !missing.Failed || missing.Error == "" {
		t.Fatal("invented a missing benchmark measurement", string(data))
	}
}
