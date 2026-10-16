package backup

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner/config"
	"github.com/cortexium-io/runner/internal/runner/core"
	"github.com/cortexium-io/runner/internal/runner/execution"
	"github.com/cortexium-io/runner/internal/runner/store"
	"github.com/cortexium-io/runner/internal/runner/workspace"
)

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

type fixture struct {
	root, path, bundle string
	settings           config.Settings
	task               core.Task
}

func setup(t *testing.T) fixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	must(t, err)
	f := fixture{root: root, path: filepath.Join(root, "config.json"), bundle: filepath.Join(root, "backup")}
	repo := filepath.Join(root, "repo")
	must(t, os.Mkdir(repo, 0700))
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
		b, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git: %s %v", b, err)
		}
	}
	git("init", "-b", "main")
	git("config", "user.name", "Test")
	git("config", "user.email", "test@example.invalid")
	must(t, os.WriteFile(filepath.Join(repo, "greeting.txt"), []byte("before\n"), 0600))
	git("add", ".")
	git("commit", "-m", "baseline")
	cfg := config.Config{Format: "runner-local-v1", Repository: config.Repository{Path: repo, BaseRef: "refs/heads/main", BaseFreshness: "recorded"}, StateDir: filepath.Join(root, "state"), WorktreeDir: filepath.Join(root, "worktrees"), Profiles: map[string]core.Profile{"build": {Harness: "codex", Model: "controlled", Reasoning: "high", Timeout: "1m"}}, Roles: map[string]string{"implementer": "build", "reviewer": "build"}, Policy: config.Policy{MaxParallelism: 1, MaxRepairs: 2}}
	cfg.Delivery.Mode = "github-draft"
	cfg.Delivery.Repository = "test/fixture"
	must(t, cfg.Validate())
	must(t, os.Mkdir(cfg.WorktreeDir, 0700))
	b, err := json.Marshal(cfg)
	must(t, err)
	must(t, os.WriteFile(f.path, b, 0600))
	id, base, err := workspace.Observe(t.Context(), cfg)
	must(t, err)
	f.settings = config.Settings{Config: cfg, RepositoryID: id, Quality: config.Quality{Definition: "Exact greeting", Base: base}}
	db, err := store.Create(t.Context(), cfg.StateDir, f.settings)
	must(t, err)
	defer db.Close()
	f.task = core.Task{ID: "one", Revision: 1, Generation: 1, Stage: core.Reviewing, Versions: []core.Version{{Number: 1, Body: "Greeting", Digest: core.Digest("Greeting"), Quality: f.settings.Quality.Revision()}}, Approval: &core.Approval{Preview: "approved", Base: base, Generation: 1}, Usage: core.Usage{Invocations: 3, Repairs: 1, Escalations: 1, BaseRefreshes: 1}}
	g := workspace.Git{Settings: f.settings}
	w, err := g.Prepare(t.Context(), f.task)
	must(t, err)
	must(t, os.WriteFile(filepath.Join(w.WorktreePath, "greeting.txt"), []byte("hello\n"), 0600))
	digest, head, err := g.Binding(t.Context(), f.task)
	must(t, err)
	a := core.Attempt{ID: "saved", TaskID: f.task.ID, Version: 1, Generation: 1, Base: base, Role: "implementer", State: "applied", EvidenceDir: filepath.Join(cfg.StateDir, "attempts", "saved"), ExecutionDir: w.WorktreePath, WorkspaceClaim: w.WorktreePath, SourceDigest: digest, SourceHead: head, Result: &core.Result{Outcome: "succeeded", Summary: "Greeting implemented"}}
	must(t, os.MkdirAll(a.EvidenceDir, 0700))
	must(t, os.WriteFile(filepath.Join(a.EvidenceDir, "result.json"), []byte("retained result evidence"), 0600))
	candidate, err := g.Construct(t.Context(), f.task, a)
	must(t, err)
	f.task.Candidate = &candidate
	f.task.Review = []core.Check{{ID: "greeting", Status: "pass", Evidence: "exact bytes"}}
	f.task.Stage = core.AwaitingDelivery
	f.task.BaseProof = &core.BaseProof{Base: base, Candidate: candidate, Quality: f.settings.Quality.Revision(), ReviewDigest: core.Digest(f.task.Review), ObservedAt: time.Now().UTC()}
	must(t, db.Add(t.Context(), f.task))
	must(t, db.Commit(t.Context(), 1, f.task, &a, false))
	f.task, err = db.Task(t.Context(), f.task.ID)
	must(t, err)
	remote := core.RemotePublication{Number: 1, URL: "https://example.invalid/1", BranchCommit: candidate.Commit, State: "OPEN", Draft: false}
	p := core.Publication{Intent: core.PublicationIntent{ID: "publication", TaskID: f.task.ID, Version: 1, Generation: 1, ApprovedBase: base, Quality: f.settings.Quality.Revision(), ReviewDigest: core.Digest(f.task.Review), Candidate: candidate, Repository: cfg.Delivery.Repository, BaseBranch: "main", Branch: "runner/one"}, State: "published", CreateRequested: true, Observation: &remote, CI: &core.CIAssessment{Proof: *f.task.BaseProof, State: "passed", Observation: &core.CIObservation{Remote: remote}}}
	must(t, db.SavePublication(t.Context(), p, false))
	p.Revision++
	p.IntegrationRequest = &core.IntegrationRequest{Merge: true, Proof: *f.task.BaseProof, CIDigest: core.Digest(p.CI)}
	p.Occupying = true
	must(t, db.SavePublication(t.Context(), p, false))
	p.Revision++
	p.MergeDispatch = &core.MergeDispatch{Request: *p.IntegrationRequest, RequestedAt: time.Now().UTC(), Admission: core.MergeAdmission{Eligible: true, PullRequestID: "PR", ProtectionRuleID: "RULE"}}
	must(t, db.SavePublication(t.Context(), p, false))
	p.Revision++
	p.Occupying = false
	p.IntegrationRequest = nil
	p.State = "attention"
	p.Detail = "merge response lost; inspect only"
	must(t, db.SavePublication(t.Context(), p, false))
	must(t, db.SaveQualityReport(t.Context(), config.QualityReport{ID: "report", RepositoryID: id, Quality: f.settings.Quality, Files: map[string]string{"greeting.txt": "original"}}))
	return f
}
func (f fixture) snapshot(t *testing.T) Manifest {
	t.Helper()
	report, err := Create(t.Context(), f.path, f.bundle)
	must(t, err)
	if report.InspectionOnly {
		t.Fatal("new backup misreports freshness")
	}
	m, _, err := inspect(t.Context(), f.bundle, f.path)
	must(t, err)
	return m
}
func vacate(t *testing.T, m Manifest) {
	t.Helper()
	for _, p := range m.Roots {
		must(t, os.Rename(p, p+".original"))
	}
}
func writable(t *testing.T, f fixture) *store.Store {
	t.Helper()
	db, err := store.Open(t.Context(), f.settings.Config.StateDir, false)
	must(t, err)
	return db
}

