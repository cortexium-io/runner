package config

import (
	"fmt"

	bundledskills "github.com/cortexium-io/runner/skills"
)

const (
	StaticCheckPassed = "passed"
	StaticCheckFailed = "failed"
)

type StaticCheckResult struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

type StaticSkillCheck struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
}

type StaticCheckReport struct {
	Ready         bool                `json:"ready"`
	ConfigVersion int                 `json:"config_version,omitempty"`
	Checks        []StaticCheckResult `json:"checks"`
	BundledSkills []StaticSkillCheck  `json:"bundled_skills,omitempty"`
}

func CheckConfig(path string) (Config, StaticCheckReport, error) {
	report := StaticCheckReport{Checks: []StaticCheckResult{}}
	cfg, err := loadConfigFile(path)
	if err != nil {
		report.Checks = append(report.Checks, StaticCheckResult{ID: "config.load", Status: StaticCheckFailed, Detail: err.Error()})
		return Config{}, report, err
	}
	report.Checks = append(report.Checks, StaticCheckResult{ID: "config.load", Status: StaticCheckPassed, Detail: "configuration is valid JSON with no unknown fields"})
	return checkDecodedConfig(cfg, report)
}

func checkDecodedConfig(cfg Config, report StaticCheckReport) (Config, StaticCheckReport, error) {
	report.ConfigVersion = cfg.ConfigVersion
	contractCheckID := fmt.Sprintf("config.v%d", ConfigVersion)
	if err := cfg.Validate(); err != nil {
		report.Checks = append(report.Checks, StaticCheckResult{ID: contractCheckID, Status: StaticCheckFailed, Detail: err.Error()})
		return Config{}, report, err
	}
	report.Checks = append(report.Checks,
		StaticCheckResult{ID: contractCheckID, Status: StaticCheckPassed, Detail: "configuration uses the supported Runner contract"},
		StaticCheckResult{ID: "workflow", Status: StaticCheckPassed, Detail: "roles, statuses, repository, and capability requirements are internally consistent"},
	)
	validatedSkills, err := bundledskills.Validate(bundledskills.EmbeddedCatalog{})
	if err != nil {
		report.Checks = append(report.Checks, StaticCheckResult{ID: "skills.embedded", Status: StaticCheckFailed, Detail: err.Error()})
		return Config{}, report, err
	}
	for _, skill := range validatedSkills {
		report.BundledSkills = append(report.BundledSkills, StaticSkillCheck{ID: skill.ID, Version: skill.Version, SHA256: skill.SHA256})
	}
	report.Checks = append(report.Checks, StaticCheckResult{ID: "skills.embedded", Status: StaticCheckPassed, Detail: fmt.Sprintf("%d trusted bundled skills match their manifests and pinned hashes", len(validatedSkills))})
	report.Ready = true
	return cfg, report, nil
}

func ValidateConfiguration(cfg Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	_, err := bundledskills.Validate(bundledskills.EmbeddedCatalog{})
	if err != nil {
		return fmt.Errorf("validate bundled skills: %w", err)
	}
	return nil
}
