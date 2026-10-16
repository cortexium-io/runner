package execution

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"regexp"
)

// Codex exec's ThreadErrorEvent exposes a message, not a typed error code.
// Recognize only its anchored HTTP status form on a terminal turn.failed event.
// Never classify stderr, tool output, arbitrary model prose, or unknown errors.
// Protocol: github.com/openai/codex/codex-rs/exec/src/exec_events.rs.
var transientHTTP = regexp.MustCompile(`^unexpected status (429|500|502|503|504)(?: |:)`)

func codexFailure(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	var code string
	for scanner.Scan() {
		var event struct {
			Type  string `json:"type"`
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(bytes.TrimSpace(scanner.Bytes()), &event) != nil {
			continue
		}
		if event.Type == "turn.failed" {
			code = event.Error.Message
		}
	}
	if scanner.Err() != nil {
		return ""
	}
	if match := transientHTTP.FindStringSubmatch(code); match != nil {
		return "Codex provider returned HTTP " + match[1] + "; terminal failure recorded in private events.jsonl"
	}
	return ""
}

// A valid result file cannot turn an interrupted/failed stream into completion.
func codexCompleted(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	completed := false
	for scanner.Scan() {
		var event struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(scanner.Bytes(), &event) != nil {
			return false
		}
		switch event.Type {
		case "turn.started", "turn.failed":
			completed = false
		case "turn.completed":
			completed = true
		}
	}
	return scanner.Err() == nil && completed
}
