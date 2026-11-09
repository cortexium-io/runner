package main

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"math"
	"os"
	"slices"
	"strconv"
	"strings"
)

// The JSON reporter remains authoritative for execution and counts. JUnit
// corroborates only the message hidden by Vitest's synthetic stack header.
type vitestDiagnostics struct {
	ReportBytes  int                       `json:"report_bytes"`
	ReportSHA256 string                    `json:"report_sha256"`
	Failures     []vitestFailureDiagnostic `json:"failures"`
}

type vitestFailureDiagnostic struct {
	File       string  `json:"file"`
	FullName   string  `json:"full_name"`
	DurationMS float64 `json:"duration_ms"`
	ErrorIndex int     `json:"error_index"`
	Message    string  `json:"message"`
}

type junitReport struct {
	XMLName xml.Name     `xml:"testsuites"`
	Suites  []junitSuite `xml:"testsuite"`
}

type junitSuite struct {
	Name  string      `xml:"name,attr"`
	Cases []junitCase `xml:"testcase"`
}

type junitCase struct {
	Class    string         `xml:"classname,attr"`
	Name     string         `xml:"name,attr"`
	Seconds  string         `xml:"time,attr"`
	Failures []junitFailure `xml:"failure"`
	Skipped  *struct{}      `xml:"skipped"`
	Errors   []struct{}     `xml:"error"`
}

type junitFailure struct {
	Message string `xml:"message,attr"`
}

type vitestDiagnosticResolver struct {
	path       string
	root       *os.Root
	limit      int
	loaded     bool
	err        error
	junit      map[string]map[string]*junitCase
	jsonCounts map[string]map[string]int
	proof      vitestDiagnostics
}

func vitestPlaceholder(message string) bool {
	header, _, _ := strings.Cut(message, "\n")
	switch strings.TrimSpace(header) {
	case "Error: STACK_TRACE_ERROR", "STACK_TRACE_ERROR":
		return true
	default:
		return false
	}
}

func hasVitestPlaceholder(messages []string) bool {
	return slices.ContainsFunc(messages, vitestPlaceholder)
}

func junitCaseName(a vitestAssertion) string {
	// Vitest 4.1.11 flattens suites with " > " while its JSON reporter
	// uses spaces. Use the structured title and ancestors, not a split of
	// the display name. The flattening can collide, so require uniqueness.
	var name strings.Builder
	for _, ancestor := range a.AncestorTitles {
		if name.Len() != 0 {
			name.WriteString(" > ")
		}
		name.WriteString(ancestor)
	}
	if name.Len() != 0 {
		name.WriteString(" > ")
	}
	name.WriteString(a.Title)
	return name.String()
}

func (r *vitestDiagnosticResolver) resolve(file string, assertions []vitestAssertion, a vitestAssertion) ([]string, error) {
	if a.Title == "" || a.AncestorTitles == nil || strings.Join(append(slices.Clone(a.AncestorTitles), a.Title), " ") != a.FullName || a.Duration == nil || *a.Duration < 0 {
		return nil, errors.New("JSON assertion identity or duration is incomplete")
	}
	if !r.loaded {
		r.loaded = true
		r.err = r.load()
	}
	if r.err != nil {
		return nil, r.err
	}
	counts := r.jsonCounts[file]
	if counts == nil {
		counts = make(map[string]int, len(assertions))
		for _, test := range assertions {
			counts[junitCaseName(test)]++
		}
		r.jsonCounts[file] = counts
	}
	name := junitCaseName(a)
	match := r.junit[file][name]
	if counts[name] != 1 || match == nil || match.Class != file || match.Skipped != nil || len(match.Errors) != 0 || len(match.Failures) != len(a.FailureMessages) {
		return nil, errors.New("JUnit failure identity or cardinality differs")
	}
	seconds, err := strconv.ParseFloat(match.Seconds, 64)
	// The official JUnit reporter rounds seconds to 10 fractional digits.
	// Duration only corroborates the association. It never diagnoses a timeout.
	if err != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds < 0 || math.Abs(seconds-*a.Duration/1000) > 1e-10 {
		return nil, errors.New("JUnit and JSON assertion durations differ")
	}
	resolved := slices.Clone(a.FailureMessages)
	var entries []vitestFailureDiagnostic
	for i, original := range a.FailureMessages {
		if !vitestPlaceholder(original) {
			continue
		}
		message := match.Failures[i].Message
		if strings.TrimSpace(message) == "" || vitestPlaceholder(message) {
			return nil, errors.New("JUnit failure message is missing or still a placeholder")
		}
		resolved[i] = message
		entries = append(entries, vitestFailureDiagnostic{
			File:       file,
			FullName:   a.FullName,
			DurationMS: *a.Duration,
			ErrorIndex: i,
			Message:    message,
		})
	}
	r.proof.Failures = append(r.proof.Failures, entries...)
	return resolved, nil
}

func (r *vitestDiagnosticResolver) load() error {
	data, err := readDiagnosticEvidence(r.root, r.path, r.limit, "JUnit diagnostic report")
	if err != nil {
		return err
	}
	var report junitReport
	d := xml.NewDecoder(bytes.NewReader(data))
	if err := d.Decode(&report); err != nil || report.XMLName.Space != "" || len(bytes.TrimSpace(data[d.InputOffset():])) != 0 {
		return errors.New("JUnit diagnostic report is malformed")
	}
	r.junit = make(map[string]map[string]*junitCase, len(report.Suites))
	r.jsonCounts = make(map[string]map[string]int)
	for _, suite := range report.Suites {
		if _, exists := r.junit[suite.Name]; exists {
			r.junit[suite.Name] = nil
			continue
		}
		cases := make(map[string]*junitCase, len(suite.Cases))
		for i := range suite.Cases {
			test := &suite.Cases[i]
			if _, exists := cases[test.Name]; exists {
				cases[test.Name] = nil
			} else {
				cases[test.Name] = test
			}
		}
		r.junit[suite.Name] = cases
	}
	r.proof.ReportBytes, r.proof.ReportSHA256 = len(data), digest(data)
	return nil
}

func readDiagnosticEvidence(root *os.Root, path string, limit int, label string) ([]byte, error) {
	lstat, open := os.Lstat, os.Open
	if root != nil {
		lstat, open = root.Lstat, root.Open
	}
	info, err := lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("regular " + label + " is missing")
	}
	if limit <= 0 || info.Size() > int64(limit) {
		return nil, errors.New(label + " exceeds remaining frontend evidence capacity")
	}
	f, err := open(path)
	if err != nil {
		return nil, errors.New(label + " cannot be opened")
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, errors.New(label + " identity changed")
	}
	data, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	after, statErr := lstat(path)
	if err != nil || len(data) > limit || statErr != nil || !after.Mode().IsRegular() || !os.SameFile(info, after) || after.Size() != int64(len(data)) || !after.ModTime().Equal(info.ModTime()) {
		return nil, errors.New(label + " is unreadable, changed or oversized")
	}
	return data, nil
}
