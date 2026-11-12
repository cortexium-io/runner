package execution

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/guidesmedia"
	"github.com/cortexium-io/runner/internal/runner"
)

type guideByteCount int64

func (n *guideByteCount) Write(b []byte) (int, error) {
	*n += guideByteCount(len(b))
	return len(b), nil
}

// This opt-in fixture reads only the approved immutable media. It creates a new
// synthetic repository, never a Jack source checkout or product verification.
func TestGuidesExactAssetsOrdinaryCaptureFeasibility(t *testing.T) {
	if os.Getenv("RUNNER_GUIDE_MEDIA_FEASIBILITY") != "1" {
		t.Skip("owned exact-asset maintenance measurement only")
	}
	m, err := guidesmedia.Approved()
	if err != nil {
		t.Fatal(err)
	}
	d, repo := workspaceDisk(t), sourceRepository(t)
	w := &Workspaces{disk: d}
	a := sourceAssignment(d, repo, "attempt_media_feasibility")
	r, err := w.Prepare(t.Context(), a)
	if err != nil {
		t.Fatal(err)
	}
	defer w.ForgetRun(context.Background(), a.RunID)
	var raw int64
	for _, g := range m.Guides {
		for _, f := range g.Files {
			data, err := guidesmedia.ReadApproved(t.Context(), f)
			if err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(r.WorkDir, guidesmedia.Target(g, f))
			if err = os.MkdirAll(filepath.Dir(file), 0700); err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(file, data, 0600); err != nil {
				t.Fatal(err)
			}
			raw += int64(len(data))
		}
	}
	index := filepath.Join(t.TempDir(), "index")
	env := append(os.Environ(), "GIT_INDEX_FILE="+index)
	for _, args := range [][]string{{"read-tree", r.Base}, {"add", "--all", "--", "frontend/public/assets/learning/guides"}} {
		if _, err := git(t.Context(), r.WorkDir, env, nil, 4096, args...); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.CommandContext(t.Context(), "git", "diff", "--cached", "--binary", "--full-index", "--no-ext-diff", "--no-textconv", "--no-renames", r.Base, "--")
	cmd.Dir, cmd.Env = r.WorkDir, env
	var patchBytes guideByteCount
	hash := sha256.New()
	cmd.Stdout = io.MultiWriter(&patchBytes, hash)
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	_, captureErr := w.Capture(t.Context(), a)
	saved, loadErr := w.load(a.RunID)
	if raw != guidesmedia.AssetBytes || !errors.Is(captureErr, runner.ErrCapacity) || int64(patchBytes) <= int64(a.Limits.MaxArtifact) || loadErr != nil || saved.Capture == nil || saved.Capture.Tree == "" || saved.Capture.PatchSHA != "" || saved.Candidate != nil {
		t.Fatal("ordinary exact-asset boundary not established", raw, patchBytes, captureErr)
	}
	receipt := map[string]any{
		"at":                  time.Now().UTC(),
		"fixture":             "new model-free synthetic repository; no product source or tests",
		"manifest_sha256":     guidesmedia.ManifestSHA256,
		"exact_asset_files":   guidesmedia.AssetCount,
		"exact_asset_bytes":   raw,
		"original_base":       r.Base,
		"captured_full_tree":  saved.Capture.Tree,
		"binary_patch_bytes":  patchBytes,
		"binary_patch_sha256": hex.EncodeToString(hash.Sum(nil)),
		"artifact_cap":        a.Limits.MaxArtifact,
		"ordinary_capture":    captureErr.Error(),
		"later_stages":        "not reached: source capture refused; no candidate/pin/join/check/package accepted",
	}
	data, _ := json.MarshalIndent(receipt, "", "  ")
	file := "/Users/dan/.codex/jbg31/coordinator/mobile-qualification-20261111/guides-publication-software/ordinary-capture-feasibility.json"
	if err := os.WriteFile(file, append(data, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	t.Log(string(data))
}