func TestRoundTripPreservesAuthorityAndConsumedEffects(t *testing.T) {
	f := setup(t)
	// A newer default must not replace the accepted candidate's pinned policy.
	db := writable(t, f)
	retainPartialPlan(t, f, db)
	must(t, db.ApplyQuality(t.Context(), f.settings.Quality.Revision(), config.Quality{Definition: "Future work", Sources: map[string]string{"future-policy.md": "future"}}))
	must(t, db.Close())
	before, err := store.ReadImage(t.Context(), f.settings.Config.StateDir)
	must(t, err)
	must(t, os.Symlink(filepath.Join(f.root, "outside"), filepath.Join(f.settings.Config.Repository.Path, "reference")))
	must(t, os.WriteFile(filepath.Join(f.root, "outside"), []byte("not followed"), 0600))
	m := f.snapshot(t)
	blocked, err := Restore(t.Context(), f.bundle, f.path, false)
	must(t, err)
	if len(blocked.Blockers) == 0 || len(blocked.Destinations) != len(m.Roots) {
		t.Fatal("preview hides occupied destinations", blocked)
	}
	// Inspection must not consume the continuity proof.
	ro, err := store.Open(t.Context(), m.State, true)
	must(t, err)
	_, err = ro.Tasks(t.Context())
	must(t, err)
	must(t, ro.Close())
	vacate(t, m)
	preview, err := Restore(t.Context(), f.bundle, f.path, false)
	must(t, err)
	if preview.Applied || preview.InspectionOnly {
		t.Fatal("unchanged snapshot not resumable", preview)
	}
	for _, p := range m.Roots {
		if _, err = os.Lstat(p); !os.IsNotExist(err) {
			t.Fatal("preview wrote destination", p)
		}
	}
	applied, err := Restore(t.Context(), f.bundle, f.path, true)
	must(t, err)
	if !applied.Applied || applied.InspectionOnly {
		t.Fatal(applied)
	}
	after, err := store.ReadImage(t.Context(), m.State)
	must(t, err)
	if !after.Drained || after.Settings.RepositoryID != before.Settings.RepositoryID || after.Settings.PhysicalRepository() == before.Settings.PhysicalRepository() {
		t.Fatal("logical/physical binding or drain lost")
	}
	after.Settings.Restoration = before.Settings.Restoration
	if !reflect.DeepEqual(before, after) {
		t.Fatal("task, policy, quality, results or delivery records changed")
	}
	db = writable(t, f)
	settings, _, err := db.Settings(t.Context())
	must(t, err)
	must(t, db.Close())
	settings.Quality = f.settings.Quality
	must(t, (workspace.Git{Settings: settings}).ValidateCandidate(t.Context(), f.task))
	// The ordinary identity guard must still reject another checkout replacement.
	common := filepath.Join(f.settings.Config.Repository.Path, ".git")
	must(t, os.Rename(common, common+".old"))
	_, err = scan(t.Context(), common+".old", common, 0, false)
	must(t, err)
	if (workspace.Git{Settings: settings}).ValidateCandidate(t.Context(), f.task) == nil {
		t.Fatal("ordinary replacement bypassed physical binding")
	}
}

