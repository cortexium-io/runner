package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/engine"
	"github.com/cortexium-io/runner/internal/github"
)

func TestDeliveryMigrationCLIIsExactPreviewOnlyWithoutConfirmation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	bin := t.TempDir()
	writeFakeGitHubProjectCommand(t, bin)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	log := filepath.Join(t.TempDir(), "calls")
	t.Setenv("GH_CALL_LOG", log)
	t.Setenv("FAKE_GH_ITEMS_JSON", `{"data":{"node":{"items":{"nodes":[],"pageInfo":{"hasNextPage":false}}}}}`)
	cfg := completeCLITestConfig(t.TempDir())
	profile := cfg.Roles["reviewer"]
	profile.Access = config.RoleAccessHost
	cfg.Roles["reviewer"] = profile
	cfg.Verification = map[string]config.VerificationEntrypoint{"complete": {Command: "/bin/sh", Args: []string{"-c", "exit 0"}, ToolchainCommands: []string{"/bin/sh"}, InputPaths: []string{"src"}, TimeoutSeconds: 10}}
	path := filepath.Join(t.TempDir(), "runner.json")
	if err := config.SaveConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	for _, mode := range []string{"--dry-run", "--json", "piped"} {
		args := []string{"delivery", "migrate", "--config", path, "--entrypoint", "complete"}
		if mode != "piped" {
			args = append(args, mode)
		}
		var out bytes.Buffer
		err := run(t.Context(), args, strings.NewReader("yes\n"), &out)
		if mode == "piped" {
			if err == nil || !strings.Contains(err.Error(), "interactive terminal") {
				t.Fatalf("piped approval accepted: %v", err)
			}
		} else if err != nil {
			t.Fatal(err)
		}
		if mode == "--json" {
			var result struct {
				Applied   bool
				Operation string
				Preview   struct {
					Project struct {
						Preserved *int `json:"preserved_legacy_done_items"`
					}
					Configuration struct {
						Digest string `json:"config_snapshot"`
						After  config.PlanDeliveryConfig
					}
				}
			}
			if err := json.Unmarshal(out.Bytes(), &result); err != nil || result.Applied || result.Operation != "migrate" || !result.Preview.Configuration.After.Enabled || result.Preview.Configuration.Digest == "" {
				t.Fatalf("preview did not bind exact config: %v", err)
			}
			if result.Preview.Project.Preserved == nil || *result.Preview.Project.Preserved != 0 {
				t.Fatal("preview omitted the separate historical-preservation count")
			}
			for _, secret := range []string{`"approval"`, `"body"`, `"items"`} {
				if strings.Contains(out.String(), secret) {
					t.Fatalf("preview exposed private authority snapshot: %s", secret)
				}
			}
		} else {
			for _, want := range []string{"Runner Plan Release TEXT", "Only configuration delta: plan_delivery", "Existing reviewed catalog entry", "Gracefully stop Runner", "Project snapshot:"} {
				if !strings.Contains(out.String(), want) {
					t.Fatalf("preview omitted %s", want)
				}
			}
		}
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("preview changed config")
	}
	calls, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"field-create", "item-edit", "issue edit", "updateProjectV2"} {
		if strings.Contains(string(calls), forbidden) {
			t.Fatalf("preview mutated Project: %s", forbidden)
		}
	}
}

func TestDeliveryMigrationPreviewSeparatesHistoryFromAuthority(t *testing.T) {
	var out bytes.Buffer
	writeDeliveryMigrationPreview(&out, engine.DeliveryMigration{Project: github.PlanFieldMigration{PreservedLegacyDoneItems: 9}})
	for _, want := range []string{"Preserve 9 legacy Done planning records unchanged", "not authenticated delivery", "dependency/execution authority", "structurally complete and entirely Done", "new delivery contracts must retain current authority"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("preview omitted %q", want)
		}
	}
}

func TestDeliveryCLIRejectsAmbiguousChangesAndDocumentsBothOperations(t *testing.T) {
	for _, args := range [][]string{
		{"delivery", "migrate"}, {"delivery", "cancel"}, {"delivery", "unknown"},
		{"delivery", "migrate", "--entrypoint", "complete", "--item", "PVTI_parent"},
		{"delivery", "cancel", "--item", "PVTI_parent", "--entrypoint", "complete"},
	} {
		var out bytes.Buffer
		if err := run(t.Context(), args, strings.NewReader(""), &out); err == nil {
			t.Fatalf("ambiguous command accepted: %v", args)
		}
	}
	var out bytes.Buffer
	if err := run(t.Context(), []string{"delivery", "--help"}, strings.NewReader(""), &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"migrate", "cancel", "amend", "--amendment-file", "--dry-run", "--json", "quiescence"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("help omitted %s", want)
		}
	}
}

func TestDeliveryAmendmentCLIRejectsUnboundedOrAmbiguousRequest(t *testing.T) {
	cfg := completeCLITestConfig(t.TempDir())
	path := filepath.Join(t.TempDir(), "runner.json")
	if err := config.SaveConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, body string }{
		{"unknown", `{"unexpected_authority":"yes"}`},
		{"multiple", `{} {}`},
		{"oversize", `{"reason":"` + strings.Repeat("x", 1024*1024) + `"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := filepath.Join(t.TempDir(), "amend.json")
			if err := os.WriteFile(request, []byte(tc.body), 0o600); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			err := run(t.Context(), []string{"delivery", "amend", "--config", path, "--item", "PVTI_parent", "--amendment-file", request, "--json"}, strings.NewReader("yes\n"), &out)
			if err == nil || out.Len() != 0 {
				t.Fatal("invalid amendment request accepted or emitted as an apply")
			}
		})
	}
}
