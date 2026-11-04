package release

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestImageBuildRequiresFreshExportedIdentity(t *testing.T) {
	for _, mode := range []string{"valid", "missing", "invalid", "symlink", "preexisting"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			idFile := filepath.Join(root, "image.id")
			id := "sha256:" + strings.Repeat("a", 64)
			if mode == "preexisting" {
				if err := os.WriteFile(idFile, []byte(id), 0600); err != nil {
					t.Fatal(err)
				}
			}
			calls := 0
			b := Live{}
			b.runCommand = func(_ context.Context, _ []byte, _ string, args ...string) ([]byte, error) {
				calls++
				if !slices.Contains(args, "--load") || !slices.Contains(args, "--iidfile") || !slices.Contains(args, idFile) {
					t.Fatal("build did not request an exported image identity", args)
				}
				switch mode {
				case "valid":
					return nil, os.WriteFile(idFile, []byte(id+"\n"), 0600)
				case "invalid":
					return nil, os.WriteFile(idFile, []byte("mutable:tag"), 0600)
				case "symlink":
					return nil, os.Symlink(filepath.Join(root, "private"), idFile)
				default:
					return nil, nil
				}
			}
			got, err := b.buildImage(t.Context(), root, "candidate:tag", idFile)
			if (err == nil) != (mode == "valid") || mode == "valid" && got != id || mode == "preexisting" && calls != 0 || calls > 1 {
				t.Fatal("unproven image accepted or build repeated", got, calls, err)
			}
		})
	}
}

func TestQualificationUsesExportedImageAndStillChecksRuntimeAndExecutable(t *testing.T) {
	for _, mode := range []string{"tag absent", "image absent", "wrong image", "wrong platform", "wrong user", "wrong executable"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "manifest.json"), []byte(`{}`), 0600); err != nil {
				t.Fatal(err)
			}
			id := "sha256:" + strings.Repeat("a", 64)
			server := []byte("reproduced executable")
			j := Job{
				Request: Request{RunID: "run", Key: "attempt"},
				Receipt: Receipt{Package: root, ServerSHA: Digest(server)},
			}
			reachedSourceCheck := errors.New("continue normal source qualification")
			builds, creates, sourceChecks := 0, 0, 0
			b := Live{Config: Config{Docker: "docker", SSH: "ssh"}}
			b.runCommand = func(_ context.Context, _ []byte, program string, args ...string) ([]byte, error) {
				if program == "ssh" {
					sourceChecks++
					return nil, reachedSourceCheck
				}
				switch args[2] {
				case "build":
					builds++
					return nil, os.WriteFile(filepath.Join(root, "image.id"), []byte(id), 0600)
				case "image":
					if args[3] != "inspect" || args[4] != id {
						t.Fatal("qualification depended on the mutable tag", args)
					}
					if mode == "image absent" {
						return nil, errors.New("exported image unavailable")
					}
					image, platform, user := id, "amd64", "65532:65532"
					switch mode {
					case "wrong image":
						image = "sha256:" + strings.Repeat("b", 64)
					case "wrong platform":
						platform = "arm64"
					case "wrong user":
						user = "0"
					}
					return json.Marshal([]any{map[string]any{
						"Id":           image,
						"Os":           "linux",
						"Architecture": platform,
						"Config":       map[string]string{"User": user},
					}})
				case "create":
					creates++
					if !slices.Contains(args, id) {
						t.Fatal("executable inspection used a different image", args)
					}
					return nil, nil
				case "cp":
					var output bytes.Buffer
					w := tar.NewWriter(&output)
					if mode == "wrong executable" {
						server = []byte("different executable")
					}
					if err := w.WriteHeader(&tar.Header{Name: "server", Mode: 0555, Size: int64(len(server))}); err != nil {
						t.Fatal(err)
					}
					if _, err := w.Write(server); err != nil {
						t.Fatal(err)
					}
					if err := w.Close(); err != nil {
						t.Fatal(err)
					}
					return output.Bytes(), nil
				case "inspect":
					return json.Marshal([]any{map[string]any{
						"Id":    "owned",
						"Image": id,
						"State": map[string]bool{"Running": false},
						"Config": map[string]any{
							"Labels": map[string]string{"cortexium.runner.run": "run"},
						},
					}})
				case "rm":
					return nil, nil
				default:
					t.Fatal("unexpected operation", args)
					return nil, nil
				}
			}
			err := b.qualifyCandidate(t.Context(), &j)
			if err == nil || j.Done || builds != 1 || (sourceChecks == 1) != (mode == "tag absent") || mode == "tag absent" && (!errors.Is(err, reachedSourceCheck) || creates != 1) {
				t.Fatal("image failure bypassed proof or repeated effects", mode, builds, creates, sourceChecks, err)
			}
		})
	}
}
