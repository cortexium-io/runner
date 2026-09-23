package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func migrationConfigFixture(t *testing.T) (string, Config) {
	t.Helper()
	cfg := explicitTestConfig()
	profile := cfg.Roles["reviewer"]
	profile.Access = RoleAccessHost
	cfg.Roles["reviewer"] = profile
	cfg.Verification = map[string]VerificationEntrypoint{"complete": {
		Command: "/bin/sh", Args: []string{"-c", "exit 0"}, ToolchainCommands: []string{"/bin/sh"}, InputPaths: []string{"src"}, TimeoutSeconds: 60,
	}}
	path := filepath.Join(t.TempDir(), "runner.json")
	if err := SaveConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	return path, cfg
}

func TestDeliveryConfigMigrationExactDeltaBackupAndIdempotence(t *testing.T) {
	path, original := migrationConfigFixture(t)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := PlanDeliveryConfigMigration(path, "complete")
	if err != nil {
		t.Fatal(err)
	}
	previewed, _ := os.ReadFile(path)
	if string(previewed) != string(before) {
		t.Fatal("preview mutated config")
	}
	if err := plan.Apply(); err != nil {
		t.Fatal(err)
	}
	after, err := LoadTrustedConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if after.PlanDelivery == nil || !after.PlanDelivery.Enabled || after.PlanDelivery.CompleteVerification != "complete" {
		t.Fatal("migration did not enable exact entry")
	}
	after.PlanDelivery = original.PlanDelivery
	if !reflect.DeepEqual(after, original) {
		t.Fatal("migration changed unrelated configuration")
	}
	backups, _ := filepath.Glob(path + ".before-plan-delivery-*")
	if len(backups) != 1 {
		t.Fatal("exact backup missing")
	}
	backup, err := os.ReadFile(backups[0])
	if err != nil || string(backup) != string(before) {
		t.Fatal("backup is not exact original")
	}
	fresh, err := PlanDeliveryConfigMigration(path, "complete")
	if err != nil {
		t.Fatal(err)
	}
	activated, _ := os.Stat(path)
	if err := fresh.Apply(); err != nil {
		t.Fatal(err)
	}
	replayed, _ := os.Stat(path)
	if !os.SameFile(activated, replayed) {
		t.Fatal("idempotent apply rewrote configuration")
	}
}

func TestDeliveryMigrationPreservesExplicitWholePlanReviewer(t *testing.T) {
	path, cfg := migrationConfigFixture(t)
	cfg.Roles["plan_reviewer"] = RoleConfig{Extends: WorkRoleReviewer, Model: modelPointer("gpt-6-astra"), Reasoning: "medium"}
	cfg.PlanDelivery = &PlanDeliveryConfig{ReviewerRole: "plan_reviewer"}
	if err := SaveConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	preview, err := PlanDeliveryConfigMigration(path, "complete")
	if err != nil {
		t.Fatal(err)
	}
	if preview.After.ReviewerRole != "plan_reviewer" {
		t.Fatal("migration discarded explicit profile")
	}
	if err := preview.Apply(); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadTrustedConfig(path)
	if err != nil || loaded.PlanDelivery.ReviewerRole != "plan_reviewer" {
		t.Fatalf("profile not persisted: %v", err)
	}
}

func TestDeliveryConfigMigrationPreservesInterveningChanges(t *testing.T) {
	for _, change := range []string{"config", "catalog", "replacement", "symlink"} {
		t.Run(change, func(t *testing.T) {
			path, cfg := migrationConfigFixture(t)
			plan, err := PlanDeliveryConfigMigration(path, "complete")
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "config":
				cfg.MaxParallelism++
				err = SaveConfig(path, cfg)
			case "catalog":
				entry := cfg.Verification["complete"]
				entry.Args = []string{"-c", "exit 1"}
				cfg.Verification["complete"] = entry
				err = SaveConfig(path, cfg)
			case "replacement":
				err = SaveConfig(path, cfg)
			case "symlink":
				other := path + ".operator"
				if err = os.Rename(path, other); err == nil {
					err = os.Symlink(other, path)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			before, _ := os.ReadFile(path)
			if err := plan.Apply(); err == nil {
				t.Fatal("stale preview overwrote operator state")
			}
			after, _ := os.ReadFile(path)
			if string(after) != string(before) {
				t.Fatal("operator config changed")
			}
		})
	}
}

func TestDeliveryConfigMigrationDoesNotGrantHostOrCreateCatalog(t *testing.T) {
	path, cfg := migrationConfigFixture(t)
	if _, err := PlanDeliveryConfigMigration(path, "unreviewed"); err == nil {
		t.Fatal("missing catalog entry accepted")
	}
	profile := cfg.Roles["reviewer"]
	profile.Access = RoleAccessSandboxed
	cfg.Roles["reviewer"] = profile
	if err := SaveConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := PlanDeliveryConfigMigration(path, "complete"); err == nil || !strings.Contains(err.Error(), "will not grant host access") {
		t.Fatalf("containment silently changed: %v", err)
	}
}
