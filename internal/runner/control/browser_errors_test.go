package control

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/runner"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func browserErrorGZIP(t *testing.T, data []byte, extra int) []byte {
	t.Helper()
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	if extra > 0 {
		w.Extra = bytes.Repeat([]byte("h"), extra)
	}
	if _, err := w.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestBrowserErrorHandoffPreservesExactCoreIdentityAndReport(t *testing.T) {
	for _, raw := range [][]byte{[]byte("{\"page_errors\":[\"detail\"]}\n"), bytes.Repeat([]byte("x"), 128<<10)} {
		in := RecoverBrowserErrorsArgs{
			RequestID:       "owner-handoff",
			RunID:           "saved-run",
			AttemptID:       "saved-qualification",
			Revision:        42,
			PublicationHead: strings.Repeat("a", 40),
			ReportGZIP:      base64.StdEncoding.EncodeToString(browserErrorGZIP(t, raw, 0)),
		}
		want := runner.RecoverBrowserErrorsRequest{
			RequestID:       in.RequestID,
			RunID:           in.RunID,
			AttemptID:       in.AttemptID,
			Revision:        in.Revision,
			PublicationHead: in.PublicationHead,
			Report:          raw,
		}
		got, err := in.request()
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatal("browser-error handoff changed Core identity or raw report", err)
		}
	}
	// A full compressed boundary remains a valid single gzip member.
	raw := []byte("report")
	base := browserErrorGZIP(t, raw, 0)
	compressed := browserErrorGZIP(t, raw, (32<<10)-len(base)-2)
	if len(compressed) != 32<<10 {
		t.Fatal("fixture did not reach the compressed bound", len(compressed))
	}
	if got, err := (RecoverBrowserErrorsArgs{ReportGZIP: base64.StdEncoding.EncodeToString(compressed)}).request(); err != nil || !bytes.Equal(got.Report, raw) {
		t.Fatal("exact compressed boundary was rejected", err)
	}
}

func TestBrowserErrorHandoffRejectsInvalidOrOversizedGZIP(t *testing.T) {
	good := browserErrorGZIP(t, []byte("report"), 0)
	corrupt := append([]byte(nil), good...)
	corrupt[len(corrupt)-8] ^= 1
	for name, invalid := range map[string][]byte{
		"missing":      nil,
		"malformed":    []byte("not gzip"),
		"truncated":    good[:len(good)-1],
		"checksum":     corrupt,
		"empty report": browserErrorGZIP(t, nil, 0),
		"compressed":   bytes.Repeat([]byte("x"), (32<<10)+1),
		"decompressed": browserErrorGZIP(t, bytes.Repeat([]byte("x"), (128<<10)+1), 0),
		"trailing":     append(append([]byte(nil), good...), 'x'),
		"concatenated": append(append([]byte(nil), good...), good...),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := (RecoverBrowserErrorsArgs{ReportGZIP: base64.StdEncoding.EncodeToString(invalid)}).request(); err == nil {
				t.Fatal("invalid browser-error gzip report reached Core")
			}
		})
	}
	if _, err := (RecoverBrowserErrorsArgs{ReportGZIP: "not valid base64"}).request(); err == nil {
		t.Fatal("invalid base64 reached decompression")
	}
	oversized := strings.Repeat("A", base64.StdEncoding.EncodedLen(32<<10)+4)
	if _, err := (RecoverBrowserErrorsArgs{ReportGZIP: oversized}).request(); err == nil || !strings.Contains(err.Error(), "base64 envelope") {
		t.Fatal("oversized base64 was not rejected before decoding", err)
	}
}

func TestBrowserErrorHandoffToolUsesStringEnvelopeWithoutAllowanceFields(t *testing.T) {
	server := httptest.NewServer(Handler(nil, "owner-token", nil))
	defer server.Close()
	session, err := Connect(t.Context(), server.URL+"/mcp", "owner-token")
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	tools, err := session.ListTools(t.Context(), &mcp.ListToolsParams{})
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools.Tools {
		if tool.Name != "runner_recover_browser_errors" {
			continue
		}
		data, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		var schema struct {
			Properties map[string]struct {
				Type string `json:"type"`
			} `json:"properties"`
		}
		if err := json.Unmarshal(data, &schema); err != nil || schema.Properties["report_gzip"].Type != "string" {
			t.Fatal("compressed report is not exposed as a JSON base64 string", err)
		}
		for _, forbidden := range []string{"report", "extra_attempts", "profile", "max_uses", "within_budget", "step"} {
			if _, ok := schema.Properties[forbidden]; ok {
				t.Fatal("browser handoff exposes uncompressed evidence or an execution override", forbidden)
			}
		}
		return
	}
	t.Fatal("owner browser-error handoff tool is missing")
}
