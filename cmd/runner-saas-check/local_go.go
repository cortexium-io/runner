package main

import (
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/cortexium-io/runner/internal/saas"
)

type goTestFact struct {
	Package             string `json:"package"`
	Name                string `json:"name"`
	Outcome             string `json:"outcome"`
	SkipDiagnostic      string `json:"skip_diagnostic"`
	SkipOutputTruncated bool   `json:"skip_output_truncated"`
}
type goScopeFacts struct {
	Status      string       `json:"status"`
	Reason      string       `json:"reason"`
	ChildExited bool         `json:"child_exited"`
	ExitCode    *int         `json:"exit_code"`
	Incomplete  bool         `json:"evidence_incomplete"`
	Passed      int          `json:"passed"`
	Failed      int          `json:"failed"`
	Skipped     int          `json:"skipped"`
	LeafPassed  int          `json:"leaf_passed"`
	Tests       []goTestFact `json:"tests"`
	Packages    []struct {
		Outcome string `json:"outcome"`
	} `json:"packages"`
	BuildFailures []json.RawMessage `json:"build_failures"`
}

var skipLine = regexp.MustCompile(`^[^:\r\n]+\.go:[0-9]+: (.+)$`)

// This fixed SQLite consumer does not change runner-go-check's stricter selected
// scope verdict. Its full original report, including status failed/exit2 for
// selected skips, remains intact. Only exact generated non-applicable cases may
// be separated from actual local proof; this is not a configurable skip policy.
func applicableLocalGo(a saas.Authority, root string, data []byte, owned bool) (int, []saas.NotApplicable, error) {
	bad := errors.New("applicable local Go proof is incomplete or contains a required/unexplained skip")
	var receipt struct {
		Scope goScopeFacts `json:"scope"`
	}
	if !owned || json.Unmarshal(data, &receipt) != nil {
		return 0, nil, bad
	}
	s := receipt.Scope
	if !s.ChildExited || s.ExitCode == nil || *s.ExitCode != 0 || s.Incomplete || s.Failed != 0 || len(s.BuildFailures) != 0 || s.Passed < 1 || s.LeafPassed < 1 || len(s.Tests) == 0 || len(s.Tests) > 1024 || len(s.Packages) == 0 || s.Skipped > 4 {
		return 0, nil, bad
	}
	for _, p := range s.Packages {
		if p.Outcome != "pass" && p.Outcome != "skip" {
			return 0, nil, bad
		}
	}
	if s.Skipped == 0 {
		if s.Status != "passed" {
			return 0, nil, bad
		}
		return s.LeafPassed, nil, nil
	}
	if s.Status != "failed" || s.Reason != "required proof is missing: selected test cases skipped" {
		return 0, nil, bad
	}
	metadata, err := boundedFile(filepath.Join(root, ".genny", "genny.json"), 1<<20)
	if err != nil {
		return 0, nil, err
	}
	var m struct {
		Schema    int `json:"schema_version"`
		Generator struct {
			Version string `json:"version"`
		} `json:"generator"`
		Project struct {
			Database string `json:"database"`
			Frontend string `json:"frontend"`
			Module   string `json:"module"`
		} `json:"project"`
		Auth struct {
			Provider string `json:"provider"`
		} `json:"auth"`
		Tenancy struct {
			Enabled bool `json:"enabled"`
		} `json:"tenancy"`
	}
	if json.Unmarshal(metadata, &m) != nil || m.Schema != 1 || m.Generator.Version != "v0.4.0" || m.Project.Database != "sqlite" || m.Project.Frontend != "vue" || m.Project.Module != a.Module || m.Auth.Provider != "password_session" || !m.Tenancy.Enabled {
		return 0, nil, bad
	}
	routes, err := boundedFile(filepath.Join(root, "internal", "app", "http", "generated_routes.gny.go"), 1<<20)
	if err != nil {
		return 0, nil, err
	}
	file, err := parser.ParseFile(token.NewFileSet(), "generated_routes.gny.go", routes, 0)
	if err != nil {
		return 0, nil, bad
	}
	empty := map[string]bool{}
	for _, d := range file.Decls {
		g, ok := d.(*ast.GenDecl)
		if !ok || g.Tok != token.VAR {
			continue
		}
		for _, spec := range g.Specs {
			v, ok := spec.(*ast.ValueSpec)
			if !ok || len(v.Names) != 1 || len(v.Values) != 1 {
				continue
			}
			literal, ok := v.Values[0].(*ast.CompositeLit)
			if ok {
				empty[v.Names[0].Name] = len(literal.Elts) == 0
			}
		}
	}
	type optional struct{ pkg, reason, symbol string }
	allowed := map[string]optional{
		"TestServerDocumentPagesFailClosedBeforeVueShellBuild":     {"/internal/app/http", "no server-backed Vue page routes", "generatedServerDocumentRoutes"},
		"TestServerDocumentPagesServeBackendDataBeforeSPAFallback": {"/internal/app/http", "no server-backed Vue page routes", "generatedServerDocumentRoutes"},
		"TestStaticVuePagesServePrerenderedHTMLBeforeSPAFallback":  {"/internal/app/http", "no explicit static Vue page routes", "generatedStaticVuePageRoutes"},
		"TestGeneratedScopedModelPostgresRLS":                      {"/internal/db", "set GENNY_POSTGRES_TEST_DSN to run live generated Postgres RLS policy tests", ""},
	}
	var notApplicable []saas.NotApplicable
	seen := map[string]bool{}
	for _, test := range s.Tests {
		if test.Outcome == "pass" {
			continue
		}
		if test.Outcome != "skip" || test.SkipOutputTruncated || seen[test.Package+"/"+test.Name] {
			return 0, nil, bad
		}
		want, ok := allowed[test.Name]
		if !ok || test.Package != a.Module+want.pkg || want.symbol != "" && !empty[want.symbol] {
			return 0, nil, bad
		}
		reason := ""
		for _, line := range strings.Split(test.SkipDiagnostic, "\n") {
			if match := skipLine.FindStringSubmatch(strings.TrimSpace(line)); match != nil {
				if reason != "" {
					return 0, nil, bad
				}
				reason = match[1]
			}
		}
		if reason != want.reason {
			return 0, nil, bad
		}
		basis := "owned Genny v0.4.0 SQLite/auth/tenancy metadata SHA256 " + saas.Digest(metadata)
		if want.symbol != "" {
			basis += "; empty AST " + want.symbol + " in generated routes SHA256 " + saas.Digest(routes)
		}
		notApplicable = append(notApplicable, saas.NotApplicable{Package: test.Package, Test: test.Name, Reason: reason, Basis: basis})
		seen[test.Package+"/"+test.Name] = true
	}
	if len(notApplicable) != s.Skipped {
		return 0, nil, bad
	}
	return s.LeafPassed, notApplicable, nil
}
