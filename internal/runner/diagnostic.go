package runner

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

// DiagnosticArtifact is bounded configured-command output, separate from a
// domain result. AtLimit means earlier bytes may have been omitted.
type DiagnosticArtifact struct {
	ID      string `json:"id"`
	Bytes   int64  `json:"bytes"`
	SHA256  string `json:"sha256"`
	AtLimit bool   `json:"at_limit,omitempty"`
}

func (p DiagnosticArtifact) Matches(attemptID string, data []byte, limit Bytes) bool {
	return p.ID == attemptID+"/command-"+p.SHA256+".log" && p.Bytes > 0 && p.Bytes <= int64(limit) && p.Bytes == int64(len(data)) && p.SHA256 == fmt.Sprintf("%x", sha256.Sum256(data))
}

func (r Run) LatestDiagnosticArtifact() *DiagnosticArtifact {
	for i := len(r.Attempts) - 1; i >= 0; i-- {
		if end := r.Attempts[i].End; end != nil {
			return Clone(end.DiagnosticArtifact)
		}
	}
	return nil
}

// DiagnosticTail preserves the latest command facts without turning them into
// domain outputs. It fits both the existing 480-byte command tail and a 512-byte
// JSON string allowance, including escaping; it never splits UTF-8.
func DiagnosticTail(s string) string {
	s = strings.ToValidUTF8(s, "�")
	if len(s) > 480 {
		s = s[len(s)-480:]
		for !utf8.RuneStart(s[0]) {
			s = s[1:]
		}
	}
	for {
		encoded, _ := json.Marshal(s)
		if len(encoded) <= 512 {
			return s
		}
		_, size := utf8.DecodeRuneInString(s)
		s = s[size:]
	}
}

// LatestDiagnostic belongs to the latest settled execution, not a search for
// an old error. A later successful execution therefore clears stale feedback.
func (r Run) LatestDiagnostic() string {
	for i := len(r.Attempts) - 1; i >= 0; i-- {
		if end := r.Attempts[i].End; end != nil {
			if end.Kind == "failed" || end.Kind == "stopped" {
				return DiagnosticTail(end.Diagnostic)
			}
			return ""
		}
	}
	return ""
}
