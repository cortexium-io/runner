package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/cortexium-io/runner/internal/saas"
)

func localGoFacts(t *testing.T) (saas.Authority, string, goScopeFacts) {
	t.Helper()
	root := t.TempDir()
	for _, dir := range []string{".genny", "internal/app/http"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	metadata := []byte(`{"schema_version":1,"generator":{"version":"v0.4.0"},"project":{"database":"sqlite","frontend":"vue","module":"fixture.invalid/local"},"auth":{"provider":"password_session"},"tenancy":{"enabled":true}}`)
	if err := os.WriteFile(filepath.Join(root, ".genny/genny.json"), metadata, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "internal/app/http/generated_routes.gny.go"), []byte("package http\nvar generatedServerDocumentRoutes = []serverDocumentRoute{}\nvar generatedStaticVuePageRoutes = []staticVuePageRoute{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	exit := 0
	s := goScopeFacts{Status: "failed", Reason: "required proof is missing: selected test cases skipped", ChildExited: true, ExitCode: &exit, Passed: 1, LeafPassed: 1, Skipped: 4}
	s.Packages = append(s.Packages, struct {
		Outcome string `json:"outcome"`
	}{"pass"})
	s.Tests = []goTestFact{{Package: "fixture.invalid/local/internal/app/http", Name: "TestRequiredTenantBoundary", Outcome: "pass"}}
	for _, v := range []struct{ pkg, name, reason string }{
		{"/internal/app/http", "TestServerDocumentPagesFailClosedBeforeVueShellBuild", "no server-backed Vue page routes"},
		{"/internal/app/http", "TestServerDocumentPagesServeBackendDataBeforeSPAFallback", "no server-backed Vue page routes"},
		{"/internal/app/http", "TestStaticVuePagesServePrerenderedHTMLBeforeSPAFallback", "no explicit static Vue page routes"},
		{"/internal/db", "TestGeneratedScopedModelPostgresRLS", "set GENNY_POSTGRES_TEST_DSN to run live generated Postgres RLS policy tests"},
	} {
		s.Tests = append(s.Tests, goTestFact{Package: "fixture.invalid/local" + v.pkg, Name: v.name, Outcome: "skip", SkipDiagnostic: "    generated_test.go:16: " + v.reason + "\n"})
	}
	return saas.Authority{Module: "fixture.invalid/local"}, root, s
}

func TestLocalApplicabilityKeepsStrictScopeAndExactGeneratedAbsence(t *testing.T) {
	a, root, s := localGoFacts(t)
	b, _ := json.Marshal(map[string]any{"scope": s})
	passed, na, err := applicableLocalGo(a, root, b, true)
	if err != nil || passed != 1 || len(na) != 4 || s.Status != "failed" {
		t.Fatal("original strict failure was changed or local scope lost", passed, na, err)
	}
	for _, tc := range []struct {
		name   string
		change func(*goScopeFacts, string)
	}{
		{"wrong reason", func(s *goScopeFacts, _ string) {
			s.Tests[1].SkipDiagnostic = "    generated_test.go:28: server-backed Vue shell has not been emitted by the frontend build\n"
		}},
		{"required skip", func(s *goScopeFacts, _ string) { s.Tests[1].Name = "TestRequiredTenantBoundary" }},
		{"foreign package", func(s *goScopeFacts, _ string) { s.Tests[1].Package = "foreign.invalid/package" }},
		{"overflow", func(s *goScopeFacts, _ string) { s.Tests[1].SkipOutputTruncated = true }},
		{"incomplete", func(s *goScopeFacts, _ string) { s.Incomplete = true }},
		{"unsettled", func(s *goScopeFacts, _ string) { s.ChildExited = false }},
		{"zero tests", func(s *goScopeFacts, _ string) { s.Passed = 0; s.LeafPassed = 0 }},
		{"exit failure", func(s *goScopeFacts, _ string) { exit := 1; s.ExitCode = &exit }},
		{"real route", func(_ *goScopeFacts, root string) {
			if err := os.WriteFile(filepath.Join(root, "internal/app/http/generated_routes.gny.go"), []byte("package http\nvar generatedServerDocumentRoutes = []serverDocumentRoute{{}}\nvar generatedStaticVuePageRoutes = []staticVuePageRoute{}\n"), 0600); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, root, s := localGoFacts(t)
			tc.change(&s, root)
			b, _ := json.Marshal(map[string]any{"scope": s})
			if _, _, err := applicableLocalGo(a, root, b, true); err == nil {
				t.Fatal("required or ambiguous proof bypassed")
			}
		})
	}
	if _, _, err = applicableLocalGo(a, root, b, false); err == nil {
		t.Fatal("unowned generation made skip basis authoritative")
	}
}
