package execution

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// NormalizeStructuredResult removes surrounding whitespace and, as a narrow
// compatibility concession, unwraps one whole-response JSON code fence. It
// deliberately does not search prose for an embedded JSON object.
func NormalizeStructuredResult(value string) string {
	trimmed := strings.TrimSpace(value)
	lines := strings.Split(trimmed, "\n")
	if len(lines) < 3 {
		return trimmed
	}
	opener := strings.TrimSpace(lines[0])
	if opener != "```" && !strings.EqualFold(opener, "```json") {
		return trimmed
	}
	if strings.TrimSpace(lines[len(lines)-1]) != "```" {
		return trimmed
	}
	inner := strings.TrimSpace(strings.Join(lines[1:len(lines)-1], "\n"))
	if !strings.HasPrefix(inner, "{") || !strings.HasSuffix(inner, "}") || strings.Contains(inner, "```") {
		return trimmed
	}
	return inner
}

// CanonicalizeStructuredResult applies Runner's complete representation-only
// compatibility policy. It removes only a whole-response JSON fence and the
// exact top-level JSON Schema residue `"type":"object"`. Missing substantive
// fields and every other extra field fail closed.
func CanonicalizeStructuredResult(value string, substantiveFields ...string) (string, error) {
	normalized := NormalizeStructuredResult(value)
	decoder := json.NewDecoder(strings.NewReader(normalized))
	var fields map[string]json.RawMessage
	if err := decoder.Decode(&fields); err != nil || fields == nil {
		if err == nil {
			err = errors.New("structured result is not an object")
		}
		return "", err
	}
	var trailer any
	if err := decoder.Decode(&trailer); !errors.Is(err, io.EOF) {
		if err == nil {
			return "", errors.New("structured result contains more than one JSON value")
		}
		return "", err
	}
	if err := validateRepresentationFields(fields, substantiveFields...); err != nil {
		return "", err
	}
	for _, name := range substantiveFields {
		if _, exists := fields[name]; !exists {
			return "", fmt.Errorf("substantive field %q is missing", name)
		}
	}
	delete(fields, "type")
	encoded, err := json.Marshal(fields)
	if err != nil {
		return "", fmt.Errorf("canonicalize structured result: %w", err)
	}
	return string(encoded), nil
}

func validateRepresentationFields(fields map[string]json.RawMessage, substantiveFields ...string) error {
	substantive := make(map[string]struct{}, len(substantiveFields))
	for _, name := range substantiveFields {
		substantive[name] = struct{}{}
	}
	for name, raw := range fields {
		if _, known := substantive[name]; known {
			continue
		}
		var value string
		if name == "type" && json.Unmarshal(raw, &value) == nil && value == "object" {
			continue
		}
		return fmt.Errorf("ambiguous extra field %q is not representation-only", name)
	}
	return nil
}

func validateStructuredExecutionResultForAssignment(assignment Assignment, value string) (StructuredExecutionResult, error) {
	structured, err := parseStructuredExecutionResult(value)
	if err != nil {
		return StructuredExecutionResult{}, err
	}
	if err := validateReviewAssessmentForAssignment(assignment, structured.Outcome, structured.ReviewAssessment); err != nil {
		return structured, err
	}
	return structured, nil
}