func TestLaterHistoryOrLostGuardCannotRestoreWritePermission(t *testing.T) {
	for _, change := range []string{"external effect and revocation", "guard missing", "guard copied"} {
		t.Run(change, func(t *testing.T) {
			f := setup(t)
			if change == "external effect and revocation" {
				db := writable(t, f)
				second := core.Task{ID: "later", Revision: 1, Generation: 1, Stage: core.Ready, NextRole: "implementer", Versions: f.task.Versions, Approval: f.task.Approval}
				must(t, db.Add(t.Context(), second))
				must(t, db.Close())
			}
			m := f.snapshot(t)
			guard := store.GuardPath(m.State)
			switch change {
			case "external effect and revocation":
				db := writable(t, f)
				// Model an external adapter effect after writable admission but before its
				// result commit. Even that crash window must invalidate the earlier image.
				must(t, os.WriteFile(filepath.Join(f.root, "remote-effect"), []byte("merged once"), 0600))
				task, err := db.Task(t.Context(), "later")
				must(t, err)
				task.Stage = core.Stopped
				task.Generation++
				task.Usage.Invocations++
				must(t, db.Commit(t.Context(), task.Revision, task, nil, false))
				must(t, db.Close())
			case "guard missing":
				must(t, os.Remove(guard))
			case "guard copied":
				b, err := os.ReadFile(guard)
				must(t, err)
				must(t, os.Rename(guard, guard+".original"))
				must(t, os.WriteFile(guard, b, 0600))
			}
			vacate(t, m)
			p, err := Restore(t.Context(), f.bundle, f.path, false)
			must(t, err)
			if !p.InspectionOnly {
				t.Fatal("unknown later history accepted")
			}
			p, err = Restore(t.Context(), f.bundle, f.path, true)
			must(t, err)
			if !p.InspectionOnly || !p.Applied {
				t.Fatal(p)
			}
			db, err := store.Open(t.Context(), m.State, true)
			must(t, err)
			task, err := db.Task(t.Context(), f.task.ID)
			must(t, err)
			if !reflect.DeepEqual(task, f.task) {
				t.Fatal("historical task was rewritten")
			}
			if change == "external effect and revocation" {
				historical, err := db.Task(t.Context(), "later")
				must(t, err)
				if historical.Stage != core.Ready || historical.Approval == nil {
					t.Fatal("history was rewritten instead of fenced")
				}
			}
			if err = db.AllowMutation(t.Context()); err == nil {
				t.Fatal("config save allowed on held authority")
			}
			must(t, db.Close())
			if db, err = store.Open(t.Context(), m.State, false); err == nil {
				db.Close()
				t.Fatal("stale restore regained write authority")
			}
		})
	}
}

