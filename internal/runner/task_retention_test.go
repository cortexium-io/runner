package runner_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
	"github.com/cortexium-io/runner/internal/runner/sqlite"
)

type retentionStore struct {
	runner.Store
	family           runner.FamilyStore
	fail             bool
	failManifest     bool
	manifestRejected chan struct{}
	once             sync.Once
}

func (s *retentionStore) Save(ctx context.Context, run runner.Run, artifacts []runner.Artifact, receipt *runner.Receipt) error {
	if s.failManifest && run.Task == nil {
		for _, group := range run.TaskGroups {
			for _, attempt := range run.Attempts {
				if group.Done && attempt.ID == group.AttemptID {
					s.once.Do(func() { close(s.manifestRejected) })
					return errors.New("synthetic manifest-result transaction failure")
				}
			}
		}
	}
	return s.Store.Save(ctx, run, artifacts, receipt)
}

func (s *retentionStore) HasExternalConsumers(ctx context.Context, ids []string) (bool, error) {
	return s.family.HasExternalConsumers(ctx, ids)
}
func (s *retentionStore) DeleteFamily(ctx context.Context, ids []string) error {
	if s.fail {
		return errors.New("synthetic family transaction failure")
	}
	return s.family.DeleteFamily(ctx, ids)
}

type retentionResources struct {
	*taskProbe
	mu          sync.Mutex
	failForget  bool
	forgetCalls int
}

func (p *retentionResources) ReleaseRun(context.Context, runner.Run) error { return nil }
func (p *retentionResources) ForgetRun(context.Context, string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.forgetCalls++
	if p.failForget {
		return errors.New("synthetic retired-source cleanup interruption")
	}
	return nil
}

func openRetention(t *testing.T, dir string, snapshot runner.Snapshot, executor runner.Executor, failure bool) (*rig, *retentionStore) {
	t.Helper()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	store, err := sqlite.Open(dir, snapshot.Instance.Storage)
	if err != nil {
		t.Fatal(err)
	}
	disk, err := runner.OpenDisk(dir, snapshot.Instance.Storage)
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	wrapped := &retentionStore{Store: store, family: store, fail: failure}
	core, err := runner.New(context.Background(), wrapped, executor, disk, snapshot)
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	return &rig{core: core, store: store, disk: disk}, wrapped
}

func collectionIntents(t *testing.T, dir string) []os.DirEntry {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(dir, "jobs", "family-retention"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	return entries
}

func completedRetentionFamily(t *testing.T, r *rig) runner.Run {
	t.Helper()
	root := startCards(t, r.core, threeCards)
	done := await(t, r.core, root.ID, func(run runner.Run) bool {
		if !released(run) || !run.ResourcesReleased {
			return false
		}
		for _, child := range run.TaskStatuses {
			if child.CleanupPending || child.Active {
				return false
			}
		}
		return true
	})
	if done.Status != "Completed" {
		t.Fatal(done.Status, done.Reason)
	}
	for _, child := range done.TaskStatuses {
		if child.CleanupPending || child.Active {
			t.Fatal("retention fixture child has unsettled resources", child)
		}
	}
	return done
}

func TestFamilyCollectionTransactionFailurePreservesEvidence(t *testing.T) {
	dir, s := t.TempDir(), plannedSnapshot(threeCards)
	s.Instance.Storage.KeepRuns = "1ns"
	p := &retentionResources{taskProbe: newTaskProbe()}
	close(p.allow)
	r, store := openRetention(t, dir, s, p, true)
	defer r.close()
	done := completedRetentionFamily(t, r)
	artifact := "artifacts/" + done.Outputs["manifest"].ArtifactID
	evidence, err := r.disk.Read(artifact, r.disk.Policy.MaxArtifact)
	if err != nil {
		t.Fatal(err)
	}
	before, err := r.store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err = r.core.Collect(); err == nil {
		t.Fatal("injected family transaction failure was hidden")
	}
	after, err := r.store.Load(t.Context())
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("failed database deletion changed retained history", err)
	}
	retained, err := r.disk.Read(artifact, r.disk.Policy.MaxArtifact)
	if err != nil || string(retained) != string(evidence) || p.forgetCalls != 0 || len(collectionIntents(t, dir)) != 1 {
		t.Fatal("transaction failure removed evidence or source metadata", err, p.forgetCalls)
	}
	store.fail = false
	if err = r.core.Collect(); err != nil {
		t.Fatal("same durable family intent did not reconcile", err)
	}
	if _, err = r.core.Get(done.ID); !errors.Is(err, runner.ErrNotFound) || len(collectionIntents(t, dir)) != 0 {
		t.Fatal("reconciled collection kept stale history/intent", err)
	}
}

