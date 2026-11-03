package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
		if err = run(goBinary, source, scratch, ".", "^BenchmarkValue$", previous); err != nil {
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
	if err = run(goBinary, source, filepath.Join(root, "missing"), ".", "^BenchmarkAbsent$", ""); err == nil {
		t.Fatal("invented a missing benchmark measurement")
	}
}