func TestBackupRefusesUnsettledOrMissingInputs(t *testing.T) {
	for _, bad := range []string{"open command", "coordinator", "not drained", "local write", "reservation", "process", "container", "baseline process", "candidate", "evidence"} {
		t.Run(bad, func(t *testing.T) {
			f := setup(t)
			cfg := f.settings.Config
			switch bad {
			case "open command":
				db, err := store.Open(t.Context(), cfg.StateDir, true)
				must(t, err)
				defer db.Close()
			case "coordinator":
				lock, err := execution.Lock(filepath.Join(cfg.StateDir, "coordinator.lock"))
				must(t, err)
				defer lock.Close()
			case "local write":
				db := writable(t, f)
				task, err := db.Task(t.Context(), f.task.ID)
				must(t, err)
				task.RefWrites = []core.RefWrite{{ID: "unsettled", State: "dispatched"}}
				must(t, db.Commit(t.Context(), task.Revision, task, nil, false))
				must(t, db.Close())
			case "not drained":
				db := writable(t, f)
				must(t, db.SetDrained(t.Context(), false))
				must(t, db.Close())
			case "reservation", "process", "container":
				db := writable(t, f)
				as, err := db.Attempts(t.Context(), f.task.ID)
				must(t, err)
				a := as[0]
				if bad == "reservation" {
					a.Occupying = true
				} else {
					a.Process = &core.Process{PID: syscall.Getpgrp(), Identity: "unresolved"}
					if bad == "container" {
						a.Process = &core.Process{PID: 2147483647, Identity: "gone", Container: "runner-check-" + core.NewID(), DockerHost: "unix:///runner-deliberately-missing-docker.sock"}
					}
				}
				must(t, db.Commit(t.Context(), f.task.Revision, f.task, &a, false))
				must(t, db.Close())
			case "baseline process":
				dir := filepath.Join(cfg.StateDir, "quality", "unfinished")
				must(t, os.MkdirAll(dir, 0700))
				b, err := json.Marshal(core.Process{PID: syscall.Getpgrp(), Identity: "unresolved"})
				must(t, err)
				must(t, os.WriteFile(filepath.Join(dir, "launch-process.json"), b, 0600))
			case "candidate":
				must(t, os.Remove(filepath.Join(f.task.Candidate.Workspace, "greeting.txt")))
			case "evidence":
				must(t, os.RemoveAll(filepath.Join(cfg.StateDir, "attempts", "saved")))
			}
			if _, err := Create(t.Context(), f.path, f.bundle); err == nil {
				t.Fatal("unsafe backup accepted", bad)
			}
			if _, err := os.Lstat(filepath.Join(f.bundle, "manifest.json")); !os.IsNotExist(err) {
				t.Fatal("failed backup published manifest")
			}
		})
	}
}

