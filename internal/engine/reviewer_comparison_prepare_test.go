package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/subprocess"
)

// These identical test-only files overlay both frozen sources. No production
// file, bundled skill, configuration or harness access setting is replaced.
var reviewerComparisonCommonFiles = []string{
	"internal/engine/eval_harness_test.go",
	"internal/engine/reviewer_eval_test.go",
	"internal/engine/reviewer_comparison_test.go",
	"internal/engine/reviewer_comparison_prepare_test.go",
	"internal/engine/testdata/reviewer/records.go",
	"internal/engine/testdata/reviewer/records_test.go",
	"internal/engine/testdata/reviewer/shallow_test.go.txt",
	"internal/engine/testdata/reviewer/COMPARISON.md",
}

func comparisonCommonFiles(root string) (map[string][]byte, string, error) {
	common := map[string][]byte{}
	var identity []byte
	for _, name := range reviewerComparisonCommonFiles {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			return nil, "", err
		}
		common[name] = data
		identity = append(identity, []byte(name+"\x00"+comparisonDigest(data)+"\n")...)
	}
	return common, comparisonDigest(identity), nil
}

func comparisonBuildCommand(ctx context.Context, directory, command string, args ...string) ([]byte, error) {
	output, err := (subprocess.OSRunner{}).RunBoundedHeadTailInput(ctx, command, args, directory, 5*time.Minute, nil, 1024*1024, "[truncated]")
	if err == nil && output.ExitCode != 0 {
		err = errors.New("comparison preparation command failed")
	}
	return []byte(output.Stdout + output.Stderr), err
}

func comparisonCleanSource(ctx context.Context, root, source string) error {
	head, err := comparisonBuildCommand(ctx, root, "git", "rev-parse", "HEAD")
	if err != nil || strings.TrimSpace(string(head)) != source || len(source) != 40 {
		return errors.New("comparison requires the exact committed revised source")
	}
	status, err := comparisonBuildCommand(ctx, root, "git", "status", "--porcelain", "--untracked-files=all")
	if err != nil || len(status) != 0 {
		return errors.New("comparison requires a clean reviewed worktree")
	}
	return nil
}

// Preparation builds and describes both workers but cannot invoke a model.
// Independent review can inspect manifest.json and the common fixture oracle
// before a separate, explicit run admission names this exact revised commit.
func TestPrepareReviewerComparison(t *testing.T) {
	if os.Getenv("CORTEXIUM_RUNNER_REVIEW_COMPARISON_MODE") != "prepare" {
		t.Skip("opt-in deterministic comparison preparation")
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	candidate := os.Getenv("CORTEXIUM_RUNNER_REVIEW_COMPARISON_APPROVED_CANDIDATE")
	if err := comparisonCleanSource(t.Context(), root, candidate); err != nil {
		t.Fatal(err)
	}
	directory := os.Getenv("CORTEXIUM_RUNNER_REVIEW_COMPARISON_DIRECTORY")
	if !filepath.IsAbs(directory) {
		t.Fatal("comparison artifact directory must be absolute and new")
	}
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	common, harnessDigest, err := comparisonCommonFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	manifest := reviewerComparisonManifest{Version: 1, HarnessDigest: harnessDigest, Model: reviewerComparisonModel, Reasoning: "medium"}
	for index, source := range []string{reviewerComparisonOldSource, candidate} {
		name := []string{"old", "revised"}[index]
		archive := filepath.Join(directory, name+".tar")
		buildDir := filepath.Join(directory, name+"-source")
		if err := os.Mkdir(buildDir, 0700); err != nil {
			t.Fatal(err)
		}
		if output, err := comparisonBuildCommand(t.Context(), root, "git", "archive", "--format=tar", "--output="+archive, source); err != nil {
			t.Fatalf("archive fixed %s source: %v\n%s", name, err, output)
		}
		if output, err := comparisonBuildCommand(t.Context(), root, "tar", "-xf", archive, "-C", buildDir); err != nil {
			t.Fatalf("extract fixed source: %v\n%s", err, output)
		}
		for path, data := range common {
			if err := os.WriteFile(filepath.Join(buildDir, path), data, 0600); err != nil {
				t.Fatal(err)
			}
		}
		binary := filepath.Join(buildDir, "internal", "engine", "reviewer.test")
		flags := "-X github.com/cortexium-io/runner/internal/engine.reviewerComparisonBuildSource=" + source
		if output, err := comparisonBuildCommand(t.Context(), buildDir, "go", "test", "-c", "-o", binary, "-ldflags", flags, "./internal/engine"); err != nil {
			t.Fatalf("compile fixed %s reviewer: %v\n%s", name, err, output)
		}
		data, err := os.ReadFile(binary)
		if err != nil {
			t.Fatal(err)
		}
		describeDir := filepath.Join(directory, name+"-describe")
		if err := os.Mkdir(describeDir, 0700); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
		response, err := invokeComparisonWorker(ctx, binary, reviewerComparisonRequest{Describe: true, Source: source, Output: filepath.Join(describeDir, "result.json")})
		cancel()
		if err != nil || response.Source != source || len(response.Cases) != 4 || response.Result != nil {
			t.Fatalf("describe %s fixed reviewer failed (no model called): %v", name, err)
		}
		if index == 0 {
			manifest.Cases = response.Cases
		} else if !reflect.DeepEqual(manifest.Cases, response.Cases) {
			t.Fatal("old/revised fixture metadata differs; no live admission allowed")
		}
		manifest.Arms = append(manifest.Arms, reviewerComparisonArm{Name: name, Source: source, Binary: binary, BinaryDigest: comparisonDigest(data), SkillDigest: response.SkillDigest})
	}
	if err := comparisonCleanSource(t.Context(), root, candidate); err != nil {
		t.Fatal(err)
	}
	if err := validateComparisonManifest(manifest); err != nil {
		t.Fatal(err)
	}
	if err := writeComparisonJSON(filepath.Join(directory, "manifest.json"), manifest); err != nil {
		t.Fatal(err)
	}
	t.Log("prepared identical frozen corpus; 0 model assignments; independent review and separate live admission still required")
}

func TestReviewerComparisonCommonOverlayContainsNoProductionOverrides(t *testing.T) {
	for _, name := range reviewerComparisonCommonFiles {
		if !strings.HasPrefix(name, "internal/engine/") || !strings.HasSuffix(name, "_test.go") && !strings.HasPrefix(name, "internal/engine/testdata/reviewer/") {
			t.Fatalf("production guidance override in common overlay: %s", name)
		}
	}
}
