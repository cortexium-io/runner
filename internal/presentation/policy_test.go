package presentation

import (
	"strings"
	"testing"
)

func TestPublishRemoteTextPolicy(t *testing.T) {
	for _, test := range []struct {
		name        string
		provenance  RemoteProvenance
		destination RemoteDestination
		text        string
		reference   string
		want        string
		wantErr     string
	}{
		{
			name:        "runner classification to project field",
			provenance:  RemoteProvenanceRunnerClassification,
			destination: RemoteDestinationProjectField,
			text:        "  Runner blocked.  ",
			want:        "Runner blocked.",
		},
		{
			name:        "runner classification to pull request body",
			provenance:  RemoteProvenanceRunnerClassification,
			destination: RemoteDestinationPullRequestBody,
			text:        "Runner QA summary.",
			want:        "Runner QA summary.",
		},
		{
			name:        "pull request feedback to project field",
			provenance:  RemoteProvenancePullRequestFeedback,
			destination: RemoteDestinationProjectField,
			reference:   "https://github.com/owner/repo/pull/12",
			want:        "Inspect the pull request discussion at https://github.com/owner/repo/pull/12 locally before continuing.",
		},
		{
			name:        "pull request feedback to pull request body denied",
			provenance:  RemoteProvenancePullRequestFeedback,
			destination: RemoteDestinationPullRequestBody,
			reference:   "https://github.com/owner/repo/pull/12",
			wantErr:     "cannot be published",
		},
		{
			name:        "model text denied",
			provenance:  RemoteProvenanceModelText,
			destination: RemoteDestinationProjectField,
			text:        "schema-valid secret",
			wantErr:     "cannot be published",
		},
		{
			name:        "local diagnostics denied",
			provenance:  RemoteProvenanceLocalDiagnostics,
			destination: RemoteDestinationProjectField,
			text:        "raw stderr",
			wantErr:     "cannot be published",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := PublishRemoteText(test.provenance, test.destination, test.text, test.reference)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("PublishRemoteText error = %v, want substring %q", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("PublishRemoteText: %v", err)
			}
			if got != test.want {
				t.Fatalf("PublishRemoteText = %q, want %q", got, test.want)
			}
		})
	}
}

func TestTerminalAndMarkdownEscaping(t *testing.T) {
	input := "plain Cafe\n\x1b]8;;https://attacker.invalid\aSpoof\x1b]8;;\a\r\b\t\u202e"
	terminal := TerminalText(input)
	for _, forbidden := range []string{"\x1b", "\r", "\b", "\t", "\a", "\u202e"} {
		if strings.Contains(terminal, forbidden) {
			t.Fatalf("TerminalText retained %q in %q", forbidden, terminal)
		}
	}
	for _, expected := range []string{"plain Cafe", `\n`, `\x1b`, `\u202e`} {
		if !strings.Contains(terminal, expected) {
			t.Fatalf("TerminalText omitted %q in %q", expected, terminal)
		}
	}

	markdown := MarkdownInline("## [link](https://attacker.invalid) `code`")
	for _, expected := range []string{`\#\#`, `\[link\]`, `\(https://attacker.invalid\)`, "\\`code\\`"} {
		if !strings.Contains(markdown, expected) {
			t.Fatalf("MarkdownInline omitted %q in %q", expected, markdown)
		}
	}
}
