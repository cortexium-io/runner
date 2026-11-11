package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/runner"
	"github.com/cortexium-io/runner/internal/runner/control"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestBrowserErrorReportFileIsBoundedAndRegular(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "report.json")
	for _, size := range []int{1, 128 << 10, 0, (128 << 10) + 1} {
		data := bytes.Repeat([]byte("x"), size)
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		got, err := readBrowserErrorReport(path)
		if size == 0 || size > 128<<10 {
			if err == nil {
				t.Fatalf("invalid %d-byte report was accepted", size)
			}
		} else if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("bounded report bytes changed: size=%d; %v", size, err)
		}
	}
	if _, err := readBrowserErrorReport(dir); err == nil {
		t.Fatal("directory accepted as a browser report")
	}
	if err := os.WriteFile(path, []byte("report"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "linked-report.json")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readBrowserErrorReport(link); err == nil {
		t.Fatal("symbolic link accepted as a browser report")
	}
}

func TestBrowserErrorsCLIUsesExactSavedReportAndOwnerIdentity(t *testing.T) {
	want := runner.RecoverBrowserErrorsRequest{
		RequestID:       "approved-browser-handoff",
		RunID:           "saved-run",
		AttemptID:       "saved-qualification",
		Revision:        42,
		PublicationHead: strings.Repeat("a", 40),
		Report:          []byte("{\"page_errors\":[\"" + strings.Repeat("detail ", 11000) + "\"]}\n"),
	}
	requests := make(chan control.RecoverBrowserErrorsArgs, 1)
	s := mcp.NewServer(&mcp.Implementation{Name: "browser-error-fixture", Version: "1"}, nil)
	mcp.AddTool(s, &mcp.Tool{Name: "runner_recover_browser_errors"}, func(_ context.Context, _ *mcp.CallToolRequest, in control.RecoverBrowserErrorsArgs) (*mcp.CallToolResult, any, error) {
		requests <- in
		out := runner.Summary{ID: in.RunID, Status: "Failed"}
		b, _ := json.Marshal(out)
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}}, out, nil
	})
	const token = "disposable-fixture-token"
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, &mcp.StreamableHTTPOptions{Stateless: true})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Error("CLI omitted owner authentication")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		h.ServeHTTP(w, r)
	}))
	defer server.Close()
	data := t.TempDir()
	if err := os.WriteFile(filepath.Join(data, "owner.token"), []byte(token), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(data, "browser-report.json")
	if err := os.WriteFile(path, want.Report, 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{
		"recover-browser-errors", "--data", data, "--endpoint", server.URL, "--request-id", want.RequestID,
		"--run", want.RunID, "--attempt", want.AttemptID, "--revision", "42", "--publication-head", want.PublicationHead,
		"--report-file", path,
	}
	if err := run(t.Context(), args); err != nil {
		t.Fatal(err)
	}
	got := <-requests
	compressed, err := base64.StdEncoding.DecodeString(got.ReportGZIP)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	raw, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	decoded := runner.RecoverBrowserErrorsRequest{
		RequestID:       got.RequestID,
		RunID:           got.RunID,
		AttemptID:       got.AttemptID,
		Revision:        got.Revision,
		PublicationHead: got.PublicationHead,
		Report:          raw,
	}
	if !reflect.DeepEqual(decoded, want) {
		t.Fatal("browser-error handoff identity or exact bytes changed")
	}
}

func TestBrowserErrorReportCompressionRejectsOversizedWirePayload(t *testing.T) {
	data := make([]byte, 128<<10)
	if _, err := rand.New(rand.NewSource(1)).Read(data); err != nil {
		t.Fatal(err)
	}
	if _, err := compressBrowserErrorReport(data); err == nil {
		t.Fatal("incompressible report exceeded the existing wire envelope")
	}
	for _, invalid := range [][]byte{nil, bytes.Repeat([]byte("x"), (128<<10)+1)} {
		if _, err := compressBrowserErrorReport(invalid); err == nil {
			t.Fatal("empty or oversized report reached compression")
		}
	}
}

func TestBrowserErrorsCLIRejectsGrantsAndUnboundRequests(t *testing.T) {
	base := []string{
		"recover-browser-errors", "--data", t.TempDir(), "--run", "saved-run", "--attempt", "saved-attempt",
		"--revision", "8", "--publication-head", strings.Repeat("a", 40), "--report-file", "must-not-be-opened.json",
	}
	for _, invalid := range [][]string{
		{"--run", ""}, {"--attempt", ""}, {"--revision", "0"}, {"--publication-head", ""}, {"--report-file", ""},
		{"--extra-attempts", "1"}, {"--max-uses", "3"}, {"--profile", "larger"}, {"--cleanup-only"}, {"--preserve-source"},
	} {
		args := append(append([]string{}, base...), invalid...)
		if err := run(t.Context(), args); err == nil || !strings.Contains(err.Error(), "it grants no attempts or profile override") {
			t.Fatalf("unbound or allowance-granting browser handoff reached file or service: %v; %v", invalid, err)
		}
	}
	if err := run(t.Context(), []string{"status", "--data", t.TempDir(), "--report-file", "must-not-be-opened.json"}); err == nil || !strings.Contains(err.Error(), "--report-file requires recover-browser-errors") {
		t.Fatal("report input reached unrelated command", err)
	}
}