func TestFamilyCollectionRestartCompletesCommittedCleanup(t *testing.T) {
	dir, s := t.TempDir(), plannedSnapshot(threeCards)
	s.Instance.Storage.KeepRuns = "1ns"
	p := &retentionResources{taskProbe: newTaskProbe(), failForget: true}
	close(p.allow)
	first, _ := openRetention(t, dir, s, p, false)
	done := completedRetentionFamily(t, first)
	artifact := "artifacts/" + done.Outputs["manifest"].ArtifactID
	if err := first.disk.Write("artifacts/outside-attempt/evidence", []byte("outside"), 1024); err != nil {
		t.Fatal(err)
	}
	if err := first.core.Collect(); err == nil {
		t.Fatal("interrupted retired-source cleanup was hidden")
	}
	if rows, err := first.store.Load(t.Context()); err != nil || len(rows) != 0 || first.core.Status().TotalRuns != 0 {
		t.Fatal("database commit did not retire family before cleanup", err, len(rows))
	}
	if _, err := first.disk.Read(artifact, first.disk.Policy.MaxArtifact); err != nil || len(collectionIntents(t, dir)) != 1 {
		t.Fatal("cleanup interruption lost ownership/evidence", err)
	}
	first.close()
	p.mu.Lock()
	p.failForget = false
	p.mu.Unlock()
	second, _ := openRetention(t, dir, s, p, false)
	defer second.close()
	if err := second.core.Collect(); err != nil {
		t.Fatal("restart failed to rejoin committed family cleanup", err)
	}
	if _, err := second.disk.Read(artifact, second.disk.Policy.MaxArtifact); !errors.Is(err, os.ErrNotExist) || len(collectionIntents(t, dir)) != 0 {
		t.Fatal("retired evidence/intent survived cleanup", err)
	}
	if data, err := second.disk.Read("artifacts/outside-attempt/evidence", 1024); err != nil || string(data) != "outside" {
		t.Fatal("family cleanup removed outside evidence", err)
	}
}

func TestFamilyCollectionRejectsMalformedIntentWithoutCleanup(t *testing.T) {
	for _, data := range []string{
		`{"root":"run_missing","runs":[{"id":"run_missing","attempts":["../outside"]}]}`,
		`{"root":"run_missing","runs":[{"id":"run_missing","attempts":["attempt_one","attempt_one"]}]}`,
		`{"root":"run_missing","runs":[{"id":"run_missing","attempts":[]}],"paths":["outside"]}`,
	} {
		t.Run(data, func(t *testing.T) {
			dir, s := t.TempDir(), snapshot(policy())
			p := &retentionResources{taskProbe: newTaskProbe()}
			r, _ := openRetention(t, dir, s, p, false)
			defer r.close()
			if err := r.disk.Write("jobs/family-retention/run_missing.json", []byte(data), r.disk.Policy.Database.MaxRecord); err != nil {
				t.Fatal(err)
			}
			if err := r.core.Collect(); err == nil || p.forgetCalls != 0 || len(collectionIntents(t, dir)) != 1 {
				t.Fatal("unknown intent granted cleanup authority", err)
			}
		})
	}
}

func TestFamilyCollectionRemovesUncommittedOwnedManifest(t *testing.T) {
	dir, s := t.TempDir(), plannedSnapshot(threeCards)
	s.Instance.Storage.KeepRuns = "1ns"
	p := &retentionResources{taskProbe: newTaskProbe()}
	r, store := openRetention(t, dir, s, p, false)
	defer r.close()
	store.failManifest = true
	store.manifestRejected = make(chan struct{})
	close(p.allow)
	root := startCards(t, r.core, threeCards)
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
	defer cancel()
	select {
	case <-store.manifestRejected:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	saved, err := r.core.Get(root.ID)
	if err != nil {
		t.Fatal(err)
	}
	group := saved.TaskGroups["cards"]
	artifact := "artifacts/" + group.AttemptID + "/results"
	if _, err = r.disk.Read(artifact, r.disk.Policy.MaxArtifact); err != nil {
		t.Fatal("fixture did not write an uncommitted group manifest", err)
	}
	if _, err = r.core.Cancel(root.ID); err != nil {
		t.Fatal(err)
	}
	await(t, r.core, root.ID, func(run runner.Run) bool { return released(run) && run.ResourcesReleased })
	if err = r.core.Collect(); err != nil {
		t.Fatal(err)
	}
	if _, err = r.disk.Read(artifact, r.disk.Policy.MaxArtifact); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("owned uncommitted manifest was orphaned", err)
	}
}
