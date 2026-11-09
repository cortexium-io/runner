package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func directory(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func collect(previous string, measurements []string, output string) (int, error) {
	return run(options{Previous: previous, Measurements: measurements, Output: output})
}

func measurementFixture(t *testing.T, round int, source byte, phase string) []byte {
	t.Helper()
	sha := strings.Repeat(string(source), 64)
	output := fmt.Sprintf("BenchmarkFixtureStudy 1 %d ns/op\n", round)
	r := report{
		Round:                 round,
		PreviousCompleteRound: round - 1,
		Package:               "...",
		Selector:              "^BenchmarkFixtureStudy$",
		Started:               fmt.Sprintf("2026-11-08T12:%02d:00.123456Z", round),
		Seconds:               0.1,
		Output:                output,
		OutputBytes:           int64(len(output)),
		OutputSHA256:          digest([]byte(output)),
		OutputComplete:        true,
		Evidence: map[string]json.RawMessage{
			"samples.json": json.RawMessage(fmt.Sprintf(`{"phase":%q,"work":42,"request":"retained <exact> JSON"}`, phase)),
		},
		EvidenceStatus: "provided",
		Source: sourceStamp{
			Identity:     "sha256:" + sha,
			SHA256:       sha,
			DigestMethod: "runner-source-snapshot-sha256-v1",
			Files:        3,
			Bytes:        2000,
		},
		SourceVerified: true,
	}
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var formatted bytes.Buffer
	if err := json.Indent(&formatted, data, "", "  "); err != nil {
		t.Fatal(err)
	}
	return append(formatted.Bytes(), '\n')
}

func put(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	file := filepath.Join(dir, name)
	if err := os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	return file
}

func readHistory(t *testing.T, file string) ([]byte, history) {
	t.Helper()
	data, err := readBounded(file)
	if err != nil {
		t.Fatal(err)
	}
	h, err := previousHistory(data)
	if err != nil {
		t.Fatal("retained history does not authenticate", err)
	}
	return data, h
}

func changeReport(t *testing.T, data []byte, name string, value any) []byte {
	t.Helper()
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	fields[name] = value
	result, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestHistoryPreservesProfiledIntermediateReportsAndWorkflowLocalRounds(t *testing.T) {
	dir := directory(t)
	var raw [][]byte
	var files []string
	for i, phase := range []string{"unprofiled before", "instrumentation added", "profiled before", "experiment current"} {
		data := measurementFixture(t, i+1, byte('a'+i), phase)
		raw = append(raw, bytes.Clone(data))
		files = append(files, put(t, dir, fmt.Sprintf("round%d.json", i+1), data))
	}
	first := filepath.Join(dir, "history.json")
	if count, err := collect(files[0], files[1:], first); err != nil || count != 4 {
		t.Fatal("complete original/profiled/current sequence did not enter history", count, err)
	}
	encoded, h := readHistory(t, first)
	for i, e := range h.Entries {
		if e.Sequence != i+1 || e.Round != i+1 || e.Source.SHA256 != strings.Repeat(string(byte('a'+i)), 64) || e.Bytes != len(raw[i]) || e.SHA256 != digest(raw[i]) || !bytes.Equal([]byte(e.Measurement), raw[i]) {
			t.Fatal("intermediate report was lost, relabelled or re-encoded", i)
		}
	}
	copy := filepath.Join(dir, "duplicate.json")
	if count, err := collect(first, []string{files[0], files[2], files[3]}, copy); err != nil || count != 4 {
		t.Fatal("exact-byte duplicates acquired new entries", count, err)
	}
	duplicate, _ := readHistory(t, copy)
	if !bytes.Equal(encoded, duplicate) {
		t.Fatal("the same evidence produced different history bytes")
	}
	// A continuation may restart its logical round counter. Source and start
	// metadata identify the distinct report; history never invents a root ID.
	continued := changeReport(t, measurementFixture(t, 1, 'e', "new workflow current"), "started", "2026-11-09T13:00:00Z")
	current := put(t, dir, "continued.json", continued)
	output := filepath.Join(dir, "continued-history.json")
	if count, err := collect(copy, []string{current}, output); err != nil || count != 5 {
		t.Fatal("workflow-local round reset lost a new source report", count, err)
	}
	_, after := readHistory(t, output)
	if after.Entries[4].Round != 1 || after.Entries[4].Sequence != 5 || after.Entries[4].Source.SHA256 == after.Entries[0].Source.SHA256 || after.Entries[4].Started == after.Entries[0].Started {
		t.Fatal("continuation was mistaken for the original before report")
	}
	for i := range h.Entries {
		if after.Entries[i] != h.Entries[i] {
			t.Fatal("append changed an earlier original or profiled report", i)
		}
	}
}

func TestHistoryRejectsIncompleteOrUnboundReportsWithoutReplacingOutput(t *testing.T) {
	valid := measurementFixture(t, 1, 'a', "candidate")
	for name, value := range map[string]any{
		"baseline":                true,
		"failed":                  true,
		"skipped":                 true,
		"collection_failed":       true,
		"source_verified":         false,
		"output_complete":         false,
		"output_bytes":            1,
		"output_sha256":           strings.Repeat("0", 64),
		"error":                   "known failure",
		"evidence_status":         "incomplete",
		"evidence":                map[string]any{},
		"round":                   0,
		"previous_complete_round": 1,
		"source":                  nil,
		"started":                 "not a timestamp",
		"package":                 "../private",
		"selector":                "none",
		"accepted":                true,
	} {
		t.Run(name, func(t *testing.T) {
			dir := directory(t)
			input := put(t, dir, "candidate.json", changeReport(t, valid, name, value))
			output := put(t, dir, "history.json", []byte("retained output"))
			if _, err := collect("", []string{input}, output); err == nil {
				t.Fatal("incomplete report entered history", name)
			}
			if data, err := os.ReadFile(output); err != nil || string(data) != "retained output" {
				t.Fatal("rejected report changed an existing output", err)
			}
		})
	}
	for name, data := range map[string][]byte{
		"duplicate key":  bytes.Replace(valid, []byte("{\n"), []byte("{\n  \"failed\":true,\"failed\":false,\n"), 1),
		"case alias":     bytes.Replace(valid, []byte("{\n"), []byte("{\n  \"Failed\":true,\n"), 1),
		"trailing JSON":  append(bytes.Clone(valid), []byte("{}")...),
		"null flag":      changeReport(t, valid, "skipped", nil),
		"unsafe sidecar": changeReport(t, valid, "evidence", map[string]any{"../outside.json": map[string]any{"sample": 1}}),
		"unknown source": changeReport(t, valid, "source", sourceStamp{Identity: "sha256:" + strings.Repeat("a", 64), SHA256: strings.Repeat("a", 64), DigestMethod: "unknown", Files: 1, Bytes: 1}),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := completeEntry(data, 1); err == nil {
				t.Fatal("malformed or ambiguous metadata was accepted")
			}
		})
	}
}

