package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/cortexium-io/runner/internal/presentation"
)

type terminalTone int

const (
	tonePlain terminalTone = iota
	toneQuestion
	toneSelected
	toneSuccess
	toneWarning
	toneFailure
	toneMuted
	toneProgress
)

const ansiReset = "\x1b[0m"

func terminalColorsEnabled(output any) bool {
	return isTerminalFile(output) && os.Getenv("NO_COLOR") == "" && !strings.EqualFold(strings.TrimSpace(os.Getenv("TERM")), "dumb")
}

func terminalStyled(enabled bool, tone terminalTone, value string) string {
	value = terminalSafeText(value)
	if !enabled || value == "" {
		return value
	}
	code := ""
	switch tone {
	case toneQuestion:
		code = "\x1b[1;36m"
	case toneSelected:
		code = "\x1b[1;32m"
	case toneSuccess:
		code = "\x1b[32m"
	case toneWarning:
		code = "\x1b[33m"
	case toneFailure:
		code = "\x1b[31m"
	case toneMuted:
		code = "\x1b[2m"
	case toneProgress:
		code = "\x1b[36m"
	}
	if code == "" {
		return value
	}
	return code + value + ansiReset
}

func terminalSafeText(value string) string {
	return presentation.TerminalText(value)
}

func styleForOutput(output io.Writer, tone terminalTone, value string) string {
	return terminalStyled(terminalColorsEnabled(output), tone, value)
}

func writeProgress(output io.Writer, message string) {
	message = strings.TrimSpace(message)
	if message == "" {
		return
	}
	fmt.Fprintln(output, styleForOutput(output, toneProgress, "… "+message))
}

func writeStateLine(output io.Writer, tone terminalTone, format string, values ...any) {
	line := fmt.Sprintf(format, values...)
	fmt.Fprintln(output, styleForOutput(output, tone, line))
}