func TestRestoreRejectsInvalidBundlesAndOccupiedDestinations(t *testing.T) {
	for _, bad := range []string{"manifest missing", "manifest corrupt", "binary", "format", "missing evidence", "corrupt database", "occupied", "ownership"} {
		t.Run(bad, func(t *testing.T) {
			f := setup(t)
			m := f.snapshot(t)
			switch bad {
			case "manifest missing":
				must(t, os.Remove(filepath.Join(f.bundle, "manifest.json")))
			case "manifest corrupt":
				must(t, os.WriteFile(filepath.Join(f.bundle, "manifest.json"), []byte("{"), 0600))
			case "binary", "format":
				if bad == "binary" {
					m.Binary = "different"
				} else {
					m.StoreFormat--
				}
				b, err := json.Marshal(envelope{Manifest: m, Digest: core.Digest(m)})
				must(t, err)
				must(t, os.WriteFile(filepath.Join(f.bundle, "manifest.json"), b, 0600))
			case "missing evidence":
				p, err := mapped(m, f.bundle, filepath.Join(m.State, "attempts", "saved", "result.json"))
				must(t, err)
				must(t, os.Remove(p))
			case "corrupt database":
				p, err := mapped(m, f.bundle, filepath.Join(m.State, "runner.db"))
				must(t, err)
				must(t, os.WriteFile(p, []byte("broken"), 0600))
			}
			vacate(t, m)
			if bad == "occupied" {
				must(t, os.WriteFile(f.path, []byte("unrelated"), 0600))
			}
			if bad == "ownership" {
				g, err := store.LockGuard(m.State, false, false)
				must(t, err)
				defer g.Close()
			}
			if _, err := Restore(t.Context(), f.bundle, f.path, true); err == nil {
				t.Fatal("unsafe restore accepted", bad)
			}
			if _, err := os.Lstat(m.State); !os.IsNotExist(err) {
				t.Fatal("invalid restore exposed authority")
			}
			if bad == "occupied" {
				b, err := os.ReadFile(f.path)
				must(t, err)
				if string(b) != "unrelated" {
					t.Fatal("unrelated destination changed")
				}
			}
		})
	}
}

func TestInterruptedRestoreLeavesNoUsableAuthorityAndDoesNotOverwrite(t *testing.T) {
	f := setup(t)
	m := f.snapshot(t)
	vacate(t, m)
	interrupted := errors.New("controlled interruption")
	_, err := restore(t.Context(), f.bundle, f.path, true, func(path string) error {
		if path == m.State {
			return interrupted
		}
		return nil
	})
	if !errors.Is(err, interrupted) {
		t.Fatal(err)
	}
	if _, err = os.Lstat(m.State); !os.IsNotExist(err) {
		t.Fatal("authority installed before other roots")
	}
	if _, err = Restore(t.Context(), f.bundle, f.path, true); err == nil || !strings.Contains(err.Error(), "must be absent") {
		t.Fatal("partial install overwritten", err)
	}
	for _, root := range m.Roots {
		if _, err = os.Lstat(root); err == nil {
			must(t, os.Rename(root, root+".partial"))
		}
	}
	p, err := Restore(t.Context(), f.bundle, f.path, true)
	must(t, err)
	if !p.InspectionOnly {
		t.Fatal("interrupted restore retained activation permission")
	}
	// Race a new destination into the final install boundary. Atomic rename must
	// refuse it rather than replace it, even when it is an empty directory.
	from, to := filepath.Join(f.root, "from"), filepath.Join(f.root, "to")
	must(t, os.Mkdir(from, 0700))
	must(t, os.Mkdir(to, 0700))
	must(t, os.WriteFile(filepath.Join(from, "ours"), []byte("data"), 0600))
	if err = install(from, to); err == nil {
		t.Fatal("exclusive install replaced an existing directory")
	}
}