func TestHistoryAuthenticatesPriorRawBytesAndIndex(t *testing.T) {
	e, err := completeEntry(measurementFixture(t, 1, 'a', "before"), 1)
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*history){
		"digest":    func(h *history) { h.Entries[0].SHA256 = strings.Repeat("0", 64) },
		"size":      func(h *history) { h.Entries[0].Bytes++ },
		"source":    func(h *history) { h.Entries[0].Source.Bytes++ },
		"round":     func(h *history) { h.Entries[0].Round++ },
		"sequence":  func(h *history) { h.Entries[0].Sequence++ },
		"raw bytes": func(h *history) { h.Entries[0].Measurement += " " },
		"duplicate": func(h *history) { h.Entries = append(h.Entries, h.Entries[0]) },
		"kind":      func(h *history) { h.Kind = "other" },
	} {
		t.Run(name, func(t *testing.T) {
			h := history{Kind: historyKind, Entries: []entry{e}}
			mutate(&h)
			data, err := json.Marshal(h)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := previousHistory(data); err == nil {
				t.Fatal("corrupt prior history was silently rebuilt", name)
			}
		})
	}
}

func TestHistoryKeepsEightRecordsAndRefusesTheNinth(t *testing.T) {
	dir := directory(t)
	var files []string
	for i := 1; i <= maxRecords; i++ {
		files = append(files, put(t, dir, fmt.Sprintf("%d.json", i), measurementFixture(t, i, 'a', "complete")))
	}
	output := filepath.Join(dir, "history.json")
	if n, err := collect("", files, output); err != nil || n != maxRecords {
		t.Fatal("eight complete records did not fit", n, err)
	}
	previous, _ := readHistory(t, output)
	ninth := put(t, dir, "9.json", measurementFixture(t, 9, 'a', "complete"))
	if _, err := collect(output, []string{ninth}, filepath.Join(dir, "too-many.json")); err == nil {
		t.Fatal("record ceiling was raised or earlier evidence discarded")
	}
	if n, err := collect(output, []string{files[7]}, filepath.Join(dir, "same-eight.json")); err != nil || n != maxRecords {
		t.Fatal("a duplicate at capacity should preserve eight records", n, err)
	}
	if data, _ := os.ReadFile(output); !bytes.Equal(data, previous) {
		t.Fatal("record overflow rewrote original history")
	}
	if _, err := collect("", append(files, ninth), filepath.Join(dir, "too-many-inputs.json")); err == nil {
		t.Fatal("more than eight measurement flags accepted")
	}
}

func TestHistoryOutputCapIncludesEscapedOriginalJSON(t *testing.T) {
	dir := directory(t)
	base := measurementFixture(t, 1, 'a', "complete")
	evidence := map[string]any{"large.json": map[string]any{"raw": strings.Repeat("\\", 3<<20)}}
	first := changeReport(t, base, "evidence", evidence)
	second := changeReport(t, first, "started", "2026-11-08T13:00:00Z")
	if len(first)+len(second) >= maxBytes {
		t.Fatal("fixture must overflow through JSON string encoding, not raw input size")
	}
	files := []string{put(t, dir, "first.json", first), put(t, dir, "second.json", second)}
	output := put(t, dir, "output.json", []byte("must remain"))
	if _, err := collect("", files, output); err == nil || !strings.Contains(err.Error(), "16MiB") {
		t.Fatal("encoded output ceiling was bypassed", err)
	}
	if data, _ := os.ReadFile(output); string(data) != "must remain" {
		t.Fatal("overflow replaced an existing output")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 3 {
		t.Fatal("failed collection leaked a temporary output", err)
	}
}
