package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestMeasurementCopyHonorsOutputBound(t *testing.T) {
	for _, size := range []int{256 << 10, (256 << 10) + 1} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			var output boundedOutput
			count, err := io.Copy(&output, io.LimitReader(strings.NewReader(strings.Repeat("x", size)), int64(size)))
			wantHash := sha256.Sum256([]byte(strings.Repeat("x", size)))
			if err != nil || count != int64(size) || output.Len() != min(size, 256<<10) || output.total != int64(size) || output.sha256() != hex.EncodeToString(wantHash[:]) || output.exceeded != (size > 256<<10) {
				t.Fatal("stdout was not fully drained/hashed within its unchanged retention bound", count, output.Len(), output.total, err)
			}
		})
	}
}

func TestVerboseBaselineKeepsOriginalLogsAndRejectsOverflow(t *testing.T) {
	goBinary, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "go.mod"), []byte("module sample\n\ngo 1.26.0\n"), 0600); err != nil {
		t.Fatal(err)
	}
	body := `package sample
import("strings";"testing")
func BenchmarkOriginal(b *testing.B){for i:=0;i<20;i++{b.Logf("original sample %02d",i)}}
func BenchmarkOverflow(b *testing.B){b.Log(strings.Repeat("x",400<<10))}
`
	if err := os.WriteFile(filepath.Join(source, "sample_test.go"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	scratch := t.TempDir()
	if err := run(goBinary, source, scratch, ".", "^BenchmarkOriginal$", "", measurementMode{Baseline: true}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(scratch, "measurements.json"))
	var m measurement
	if err != nil || json.Unmarshal(data, &m) != nil || m.Failed || !m.Baseline || m.EvidenceStatus != "incomplete" || len(m.Evidence) != 0 || strings.Contains(m.Output, "output truncated") {
		t.Fatalf("original baseline logs were lost or research sidecars invented: %s; %v", data, err)
	}
	for i := range 20 {
		if !strings.Contains(m.Output, fmt.Sprintf("original sample %02d\n", i)) {
			t.Fatalf("original benchmark log %d missing", i)
		}
	}
	scratch = t.TempDir()
	if err := run(goBinary, source, scratch, ".", "^BenchmarkOverflow$", "", measurementMode{Baseline: true}); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(filepath.Join(scratch, "measurements.json"))
	if err != nil || json.Unmarshal(data, &m) != nil || !m.CollectionFailed || !m.Failed || m.OutputComplete || len(m.Output) != 256<<10 || m.OutputBytes <= 256<<10 || !strings.Contains(m.Error, "exceeds 256KiB") {
		t.Fatal("overflow lost diagnostics or claimed a complete baseline", err, m.Error)
	}
	data, err = os.ReadFile(filepath.Join(scratch, "domain.json"))
	var domain map[string]string
	if err != nil || json.Unmarshal(data, &domain) != nil || domain["outcome"] != "blocked" {
		t.Fatal("legacy baseline overflow entered measurement correction", string(data), err)
	}
}

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
	err = run(goBinary, source, t.TempDir(), ".", "^BenchmarkCohort", "", measurementMode{})
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
		if err = run(goBinary, source, scratch, ".", selector, "", measurementMode{Baseline: true}); err != nil {
			t.Fatal(err)
		}
		data, _ := os.ReadFile(filepath.Join(scratch, "measurements.json"))
		var m measurement
		if json.Unmarshal(data, &m) != nil || !m.Baseline || !m.Failed || m.Error == "" || m.Skipped || m.EvidenceStatus != "incomplete" {
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
		if err = run(goBinary, source, scratch, ".", "^BenchmarkFailure$", previous, measurementMode{}); err != nil {
			t.Fatal(err)
		}
		data, _ := os.ReadFile(filepath.Join(scratch, "domain.json"))
		var domain map[string]string
		json.Unmarshal(data, &domain)
		if domain["outcome"] != "continue" {
			t.Fatal("failed candidate measurement advanced to review", domain)
		}
	}
	if err = run(goBinary, source, t.TempDir(), ".", "none", "", measurementMode{Baseline: true}); err == nil {
		t.Fatal("baseline skipped the measurement")
	}
	if err = run(filepath.Join(source, "absent-go"), source, t.TempDir(), ".", "^BenchmarkFailure$", "", measurementMode{Baseline: true}); err == nil {
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
		if err = run(goBinary, source, scratch, ".", "^BenchmarkValue$", previous, measurementMode{}); err != nil {
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
	if err = run(goBinary, source, scratch, ".", "^BenchmarkAbsent$", "", measurementMode{}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(scratch, "measurements.json"))
	var missing measurement
	if json.Unmarshal(data, &missing) != nil || !missing.Failed || missing.Error == "" {
		t.Fatal("invented a missing benchmark measurement", string(data))
	}
}
