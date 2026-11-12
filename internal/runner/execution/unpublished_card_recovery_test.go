package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
)

func unpublishedSourceFixture(t *testing.T) (*taskSourceFixture, runner.Assignment, runner.Run, runner.UnpublishedCardAssessment) {
	return unpublishedSourceFixturePins(t, 1)
}

func unpublishedSourceFixturePins(t *testing.T, count int) (*taskSourceFixture, runner.Assignment, runner.Run, runner.UnpublishedCardAssessment) {
	t.Helper()
	f := newTaskSourceFixture(t, true)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := f.service.Close(ctx); err != nil {
			t.Error("owned fixture service cleanup", err)
		}
	})
	var pins []runner.TaskCandidate
	for i := range count {
		var dependencies []runner.TaskCandidate
		if len(pins) > 0 {
			dependencies = pins[len(pins)-1:]
		}
		pinSeed, pin := pinReleasedTaskAttempts(t, f, fmt.Sprintf("accepted%d", i), fmt.Sprintf("accepted %d\n", i), dependencies...)
		if err := f.service.workspaces.ReleaseRun(t.Context(), pinSeed.RunID); err != nil {
			t.Fatal(err)
		}
		pins = append(pins, *pin)
	}
	dependencies := pins
	if len(dependencies) > 5 {
		dependencies = dependencies[:5]
	}
	a := f.seed(t, "recover", "run_recover", []string{"shared.txt"}, dependencies...)
	source := f.edit(t, a, "late", map[string]string{"shared.txt": "captured unreviewed\n"})
	if err := f.service.Release(t.Context(), a.AttemptID); err != nil {
		t.Fatal(err)
	}
	if err := f.service.workspaces.ReleaseAttempt(t.Context(), runner.Assignment{RunID: a.RunID, AttemptID: source.AttemptID}); err != nil {
		t.Fatal(err)
	}
	if err := f.service.workspaces.ReleaseRun(t.Context(), a.RunID); err != nil {
		t.Fatal(err)
	}
	r := runner.Run{ID: a.RunID, Repository: f.repo, Source: a.Source, ResourcesReleased: true}
	proof := runner.UnpublishedCardAssessment{Source: *source, Authority: *a.TaskSource, Pins: pins}
	ids := []string{source.PatchID, source.InputPatchID}
	for _, pin := range pins {
		ids = append(ids, pin.PatchID)
	}
	for _, id := range ids {
		raw, err := f.disk.Read("artifacts/"+id, f.disk.Policy.MaxArtifact)
		if err != nil {
			t.Fatal(err)
		}
		proof.Artifacts = append(proof.Artifacts, runner.CardRecoveryArtifact{ID: id, Bytes: int64(len(raw)), SHA256: captureDigest(raw)})
	}
	return f, a, r, proof
}

func TestUnpublishedCardSevenPinsFitDispatchObservationAndNativeRecord(t *testing.T) {
	f, a, _, proof := unpublishedSourceFixturePins(t, 7)
	a.AttemptID = "attempt_qualified_recovery_owner"
	// Match the admitted process's existing record envelope; the general
	// task-source fixture defaults to a smaller sixteen-KiB document.
	a.Limits.MaxRecord = 40 << 10
	a.Setup.Profile = runner.Profile{Harness: "codex", Access: "workspace-write", MaxOutput: 4096}
	a.UnpublishedCardRecovery = &proof
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	start := time.Now()
	if err := f.service.validateRecoveredCardStart(ctx, a); err != nil {
		t.Fatal(err)
	}
	job, err := json.Marshal(nativeJob{Assignment: a, Stage: "allocated"})
	if err != nil || len(job)+int(a.Setup.Profile.MaxOutput)+2048 > int(a.Limits.MaxRecord) {
		t.Fatal("bound proof cannot fit ordinary native record", len(job), err)
	}
	t.Logf("seven pins/five dependencies dispatch validation=%s native assignment=%d record limit=%d", time.Since(start), len(job), a.Limits.MaxRecord)
}

