package config

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/cortexium-io/runner/internal/runner/core"
)

// CheckEnvironment is fixed by Runner, not selected by repository code or agents.
const CheckEnvironment = "isolated PATH; private HOME/TMPDIR; LANG=C LC_ALL=C; no inherited credentials"

type QualityCheck struct {
	Path          string   `json:"-"`
	ID            string   `json:"id"`
	Command       string   `json:"command"`
	Directory     string   `json:"directory"`
	Prerequisites []string `json:"prerequisites"`
	Timeout       string   `json:"timeout"`
}
type QualitySpec struct {
	RequiredCI []core.CIRequirement `json:"required_ci,omitempty"`
	Definition string               `json:"definition"`
	Sources    []string             `json:"sources"`
	Checks     []QualityCheck       `json:"checks"`
}
type ExceptionRequest struct {
	CheckID   string   `json:"check_id"`
	Rationale string   `json:"rationale"`
	Scope     []string `json:"scope"`
}
type BaselineException struct {
	CheckID      string            `json:"check_id"`
	Rationale    string            `json:"rationale"`
	Files        map[string]string `json:"files"`
	ExitCode     int               `json:"exit_code"`
	OutputDigest string            `json:"output_digest"`
}
type Quality struct {
	RequiredCI []core.CIRequirement    `json:"required_ci,omitempty"`
	Path       string                  `json:"executable_path"`
	Definition string                  `json:"definition"`
	Documents  map[string]string       `json:"source_documents,omitempty"`
	Sources    map[string]string       `json:"sources"`
	Checks     []QualityCheck          `json:"checks"`
	Base       string                  `json:"baseline_base"`
	Baseline   []core.CheckObservation `json:"baseline"`
	Exceptions []BaselineException     `json:"exceptions"`
}

func (q Quality) Revision() string {
	if q.Definition == "" {
		return ""
	}
	return core.Digest(q)
}

type QualityReport struct {
	ID           string            `json:"id"`
	RepositoryID string            `json:"repository_id"`
	Quality      Quality           `json:"quality"`
	Files        map[string]string `json:"files"`
}

var checkID = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)

func RelativePath(p string) bool {
	return p != "" && !filepath.IsAbs(p) && filepath.Clean(p) == p && p != ".." && !strings.HasPrefix(p, "../") && p != ".git" && !strings.HasPrefix(p, ".git/")
}
func (s QualitySpec) Validate() error {
	if strings.TrimSpace(s.Definition) == "" || len(s.Checks) == 0 {
		return errors.New("quality needs a definition and at least one named check")
	}
	seen := map[string]bool{}
	for _, c := range s.Checks {
		d, err := time.ParseDuration(c.Timeout)
		if !checkID.MatchString(c.ID) || seen[c.ID] || strings.TrimSpace(c.Command) == "" || !RelativePath(c.Directory) || err != nil || d <= 0 || len(c.Prerequisites) == 0 {
			return fmt.Errorf("invalid or duplicate check %q: command, relative directory, prerequisites and positive timeout required", c.ID)
		}
		for _, p := range c.Prerequisites {
			if p == "" || strings.ContainsAny(p, "/ \t\n") {
				return fmt.Errorf("check %s: prerequisites must be executable names", c.ID)
			}
		}
		seen[c.ID] = true
	}
	seen = map[string]bool{}
	for _, p := range s.Sources {
		if !RelativePath(p) || p == "." || seen[p] {
			return fmt.Errorf("invalid or duplicate quality source %q", p)
		}
		seen[p] = true
	}
	ciSeen := map[core.CIRequirement]bool{}
	if len(s.RequiredCI) > 100 {
		return errors.New("required_ci supports at most 100 checks")
	}
	for _, r := range s.RequiredCI {
		if r.Name == "" || len(r.Name) > 200 || strings.TrimSpace(r.Name) != r.Name || strings.ContainsFunc(r.Name, unicode.IsControl) || ciSeen[r] ||
			(r.Kind != "check" && r.Kind != "status") || (r.Kind == "check" && r.AppID <= 0) || (r.Kind == "status" && r.AppID != 0) {
			return errors.New("required_ci needs unique exact names, kind check with a positive app_id, or kind status without app_id")
		}
		ciSeen[r] = true
	}

	return nil
}

// ResolveExceptions accepts only observed, reproducible failures, never unavailable checks.
func (r QualityReport) ResolveExceptions(requests []ExceptionRequest) (Quality, error) {
	q := r.Quality
	q.Exceptions = nil
	seen := map[string]bool{}
	for _, req := range requests {
		if seen[req.CheckID] || strings.TrimSpace(req.Rationale) == "" || len(req.Scope) == 0 {
			return q, errors.New("each exception needs a unique check, rationale and exact source scope")
		}
		seen[req.CheckID] = true
		var found *core.CheckObservation
		for i := range q.Baseline {
			if q.Baseline[i].CheckID == req.CheckID {
				found = &q.Baseline[i]
			}
		}
		if found == nil || found.Status != "failed" || !found.Stopped || found.OutputDigest == "" {
			return q, fmt.Errorf("check %s is not an observed failure eligible for an exception", req.CheckID)
		}
		ex := BaselineException{CheckID: req.CheckID, Rationale: req.Rationale, ExitCode: found.ExitCode, OutputDigest: found.OutputDigest, Files: map[string]string{}}
		for _, path := range req.Scope {
			digest, ok := r.Files[path]
			if !RelativePath(path) || !ok || digest == "directory" || strings.HasPrefix(digest, "link:") || ex.Files[path] != "" {
				return q, fmt.Errorf("exception scope %q must identify an existing regular source file exactly once", path)
			}
			ex.Files[path] = digest
		}
		q.Exceptions = append(q.Exceptions, ex)
	}
	return q, nil
}
func (q Quality) Accepts(result core.CheckObservation, files map[string]string) error {
	if !result.Stopped || result.Status == "unavailable" {
		return fmt.Errorf("check %s unavailable: %s", result.CheckID, result.Detail)
	}
	if result.Status == "passed" && result.ExitCode == 0 {
		return nil
	}
	for _, ex := range q.Exceptions {
		if ex.CheckID != result.CheckID {
			continue
		}
		if result.Status != "failed" || result.ExitCode != ex.ExitCode || result.OutputDigest != ex.OutputDigest {
			break
		}
		for path, digest := range ex.Files {
			if files[path] != digest {
				return fmt.Errorf("exception %s invalidated by changed scope %s", ex.CheckID, path)
			}
		}
		return nil
	}
	return fmt.Errorf("check %s failed without a matching unchanged baseline exception", result.CheckID)
}

func (q Quality) ValidateResults(results []core.CheckObservation, files map[string]string) error {
	if len(results) != len(q.Checks) {
		return errors.New("required quality check evidence is incomplete")
	}
	for i, check := range q.Checks {
		if results[i].CheckID != check.ID {
			return errors.New("quality check identity mismatch")
		}
		if err := q.Accepts(results[i], files); err != nil {
			return err
		}
	}
	return nil
}
