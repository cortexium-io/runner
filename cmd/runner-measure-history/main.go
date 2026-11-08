// runner-measure-history retains complete candidate reports for independent review.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/cortexium-io/runner/internal/sourcesnapshot"
)

const (
	historyKind = "runner-measure-history-v1"
	maxRecords  = 8
	maxBytes    = 16 << 20
)

type sourceStamp = sourcesnapshot.Stamp

type report struct {
	Round                 int                        `json:"round"`
	PreviousCompleteRound int                        `json:"previous_complete_round"`
	Package               string                     `json:"package"`
	Selector              string                     `json:"selector"`
	Started               string                     `json:"started"`
	Seconds               float64                    `json:"seconds"`
	Output                string                     `json:"output"`
	OutputBytes           int64                      `json:"output_bytes"`
	OutputSHA256          string                     `json:"output_sha256"`
	OutputComplete        bool                       `json:"output_complete"`
	Skipped               bool                       `json:"skipped"`
	Baseline              bool                       `json:"baseline,omitempty"`
	Failed                bool                       `json:"failed,omitempty"`
	CollectionFailed      bool                       `json:"collection_failed,omitempty"`
	Error                 string                     `json:"error,omitempty"`
	Evidence              map[string]json.RawMessage `json:"evidence"`
	EvidenceStatus        string                     `json:"evidence_status"`
	EvidenceNote          string                     `json:"evidence_note,omitempty"`
	Source                sourceStamp                `json:"source"`
	SourceVerified        bool                       `json:"source_verified"`
}

type entry struct {
	Sequence              int         `json:"sequence"`
	Round                 int         `json:"round"`
	PreviousCompleteRound int         `json:"previous_complete_round"`
	Package               string      `json:"package"`
	Selector              string      `json:"selector"`
	Started               string      `json:"started"`
	Source                sourceStamp `json:"source"`
	Bytes                 int         `json:"bytes"`
	SHA256                string      `json:"sha256"`
	Measurement           string      `json:"measurement"`
}

type history struct {
	Kind           string           `json:"kind"`
	Entries        []entry          `json:"entries"`
	Checks         *contextDocument `json:"checks,omitempty"`
	SourceBindings []sourceBinding  `json:"source_bindings,omitempty"`
}

type contextDocument struct {
	Kind   string `json:"kind"`
	Bytes  int    `json:"bytes"`
	SHA256 string `json:"sha256"`
	JSON   string `json:"json"`
}

type sourceBinding struct {
	MeasurementSHA256 string      `json:"measurement_sha256"`
	Source            sourceStamp `json:"source"`
}

type options struct {
	Previous     string
	Measurements []string
	Output       string
	Source       string
	Checks       string
}

type measurementFiles []string

func (m *measurementFiles) String() string { return strings.Join(*m, ", ") }
func (m *measurementFiles) Set(file string) error {
	if len(*m) == maxRecords {
		return errors.New("at most eight --measurement files are supported")
	}
	*m = append(*m, file)
	return nil
}

func main() {
	previous := flag.String("previous", "", "explicit prior history or initial complete candidate report")
	output := flag.String("output", "", "explicit JSON output file")
	source := flag.String("source", "", "optional read-only workspace; must match the last complete report")
	checks := flag.String("checks", "", "optional original Go-check JSON context; never acceptance or source proof")
	var measurements measurementFiles
	flag.Var(&measurements, "measurement", "complete candidate report; repeat up to eight times in evidence order")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "unexpected positional arguments")
		os.Exit(1)
	}
	count, err := run(options{
		Previous:     *previous,
		Measurements: measurements,
		Output:       *output,
		Source:       *source,
		Checks:       *checks,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "measurement history:", err)
		os.Exit(1)
	}
	fmt.Printf("Retained %d complete measurement reports; semantic acceptance requires independent review.\n", count)
}

