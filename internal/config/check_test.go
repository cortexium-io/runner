package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	bundledskills "github.com/cortexium-io/runner/skills"
)

func TestStaticCheckValidatesConfigContractAndPinnedSkills(t *testing.T) {
	cfg := explicitTestConfig()
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("encode config: %v", err)
	}
	path := filepath.Join(t.TempDir(), "runner.config.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	loaded, report, err := CheckConfig(path)
	if err != nil {
		t.Fatalf("check config: %v", err)
	}
	if !report.Ready || loaded.ConfigVersion != ConfigVersion || len(report.BundledSkills) != len((bundledskills.EmbeddedCatalog{}).List()) {
		t.Fatalf("unexpected static report %#v", report)
	}
}

func TestStaticCheckRejectsUnknownConfigMajor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runner.config.json")
	config := `{"config_version":999,"runner_id":"runner","project_dir":"/project","github_project":{"owner":"example","number":1}}`
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	_, report, err := CheckConfig(path)
	if err == nil || report.Ready || !strings.Contains(err.Error(), "config_version") {
		t.Fatalf("unsupported config contract was accepted: report=%#v error=%v", report, err)
	}
}

func TestStaticCheckRejectsExecutionSettingsInHarnessDefinitions(t *testing.T) {
	data, err := json.Marshal(explicitTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"timeout_seconds":30,`, `"model":"configured-model",`, `"reasoning_effort":"high",`, `"working_dir":"/tmp",`} {
		t.Run(field, func(t *testing.T) {
			value := strings.Replace(string(data), `"command":"codex",`, `"command":"codex",`+field, 1)
			path := filepath.Join(t.TempDir(), "runner.config.json")
			if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, _, err := CheckConfig(path); err == nil || !strings.Contains(err.Error(), "unknown field") {
				t.Fatalf("harness execution setting %s was accepted: %v", field, err)
			}
		})
	}
}
