package release

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/cortexium-io/runner/internal/saas"
)

// A fixed per-operation maximum: one decoded copy context, one serialized
// viewport artifact and16MiB conservative journal/command costs. This is a
// completion reservation, not an increase of any storage/output allowance.
// Runtime images
// and engine-owned tmpfs are separate, explicitly checked resource domains.
const cortexiumLocalWorkBudget = int64(saas.MaxBuildDecoded + saas.MaxBundle + 16<<20)

var cortexiumDiagnostic = regexp.MustCompile(`^(diagnostic|build)-[0-9a-f]{16}\.(log|stdout\.log|stdout\.json)$`)

func cortexiumOwnedLocalFile(name string) bool {
	if cortexiumDiagnostic.MatchString(name) {
		return true
	}
	switch name {
	case "input-request.json", "input-request.json.partial", "input.tar.gz", "input.tar.gz.partial", "upload.lock", "worker.lock", "work.json", "result.json", "stop", "started", "integrity-lost", "resources-intent", "resources-settled", "owned-image", "owned-anchor", "owned-app", "owned-browser", "owned-builder", "owned-verify", "storage-peak.json", "engine-capacity.json", "partial-export.json", "evidence.tar.gz", "image/server", "image/manifest.json":
		return true
	case "copy-readback.json", "cleanup.json", "cleanup-started", "resource-usage.json", "app-diagnostics.json", "app-diagnostics.private.log":
		return true
	}
	return false
}
func sameCortexiumRequest(a, b Request) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}

func (b *Live) cortexiumCapacity() (int64, error) { return b.cortexiumCapacityFor(nil) }
func (b *Live) cortexiumCapacityFor(incoming *Request) (int64, error) {
	var used, reserved int64
	sizes := map[string]int64{}
	err := filepath.WalkDir(b.Config.StateDir, func(name string, entry os.DirEntry, err error) error {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			used += info.Size()
			sizes[name] = info.Size()
		} else if !info.IsDir() {
			return errors.New("foreign link in Cortexium storage")
		}
		return nil
	})
	if err != nil {
		return used, err
	}
	policy, _ := json.Marshal(b.Config)
	op := Operator{Project: b.Config.Project, PolicyHash: Digest(policy), Cortexium: true}
	root := b.Config.workRoot()
	runs, err := os.ReadDir(root)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return used, err
	}
	foundIncoming := false
	seen := map[string]bool{}
	for _, run := range runs {
		if !run.IsDir() || !safeID.MatchString(run.Name()) {
			return used, errors.New("foreign Cortexium run directory")
		}
		attempts, err := os.ReadDir(filepath.Join(root, run.Name()))
		if err != nil {
			return used, err
		}
		for _, attempt := range attempts {
			if !attempt.IsDir() || !safeID.MatchString(attempt.Name()) {
				return used, errors.New("foreign Cortexium attempt directory")
			}
			dir := filepath.Join(root, run.Name(), attempt.Name())
			data, err := readFile(filepath.Join(dir, "input-request.json"), 32<<10)
			if errors.Is(err, os.ErrNotExist) {
				entries, e := os.ReadDir(dir)
				if e != nil {
					return used, e
				}
				for _, entry := range entries {
					if entry.Name() != "input-request.json.partial" && entry.Name() != "upload.lock" {
						return used, errors.New("Cortexium operation lost its committed reservation intent")
					}
				}
				continue
			}
			if err != nil {
				return used, err
			}
			var q Request
			if json.Unmarshal(data, &q) != nil || !op.validCortexiumRequest(q) || q.Cortexium.Transport != "" || q.RunID != run.Name() || q.Key != attempt.Name() || seen[q.Key] {
				return used, errors.New("Cortexium reservation custody differs")
			}
			seen[q.Key] = true
			if incoming != nil && incoming.Key == q.Key {
				if !sameCortexiumRequest(q, *incoming) {
					return used, errors.New("Cortexium incoming identity differs from its reserved operation")
				}
				foundIncoming = true
			}
			var materialized, inputBytes, contextBytes, metadataBytes int64
			for name, size := range sizes {
				rel, err := filepath.Rel(dir, name)
				if err != nil || strings.HasPrefix(rel, "../") || rel == ".." {
					continue
				}
				if !cortexiumOwnedLocalFile(filepath.ToSlash(rel)) {
					return used, errors.New("foreign Cortexium operation file")
				}
				switch filepath.ToSlash(rel) {
				case "input.tar.gz":
					if size != q.Cortexium.Bytes {
						return used, errors.New("Cortexium retained input size differs")
					}
					inputBytes += size
				case "input.tar.gz.partial":
					inputBytes += size
				case "image/server", "image/manifest.json":
					contextBytes += size
				case "evidence.tar.gz":
					if size > saas.MaxBundle {
						return used, errors.New("Cortexium evidence exceeds its fixed bound")
					}
				default:
					metadataBytes += size
				}
				materialized += size
			}
			for _, suffix := range []string{".json", ".lock"} {
				s := sizes[filepath.Join(b.Config.StateDir, q.Key+suffix)]
				materialized += s
				metadataBytes += s
			}
			if inputBytes > q.Cortexium.Bytes || contextBytes > saas.MaxBuildDecoded || metadataBytes > 16<<20 {
				return used, errors.New("Cortexium local component exceeds its completion reservation")
			}
			budget := q.Cortexium.Bytes + cortexiumLocalWorkBudget
			if materialized > budget {
				return used, errors.New("Cortexium operation exceeded its fixed local footprint")
			}
			settled := false
			if jobData, e := readFile(filepath.Join(b.Config.StateDir, q.Key+".json"), 32<<10); e == nil {
				var job Job
				if json.Unmarshal(jobData, &job) != nil || !sameCortexiumRequest(job.Request, q) || job.Receipt.Policy != op.PolicyHash {
					return used, errors.New("Cortexium settled reservation identity differs")
				}
				settled = job.Done
				if settled {
					used += int64(len(jobData)) - sizes[filepath.Join(b.Config.StateDir, q.Key+".json")]
				}
			} else if !errors.Is(e, os.ErrNotExist) {
				return used, e
			}
			if !settled {
				reserved += budget - materialized
			}
		}
	}
	if incoming != nil && !foundIncoming {
		if !op.validCortexiumRequest(*incoming) || incoming.Cortexium.Transport != "" {
			return used, errors.New("invalid Cortexium incoming reservation")
		}
		reserved += incoming.Cortexium.Bytes + cortexiumLocalWorkBudget
	}
	ancestor := b.Config.StateDir
	for {
		if _, err := os.Stat(ancestor); !errors.Is(err, os.ErrNotExist) {
			break
		}
		ancestor = filepath.Dir(ancestor)
	}
	free, err := deliveryFree(ancestor)
	if err != nil {
		return used, err
	}
	if used+reserved > b.Config.Cortexium.StorageLimit || free < b.Config.Cortexium.MinFree+reserved {
		return used, errors.New("Cortexium unfinished local completion reservations do not fit unchanged1GiB cap/reserve")
	}
	return used, nil
}