func digest(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// Object metadata is case-sensitive and rejects duplicate keys. Sidecar values
// remain opaque valid JSON: this helper cannot judge their scientific claims.
func objectFields(data []byte) (map[string]json.RawMessage, error) {
	if !utf8.Valid(data) {
		return nil, errors.New("JSON must be valid UTF-8")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return nil, errors.New("expected a JSON object")
	}
	fields := map[string]json.RawMessage{}
	for d.More() {
		key, err := d.Token()
		if err != nil {
			return nil, err
		}
		name, ok := key.(string)
		if !ok || fields[name] != nil {
			return nil, errors.New("JSON object has an invalid or duplicate key")
		}
		var value json.RawMessage
		if err := d.Decode(&value); err != nil {
			return nil, err
		}
		fields[name] = value
	}
	if _, err := d.Token(); err != nil {
		return nil, err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return nil, errors.New("JSON has trailing data")
	}
	return fields, nil
}

func decodeObject(data []byte, target any, required, optional string) (map[string]json.RawMessage, error) {
	fields, err := objectFields(data)
	if err != nil {
		return nil, err
	}
	allowed := map[string]bool{}
	for _, name := range strings.Fields(required) {
		if fields[name] == nil {
			return nil, fmt.Errorf("required metadata %s is missing", name)
		}
		allowed[name] = true
	}
	for _, name := range strings.Fields(optional) {
		allowed[name] = true
	}
	for name, value := range fields {
		if !allowed[name] || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return nil, fmt.Errorf("unsupported metadata %s", name)
		}
	}
	return fields, json.Unmarshal(data, target)
}

func completeEntry(data []byte, sequence int) (entry, error) {
	var r report
	fields, err := decodeObject(data, &r,
		"round previous_complete_round package selector started seconds output output_bytes output_sha256 output_complete skipped evidence evidence_status source source_verified",
		"baseline failed collection_failed error evidence_note")
	if err != nil {
		return entry{}, err
	}
	if _, err := decodeObject(fields["source"], &r.Source, "identity sha256 digestMethod files bytes", ""); err != nil {
		return entry{}, err
	}
	if r.Round < 1 || r.Round > 128 || r.PreviousCompleteRound != r.Round-1 || r.Baseline || r.Skipped || r.Failed || r.CollectionFailed || r.Error != "" || !r.OutputComplete || !r.SourceVerified || r.EvidenceStatus != "provided" {
		return entry{}, errors.New("only complete verified candidate measurements can enter history")
	}
	validPackage := r.Package == "..." || regexp.MustCompile(`^[A-Za-z0-9_.-]+(/[A-Za-z0-9_.-]+)*$`).MatchString(r.Package) && !strings.Contains(r.Package, "..")
	if !validPackage || r.Selector == "none" || len(r.Selector) > 256 || !strings.HasPrefix(r.Selector, "^Benchmark") {
		return entry{}, errors.New("measurement needs a declared package and named benchmark")
	}
	if _, err := regexp.Compile(r.Selector); err != nil {
		return entry{}, errors.New("measurement benchmark selector is invalid")
	}
	started, err := time.Parse(time.RFC3339Nano, r.Started)
	if err != nil || started.IsZero() || r.Seconds <= 0 {
		return entry{}, errors.New("measurement execution metadata is incomplete")
	}
	output := []byte(r.Output)
	if len(output) > 256<<10 || r.OutputBytes != int64(len(output)) || r.OutputSHA256 != digest(output) || !regexp.MustCompile(`(?m)^Benchmark\S+\s+\d+\s+`).Match(output) {
		return entry{}, errors.New("complete measurement console bytes, hash or benchmark result differ")
	}
	if r.Source.Identity != "sha256:"+r.Source.SHA256 || r.Source.DigestMethod != sourcesnapshot.Method || len(r.Source.SHA256) != 64 || r.Source.Files < 1 || r.Source.Files > 1<<19 || r.Source.Bytes < 1 {
		return entry{}, errors.New("verified source metadata is invalid")
	}
	if _, err := hex.DecodeString(r.Source.SHA256); err != nil || strings.ToLower(r.Source.SHA256) != r.Source.SHA256 {
		return entry{}, errors.New("source SHA256 is invalid")
	}
	evidence, err := objectFields(fields["evidence"])
	if err != nil || len(evidence) == 0 || len(evidence) > 64 {
		return entry{}, errors.New("measurement structured evidence is missing or malformed")
	}
	total := 0
	for name, value := range evidence {
		if len(name) > 128 || filepath.Base(name) != name || strings.ContainsAny(name, "\\/") || !strings.HasSuffix(name, ".json") {
			return entry{}, errors.New("measurement structured evidence has an unsafe filename")
		}
		total += len(value)
	}
	if total > 8<<20 {
		return entry{}, errors.New("measurement structured evidence exceeds 8MiB")
	}
	return entry{
		Sequence:              sequence,
		Round:                 r.Round,
		PreviousCompleteRound: r.PreviousCompleteRound,
		Package:               r.Package,
		Selector:              r.Selector,
		Started:               r.Started,
		Source:                r.Source,
		Bytes:                 len(data),
		SHA256:                digest(data),
		Measurement:           string(data),
	}, nil
}

