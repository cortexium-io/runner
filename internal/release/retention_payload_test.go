package release

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var fixturePayloadNames = []string{
	"source.tar.gz",
	"context/server",
	"server-native",
	"importer/daily-import-linux-amd64",
	"importer/daily-import-native",
	"frontend_dist/index.html",
	"frontend_dist/assets/app.js",
	"ssr/entry.js",
	"ssr/assets/chunk.js",
}

var fixturePreservedNames = []string{
	"manifest.json",
	"reproduction.json",
	"held-out.json",
	"context/Dockerfile",
	"context/.dockerignore",
	"commands.log",
	"graphs/cost.svg",
	"metadata.json",
	"rehearsal-migrated.db",
	"runtime-http.json",
	"browser/receipt.json",
	"build-1.log",
	"commands.json",
	"build-inputs.json",
	"native-client-graph.json",
}

func payloadFixture(t *testing.T, b *Live, n int) Job {
	t.Helper()
	j := retainedFixture(t, b, n)
	manifestPath := filepath.Join(j.Receipt.Package, "manifest.json")
	data, err := os.ReadFile(manifestPath)
	var manifest packageManifest
	if err != nil || json.Unmarshal(data, &manifest) != nil {
		t.Fatal("fixture manifest unavailable", err)
	}
	for _, name := range append(append([]string{}, fixturePayloadNames[2:]...), fixturePreservedNames[3:]...) {
		data := []byte("manifest-bound " + name)
		path := filepath.Join(j.Receipt.Package, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		manifest.Artifacts[name] = Digest(data)
	}
	if err := writeJSON(manifestPath, manifest); err != nil {
		t.Fatal(err)
	}
	stamp := time.Unix(1_700_000_000+int64(n), 0)
	if err := os.Chtimes(j.Worker, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	return j
}

func payloadRetentionFixture(t *testing.T, count int) (*Live, []Job) {
	t.Helper()
	b := archiveFixture(t)
	b.Config.Delivery.Retention.BuildPayloads = 2
	jobs := make([]Job, count)
	for n := range jobs {
		jobs[n] = payloadFixture(t, b, n)
	}
	return b, jobs
}

func retainedEvidenceSnapshot(t *testing.T, b *Live, jobs []Job) map[string][]byte {
	t.Helper()
	snapshot := map[string][]byte{}
	for _, j := range jobs {
		paths := []string{j.Worker, filepath.Join(b.Config.StateDir, j.Request.Key+".json")}
		for _, name := range fixturePreservedNames {
			paths = append(paths, filepath.Join(j.Receipt.Package, name))
		}
		for _, path := range paths {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			snapshot[path] = data
		}
	}
	return snapshot
}

func assertEvidenceUnchanged(t *testing.T, snapshot map[string][]byte) {
	t.Helper()
	for path, before := range snapshot {
		data, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(data, before) {
			t.Fatal("retention changed owning history or non-payload evidence", path, err)
		}
	}
}

func assertPayloadPresent(t *testing.T, j Job, names []string, present bool) {
	t.Helper()
	for _, name := range names {
		_, err := os.Lstat(filepath.Join(j.Receipt.Package, name))
		if present && err != nil || !present && !errors.Is(err, os.ErrNotExist) {
			t.Fatal("build payload retention boundary differs", j.Request.Key, name, present, err)
		}
	}
}

func TestBuildPayloadRetentionOptInPreservesLegacyPolicyJSON(t *testing.T) {
	legacy := []byte(`{"raw_archives":2}`)
	for _, p := range []ArchiveRetention{{RawArchives: 2}, {RawArchives: 2, BuildPayloads: 0}} {
		data, err := json.Marshal(p)
		if err != nil || !bytes.Equal(data, legacy) || Digest(data) != Digest(legacy) {
			t.Fatal("disabled build retention changed legacy policy JSON/hash", string(data), err)
		}
	}
	for _, keep := range []int{-1, 0, 1, 2, 8, 9} {
		p := &ArchiveRetention{RawArchives: 2, BuildPayloads: keep}
		want := keep == 0 || keep >= 2 && keep <= 8
		if err := p.validate(); (err == nil) != want {
			t.Fatal("build payload retention opt-in range differs", keep, err)
		}
	}
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) {
			b := archiveFixture(t)
			if enabled {
				b.Config.Delivery.Retention.BuildPayloads = 2
			}
			old := payloadFixture(t, b, 0)
			payloadFixture(t, b, 1)
			payloadFixture(t, b, 2)
			before := retainedEvidenceSnapshot(t, b, []Job{old})
			if err := b.collectArchives(t.Context()); err != nil {
				t.Fatal(err)
			}
			assertPayloadPresent(t, old, []string{"source.tar.gz"}, false)
			assertPayloadPresent(t, old, fixturePayloadNames[1:], !enabled)
			assertEvidenceUnchanged(t, before)
		})
	}
}