func TestCancelledBackupHasNoCompletionMarker(t *testing.T) {
	f := setup(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := Create(ctx, f.path, f.bundle); err == nil {
		t.Fatal("cancelled backup succeeded")
	}
	if _, err := os.Stat(filepath.Join(f.bundle, "manifest.json")); !os.IsNotExist(err) {
		t.Fatal("cancelled backup published marker")
	}
}

func TestInterruptedActivationLeavesInspectionOnlyState(t *testing.T) {
	f := setup(t)
	m := f.snapshot(t)
	vacate(t, m)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	_, err := restore(ctx, f.bundle, f.path, true, func(path string) error {
		if path == m.State {
			cancel()
		}
		return nil
	})
	if err == nil {
		t.Fatal("cancelled activation succeeded")
	}
	db, err := store.Open(t.Context(), m.State, true)
	must(t, err)
	settings, drained, err := db.Settings(t.Context())
	must(t, err)
	must(t, db.Close())
	if !drained || settings.Restoration == nil || !settings.Restoration.InspectionOnly {
		t.Fatal("interrupted activation left usable authority")
	}
	if db, err = store.Open(t.Context(), m.State, false); err == nil {
		db.Close()
		t.Fatal("interrupted authority writable")
	}
}

// Extend the existing archive round-trip with private inclusion while a sibling
// is still pending. Lifecycle construction is tested by the engine journeys.
func retainPartialPlan(t *testing.T, f fixture, db *store.Store) {
	t.Helper()
	parent := core.Task{ID: "partial-plan", Generation: 1, Stage: core.Ready, PlanRequest: true, Versions: []core.Version{{Number: 1, Body: "Combined outcome", Quality: f.settings.Quality.Revision()}}, Approval: &core.Approval{Generation: 1, Base: f.task.Base()}, Release: &core.PlanRelease{Preview: "release", Children: []string{"included", "pending"}}, PlanHead: f.task.Base()}
	g := workspace.Git{Settings: f.settings}
	must(t, g.ApplyRef(t.Context(), core.RefWrite{Kind: "plan", Ref: parent.PlanRef(), Commit: parent.PlanHead}))
	must(t, db.Add(t.Context(), parent))
	for _, id := range parent.Release.Children {
		child := core.Task{ID: id, ParentID: parent.ID, Generation: 1, Stage: core.Ready, Versions: []core.Version{{Number: 1, Body: "Greeting", Digest: core.Digest(id), Quality: f.settings.Quality.Revision()}}, Approval: &core.Approval{Generation: 1, Base: f.task.Base()}, NextRole: "implementer"}
		if id == "included" {
			m, err := g.Prepare(t.Context(), child)
			must(t, err)
			must(t, os.WriteFile(filepath.Join(m.WorktreePath, "greeting.txt"), []byte("included greeting\n"), 0600))
			digest, _, err := g.Binding(t.Context(), child)
			must(t, err)
			c, err := g.Construct(t.Context(), child, core.Attempt{SourceDigest: digest})
			must(t, err)
			child.Candidate = &c
			child.Review = []core.Check{{ID: "change", Status: "pass", Evidence: "Exact included file"}}
			child.Stage, child.NextRole = core.AwaitingDelivery, ""
			child.BaseProof = &core.BaseProof{Base: child.Base(), Candidate: c, Quality: child.Current().Quality, ReviewDigest: core.Digest(child.Review), ObservedAt: time.Now().UTC()}
			w := core.RefWrite{ID: "inclusion", Kind: "inclusion", TaskID: id, Version: 1, Generation: 1, Release: parent.Release.Preview, Ref: parent.PlanRef(), Old: parent.PlanHead, Commit: c.Commit, Proof: child.BaseProof, State: "confirmed", ObservedAt: time.Now().UTC()}
			must(t, g.ApplyRef(t.Context(), w))
			child.RefWrites = []core.RefWrite{w}
			child.Inclusion = &core.PlanInclusion{WriteID: w.ID, Release: w.Release, Version: 1, Generation: 1, Previous: w.Old, Candidate: c}
			parent.PlanHead = c.Commit
			must(t, db.Commit(t.Context(), parent.Revision, parent, nil, false))
			parent.Revision++
		}
		must(t, db.Add(t.Context(), child))
	}
}
