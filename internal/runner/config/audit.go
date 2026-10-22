package config

import (
	"errors"
	"time"

	"github.com/cortexium-io/runner/internal/runner/core"
)

// The adapter is deliberately tied to one qualified maintained npm evaluator.
const NPMAuditAdapter = "npm-public-v1"
const NPMAuditTool = "11.19.0"
const NPMRegistry = "https://registry.npmjs.org"

type NPMAuditPolicy struct {
	Omit   []string `json:"omit,omitempty"`
	Level  string   `json:"level"`
	MaxAge string   `json:"max_age,omitempty"`
}

func (p NPMAuditPolicy) Age() time.Duration {
	if p.MaxAge == "" {
		return time.Hour
	}
	d, _ := time.ParseDuration(p.MaxAge)
	return d
}

func (p NPMAuditPolicy) Validate() error {
	if p.Level != "info" && p.Level != "low" && p.Level != "moderate" && p.Level != "high" && p.Level != "critical" {
		return errors.New("npm audit level must be info, low, moderate, high or critical")
	}
	if p.Age() <= 0 {
		return errors.New("npm audit max_age must be a positive duration (default 1h)")
	}
	seen := map[string]bool{}
	for _, omit := range p.Omit {
		if (omit != "dev" && omit != "optional" && omit != "peer") || seen[omit] {
			return errors.New("npm audit omit supports unique dev, optional and peer values")
		}
		seen[omit] = true
	}
	return nil
}

func (c QualityCheck) AuditPolicy() string {
	return core.Digest(struct {
		Adapter, Tool, Registry, Directory, Timeout, At string
		Policy                                          *NPMAuditPolicy
	}{NPMAuditAdapter, NPMAuditTool, NPMRegistry, c.Directory, c.Timeout, c.At, c.NPMAudit})
}

func (q Quality) Audits(t core.Task) Quality {
	q = q.For(t, t.Release != nil)
	checks := []QualityCheck{}
	for _, c := range q.Checks {
		if c.NPMAudit != nil {
			checks = append(checks, c)
		}
	}
	q.Checks = checks
	return q
}