func previousHistory(data []byte) (history, error) {
	fields, err := objectFields(data)
	if err != nil {
		return history{}, err
	}
	if fields["kind"] == nil {
		e, err := completeEntry(data, 1)
		return history{Kind: historyKind, Entries: []entry{e}}, err
	}
	var h history
	if _, err := decodeObject(data, &h, "kind entries", "checks source_bindings"); err != nil {
		return history{}, err
	}
	if h.Kind != historyKind || len(h.Entries) == 0 || len(h.Entries) > maxRecords {
		return history{}, errors.New("unsupported measurement history or record count")
	}
	var rows []json.RawMessage
	if err := json.Unmarshal(fields["entries"], &rows); err != nil {
		return history{}, err
	}
	seen := map[string]bool{}
	for i, row := range rows {
		var e entry
		f, err := decodeObject(row, &e, "sequence round previous_complete_round package selector started source bytes sha256 measurement", "")
		if err != nil {
			return history{}, err
		}
		if _, err := decodeObject(f["source"], &e.Source, "identity sha256 digestMethod files bytes", ""); err != nil {
			return history{}, err
		}
		want, err := completeEntry([]byte(e.Measurement), i+1)
		if err != nil || e != want || seen[e.Measurement] {
			return history{}, errors.New("history report bytes, index or digest differ")
		}
		seen[e.Measurement] = true
	}
	if h.Checks != nil {
		var doc contextDocument
		if _, err := decodeObject(fields["checks"], &doc, "kind bytes sha256 json", ""); err != nil {
			return history{}, err
		}
		want, err := checkContext([]byte(doc.JSON))
		if err != nil || doc != want {
			return history{}, errors.New("original checks context bytes or digest differ")
		}
	}
	if len(h.SourceBindings) > maxRecords {
		return history{}, errors.New("too many source bindings")
	}
	var bindings []json.RawMessage
	if fields["source_bindings"] != nil {
		if err := json.Unmarshal(fields["source_bindings"], &bindings); err != nil {
			return history{}, err
		}
	}
	bound := map[string]bool{}
	for _, raw := range bindings {
		var binding sourceBinding
		fields, err := decodeObject(raw, &binding, "measurement_sha256 source", "")
		if err != nil {
			return history{}, err
		}
		if _, err := decodeObject(fields["source"], &binding.Source, "identity sha256 digestMethod files bytes", ""); err != nil {
			return history{}, err
		}
		found := false
		for _, e := range h.Entries {
			found = found || e.SHA256 == binding.MeasurementSHA256 && e.Source == binding.Source
		}
		if !found || bound[binding.MeasurementSHA256] {
			return history{}, errors.New("source binding has no unique matching retained report")
		}
		bound[binding.MeasurementSHA256] = true
	}
	return h, nil
}

