package execution

import (
	"bytes"
	"encoding/json"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/cortexium-io/runner/internal/runner/core"
	"github.com/cortexium-io/runner/internal/securefs"
)

func artifactFixture(t *testing.T, root string) (string, core.CheckObservation) {
	t.Helper()
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "check")
	reports := filepath.Join(source, ".runner-output", "reports")
	if err = os.MkdirAll(reports, 0700); err != nil {
		t.Fatal(err)
	}
	var picture bytes.Buffer
	if err = png.Encode(&picture, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{"shot.png": picture.Bytes(), "report.json": []byte(`{"image":"shot.png"}`)} {
		if err = os.WriteFile(filepath.Join(reports, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err = os.Mkdir(filepath.Join(root, "evidence"), 0700); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(root, "evidence", "browser.log")
	data := []byte("browser check finished\n")
	if err = os.WriteFile(log, data, 0600); err != nil {
		t.Fatal(err)
	}
	return source, core.CheckObservation{Directory: source, CheckID: "browser", Status: "passed", Stopped: true, Evidence: log, OutputDigest: artifactHash(data)}
}

func TestRetainedCheckArtifactsAreClosedAndSourceBound(t *testing.T) {
	for _, change := range []string{"none", "source", "location", "missing", "bytes", "extra", "manifest", "log", "symlink", "hardlink"} {
		t.Run(change, func(t *testing.T) {
			source, check := artifactFixture(t, t.TempDir())
			if err := os.WriteFile(filepath.Join(source, ".runner-output", "unselected.txt"), []byte("private output"), 0600); err != nil {
				t.Fatal(err)
			}
			binding := core.Digest("candidate")
			var err error
			check.Artifacts, err = RetainCheckArtifacts(source, []string{".runner-output/reports"}, binding, check)
			if err != nil {
				t.Fatal(err)
			}
			shot := filepath.Join(check.Artifacts.Directory, ".runner-output/reports/shot.png")
			switch change {
			case "source":
				binding = core.Digest("different candidate")
			case "location":
				check.Artifacts.Directory = source
			case "missing":
				err = os.Remove(shot)
			case "bytes":
				err = os.WriteFile(shot, []byte("replaced"), 0600)
			case "extra":
				err = os.WriteFile(filepath.Join(check.Artifacts.Directory, "extra.txt"), []byte("not captured"), 0600)
			case "manifest":
				err = os.WriteFile(filepath.Join(check.Artifacts.Directory, "manifest.json"), []byte("{}"), 0600)
			case "log":
				err = os.WriteFile(check.Evidence, []byte("relabelled"), 0600)
			case "symlink", "hardlink":
				if err = os.Remove(shot); err == nil {
					if change == "symlink" {
						err = os.Symlink(check.Evidence, shot)
					} else {
						err = os.Link(filepath.Join(source, ".runner-output/reports/shot.png"), shot)
					}
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			// The original workspace is neither required nor a read grant.
			if change == "none" {
				if err = os.RemoveAll(source); err != nil {
					t.Fatal(err)
				}
			}
			roots, err := CheckReadRoots(check, binding)
			if (err == nil) != (change == "none") {
				t.Fatal(change, roots, err)
			}
			if change == "none" {
				if !slices.Equal(roots, []string{check.Evidence, check.Artifacts.Directory}) || slices.Contains(roots, source) {
					t.Fatal("unexpected read grant", roots)
				}
				data, err := os.ReadFile(shot)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = png.Decode(bytes.NewReader(data)); err != nil {
					t.Fatal("PNG not retained", err)
				}
				data, err = os.ReadFile(filepath.Join(check.Artifacts.Directory, "manifest.json"))
				var manifest checkArtifactManifest
				if err != nil || json.Unmarshal(data, &manifest) != nil || len(manifest.Files) != 2 {
					t.Fatal("selection included unrelated output", string(data), err)
				}
			}
		})
	}
}

func TestArtifactCaptureRejectsUnavailableAndUnsafeOutputs(t *testing.T) {
	for _, change := range []string{"missing", "empty", "symlink ancestor", "symlink file", "hardlink", "unsettled", "unavailable"} {
		t.Run(change, func(t *testing.T) {
			source, check := artifactFixture(t, t.TempDir())
			reports := filepath.Join(source, ".runner-output/reports")
			switch change {
			case "unsettled":
				check.Stopped = false
			case "unavailable":
				check.Status = "unavailable"
			case "missing", "empty", "symlink ancestor":
				if err := os.RemoveAll(reports); err != nil {
					t.Fatal(err)
				}
				if change == "empty" {
					if err := os.Mkdir(reports, 0700); err != nil {
						t.Fatal(err)
					}
				} else if change == "symlink ancestor" {
					if err := os.Symlink(filepath.Dir(check.Evidence), reports); err != nil {
						t.Fatal(err)
					}
				}
			case "symlink file", "hardlink":
				path := filepath.Join(reports, "alias")
				var err error
				if change == "symlink file" {
					err = os.Symlink(check.Evidence, path)
				} else {
					err = os.Link(filepath.Join(reports, "shot.png"), path)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			if receipt, err := RetainCheckArtifacts(source, []string{".runner-output/reports"}, "candidate", check); err == nil || receipt != nil {
				t.Fatal("unsafe capture accepted", receipt, err)
			}
		})
	}
}

func TestArtifactSnapshotBudgets(t *testing.T) {
	source, _ := artifactFixture(t, t.TempDir())
	for _, limits := range []securefs.SnapshotLimits{
		{MaxEntries: 1, MaxFileBytes: 1000, MaxTotalBytes: 2000},
		{MaxEntries: 100, MaxFileBytes: 1, MaxTotalBytes: 2000},
		{MaxEntries: 100, MaxFileBytes: 1000, MaxTotalBytes: 1000},
	} {
		// The last budget uses sufficiently large files to overflow only aggregate.
		if err := os.WriteFile(filepath.Join(source, ".runner-output/reports/a"), bytes.Repeat([]byte("a"), 600), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(source, ".runner-output/reports/b"), bytes.Repeat([]byte("b"), 600), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := scanArtifacts(source, []string{".runner-output/reports"}, limits, nil); err == nil {
			t.Fatal("budget ignored", limits)
		}
	}
}
