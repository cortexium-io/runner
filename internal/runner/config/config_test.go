package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func example(t *testing.T) []byte {
	t.Helper()
	b, e := os.ReadFile("../../../docs/runner-redesign-proposal.md")
	if e != nil {
		t.Fatal(e)
	}
	for _, block := range strings.Split(string(b), "```json\n")[1:] {
		body := strings.SplitN(block, "```", 2)[0]
		if strings.Contains(body, `"format": "runner-local-v1"`) {
			return []byte(body)
		}
	}
	t.Fatal("configuration example missing")
	return nil
}
func TestPublishedConfigAndUnsupportedControls(t *testing.T) {
	raw := example(t)
	var c Config
	if e := Decode(raw, &c); e != nil {
		t.Fatal(e)
	}
	if e := c.Validate(); e != nil {
		t.Fatal(e)
	}
	for _, mutate := range []func(*Config){func(c *Config) { c.Policy.MaxParallelism = 17 }, func(c *Config) { c.Policy.MaxRepairs = -1 }, func(c *Config) { c.Policy.MaxTransientRetries = -1 }, func(c *Config) { c.Policy.MaxBaseRefreshes = -1 }, func(c *Config) { c.Policy.ImplementationLadder = []string{"larger"} }, func(c *Config) { c.Repository.BaseFreshness = "remote" }, func(c *Config) { c.Delivery.Mode = "github" }, func(c *Config) { c.StateDir = c.Repository.Path }, func(c *Config) {
		p := c.Profiles[c.Roles["implementer"]]
		p.Timeout = "bad"
		c.Profiles[c.Roles["implementer"]] = p
	}} {
		var invalid Config
		if e := Decode(raw, &invalid); e != nil {
			t.Fatal(e)
		}
		mutate(&invalid)
		if invalid.Validate() == nil {
			t.Fatal("unsupported config accepted")
		}
	}
	var unknown map[string]any
	if e := json.Unmarshal(raw, &unknown); e != nil {
		t.Fatal(e)
	}
	unknown["hidden_retries"] = 5
	b, _ := json.Marshal(unknown)
	if Decode(b, &Config{}) == nil {
		t.Fatal("unknown key accepted")
	}
	if Decode(append(raw, []byte(" {}")...), &Config{}) == nil {
		t.Fatal("trailing configuration accepted")
	}
}
func TestSymlinkPathsRejectedBeforeExecution(t *testing.T) {
	root, e := filepath.EvalSymlinks(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	link := filepath.Join(root, "link")
	if e = os.Symlink(root, link); e != nil {
		t.Fatal(e)
	}
	if NoSymlinks(filepath.Join(link, "state")) == nil {
		t.Fatal("symlink ancestor accepted")
	}
}

func TestReadyPolicyLimitsAndEscalation(t *testing.T) {
	raw := example(t)
	for _, tc := range []struct {
		name   string
		change func(*Config)
		valid  bool
	}{
		{"maximum parallelism", func(c *Config) {
			c.Policy.MaxParallelism = 16
			c.Policy.MaxRepairs = 2
			c.Policy.MaxTransientRetries = 3
		}, true},
		{"base refresh allowance", func(c *Config) { c.Policy.MaxBaseRefreshes = 2 }, true},
		{"successor", func(c *Config) { c.Policy.ImplementationLadder = []string{"review"} }, true},
		{"starting profile repeated", func(c *Config) { c.Policy.ImplementationLadder = []string{"build"} }, false},
		{"successor repeated", func(c *Config) { c.Policy.ImplementationLadder = []string{"review", "review"} }, false},
		{"positive ceiling", func(c *Config) { n := 3; c.Policy.MaxInvocationsPerTask = &n }, true},
		{"optional planner", func(c *Config) { c.Roles["planner"] = "build" }, true},
		{"draft publication", func(c *Config) { c.Delivery.Mode = "github-draft"; c.Delivery.Repository = "owner/repo" }, true},
		{"missing destination", func(c *Config) { c.Delivery.Mode = "github-draft" }, false},
		{"destination injection", func(c *Config) { c.Delivery.Mode = "github-draft"; c.Delivery.Repository = "owner/repo?token=x" }, false},
		{"ignored destination", func(c *Config) { c.Delivery.Repository = "owner/repo" }, false},
		{"unknown planner profile", func(c *Config) { c.Roles["planner"] = "missing" }, false},
		{"zero ceiling", func(c *Config) { n := 0; c.Policy.MaxInvocationsPerTask = &n }, false},
		{"negative ceiling", func(c *Config) { n := -1; c.Policy.MaxInvocationsPerTask = &n }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var c Config
			if err := Decode(raw, &c); err != nil {
				t.Fatal(err)
			}
			tc.change(&c)
			if err := c.Validate(); (err == nil) != tc.valid {
				t.Fatal("unexpected validation", err)
			}
		})
	}
}
