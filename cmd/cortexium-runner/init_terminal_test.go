package main

import (
	"bufio"
	"bytes"
	"reflect"
	"strings"
	"testing"
)

func TestTerminalMenuUsesArrowKeysAndEnter(t *testing.T) {
	input := bufio.NewReader(strings.NewReader("\x1b[B\x1b[B\x1b[A\r"))
	changes := []int{}
	selected, err := readTerminalMenuSelection(input, 3, 0, func(index int) {
		changes = append(changes, index)
	})
	if err != nil {
		t.Fatalf("read terminal menu: %v", err)
	}
	if selected != 1 {
		t.Fatalf("selected index = %d, want 1", selected)
	}
	if !reflect.DeepEqual(changes, []int{1, 2, 1}) {
		t.Fatalf("selection changes = %#v", changes)
	}
}

func TestInteractiveMenusEncodeUntrustedLabelsAndDescriptions(t *testing.T) {
	options := []initMenuOption{{Label: "safe"}, {Label: "model\x1b[2J\rspoof\u202e", Description: "desc\x9b31m"}}
	var numbered bytes.Buffer
	p := &initPrompter{input: bufio.NewReader(strings.NewReader("1\n")), output: &numbered}
	if _, err := p.selectNumberedMenu("Choose", options, 0); err != nil {
		t.Fatal(err)
	}
	var arrow bytes.Buffer
	renderTerminalMenu(&arrow, options, 0, 80, false)
	for name, output := range map[string]string{"numbered": numbered.String(), "arrow": arrow.String()} {
		if strings.Contains(output, "\x1b[2J") || strings.Contains(output, "\rspoof") || strings.Contains(output, "\u202e") || strings.Contains(output, "\x9b") {
			t.Fatalf("%s menu exposed terminal controls: %q", name, output)
		}
		for _, escaped := range []string{"\\x1b", "\\r", "\\u202e", "�"} {
			if !strings.Contains(output, escaped) {
				t.Fatalf("%s menu omitted escaped %q: %q", name, escaped, output)
			}
		}
	}
}

func TestTerminalMenuWrapsAndSupportsNumberKeys(t *testing.T) {
	input := bufio.NewReader(strings.NewReader("\x1b[A3\r"))
	selected, err := readTerminalMenuSelection(input, 3, 0, func(int) {})
	if err != nil {
		t.Fatalf("read terminal menu: %v", err)
	}
	if selected != 2 {
		t.Fatalf("selected index = %d, want 2", selected)
	}
}

func TestTerminalMenuTextIncludesAndTruncatesDescriptions(t *testing.T) {
	value := truncateTerminalMenuText("Opus — suited to complex agentic coding", 20)
	if value != "Opus — suited to co…" {
		t.Fatalf("truncated menu text = %q", value)
	}
}