func TestBuildPayloadRetentionProtectsReleaseAndRecoveryDependencies(t *testing.T) {
	b, jobs := payloadRetentionFixture(t, 8)
	for _, n := range []int{0, 1} {
		j := jobs[n]
		j.Request.Key, j.Receipt.Key = fmt.Sprintf("verified_%d", n), fmt.Sprintf("verified_%d", n)
		j.Request.Action, j.Receipt.Action, j.Failed = "verify", "verify", false
		saveRetainedJob(t, b, j)
	}
	for _, state := range []struct {
		n      int
		action string
		done   bool
	}{{2, "merge", false}, {3, "migrate", true}} {
		j := jobs[state.n]
		j.Request.Key, j.Receipt.Key = "protected_"+state.action, "protected_"+state.action
		j.Request.Action, j.Receipt.Action, j.Done = state.action, state.action, state.done
		saveRetainedJob(t, b, j)
	}
	before := retainedEvidenceSnapshot(t, b, jobs)
	if err := b.collectArchives(t.Context()); err != nil {
		t.Fatal(err)
	}
	for n, j := range jobs {
		assertPayloadPresent(t, j, fixturePayloadNames, n < 4 || n >= 6)
	}
	assertEvidenceUnchanged(t, before)
	if err := b.collectArchives(t.Context()); err != nil {
		t.Fatal("completed payload collection was not idempotent", err)
	}
	assertEvidenceUnchanged(t, before)
}

func TestBuildPayloadRetentionKeepsIndependentRawAndCompiledWindows(t *testing.T) {
	for _, keep := range []struct{ raw, compiled int }{{2, 3}, {3, 2}} {
		t.Run(fmt.Sprintf("raw%d_build%d", keep.raw, keep.compiled), func(t *testing.T) {
			b := archiveFixture(t)
			b.Config.Delivery.Retention.RawArchives = keep.raw
			b.Config.Delivery.Retention.BuildPayloads = keep.compiled
			jobs := make([]Job, 4)
			for n := range jobs {
				jobs[n] = payloadFixture(t, b, n)
			}
			before := retainedEvidenceSnapshot(t, b, jobs)
			for range 2 {
				if err := b.collectArchives(t.Context()); err != nil {
					t.Fatal("independent retention windows failed idempotent collection", err)
				}
				for n, j := range jobs {
					assertPayloadPresent(t, j, []string{"source.tar.gz"}, n >= len(jobs)-keep.raw)
					assertPayloadPresent(t, j, fixturePayloadNames[1:], n >= len(jobs)-keep.compiled)
				}
				assertEvidenceUnchanged(t, before)
			}
		})
	}
}

