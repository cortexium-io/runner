package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestTerminalStyledUsesExpectedColorsAndReset(t *testing.T) {
	for _, test := range []struct {
		tone terminalTone
		code string
	}{
		{tone: toneQuestion, code: "\x1b[1;36m"},
		{tone: toneSelected, code: "\x1b[1;32m"},
		{tone: toneSuccess, code: "\x1b[32m"},
		{tone: toneWarning, code: "\x1b[33m"},
		{tone: toneFailure, code: "\x1b[31m"},
	} {
		styled := terminalStyled(true, test.tone, "example")
		if styled != test.code+"example"+ansiReset {
			t.Fatalf("tone %d = %q", test.tone, styled)
		}
	}
	if plain := terminalStyled(false, toneFailure, "example"); plain != "example" {
		t.Fatalf("disabled color = %q", plain)
	}
}

func TestTerminalMenuColorsOnlyTheSelectedAnswer(t *testing.T) {
	var output bytes.Buffer
	renderTerminalMenu(&output, []initMenuOption{{Label: "First"}, {Label: "Second"}}, 1, 80, true)
	value := output.String()
	if strings.Count(value, "\x1b[1;32m") != 1 || !strings.Contains(value, "\x1b[1;32m  › Second\x1b[0m") {
		t.Fatalf("selected answer was not highlighted: %q", value)
	}
}

func TestProgressOutputStaysPlainForNonTerminalWriters(t *testing.T) {
	var output bytes.Buffer
	writeProgress(&output, "Checking readiness…")
	if output.String() != "… Checking readiness…\n" || strings.Contains(output.String(), "\x1b[") {
		t.Fatalf("non-terminal progress = %q", output.String())
	}
}

func TestTerminalOutputEscapesControlsAndBidiText(t *testing.T) {
	var output bytes.Buffer
	writeProgress(&output, "check\x1b]8;;https://attacker.invalid\alink\x1b]8;;\a\r\u202e")
	writeStateLine(&output, toneFailure, "status: %s", "bad\x1b[2J\r\u202e")
	rendered := output.String()
	for _, control := range []string{"\x1b", "\r", "\a", "\u202e"} {
		if strings.Contains(rendered, control) {
			t.Fatalf("terminal output retained %q in %q", control, rendered)
		}
	}
	for _, escaped := range []string{`\x1b`, `\r`, `\u202e`} {
		if !strings.Contains(rendered, escaped) {
			t.Fatalf("terminal output omitted %q in %q", escaped, rendered)
		}
	}
}
