package config

import "testing"

func TestReviewEvidencePathsAreExplicitBoundedSelections(t *testing.T) {
	for _, paths := range [][]string{{}, {"test-results/receipt.json", "artifacts/browser"}} {
		cfg := explicitTestConfig()
		cfg.ReviewEvidencePaths = paths
		resolved, err := cfg.Resolve()
		if err != nil || len(resolved.ReviewEvidencePaths) != len(paths) {
			t.Fatalf("valid evidence paths did not resolve: %v", err)
		}
		if len(paths) > 0 {
			resolved.ReviewEvidencePaths[0] = "changed"
			if cfg.ReviewEvidencePaths[0] == "changed" {
				t.Fatal("resolved paths alias saved configuration")
			}
		}
	}
	for _, paths := range [][]string{
		{""}, {"."}, {".."}, {"../reports"}, {"/tmp/reports"}, {"reports/../private"},
		{"reports//report"}, {"reports/"}, {"reports/*"}, {`reports\file`}, {"reports\nsecret"},
		{".git"}, {"reports/.GIT/config"}, {"reports", "reports/test.json"}, {"Reports", "reports"},
	} {
		cfg := explicitTestConfig()
		cfg.ReviewEvidencePaths = paths
		if _, err := cfg.Resolve(); err == nil {
			t.Fatalf("unsafe or ambiguous selection accepted: %q", paths)
		}
	}
}
