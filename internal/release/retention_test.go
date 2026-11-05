package release

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func archiveFixture(t *testing.T) *Live {
	t.Helper()
	dir := t.TempDir()
	b := &Live{Config: Config{
		Project:    "demo",
		Repository: "example/demo",
		StateDir:   filepath.Join(dir, "operator"),
		Delivery:   &DeliveryConfig{Root: filepath.Join(dir, "delivery"), Retention: &ArchiveRetention{RawArchives: 2}},
	}}
	for _, dir := range []string{b.Config.StateDir, b.Config.Delivery.Root} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	return b
}

func retainedFixture(t *testing.T, b *Live, n int) Job {
	t.Helper()
	key, run := fmt.Sprintf("package_%02d", n), fmt.Sprintf("run_%02d", n)
	root := filepath.Join(b.Config.Delivery.Root, run, key)
	output := filepath.Join(root, "output")
	if err := os.MkdirAll(filepath.Join(output, "context"), 0700); err != nil {
		t.Fatal(err)
	}
	head, base := strings.Repeat("a", 40), strings.Repeat("b", 40)
	server := []byte("independently reproduced server")
	j := Job{
		Request: Request{
			Project:   b.Config.Project,
			RunID:     run,
			Key:       key,
			Action:    "qualify",
			Source:    base,
			Candidate: head,
		},
		Receipt: Receipt{
			Key:       key,
			RunID:     run,
			Action:    "qualify",
			Head:      head,
			Base:      base,
			Source:    base,
			PR:        3,
			Package:   output,
			ServerSHA: Digest(server),
		},
		Worker: filepath.Join(root, "work.json"),
		Done:   true,
		Failed: true,
	}
	policy, _ := json.Marshal(b.Config)
	j.Receipt.Policy = Digest(policy)
	manifest := packageManifest{
		Revision:  head,
		Clean:     true,
		Identity:  "source-sha256:" + Digest([]byte("source")),
		Sources:   map[string]string{"file.go": Digest([]byte("source"))},
		Artifacts: map[string]string{},
	}
	for name, data := range map[string][]byte{"source.tar.gz": []byte("frozen source " + key), "context/server": server} {
		if err := os.WriteFile(filepath.Join(output, name), data, 0755); err != nil {
			t.Fatal(err)
		}
		manifest.Artifacts[name] = Digest(data)
	}
	for file, data := range map[string]any{
		j.Worker: deliveryWork{Config: b.Config, Job: j},
		filepath.Join(b.Config.StateDir, key+".json"): j,
		filepath.Join(output, "manifest.json"):        manifest,
		filepath.Join(output, "reproduction.json"):    map[string]string{"status": "passed", "commit": head, "serverSHA256": Digest(server)},
		filepath.Join(output, "held-out.json"):        map[string]string{"status": "retained qualification report"},
	} {
		if err := writeJSON(file, data); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "worker.lock"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	stamp := time.Unix(1_700_000_000+int64(n), 0)
	for _, path := range []string{filepath.Join(output, "source.tar.gz"), filepath.Join(b.Config.StateDir, key+".json")} {
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	return j
}

func saveRetainedJob(t *testing.T, b *Live, j Job) {
	t.Helper()
	if err := writeJSON(filepath.Join(b.Config.StateDir, j.Request.Key+".json"), j); err != nil {
		t.Fatal(err)
	}
}

func TestArchiveRetentionKeepsHistoryReportsAndReleaseDependencies(t *testing.T) {
	b := archiveFixture(t)
	var jobs []Job
	for n := range 8 {
		jobs = append(jobs, retainedFixture(t, b, n))
	}
	// Current and prior successful releases must survive even when older than
	// the ordinary recent-archive window. A pending release also retains source.
	for _, n := range []int{0, 1} {
		j := jobs[n]
		j.Request.Key, j.Receipt.Key = fmt.Sprintf("verified_%d", n), fmt.Sprintf("verified_%d", n)
		j.Request.Action, j.Receipt.Action, j.Failed = "verify", "verify", false
		saveRetainedJob(t, b, j)
	}
	pending := jobs[2]
	pending.Request.Key, pending.Receipt.Key = "pending_merge", "pending_merge"
	pending.Request.Action, pending.Receipt.Action, pending.Done = "merge", "merge", false
	saveRetainedJob(t, b, pending)
	if err := b.collectArchives(t.Context()); err != nil {
		t.Fatal(err)
	}
	for n, j := range jobs {
		_, err := os.Stat(filepath.Join(j.Receipt.Package, "source.tar.gz"))
		want := n < 3 || n >= 6
		if (err == nil) != want {
			t.Fatal("archive retention lost a dependency or retained a superseded copy", n, err)
		}
		for _, file := range []string{j.Worker, filepath.Join(b.Config.StateDir, j.Request.Key+".json"), filepath.Join(j.Receipt.Package, "held-out.json"), filepath.Join(j.Receipt.Package, "context/server")} {
			if _, err := os.Stat(file); err != nil {
				t.Fatal("retention removed owning history or non-raw evidence", file, err)
			}
		}
		if !want {
			data, err := os.ReadFile(filepath.Join(b.Config.StateDir, "archive-retention", j.Request.Key+".json"))
			var receipt struct {
				Complete bool `json:"complete"`
			}
			if err != nil || json.Unmarshal(data, &receipt) != nil || !receipt.Complete {
				t.Fatal("collected archive has no completed deletion receipt", err)
			}
		}
	}
	if err := b.collectArchives(t.Context()); err != nil {
		t.Fatal("collection was not idempotent", err)
	}
}

func TestArchiveRetentionPreservesUnprovenOrActivelyOwnedBytes(t *testing.T) {
	for _, mode := range []string{"unsettled", "busy lease", "archive changed", "archive symlink", "attempt symlink", "foreign manifest", "missing reproduction", "policy changed", "receipt directory symlink"} {
		t.Run(mode, func(t *testing.T) {
			b := archiveFixture(t)
			j := retainedFixture(t, b, 0)
			retainedFixture(t, b, 1)
			retainedFixture(t, b, 2)
			archive := filepath.Join(j.Receipt.Package, "source.tar.gz")
			wantError := true
			switch mode {
			case "unsettled":
				j.Done = false
				saveRetainedJob(t, b, j)
				wantError = false
			case "busy lease":
				lease, err := os.OpenFile(filepath.Join(filepath.Dir(j.Worker), "worker.lock"), os.O_RDWR, 0600)
				if err != nil || lockOperator(lease) != nil {
					t.Fatal("fixture lease unavailable", err)
				}
				defer lease.Close()
				wantError = false
			case "archive changed":
				os.WriteFile(archive, []byte("uncaptured bytes"), 0600)
				old := time.Unix(1_700_000_000, 0)
				os.Chtimes(archive, old, old)
			case "archive symlink":
				sentinel := filepath.Join(t.TempDir(), "private")
				os.WriteFile(sentinel, []byte("keep"), 0600)
				os.Remove(archive)
				os.Symlink(sentinel, archive)
			case "attempt symlink":
				root := filepath.Dir(j.Worker)
				os.Rename(root, root+"-retained")
				os.Symlink(root+"-retained", root)
			case "foreign manifest":
				writeJSON(filepath.Join(j.Receipt.Package, "manifest.json"), packageManifest{Clean: true, Revision: "other"})
			case "missing reproduction":
				os.Remove(filepath.Join(j.Receipt.Package, "reproduction.json"))
				wantError = false
			case "policy changed":
				j.Receipt.Policy = strings.Repeat("0", 64)
				saveRetainedJob(t, b, j)
			case "receipt directory symlink":
				os.Symlink(t.TempDir(), filepath.Join(b.Config.StateDir, "archive-retention"))
			}
			err := b.collectArchives(t.Context())
			if (err != nil) != wantError {
				t.Fatal("unsafe retention condition was misclassified", err)
			}
			if _, err := os.Lstat(archive); err != nil {
				t.Fatal("retention removed unproven or actively owned source", err)
			}
		})
	}
}

func TestSameCandidateReuseCopiesOnlyBoundBuildArtifacts(t *testing.T) {
	for _, mode := range []string{"reuse", "different candidate", "different runtime", "retention upgrade", "artifact changed"} {
		t.Run(mode, func(t *testing.T) {
			b := archiveFixture(t)
			origin := retainedFixture(t, b, 0)
			current := origin
			current.Request.Key, current.Receipt.Key = "new_qualification", "new_qualification"
			current.Request.RunID, current.Receipt.RunID = "new_run", "new_run"
			root := filepath.Join(b.Config.Delivery.Root, current.Request.RunID, current.Request.Key)
			os.MkdirAll(root, 0700)
			current.Worker, current.Receipt.Package = filepath.Join(root, "work.json"), filepath.Join(root, "output")
			switch mode {
			case "different candidate":
				current.Receipt.Head = strings.Repeat("c", 40)
			case "different runtime":
				b.Config.Delivery.Go = "/other/runtime/go"
			case "retention upgrade":
				b.Config.Delivery.Retention = &ArchiveRetention{RawArchives: 3}
			case "artifact changed":
				os.WriteFile(filepath.Join(origin.Receipt.Package, "context/server"), []byte("unverified executable"), 0700)
			}
			reused, err := b.reusePackage(t.Context(), &current)
			if mode == "artifact changed" {
				if err == nil {
					t.Fatal("reused an artifact that differs from the independently reproduced package")
				}
				return
			}
			want := mode == "reuse" || mode == "retention upgrade"
			if err != nil || reused != want {
				t.Fatal("same-candidate build reuse boundary differs", reused, err)
			}
			if !want {
				return
			}
			for _, name := range []string{"source.tar.gz", "context/server", "manifest.json", "package-reuse.json"} {
				if _, err := os.Stat(filepath.Join(current.Receipt.Package, name)); err != nil {
					t.Fatal("reused package is not self-contained", name, err)
				}
			}
			for _, name := range []string{"reproduction.json", "held-out.json"} {
				if _, err := os.Stat(filepath.Join(current.Receipt.Package, name)); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("reuse copied execution proof that must run independently", name, err)
				}
			}
			// Collection of the origin cannot break this new self-contained copy.
			if err := os.Remove(filepath.Join(origin.Receipt.Package, "source.tar.gz")); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(current.Receipt.Package, "source.tar.gz")); err != nil {
				t.Fatal("reuse depends on an earlier disposable archive", err)
			}
		})
	}
}