func checkContext(data []byte) (contextDocument, error) {
	fields, err := objectFields(data)
	if err != nil || fields["package"] == nil || fields["scope"] == nil && (fields["regression"] == nil || fields["development"] == nil) {
		return contextDocument{}, errors.New("original checks context is not a Go-check JSON document")
	}
	var pkg string
	if json.Unmarshal(fields["package"], &pkg) != nil || pkg == "" {
		return contextDocument{}, errors.New("original checks package metadata is malformed")
	}
	scopes := []string{"scope"}
	if fields["scope"] == nil {
		scopes = []string{"regression", "development"}
	}
	for _, name := range scopes {
		if _, err := objectFields(fields[name]); err != nil {
			return contextDocument{}, errors.New("original checks scope is malformed")
		}
	}
	return contextDocument{Kind: "runner-go-check-context-v1", Bytes: len(data), SHA256: digest(data), JSON: string(data)}, nil
}

func run(o options) (int, error) {
	previous, measurements, output := o.Previous, o.Measurements, o.Output
	if previous == "" && len(measurements) == 0 || len(measurements) > maxRecords {
		return 0, errors.New("provide prior evidence or one to eight measurement files")
	}
	if _, err := safeParent(output); err != nil {
		return 0, err
	}
	inputs := append([]string{}, measurements...)
	if previous != "" {
		inputs = append(inputs, previous)
	}
	if o.Checks != "" {
		inputs = append(inputs, o.Checks)
	}
	for _, file := range inputs {
		if file == output {
			return 0, errors.New("history output must not replace an input report")
		}
		if target, err := os.Lstat(output); err == nil {
			if input, err := os.Lstat(file); err == nil && os.SameFile(input, target) {
				return 0, errors.New("history output aliases an input report")
			}
		}
	}
	h := history{Kind: historyKind}
	if previous != "" {
		data, err := readBounded(previous)
		if err != nil {
			return 0, err
		}
		h, err = previousHistory(data)
		if err != nil {
			return 0, err
		}
	}
	rawBytes := 0
	for _, old := range h.Entries {
		rawBytes += old.Bytes
	}
	for _, file := range measurements {
		data, err := readBounded(file)
		if err != nil {
			return 0, err
		}
		e, err := completeEntry(data, len(h.Entries)+1)
		if err != nil {
			return 0, err
		}
		duplicate := false
		for _, old := range h.Entries {
			duplicate = duplicate || old.Measurement == e.Measurement
		}
		if !duplicate {
			if rawBytes+e.Bytes > maxBytes {
				return 0, errors.New("measurement reports exceed the 16MiB history bound")
			}
			h.Entries = append(h.Entries, e)
			rawBytes += e.Bytes
		}
		if len(h.Entries) > maxRecords {
			return 0, errors.New("complete measurement history exceeds eight records")
		}
	}
	if o.Checks != "" {
		data, err := readBounded(o.Checks)
		if err != nil {
			return 0, err
		}
		doc, err := checkContext(data)
		if err != nil {
			return 0, err
		}
		if h.Checks != nil && *h.Checks != doc {
			return 0, errors.New("original checks context cannot be replaced")
		}
		h.Checks = &doc
	}
	if h.Checks != nil && rawBytes+h.Checks.Bytes > maxBytes {
		return 0, errors.New("reports and checks context exceed the 16MiB history bound")
	}
	if o.Source != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		snapshot, err := sourcesnapshot.Take(ctx, o.Source)
		if err != nil {
			return 0, fmt.Errorf("current source snapshot: %w", err)
		}
		last := h.Entries[len(h.Entries)-1]
		if snapshot.Stamp != last.Source {
			return 0, errors.New("last complete report differs from the actual workspace source")
		}
		if err := snapshot.Verify(ctx); err != nil {
			return 0, err
		}
		binding := sourceBinding{MeasurementSHA256: last.SHA256, Source: snapshot.Stamp}
		duplicate := false
		for _, old := range h.SourceBindings {
			duplicate = duplicate || old == binding
		}
		if !duplicate {
			h.SourceBindings = append(h.SourceBindings, binding)
		}
	}
	if err := writeHistory(output, h); err != nil {
		return 0, err
	}
	return len(h.Entries), nil
}
