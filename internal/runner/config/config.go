package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/cortexium-io/runner/internal/runner/core"
	"github.com/cortexium-io/runner/internal/securefs"
)

type Repository struct {
	Path          string `json:"path"`
	BaseRef       string `json:"base_ref"`
	BaseFreshness string `json:"base_freshness"`
}
type Policy struct {
	MaxParallelism        int      `json:"max_parallelism"`
	MaxRepairs            int      `json:"max_repairs"`
	MaxTransientRetries   int      `json:"max_transient_retries"`
	MaxBaseRefreshes      int      `json:"max_base_refreshes"`
	MaxInvocationsPerTask *int     `json:"max_invocations_per_task,omitempty"`
	ImplementationLadder  []string `json:"implementation_ladder"`
}
type Config struct {
	Format      string                  `json:"format"`
	Repository  Repository              `json:"repository"`
	StateDir    string                  `json:"state_dir"`
	WorktreeDir string                  `json:"worktree_dir"`
	Profiles    map[string]core.Profile `json:"profiles"`
	Roles       map[string]string       `json:"roles"`
	Policy      Policy                  `json:"policy"`
	Delivery    struct {
		Mode       string `json:"mode"`
		Repository string `json:"repository,omitempty"`
	} `json:"delivery"`
}

func Load(path string) (Config, error) {
	var c Config
	b, _, state, err := securefs.ReadFile(path, 1024*1024)
	if err != nil {
		return c, err
	}
	if err = securefs.ValidateOwnedRegularFile(state, uint32(os.Geteuid())); err != nil {
		return c, fmt.Errorf("config provenance: %w", err)
	}
	if err = Decode(b, &c); err != nil {
		return c, err
	}
	return c, c.Validate()
}

func Decode(b []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return errors.New("expected exactly one JSON value")
	}
	return nil
}

func (c Config) Validate() error {
	if c.Format != "runner-local-v1" {
		return errors.New("configuration format must be runner-local-v1")
	}
	if c.Repository.BaseFreshness != "recorded" || !strings.HasPrefix(c.Repository.BaseRef, "refs/heads/") {
		return errors.New("this bundle requires a local refs/heads base and explicit recorded freshness")
	}
	if c.Delivery.Mode != "local" && c.Delivery.Mode != "github-draft" {
		return errors.New("delivery mode must be local or github-draft; integration is not implemented")
	}
	if c.Delivery.Mode == "local" && c.Delivery.Repository != "" || c.Delivery.Mode == "github-draft" && !ValidRepository(c.Delivery.Repository) {
		return errors.New("github-draft requires an explicit OWNER/REPO destination; local delivery has no remote destination")
	}
	if c.Policy.MaxParallelism < 1 || c.Policy.MaxParallelism > 16 || c.Policy.MaxRepairs < 0 || c.Policy.MaxTransientRetries < 0 || c.Policy.MaxBaseRefreshes < 0 {
		return errors.New("parallelism must be 1–16; repair/retry/base-refresh limits must be nonnegative")
	}
	if c.Policy.MaxInvocationsPerTask != nil && *c.Policy.MaxInvocationsPerTask < 1 {
		return errors.New("max_invocations_per_task must be absent or positive")
	}
	seen := map[string]bool{c.Roles["implementer"]: true}
	for _, name := range c.Policy.ImplementationLadder {
		if _, ok := c.Profiles[name]; !ok || seen[name] {
			return fmt.Errorf("implementation ladder profile %q is missing or repeated", name)
		}
		seen[name] = true
	}
	if c.Roles["implementer"] == "" || c.Roles["reviewer"] == "" {
		return errors.New("implementer and reviewer roles are required")
	}
	for role, name := range c.Roles {
		if role != "implementer" && role != "reviewer" && role != "planner" {
			return fmt.Errorf("unsupported role %q", role)
		}
		if _, ok := c.Profiles[name]; !ok {
			return fmt.Errorf("%s references missing profile %s", role, name)
		}
	}
	for name, p := range c.Profiles {
		d, err := time.ParseDuration(p.Timeout)
		if p.Harness != "codex" || strings.TrimSpace(p.Model) == "" || strings.TrimSpace(p.Reasoning) == "" || err != nil || d <= 0 {
			return fmt.Errorf("profile %s needs codex, explicit model/reasoning and positive timeout", name)
		}
	}
	roots := []string{c.Repository.Path, c.StateDir, c.WorktreeDir}
	for i, p := range roots {
		if !filepath.IsAbs(p) || filepath.Clean(p) != p {
			return errors.New("repository, state and worktree paths must be clean absolute paths")
		}
		if err := NoSymlinks(p); err != nil {
			return err
		}
		for _, q := range roots[:i] {
			if Within(p, q) || Within(q, p) {
				return errors.New("repository, state and worktree paths must not overlap")
			}
		}
	}
	return nil
}

var repositoryName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]*/[A-Za-z0-9][A-Za-z0-9_.-]*$`)

func ValidRepository(s string) bool {
	return repositoryName.MatchString(s) && !strings.HasSuffix(s, ".git")
}

func NoSymlinks(path string) error {
	for p := path; p != filepath.Dir(p); p = filepath.Dir(p) {
		info, err := os.Lstat(p)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("path contains a symlink: %s", p)
		}
	}
	return nil
}
func Within(path, root string) bool {
	r, e := filepath.Rel(root, path)
	return e == nil && (r == "." || (!strings.HasPrefix(r, ".."+string(filepath.Separator)) && r != ".."))
}
func (c Config) Revision() string { return core.Digest(c) }
func (c Config) Role(role string) (string, core.Profile) {
	if role == "audit" || role == "verify" {
		role = "reviewer"
	}
	if core.PlanningRole(role) {
		role = "planner"
	}
	n := c.Roles[role]
	return n, c.Profiles[n]
}

type Settings struct {
	Config       Config  `json:"config"`
	RepositoryID string  `json:"repository_id"`
	Quality      Quality `json:"quality"`
}

// SameAuthority permits live execution-policy edits, never a change to the
// repository, state, quality agreement, or role permission boundaries.
func SameAuthority(a, b Settings) bool {
	return core.Digest(a.Quality) == core.Digest(b.Quality) && SameRepository(a, b)
}

func SameRepository(a, b Settings) bool {
	return a.RepositoryID == b.RepositoryID &&
		a.Config.Format == b.Config.Format && a.Config.Repository == b.Config.Repository &&
		a.Config.StateDir == b.Config.StateDir && a.Config.WorktreeDir == b.Config.WorktreeDir && a.Config.Delivery == b.Config.Delivery
}

func (c Config) Implementer(escalations int) (string, core.Profile) {
	names := append([]string{c.Roles["implementer"]}, c.Policy.ImplementationLadder...)
	if escalations >= len(names) {
		escalations = len(names) - 1
	}
	name := names[escalations]
	return name, c.Profiles[name]
}
