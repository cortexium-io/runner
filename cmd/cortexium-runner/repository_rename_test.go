package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/engine"
	"github.com/cortexium-io/runner/internal/github"
	"github.com/cortexium-io/runner/internal/workspace"
)

func TestRepositoryRenameCLIOnlyAppliesAfterTerminalConfirmation(t *testing.T) {
	plan := engine.RepositoryRenameRecovery{Batch: github.RepositoryRenamePlan{
		From: "owner/old", To: "owner/new", Repository: github.RepositoryIdentity{ID: 123, NodeID: "R_identity"}, Source: github.WorkItem{ID: "parent"},
		Children: []github.RepositoryRenameChild{{Before: github.WorkItem{ID: "child", Body: "Exact old requirements", QAFailures: 2}, After: github.WorkItem{Body: "Exact old requirements", Status: "Ready"}}},
	}, Workspaces: []engine.RenameWorkspace{{ItemID: "child", Before: workspace.Identity{WorktreePath: "/retained/worktree", Branch: "runner/retained"}, Snapshot: workspace.Snapshot{Head: "retained-head", Fingerprint: "dirty-byte-fingerprint"}}}}
	for _, mode := range []string{"dry-run", "json", "piped yes"} {
		t.Run(mode, func(t *testing.T) {
			var output bytes.Buffer
			applied := false
			err := reviewRepositoryRename(plan, mode == "dry-run", mode == "json", strings.NewReader("1\n"), &output, func() error { applied = true; return nil })
			if applied {
				t.Fatal("preview or piped approval applied recovery")
			}
			if mode == "piped yes" {
				if err == nil || !strings.Contains(err.Error(), "interactive terminal") {
					t.Fatal("piped approval did not require a terminal")
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if mode == "json" {
				var preview struct {
					Applied  bool                            `json:"applied"`
					Recovery engine.RepositoryRenameRecovery `json:"recovery"`
				}
				if err := json.Unmarshal(output.Bytes(), &preview); err != nil || preview.Applied || preview.Recovery.Batch.Source.ID != "parent" {
					t.Fatalf("invalid read-only preview: %v", err)
				}
				return
			}
			for _, expected := range []string{"owner/old -> owner/new", "123 / R_identity", "Exact old requirements", "planning provenance", "QA rejections: 2", "/retained/worktree", "runner/retained", "retained-head", "dirty-byte-fingerprint", "No QA acceptance"} {
				if !strings.Contains(output.String(), expected) {
					t.Fatalf("preview omitted %q", expected)
				}
			}
		})
	}
	for _, input := range []string{"\n", "2\n", "1\n"} {
		accepted, err := confirmRepositoryRename(newInitPrompter(strings.NewReader(input), &bytes.Buffer{}))
		if err != nil || accepted != (input == "1\n") {
			t.Fatalf("confirmation %q: accepted=%t error=%v", input, accepted, err)
		}
	}
	var output bytes.Buffer
	plan.Batch.Source.Title = "untrusted\x1b[31mtitle"
	writeRepositoryRenamePreview(&output, plan)
	if strings.Contains(output.String(), "\x1b") {
		t.Fatal("preview emitted untrusted terminal controls")
	}
}

func TestRepositoryRenameCommandHelpAndRequiredFlags(t *testing.T) {
	for _, args := range [][]string{{"recover-repository-rename", "--help"}, {"recover-repository-rename", "--item", "parent"}, {"recover-repository-rename", "--from", "owner/old"}} {
		var output bytes.Buffer
		err := run(t.Context(), args, strings.NewReader(""), &output)
		if args[1] == "--help" {
			if err != nil || !strings.Contains(output.String(), "--from") {
				t.Fatalf("help: %s %v", output.String(), err)
			}
		} else if err == nil || !strings.Contains(err.Error(), "requires --item and --from") {
			t.Fatalf("missing required flag: %v", err)
		}
	}
}
