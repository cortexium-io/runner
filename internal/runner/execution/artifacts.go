package execution

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"

	"github.com/cortexium-io/runner/internal/runner/config"
	"github.com/cortexium-io/runner/internal/runner/core"
	"github.com/cortexium-io/runner/internal/securefs"
)

const artifactManifestLimit = 8 << 20

var artifactLimits = securefs.SnapshotLimits{MaxEntries: 10000, MaxFileBytes: 32 << 20, MaxTotalBytes: 256 << 20}

type checkArtifactManifest struct {
	CheckID      string            `json:"check_id"`
	SourceDigest string            `json:"source_digest"`
	OutputDigest string            `json:"output_digest"`
	Files        map[string]string `json:"files"`
}

func artifactDirectory(check core.CheckObservation) (string, error) {
	if !filepath.IsAbs(check.Evidence) || filepath.Clean(check.Evidence) != check.Evidence ||
		!config.RelativePath(check.CheckID) || filepath.Base(check.CheckID) != check.CheckID {
		return "", errors.New("invalid retained check identity")
	}
	return filepath.Join(filepath.Dir(check.Evidence), check.CheckID+"-artifacts"), nil
}

func artifactBytes(path string, limit int64) ([]byte, error) {
	data, _, state, err := securefs.ReadFile(path, limit)
	if err == nil {
		err = securefs.ValidateOwnedRegularFile(state, uint32(os.Geteuid()))
	}
	return data, err
}

