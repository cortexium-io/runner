package subprocess

import (
	"strings"
	"testing"
)

func TestLineFilteredCaptureKeepsOnlyCompleteSelectedLines(t *testing.T) {
	capture := newLineFilteredCapture(1024, "[truncated]", func(line []byte) bool {
		return strings.Contains(string(line), `"keep":true`)
	})
	for _, part := range []string{
		`{"keep":false}` + "\n" + `{"keep"`,
		`:true,"value":1}` + "\n" + `{"keep":true,"value":2}`,
	} {
		if _, err := capture.Write([]byte(part)); err != nil {
			t.Fatal(err)
		}
	}
	capture.Flush()
	want := "{\"keep\":true,\"value\":1}\n{\"keep\":true,\"value\":2}\n"
	if capture.String() != want {
		t.Fatalf("filtered output = %q, want %q", capture.String(), want)
	}
}

func TestLineFilteredCaptureDropsOversizedLineWithoutGrowingPastBound(t *testing.T) {
	capture := newLineFilteredCapture(64, "[truncated]", func([]byte) bool { return true })
	for range 20 {
		if _, err := capture.Write([]byte(strings.Repeat("x", 16))); err != nil {
			t.Fatal(err)
		}
	}
	if len(capture.pending) != 0 || !capture.discarding {
		t.Fatalf("oversized unterminated line remained buffered: pending=%d discarding=%t", len(capture.pending), capture.discarding)
	}
	if _, err := capture.Write([]byte("\nkept\n")); err != nil {
		t.Fatal(err)
	}
	capture.Flush()
	if capture.String() != "kept\n" {
		t.Fatalf("capture did not resume after oversized line: %q", capture.String())
	}
}