func TestBuildPayloadRetentionAuthenticatesPackagesAfterRawCollection(t *testing.T) {
	for _, receipt := range []bool{false, true} {
		t.Run(fmt.Sprint(receipt), func(t *testing.T) {
			b, jobs := payloadRetentionFixture(t, 3)
			old := jobs[0]
			if receipt {
				b.Config.Delivery.Retention.BuildPayloads = 0
				if err := b.collectArchives(t.Context()); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Remove(filepath.Join(old.Receipt.Package, "source.tar.gz")); err != nil {
				t.Fatal(err)
			}
			assertPayloadPresent(t, old, []string{"source.tar.gz"}, false)
			assertPayloadPresent(t, old, fixturePayloadNames[1:], true)
			b.Config.Delivery.Retention.BuildPayloads = 2
			before := retainedEvidenceSnapshot(t, b, jobs)
			if err := b.collectArchives(t.Context()); err != nil {
				t.Fatal("authenticated compiled package disappeared when raw archive was already absent", err)
			}
			assertPayloadPresent(t, old, fixturePayloadNames, false)
			data, err := os.ReadFile(filepath.Join(b.Config.StateDir, "build-payload-retention", old.Request.Key+".json"))
			var intent struct {
				ArchiveAlreadyAbsent bool `json:"archive_already_absent"`
			}
			if err != nil || json.Unmarshal(data, &intent) != nil || !intent.ArchiveAlreadyAbsent {
				t.Fatal("compiled retirement mislabeled already-absent raw archive", string(data), err)
			}
			assertEvidenceUnchanged(t, before)
		})
	}
}

func TestBuildPayloadRetentionPreflightsAllTargetsAndPreservesUnsafeState(t *testing.T) {
	for _, mode := range []string{
		"drift",
		"raw drift",
		"raw symlink",
		"target symlink",
		"tree symlink",
		"unmanifested payload",
		"unexplained missing",
		"missing proof",
		"proof symlink",
		"foreign proof",
		"foreign manifest",
		"unselected foreign manifest",
		"policy drift",
		"active",
		"busy",
		"receipt directory symlink",
		"cancelled",
	} {
		t.Run(mode, func(t *testing.T) {
			b, jobs := payloadRetentionFixture(t, 3)
			old := jobs[0]
			ctx := t.Context()
			sentinel := filepath.Join(t.TempDir(), "outside")
			if err := os.WriteFile(sentinel, []byte("foreign bytes remain"), 0600); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "drift":
				if err := os.WriteFile(filepath.Join(old.Receipt.Package, "ssr/assets/chunk.js"), []byte("drift"), 0600); err != nil {
					t.Fatal(err)
				}
			case "raw drift":
				if err := os.WriteFile(filepath.Join(old.Receipt.Package, "source.tar.gz"), []byte("drift"), 0600); err != nil {
					t.Fatal(err)
				}
			case "raw symlink":
				path := filepath.Join(old.Receipt.Package, "source.tar.gz")
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(sentinel, path); err != nil {
					t.Fatal(err)
				}
			case "target symlink":
				path := filepath.Join(old.Receipt.Package, "server-native")
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(sentinel, path); err != nil {
					t.Fatal(err)
				}
			case "tree symlink":
				path := filepath.Join(old.Receipt.Package, "frontend_dist")
				if err := os.Rename(path, path+"-preserved"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(path+"-preserved", path); err != nil {
					t.Fatal(err)
				}
			case "unexplained missing":
				if err := os.Remove(filepath.Join(old.Receipt.Package, "server-native")); err != nil {
					t.Fatal(err)
				}
			case "unmanifested payload":
				if err := os.WriteFile(filepath.Join(old.Receipt.Package, "frontend_dist/unrecorded.js"), []byte("unbound bytes"), 0600); err != nil {
					t.Fatal(err)
				}
			case "missing proof":
				if err := os.Remove(filepath.Join(old.Receipt.Package, "reproduction.json")); err != nil {
					t.Fatal(err)
				}
			case "foreign proof":
				if err := writeJSON(filepath.Join(old.Receipt.Package, "reproduction.json"), map[string]any{"status": "passed", "commit": strings.Repeat("c", 40), "serverSHA256": old.Receipt.ServerSHA}); err != nil {
					t.Fatal(err)
				}
			case "proof symlink":
				path := filepath.Join(old.Receipt.Package, "reproduction.json")
				if err := os.Rename(path, path+"-preserved"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(path+"-preserved", path); err != nil {
					t.Fatal(err)
				}
			case "foreign manifest", "unselected foreign manifest":
				data, err := os.ReadFile(filepath.Join(old.Receipt.Package, "manifest.json"))
				var m packageManifest
				if err != nil || json.Unmarshal(data, &m) != nil {
					t.Fatal(err)
				}
				name := "frontend_dist/../../outside"
				if mode == "unselected foreign manifest" {
					name = "../outside"
				}
				m.Artifacts[name] = Digest([]byte("foreign"))
				if err := writeJSON(filepath.Join(old.Receipt.Package, "manifest.json"), m); err != nil {
					t.Fatal(err)
				}
			case "policy drift":
				old.Receipt.Policy = strings.Repeat("0", 64)
				saveRetainedJob(t, b, old)
			case "active":
				old.Done = false
				saveRetainedJob(t, b, old)
			case "busy":
				lease, err := os.OpenFile(filepath.Join(filepath.Dir(old.Worker), "worker.lock"), os.O_RDWR, 0600)
				if err != nil || lockOperator(lease) != nil {
					t.Fatal("fixture lease unavailable", err)
				}
				t.Cleanup(func() { unlockOperator(lease); _ = lease.Close() })
			case "receipt directory symlink":
				if err := os.Symlink(t.TempDir(), filepath.Join(b.Config.StateDir, "build-payload-retention")); err != nil {
					t.Fatal(err)
				}
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			err := b.collectArchives(ctx)
			if mode != "missing proof" && mode != "active" && mode != "busy" && err == nil {
				t.Fatal("unsafe payload collection did not report a blocked preflight", mode)
			}
			if mode == "cancelled" && !errors.Is(err, context.Canceled) {
				t.Fatal("cancelled collector lost its context result", err)
			}
			// A late-sorted corrupt payload must prevent even earlier removals.
			assertPayloadPresent(t, old, []string{"context/server", "importer/daily-import-native"}, true)
			assertPayloadPresent(t, old, []string{"source.tar.gz"}, true)
			data, err := os.ReadFile(sentinel)
			if err != nil || string(data) != "foreign bytes remain" {
				t.Fatal("unsafe retention traversed a foreign target", err)
			}
			if _, err := os.Stat(filepath.Join(old.Receipt.Package, "held-out.json")); err != nil {
				t.Fatal("unsafe collection removed retained report", err)
			}
		})
	}
}

func ownedPayloadPackage(t *testing.T, b *Live, j Job) retainedPackage {
	t.Helper()
	packages, _, err := b.retainedPackages()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range packages {
		if p.Job.Request.Key == j.Request.Key {
			return p
		}
	}
	t.Fatal("fixture lost authenticated package", j.Request.Key)
	return retainedPackage{}
}

func preparePayloadIntent(t *testing.T, b *Live, j Job) string {
	t.Helper()
	p := ownedPayloadPackage(t, b, j)
	lease, err := os.OpenFile(filepath.Join(filepath.Dir(j.Worker), "worker.lock"), os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	if err = lockOperator(lease); err != nil {
		t.Fatal(err)
	}
	defer unlockOperator(lease)
	if _, err = b.preparePayloadRetention(t.Context(), p, true, true); err != nil {
		t.Fatal("payload preflight failed", err)
	}
	return filepath.Join(b.Config.StateDir, "build-payload-retention", j.Request.Key+".json")
}

func TestBuildPayloadRetentionRejoinsDurablePartialDeletion(t *testing.T) {
	b, jobs := payloadRetentionFixture(t, 3)
	old := jobs[0]
	before := retainedEvidenceSnapshot(t, b, jobs)
	intent := preparePayloadIntent(t, b, old)
	assertPayloadPresent(t, old, fixturePayloadNames, true)
	var receipt struct{ Complete bool }
	data, err := os.ReadFile(intent)
	if err != nil || json.Unmarshal(data, &receipt) != nil || receipt.Complete {
		t.Fatal("preflight did not persist an incomplete durable intent", string(data), err)
	}
	// Simulate a crash after one authorized unlink but before completion. A new
	// collector instance must use the existing intent rather than a new policy.
	if err = os.Remove(filepath.Join(old.Receipt.Package, "context/server")); err != nil {
		t.Fatal(err)
	}
	restarted := &Live{Config: b.Config}
	if err = restarted.collectArchives(t.Context()); err != nil {
		t.Fatal("collector failed to rejoin its own partial deletion", err)
	}
	assertPayloadPresent(t, old, fixturePayloadNames, false)
	assertEvidenceUnchanged(t, before)
	data, err = os.ReadFile(intent)
	if err != nil || json.Unmarshal(data, &receipt) != nil || !receipt.Complete {
		t.Fatal("retired payload has no completed intent", string(data), err)
	}
	completed := bytes.Clone(data)
	if err = restarted.collectArchives(t.Context()); err != nil {
		t.Fatal("completed retirement failed idempotent rejoin", err)
	}
	data, err = os.ReadFile(intent)
	if err != nil || !bytes.Equal(data, completed) {
		t.Fatal("completed immutable evidence was rewritten on rejoin", err)
	}
}

func TestBuildPayloadRetentionRejectsTamperedOrLinkedIntent(t *testing.T) {
	for _, mode := range []string{
		"identity",
		"receipt symlink",
		"oversized intent",
		"cancelled rejoin",
		"post-intent drift",
		"journal drift",
	} {
		t.Run(mode, func(t *testing.T) {
			b, jobs := payloadRetentionFixture(t, 3)
			old := jobs[0]
			path := preparePayloadIntent(t, b, old)
			ctx := t.Context()
			var sentinel string
			switch mode {
			case "identity":
				data, err := os.ReadFile(path)
				var m map[string]any
				if err != nil || json.Unmarshal(data, &m) != nil {
					t.Fatal(err)
				}
				m["package"] = filepath.Join(t.TempDir(), "foreign")
				if err = writeJSON(path, m); err != nil {
					t.Fatal(err)
				}
			case "receipt symlink":
				sentinel = filepath.Join(t.TempDir(), "unowned.json")
				if err := os.Rename(path, sentinel); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(sentinel, path); err != nil {
					t.Fatal(err)
				}
			case "oversized intent":
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(path, append(data, bytes.Repeat([]byte(" "), 32<<10)...), 0600); err != nil {
					t.Fatal(err)
				}
			case "cancelled rejoin":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "post-intent drift":
				if err := os.WriteFile(filepath.Join(old.Receipt.Package, "ssr/assets/chunk.js"), []byte("drift"), 0600); err != nil {
					t.Fatal(err)
				}
			case "journal drift":
				old.Reason = "journal changed after the durable preflight"
				saveRetainedJob(t, b, old)
			}
			intentBytes, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err = b.collectArchives(ctx); err == nil {
				t.Fatal("untrusted/cancelled retirement intent was adopted", mode)
			}
			assertPayloadPresent(t, old, fixturePayloadNames, true)
			data, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(data, intentBytes) {
				t.Fatal("unsafe retirement rewrote retained intent", err)
			}
			if sentinel != "" {
				info, err := os.Lstat(path)
				if err != nil || info.Mode()&os.ModeSymlink == 0 {
					t.Fatal("collector replaced an unowned receipt link", err)
				}
			}
		})
	}
}

func TestBuildPayloadRetirementForcesFreshPackageInsteadOfReuse(t *testing.T) {
	for _, removeRaw := range []bool{false, true} {
		t.Run(fmt.Sprint(removeRaw), func(t *testing.T) {
			b, jobs := payloadRetentionFixture(t, 1)
			old := jobs[0]
			p := ownedPayloadPackage(t, b, old)
			if err := b.removeBuildPayloads(t.Context(), p, removeRaw, true); err != nil {
				t.Fatal(err)
			}
			assertPayloadPresent(t, old, []string{"source.tar.gz"}, !removeRaw)
			assertPayloadPresent(t, old, fixturePayloadNames[1:], false)
			current := old
			current.Request.RunID, current.Receipt.RunID = "fresh_run", "fresh_run"
			current.Request.Key, current.Receipt.Key = "fresh_qualification", "fresh_qualification"
			root := filepath.Join(b.Config.Delivery.Root, current.Request.RunID, current.Request.Key)
			if err := os.MkdirAll(root, 0700); err != nil {
				t.Fatal(err)
			}
			current.Worker, current.Receipt.Package = filepath.Join(root, "work.json"), filepath.Join(root, "output")
			b.runCommand = func(context.Context, []byte, string, ...string) ([]byte, error) {
				t.Fatal("reuse issued an external command")
				return nil, nil
			}
			reused, err := b.reusePackage(t.Context(), &current)
			if err != nil || reused {
				t.Fatal("retired package was reused or blocks a fresh independent build", reused, err)
			}
			if _, err := os.Lstat(current.Receipt.Package); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("retired reuse created a partial package output", err)
			}
		})
	}
}

func phaseRetentionFixture(t *testing.T, raw, compiled int) (*Live, []Job, string) {
	t.Helper()
	b := archiveFixture(t)
	b.Config.Delivery.Retention.RawArchives = raw
	b.Config.Delivery.Retention.BuildPayloads = compiled
	jobs := make([]Job, 3)
	for n := range jobs {
		jobs[n] = payloadFixture(t, b, n)
	}
	if err := b.collectArchives(t.Context()); err != nil {
		t.Fatal("initial retention phase failed", err)
	}
	assertPayloadPresent(t, jobs[0], []string{"source.tar.gz"}, raw == 3)
	assertPayloadPresent(t, jobs[0], fixturePayloadNames[1:], compiled == 3)
	return b, jobs, filepath.Join(b.Config.StateDir, "build-payload-retention", jobs[0].Request.Key+".json")
}

func TestBuildPayloadRawOnlyIntentCannotExplainMissingCompiledBytes(t *testing.T) {
	for _, ageOut := range []bool{false, true} {
		t.Run(fmt.Sprint(ageOut), func(t *testing.T) {
			b, jobs, intentPath := phaseRetentionFixture(t, 2, 3)
			old := jobs[0]
			before, err := os.ReadFile(intentPath)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.Remove(filepath.Join(old.Receipt.Package, "context/server")); err != nil {
				t.Fatal(err)
			}
			if ageOut {
				payloadFixture(t, b, 3)
			}
			if err = b.collectArchives(t.Context()); err == nil {
				t.Fatal("raw-only intent authorized unexplained compiled absence", ageOut)
			}
			assertPayloadPresent(t, old, fixturePayloadNames[2:], true)
			data, err := os.ReadFile(intentPath)
			if err != nil || !bytes.Equal(data, before) {
				t.Fatal("invalid phase upgrade changed retained authority", err)
			}
		})
	}
}

func TestBuildPayloadRetentionUpgradesOnlyAfterNewPhasePreflight(t *testing.T) {
	for _, phase := range []struct {
		name          string
		raw, compiled int
		newTarget     string
	}{{"raw to compiled", 2, 3, "ssr/assets/chunk.js"}, {"compiled to raw", 3, 2, "source.tar.gz"}} {
		for _, drift := range []bool{false, true} {
			t.Run(phase.name+fmt.Sprint(drift), func(t *testing.T) {
				b, jobs, intentPath := phaseRetentionFixture(t, phase.raw, phase.compiled)
				old := jobs[0]
				before := retainedEvidenceSnapshot(t, b, jobs)
				intentBytes, err := os.ReadFile(intentPath)
				if err != nil {
					t.Fatal(err)
				}
				payloadFixture(t, b, 3) // The oldest package now leaves both windows.
				if drift {
					if err = os.WriteFile(filepath.Join(old.Receipt.Package, phase.newTarget), []byte("new phase drift"), 0600); err != nil {
						t.Fatal(err)
					}
					if err = b.collectArchives(t.Context()); err == nil {
						t.Fatal("new phase bypassed target hash verification")
					}
					data, readErr := os.ReadFile(intentPath)
					if readErr != nil || !bytes.Equal(data, intentBytes) {
						t.Fatal("failed preflight persisted expanded deletion authority", readErr)
					}
					assertPayloadPresent(t, old, []string{"source.tar.gz"}, phase.raw == 3)
					assertPayloadPresent(t, old, fixturePayloadNames[1:], phase.compiled == 3)
					return
				}
				// Exercise the durable-before-delete seam explicitly: all newly
				// selected targets remain present after authorization is saved.
				preparePayloadIntent(t, b, old)
				assertPayloadPresent(t, old, []string{"source.tar.gz"}, phase.raw == 3)
				assertPayloadPresent(t, old, fixturePayloadNames[1:], phase.compiled == 3)
				data, err := os.ReadFile(intentPath)
				var expanded payloadRetentionIntent
				if err != nil || json.Unmarshal(data, &expanded) != nil || !expanded.RawAuthorized || !expanded.CompiledAuthorized || expanded.Complete || bytes.Equal(data, intentBytes) {
					t.Fatal("new phase lacks durable explicit authority before unlink", string(data), err)
				}
				if err = b.collectArchives(t.Context()); err != nil {
					t.Fatal("valid phased retirement failed", err)
				}
				assertPayloadPresent(t, old, fixturePayloadNames, false)
				assertEvidenceUnchanged(t, before)
				data, err = os.ReadFile(intentPath)
				if err != nil || json.Unmarshal(data, &expanded) != nil || !expanded.Complete {
					t.Fatal("upgraded retirement did not complete", string(data), err)
				}
			})
		}
	}
}
