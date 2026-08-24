package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"golang.org/x/term"
)

type initMenuOption struct {
	Label       string
	Description string
	Value       string
}

func isTerminalFile(value any) bool {
	file, ok := value.(*os.File)
	return ok && term.IsTerminal(int(file.Fd()))
}

func (p *initPrompter) selectMenu(label string, options []initMenuOption, selected int) (int, error) {
	if len(options) == 0 {
		return -1, errors.New("interactive menu has no options")
	}
	if selected < 0 || selected >= len(options) {
		selected = 0
	}
	if p.terminalIn != nil && p.terminalOut != nil {
		state, err := term.MakeRaw(int(p.terminalIn.Fd()))
		if err == nil {
			defer term.Restore(int(p.terminalIn.Fd()), state) //nolint:errcheck -- best effort after terminal interaction
			return p.selectMenuWithArrowKeys(label, options, selected)
		}
	}
	return p.selectNumberedMenu(label, options, selected)
}

func (p *initPrompter) selectNumberedMenu(label string, options []initMenuOption, selected int) (int, error) {
	for {
		fmt.Fprintln(p.output, terminalStyled(p.colors, toneQuestion, label+":"))
		for index, option := range options {
			optionLabel := terminalSafeText(option.Label)
			optionDescription := terminalSafeText(option.Description)
			if option.Description == "" {
				fmt.Fprintf(p.output, "  %d) %s\n", index+1, optionLabel)
			} else {
				fmt.Fprintf(p.output, "  %d) %s — %s\n", index+1, optionLabel, optionDescription)
			}
		}
		selection, err := p.read(fmt.Sprintf("Choose an option [1-%d]", len(options)))
		if err != nil {
			return -1, err
		}
		if selection == "" {
			return selected, nil
		}
		index, conversionErr := strconv.Atoi(selection)
		if conversionErr == nil && index >= 1 && index <= len(options) {
			return index - 1, nil
		}
		for candidate, option := range options {
			if strings.EqualFold(selection, option.Label) || (option.Value != "" && strings.EqualFold(selection, option.Value)) {
				return candidate, nil
			}
		}
		fmt.Fprintf(p.output, "Choose a number from 1 to %d.\n", len(options))
	}
}

func (p *initPrompter) selectMenuWithArrowKeys(label string, options []initMenuOption, selected int) (int, error) {
	columns, _, sizeErr := term.GetSize(int(p.terminalOut.Fd()))
	if sizeErr != nil || columns < 24 {
		columns = 80
	}
	fmt.Fprintf(
		p.output,
		"%s\r\n%s\r\n",
		terminalStyled(p.colors, toneQuestion, label),
		terminalStyled(p.colors, toneMuted, "Choose with ↑/↓ and Enter"),
	)
	renderTerminalMenu(p.output, options, selected, columns, p.colors)
	chosen, err := readTerminalMenuSelection(p.input, len(options), selected, func(next int) {
		fmt.Fprintf(p.output, "\x1b[%dA", len(options))
		renderTerminalMenu(p.output, options, next, columns, p.colors)
	})
	if err != nil {
		return -1, err
	}
	fmt.Fprintf(
		p.output,
		"\x1b[%dA\x1b[J%s %s\r\n",
		len(options)+1,
		terminalStyled(p.colors, toneMuted, "Selected:"),
		terminalStyled(p.colors, toneSelected, options[chosen].Label),
	)
	return chosen, nil
}

func renderTerminalMenu(output io.Writer, options []initMenuOption, selected, columns int, colors bool) {
	for index, option := range options {
		marker := " "
		if index == selected {
			marker = "›"
		}
		text := terminalSafeText(option.Label)
		if option.Description != "" {
			text += " — " + terminalSafeText(option.Description)
		}
		line := fmt.Sprintf("  %s %s", marker, truncateTerminalMenuText(text, columns-5))
		if index == selected {
			line = terminalStyled(colors, toneSelected, line)
		}
		fmt.Fprintf(output, "\x1b[2K\r%s\r\n", line)
	}
}

func truncateTerminalMenuText(value string, limit int) string {
	characters := []rune(value)
	if limit < 2 || len(characters) <= limit {
		return value
	}
	return string(characters[:limit-1]) + "…"
}

func readTerminalMenuSelection(input io.ByteReader, count, selected int, changed func(int)) (int, error) {
	for {
		key, err := input.ReadByte()
		if err != nil {
			return -1, fmt.Errorf("read interactive response: %w", err)
		}
		next := selected
		switch key {
		case '\r', '\n':
			return selected, nil
		case 3:
			return -1, errors.New("interactive selection cancelled")
		case '\t', 'j', 'J':
			next = (selected + 1) % count
		case 'k', 'K':
			next = (selected - 1 + count) % count
		case 27:
			prefix, prefixErr := input.ReadByte()
			if prefixErr != nil {
				return -1, fmt.Errorf("read interactive response: %w", prefixErr)
			}
			if prefix != '[' && prefix != 'O' {
				continue
			}
			direction, directionErr := input.ReadByte()
			if directionErr != nil {
				return -1, fmt.Errorf("read interactive response: %w", directionErr)
			}
			switch direction {
			case 'A':
				next = (selected - 1 + count) % count
			case 'B':
				next = (selected + 1) % count
			case 'H':
				next = 0
			case 'F':
				next = count - 1
			}
		default:
			if key >= '1' && key <= '9' {
				index := int(key - '1')
				if index < count {
					next = index
				}
			}
		}
		if next != selected {
			selected = next
			changed(selected)
		}
	}
}