func artifactHash(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func verifyArtifactLog(check core.CheckObservation) error {
	if !check.Stopped || (check.Status != "passed" && check.Status != "failed") || check.OutputDigest == "" {
		return errors.New("check artifacts require a stopped, observed check")
	}
	data, err := artifactBytes(check.Evidence, 8<<20)
	if err != nil {
		return err
	}
	if artifactHash(data) != check.OutputDigest {
		return errors.New("check log changed before artifact inspection")
	}
	return nil
}

// RetainCheckArtifacts runs only after source verification and process settlement.
// It copies approved outputs out of the disposable workspace, never grants that
// workspace, and leaves partial captures private and unreferenced on failure.
func RetainCheckArtifacts(source string, paths []string, sourceDigest string, check core.CheckObservation) (*core.CheckArtifacts, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	if err := config.ValidateArtifactPaths(paths); err != nil {
		return nil, err
	}
	if sourceDigest == "" {
		return nil, errors.New("check artifacts lack a source binding")
	}
	if err := verifyArtifactLog(check); err != nil {
		return nil, err
	}
	destination, err := artifactDirectory(check)
	if err != nil {
		return nil, err
	}
	if err = securefs.EnsurePrivateDir(filepath.Dir(destination)); err != nil {
		return nil, err
	}
	if err = os.Mkdir(destination, 0700); err != nil {
		return nil, err
	}
	copyFile := func(name string, data []byte) error {
		path := filepath.Join(destination, name)
		if err := securefs.EnsurePrivateDir(filepath.Dir(path)); err != nil {
			return err
		}
		return securefs.WriteFileExclusive(path, data, 0600)
	}
	files, err := scanArtifacts(source, paths, artifactLimits, copyFile)
	if err != nil {
		return nil, err
	}
	after, err := scanArtifacts(source, paths, artifactLimits, nil)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(files, after) {
		return nil, errors.New("check artifacts changed during retention")
	}
	manifest := checkArtifactManifest{check.CheckID, sourceDigest, check.OutputDigest, files}
	data, err := json.Marshal(manifest)
	if err != nil {
		return nil, err
	}
	if len(data) > artifactManifestLimit {
		return nil, errors.New("check artifact manifest exceeds limit")
	}
	if err = securefs.WriteFileExclusive(filepath.Join(destination, "manifest.json"), data, 0600); err != nil {
		return nil, err
	}
	check.Artifacts = &core.CheckArtifacts{Directory: destination, SourceDigest: sourceDigest, ManifestDigest: artifactHash(data)}
	if _, err = CheckReadRoots(check, sourceDigest); err != nil {
		return nil, err
	}
	return check.Artifacts, nil
}

// CheckReadRoots verifies the closed set of retained bytes before granting only
// the log and private artifact bundle. Call again after the role returns.
func CheckReadRoots(check core.CheckObservation, sourceDigest string) ([]string, error) {
	roots := []string{}
	if check.Audit != nil {
		if check.Audit.SourceDigest == "" || (sourceDigest != "" && sourceDigest != check.Audit.SourceDigest) {
			return nil, errors.New("audit assessment source binding changed")
		}
		if err := VerifyAuditReceipt(check.Audit.Receipt); err != nil {
			return nil, err
		}
		roots = append(roots, check.Audit.Receipt.Report)
	}
	if check.Evidence != "" {
		roots = append(roots, check.Evidence)
	}
	if check.Artifacts == nil {
		return roots, nil
	}
	dir, err := artifactDirectory(check)
	if err != nil {
		return nil, err
	}
	if dir != check.Artifacts.Directory || sourceDigest == "" || sourceDigest != check.Artifacts.SourceDigest {
		return nil, errors.New("retained check artifact location or source binding changed")
	}
	if err = verifyArtifactLog(check); err != nil {
		return nil, err
	}
	data, err := artifactBytes(filepath.Join(dir, "manifest.json"), artifactManifestLimit)
	if err != nil {
		return nil, err
	}
	if artifactHash(data) != check.Artifacts.ManifestDigest {
		return nil, errors.New("retained check artifact manifest changed")
	}
	var manifest checkArtifactManifest
	if err = config.Decode(data, &manifest); err != nil {
		return nil, err
	}
	if manifest.CheckID != check.CheckID || manifest.SourceDigest != sourceDigest || manifest.OutputDigest != check.OutputDigest || len(manifest.Files) == 0 {
		return nil, errors.New("retained artifact manifest does not match the check/source")
	}
	files, err := scanArtifacts(dir, nil, artifactLimits, nil)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(files, manifest.Files) {
		return nil, errors.New("retained check artifacts changed")
	}
	return append(roots, dir), nil
}

// A nil selection scans a retained bundle, omitting only its root manifest.
// Pinned directory/file reads reject symlinks, hardlinks and unsafe ownership;
// names are budgeted before collection and contents before copying.
func scanArtifacts(root string, paths []string, limits securefs.SnapshotLimits, visit func(string, []byte) error) (map[string]string, error) {
	budget, err := securefs.NewSnapshotBudget(limits)
	if err != nil {
		return nil, err
	}
	files := map[string]string{}
	var total int64
	file := func(directory *securefs.Directory, name, relative string) error {
		if err := budget.AddEntry(filepath.Join(root, relative)); err != nil {
			return err
		}
		data, _, state, err := directory.ReadFile(name, min(limits.MaxFileBytes, limits.MaxTotalBytes-total+1))
		if err != nil {
			return err
		}
		if err = securefs.ValidateOwnedRegularFile(state, uint32(os.Geteuid())); err != nil {
			return err
		}
		total += int64(len(data))
		if total > limits.MaxTotalBytes {
			return errors.New("check artifacts exceed aggregate byte limit")
		}
		files[relative] = artifactHash(data)
		if visit != nil {
			return visit(relative, data)
		}
		return nil
	}
	var walk func(*securefs.Directory, string) error
	walk = func(directory *securefs.Directory, relative string) error {
		names, err := directory.ReadDirNamesWithBudget(budget)
		if err != nil {
			return err
		}
		for _, name := range names {
			if paths == nil && relative == "" && name == "manifest.json" {
				continue
			}
			path := filepath.Join(relative, name)
			info, err := os.Lstat(filepath.Join(root, path))
			if err != nil {
				return err
			}
			if info.IsDir() {
				child, err := directory.OpenDir(name)
				if err != nil {
					return err
				}
				err = walk(child, path)
				_ = child.Close()
				if err != nil {
					return err
				}
			} else if err = file(directory, name, path); err != nil {
				return err
			}
		}
		return directory.Verify()
	}
	if paths == nil {
		directory, err := securefs.OpenDir(root)
		if err != nil {
			return nil, err
		}
		defer directory.Close()
		err = walk(directory, "")
		return files, err
	}
	for _, path := range paths {
		full := filepath.Join(root, path)
		if err := config.NoSymlinks(full); err != nil {
			return nil, err
		}
		info, err := os.Lstat(full)
		if err != nil {
			return nil, fmt.Errorf("declared artifact %s unavailable: %w", path, err)
		}
		before := len(files)
		if info.IsDir() {
			directory, err := securefs.OpenDir(full)
			if err != nil {
				return nil, err
			}
			err = walk(directory, path)
			_ = directory.Close()
			if err != nil {
				return nil, err
			}
		} else {
			directory, err := securefs.OpenDir(filepath.Dir(full))
			if err != nil {
				return nil, err
			}
			err = file(directory, filepath.Base(full), path)
			_ = directory.Close()
			if err != nil {
				return nil, err
			}
		}
		if len(files) == before {
			return nil, fmt.Errorf("declared artifact %s contains no files", path)
		}
	}
	return files, nil
}
