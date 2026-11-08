package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/cortexium-io/runner/internal/sourcesnapshot"
)

func sourceFixture(t *testing.T) string {
	t.Helper()
	source := directory(t)
	put(t, source, "source.go", []byte("package fixture\n"))
	return source
}

func boundReport(t *testing.T, source string, round int, phase string) []byte {
	t.Helper()
	snapshot, err := sourcesnapshot.Take(t.Context(), source)
	if err != nil {
		t.Fatal(err)
	}
	return changeReport(t, measurementFixture(t, round, 'a', phase), "source", snapshot.Stamp)
}

func TestHistoryBindsCurrentRestoredSourceAndPreservesOriginalCheckContext(t *testing.T) {
	dir, source := directory(t), sourceFixture(t)
	before := measurementFixture(t, 3, 'b', "profiled before")
	current := boundReport(t, source, 4, "experiment current")
	files := []string{put(t, dir, "before.json", before), put(t, dir, "current.json", current)}
	// A check context can describe failure. Carrying it is never a passing gate
	// or an assertion that its old report contains source provenance.
	checks := []byte("{\n  \"package\":\"...\",\"scope\":{\"status\":\"failed\",\"failed\":1}\n}\n")
	checkFile := put(t, dir, "checks.json", checks)
	scratch := filepath.Join(source, ".runner-tmp", "history")
	if err := os.MkdirAll(scratch, 0700); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(scratch, "initial.json")
	if n, err := run(options{Measurements: files, Output: output, Source: source, Checks: checkFile}); err != nil || n != 2 {
		t.Fatal("current source-bound history did not collect", n, err)
	}
	_, h := readHistory(t, output)
	if h.Checks == nil || h.Checks.Kind != "runner-go-check-context-v1" || h.Checks.Bytes != len(checks) || h.Checks.SHA256 != digest(checks) || !bytes.Equal([]byte(h.Checks.JSON), checks) || len(h.SourceBindings) != 1 || h.SourceBindings[0].MeasurementSHA256 != digest(current) || h.SourceBindings[0].Source != h.Entries[1].Source {
		t.Fatal("old checks were relabelled or current source binding was lost")
	}
	if h.Entries[0].Measurement != string(before) || h.Entries[1].Measurement != string(current) {
		t.Fatal("actual workspace binding rewrote original reports")
	}
	put(t, source, "source.go", []byte("package changed\n"))
	newCurrent := boundReport(t, source, 1, "next workflow current")
	newFile := put(t, dir, "new.json", newCurrent)
	appended := filepath.Join(scratch, "appended.json")
	if n, err := run(options{Previous: output, Measurements: []string{newFile}, Output: appended, Source: source}); err != nil || n != 3 {
		t.Fatal("source-bound append failed", n, err)
	}
	_, after := readHistory(t, appended)
	if len(after.SourceBindings) != 2 || after.SourceBindings[0] != h.SourceBindings[0] || after.SourceBindings[1].MeasurementSHA256 != digest(newCurrent) || after.Checks == nil || *after.Checks != *h.Checks || after.Entries[0] != h.Entries[0] || after.Entries[1] != h.Entries[1] {
		t.Fatal("append discarded original context, binding or experiment reports")
	}
	changedChecks := put(t, dir, "other-checks.json", []byte(`{"package":"...","scope":{"status":"passed"}}`))
	if _, err := run(options{Previous: appended, Output: filepath.Join(scratch, "replaced-context.json"), Checks: changedChecks}); err == nil {
		t.Fatal("old check context could be replaced by a different result")
	}
}

func TestHistorySourceBindingChecksLastReportBytesModesPathsAndCounts(t *testing.T) {
	for _, kind := range []string{"bytes", "mode", "path", "extra file", "file count", "byte count", "wrong last report"} {
		t.Run(kind, func(t *testing.T) {
			dir, source := directory(t), sourceFixture(t)
			raw := boundReport(t, source, 4, "current")
			var r report
			if err := json.Unmarshal(raw, &r); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "bytes":
				put(t, source, "source.go", []byte("package changed\n"))
			case "mode":
				if err := os.Chmod(filepath.Join(source, "source.go"), 0700); err != nil {
					t.Fatal(err)
				}
			case "path":
				if err := os.Rename(filepath.Join(source, "source.go"), filepath.Join(source, "other.go")); err != nil {
					t.Fatal(err)
				}
			case "extra file":
				put(t, source, "other.go", []byte("package fixture\n"))
			case "file count":
				r.Source.Files++
				raw = changeReport(t, raw, "source", r.Source)
			case "byte count":
				r.Source.Bytes++
				raw = changeReport(t, raw, "source", r.Source)
			}
			files := []string{put(t, dir, "current.json", raw)}
			if kind == "wrong last report" {
				files = append(files, put(t, dir, "old.json", measurementFixture(t, 3, 'b', "old before")))
			}
			output := put(t, dir, "output.json", []byte("preserved"))
			if _, err := run(options{Measurements: files, Output: output, Source: source}); err == nil {
				t.Fatal("caller metadata authenticated a different actual source", kind)
			}
			if data, _ := os.ReadFile(output); string(data) != "preserved" {
				t.Fatal("failed source binding changed an existing output")
			}
		})
	}
}

func TestHistoryRejectsForgedContextAndSourceBindingMetadata(t *testing.T) {
	e, err := completeEntry(measurementFixture(t, 1, 'a', "before"), 1)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := checkContext([]byte(`{"package":"...","scope":{"status":"blocked"}}`))
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*history){
		"context digest": func(h *history) { h.Checks.SHA256 = digest(nil) },
		"context bytes":  func(h *history) { h.Checks.Bytes++ },
		"context kind":   func(h *history) { h.Checks.Kind = "accepted" },
		"source digest":  func(h *history) { h.SourceBindings[0].MeasurementSHA256 = digest(nil) },
		"source stamp":   func(h *history) { h.SourceBindings[0].Source.Files++ },
		"duplicate link": func(h *history) { h.SourceBindings = append(h.SourceBindings, h.SourceBindings[0]) },
	} {
		t.Run(name, func(t *testing.T) {
			checks := doc
			h := history{Kind: historyKind, Entries: []entry{e}, Checks: &checks, SourceBindings: []sourceBinding{{MeasurementSHA256: e.SHA256, Source: e.Source}}}
			mutate(&h)
			data, err := json.Marshal(h)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := previousHistory(data); err == nil {
				t.Fatal("forged context or source link was adopted", name)
			}
		})
	}
}