func TestUnpublishedCardSourceAuthenticatesRestoresAndPinsNormally(t *testing.T) {
	f, a, r, proof := unpublishedSourceFixture(t)
	before, err := f.service.workspaces.load(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	pinBefore, err := f.service.workspaces.load(proof.Pins[0].RunID)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.service.ValidateUnpublishedCardRecovery(t.Context(), r, proof); err != nil {
		t.Fatal(err)
	}
	after, _ := f.service.workspaces.load(r.ID)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("preflight changed workspace metadata")
	}
	if _, err = os.Lstat(before.WorkDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("preflight restored checkout", err)
	}
	a.AttemptID = "attempt_recovery_owner"
	a.Setup.Profile = runner.Profile{Harness: "codex", Access: "workspace-write"}
	a.UnpublishedCardRecovery = &proof
	if err = f.service.validateRecoveredCardStart(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	restored, err := f.service.workspaces.Prepare(t.Context(), a)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(restored.WorkDir, "shared.txt"))
	if err != nil || string(raw) != "captured unreviewed\n" {
		t.Fatal("exact captured source not restored", string(raw), err)
	}
	if err = os.WriteFile(filepath.Join(restored.WorkDir, "shared.txt"), []byte("ordinary owner completion\n"), 0600); err != nil {
		t.Fatal(err)
	}
	source, err := f.service.workspaces.Capture(t.Context(), a)
	if err != nil {
		t.Fatal(err)
	}
	pinAssignment := runner.Clone(a)
	pinAssignment.UnpublishedCardRecovery = nil
	pinAssignment.Setup.Profile = runner.Profile{Harness: "task-source", Mode: "pin", Access: "workspace-read"}
	candidate := f.pin(t, pinAssignment, source, "recovery", "ready")
	_, join := f.join(t, []runner.TaskCandidate{proof.Pins[0], *candidate}, "ready")
	if join.Source == nil {
		t.Fatal("normal join lost source")
	}
	assertReleasedTaskEvidence(t, f, pinBefore, proof.Pins[0], mustRecoveryPatch(t, f, proof.Pins[0].PatchID))
	// The failed capture stays immutable even after the new ordinary pin.
	if captureDigest(mustRecoveryPatch(t, f, proof.Source.PatchID)) != proof.Artifacts[0].SHA256 {
		t.Fatal("failed source replaced")
	}
}
func mustRecoveryPatch(t *testing.T, f *taskSourceFixture, id string) []byte {
	t.Helper()
	b, e := f.disk.Read("artifacts/"+id, f.disk.Policy.MaxArtifact)
	if e != nil {
		t.Fatal(e)
	}
	return b
}

func TestUnpublishedCardSourceRefusesUnownedChangedOrActiveEvidence(t *testing.T) {
	for _, kind := range []string{"source bytes", "seed bytes", "pin bytes", "seed ref", "pin ref", "authority", "dependency", "scope", "generated ownership", "released", "existing checkout", "writer lease", "unknown execution"} {
		t.Run(kind, func(t *testing.T) {
			f, _, r, p := unpublishedSourceFixture(t)
			record, e := f.service.workspaces.load(r.ID)
			if e != nil {
				t.Fatal(e)
			}
			switch kind {
			case "source bytes", "seed bytes", "pin bytes":
				id := p.Source.PatchID
				if kind == "seed bytes" {
					id = p.Source.InputPatchID
				}
				if kind == "pin bytes" {
					id = p.Pins[0].PatchID
				}
				e = f.disk.Write("artifacts/"+id, []byte("changed"), f.disk.Policy.MaxArtifact)
			case "seed ref":
				fixtureGit(t, f.repo, "update-ref", taskSeedRef(r.ID, "cards"), f.base)
			case "pin ref":
				fixtureGit(t, f.repo, "update-ref", taskPinRef(p.Pins[0].RunID), f.base)
			case "authority":
				p.Authority.WritePaths = []string{"outside.txt"}
			case "dependency":
				p.Authority.Dependencies[0].Tree = f.base
			case "scope": // Bound authority is unchanged, but the captured change is outside it.
				p.Authority.WritePaths = []string{"outside.txt"}
				record.TaskSources["cards"].Authority = p.Authority
				e = f.service.workspaces.save(record)
			case "generated ownership":
				f.service.workspaces.readOnlyPaths = []string{"shared.txt"}
			case "released":
				record.Released = false
				e = f.service.workspaces.save(record)
			case "existing checkout":
				e = os.MkdirAll(record.WorkDir, 0700)
			case "writer lease":
				if err := captureDirectory(f.disk.Root, "work", p.Source.AttemptID, "capture-v1"); err != nil {
					t.Fatal(err)
				}
				lease, err := os.OpenFile(filepath.Join(f.disk.Root, "work", p.Source.AttemptID, "capture-v1", "lease"), os.O_CREATE|os.O_RDWR, 0600)
				if err != nil {
					t.Fatal(err)
				}
				defer lease.Close()
				if ok, err := tryCaptureLease(lease); err != nil || !ok {
					t.Fatal("fixture capture lease", err)
				}
			case "unknown execution":
				e = f.disk.Write("jobs/unknown.codex.json", []byte(`{"assignment":{"run_id":"run_recover"}}`), f.disk.Policy.Database.MaxRecord)
			}
			if e != nil {
				t.Fatal(e)
			}
			if e = f.service.ValidateUnpublishedCardRecovery(t.Context(), r, p); e == nil {
				t.Fatal("unsafe evidence accepted", kind)
			}
		})
	}
}

func TestUnpublishedCardPreDispatchDriftIsSettledFailureWithoutNativeJob(t *testing.T) {
	f, a, _, proof := unpublishedSourceFixture(t)
	a.AttemptID = "attempt_refused_recovery_owner"
	a.Setup.Profile = runner.Profile{Harness: "codex", Access: "workspace-write"}
	a.UnpublishedCardRecovery = &proof
	if e := f.disk.Write("artifacts/"+proof.Source.PatchID, []byte("changed after grant"), f.disk.Policy.MaxArtifact); e != nil {
		t.Fatal(e)
	}
	o, e := f.service.Start(t.Context(), a)
	if e != nil || o.State != runner.Finished || o.End == nil || o.End.Kind != "failed" || o.End.Retryable {
		t.Fatal("known prelaunch refusal became uncertain or retryable", o, e)
	}
	if exists, e := f.service.native(a.AttemptID); e != nil || exists {
		t.Fatal("rejection allocated native work", e)
	}
}
