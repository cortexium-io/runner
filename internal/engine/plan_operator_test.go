package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/github"
	"github.com/cortexium-io/runner/internal/subprocess"
)

func TestDeliveryCancellationFencesAdmissionWithoutDiscardingHistory(t *testing.T) {
	f := newProductionDelivery(t)
	// Retain real signed feedback/counts on the parent; cancellation is not a
	// retry and must not rewrite that history or any child's immutable release.
	action, err := f.service.source.Authorize(t.Context(), f.parent(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.service.source.TransitionRejection(t.Context(), action, "Backlog", github.PlanDeliveryPhase, "Retained historical feedback", 2); err != nil {
		t.Fatal(err)
	}
	before, err := f.service.source.LifecycleItems(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	plan, err := f.service.PlanDeliveryCancellation(t.Context(), f.parentID)
	if err != nil {
		t.Fatal(err)
	}
	if plan.AlreadyCancelled || len(plan.Members) != 2 || plan.QAFailures != 2 {
		t.Fatal("preview omitted exact cancellation state")
	}
	previewed, _ := f.service.source.LifecycleItems(t.Context())
	if !reflect.DeepEqual(previewed, before) {
		t.Fatal("preview mutated authority")
	}
	after, err := f.service.ApplyDeliveryCancellation(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if !after.AlreadyCancelled || after.QAFailures != 2 || !reflect.DeepEqual(plan.Members, after.Members) {
		t.Fatal("cancellation discarded members or counters")
	}
	items, _ := f.service.source.LifecycleItems(t.Context())
	for i, item := range items {
		old := before[i]
		if item.ID != f.parentID {
			if !reflect.DeepEqual(item, old) {
				t.Fatal("cancellation rewrote a child")
			}
			continue
		}
		if item.Phase != github.PlanCancelledPhase || item.Status != "Backlog" || item.Result != old.Result || item.PlanRelease != old.PlanRelease || item.Body != old.Body || item.QAFailures != old.QAFailures {
			t.Fatal("parent cancellation broadened its intended delta")
		}
	}
	ready, err := f.service.source.ReadyItems(t.Context(), items, len(items))
	if err != nil || len(ready) != 0 {
		t.Fatalf("cancelled members remained eligible: %d %v", len(ready), err)
	}
	writes := len(f.runner.project.calls)
	if _, err := f.service.ApplyDeliveryCancellation(t.Context(), after); err != nil {
		t.Fatal(err)
	}
	for _, call := range f.runner.project.calls[writes:] {
		if strings.Contains(call, "item-edit") || strings.Contains(call, "issue edit") {
			t.Fatal("cancellation replay repeated mutation")
		}
	}
	if f.runner.implementations != 0 || f.runner.reviews != 0 || f.runner.creates != 0 || f.runner.planPushes != 0 {
		t.Fatal("cancel performed work/publication")
	}
}

func TestDeliveryCancellationRefusesLiveOrChangedWork(t *testing.T) {
	for _, which := range []string{"worker", "planner", "reviewer", "verification", "quarantined cleanup", "member edit", "unrecorded PR"} {
		t.Run(which, func(t *testing.T) {
			f := newProductionDelivery(t)
			plan, err := f.service.PlanDeliveryCancellation(t.Context(), f.parentID)
			if err != nil {
				t.Fatal(err)
			}
			var guard *github.ProcessLock
			project := f.service.cfg.GitHubProject.GitHubProjectConfig
			switch which {
			case "worker":
				guard, err = github.AcquireProcessLock(project)
			case "planner":
				guard, err = github.AcquirePlanningLock(project)
			case "reviewer":
				guard, err = github.AcquireQAReviewLock(project, plan.Members[0].ID)
			case "verification":
				guard, err = github.AcquireVerificationOperationLock(project, "complete")
			case "quarantined cleanup":
				f.service.processOwnership.RecordCleanupFailure()
			case "member edit":
				f.runner.project.remoteItems[1].Body += "\nUnexpected operator edit."
			case "unrecorded PR":
				f.runner.creates = 1
				f.runner.branch = plan.Branch
			}
			if err != nil {
				t.Fatal(err)
			}
			if guard != nil {
				defer guard.Release()
			}
			before, _ := f.service.source.LifecycleItems(t.Context())
			if _, err := f.service.ApplyDeliveryCancellation(t.Context(), plan); err == nil {
				t.Fatal("unsafe cancellation accepted")
			}
			after, _ := f.service.source.LifecycleItems(t.Context())
			if !reflect.DeepEqual(before, after) {
				t.Fatal("refusal changed work")
			}
		})
	}
}

// Substitute only the GitHub transport. The real engine migration takes its
// config CAS, offline locks, fresh Project preview, field write and readback.
type deliveryMigrationRunner struct {
	project                   *fakeGitHubProjectRunner
	field, lost, fail, mutate bool
	fieldType, fieldName      string
	creates                   int
}

func (r *deliveryMigrationRunner) Run(ctx context.Context, command string, args []string, dir string, timeout time.Duration) (subprocess.Result, error) {
	joined := strings.Join(args, " ")
	if command == "gh" && isProjectFieldsCall(joined) {
		fields := projectFieldsGraphQLJSON()
		exact := `,{"__typename":"ProjectV2Field","id":"F_plan_release","name":"Runner Plan Release","dataType":"TEXT"}`
		if !r.field {
			fields = strings.Replace(fields, exact, "", 1)
		}
		if r.fieldType != "" {
			fields = strings.Replace(fields, `"name":"Runner Plan Release","dataType":"TEXT"`, `"name":"Runner Plan Release","dataType":"`+r.fieldType+`"`, 1)
		}
		if r.fieldName != "" {
			fields = strings.Replace(fields, `"name":"Runner Plan Release"`, `"name":"`+r.fieldName+`"`, 1)
		}
		return subprocess.Result{Stdout: fields}, nil
	}
	if command == "gh" && strings.HasPrefix(joined, "project field-create ") {
		if argumentValue(args, "--name") != config.RunnerPlanReleaseFieldName || argumentValue(args, "--data-type") != "TEXT" {
			return subprocess.Result{}, errors.New("unexpected field mutation")
		}
		r.creates++
		if r.fail {
			return subprocess.Result{}, errors.New("field unavailable")
		}
		r.field = true
		if r.mutate {
			r.project.loadRemoteItems()
			r.project.remoteItems = append(r.project.remoteItems, github.WorkItem{ID: "PVTI_operator", Title: "Operator change", Status: "Backlog"})
		}
		if r.lost {
			return subprocess.Result{}, errors.New("lost field creation response")
		}
		return subprocess.Result{Stdout: `{"id":"F_plan_release"}`}, nil
	}
	return r.project.Run(ctx, command, args, dir, timeout)
}

func migrationEngineFixture(t *testing.T) (*Engine, *deliveryMigrationRunner, string, config.Config) {
	t.Helper()
	cfg := completeEngineTestConfig(config.Config{ProjectDir: t.TempDir(), Verification: map[string]config.VerificationEntrypoint{
		"complete": {Command: "/bin/sh", Args: []string{"-c", "exit 0"}, ToolchainCommands: []string{"/bin/sh"}, InputPaths: []string{"src"}, TimeoutSeconds: 30},
	}})
	profile := cfg.Roles["reviewer"]
	profile.Access = config.RoleAccessHost
	cfg.Roles["reviewer"] = profile
	path := filepath.Join(t.TempDir(), "runner.json")
	if err := config.SaveConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	runner := &deliveryMigrationRunner{project: &fakeGitHubProjectRunner{itemsJSON: `{"items":[]}`}}
	service, err := New(cfg, runner)
	if err != nil {
		t.Fatal(err)
	}
	return service, runner, path, cfg
}

func TestDeliveryMigrationRealBoundaryFieldThenExactConfiguration(t *testing.T) {
	for _, which := range []string{"missing field", "existing field", "lost response", "field failure", "operator race"} {
		t.Run(which, func(t *testing.T) {
			service, runner, path, original := migrationEngineFixture(t)
			runner.field, runner.lost, runner.fail, runner.mutate = which == "existing field", which == "lost response", which == "field failure", which == "operator race"
			plan, err := service.PlanDeliveryMigration(t.Context(), path, "complete")
			if err != nil {
				t.Fatal(err)
			}
			if runner.creates != 0 {
				t.Fatal("preview mutated field")
			}
			err = service.ApplyDeliveryMigration(t.Context(), plan)
			if which == "field failure" || which == "operator race" {
				if err == nil {
					t.Fatal("failed migration accepted")
				}
				cfg, readErr := config.LoadTrustedConfig(path)
				if readErr != nil || !reflect.DeepEqual(cfg, original) {
					t.Fatal("failed field phase activated config")
				}
				if which == "operator race" && !runner.field {
					t.Fatal("partial created field removed")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			cfg, err := config.LoadTrustedConfig(path)
			if err != nil || cfg.PlanDelivery == nil || !cfg.PlanDelivery.Enabled {
				t.Fatalf("config not activated: %v", err)
			}
			cfg.PlanDelivery = nil
			if !reflect.DeepEqual(cfg, original) {
				t.Fatal("unrelated config changed")
			}
			want := 1
			if which == "existing field" {
				want = 0
			}
			if runner.creates != want {
				t.Fatalf("field creation count %d", runner.creates)
			}
		})
	}
}

func TestDeliveryMigrationRefusesChangedPreviewOrUnfinishedBatch(t *testing.T) {
	t.Run("unfinished batch", func(t *testing.T) {
		f := newProductionDelivery(t)
		if _, err := f.service.source.PlanDeliveryMigration(t.Context()); err == nil {
			t.Fatal("unfinished batch accepted")
		}
	})
	for _, which := range []string{"field type", "field spelling", "config edit", "schema edit", "worker", "standalone review", "standalone verification", "quarantined cleanup"} {
		t.Run(which, func(t *testing.T) {
			service, runner, path, cfg := migrationEngineFixture(t)
			runner.project.loadRemoteItems()
			runner.project.remoteItems = []github.WorkItem{{ID: "PVTI_done", Title: "Historical standalone", Status: "Done"}}
			if which == "field type" || which == "field spelling" {
				runner.field = true
				if which == "field type" {
					runner.fieldType = "NUMBER"
				} else {
					runner.fieldName = "runner plan release"
				}
				if _, err := service.PlanDeliveryMigration(t.Context(), path, "complete"); err == nil {
					t.Fatal("conflicting field accepted")
				}
				return
			}
			plan, err := service.PlanDeliveryMigration(t.Context(), path, "complete")
			if err != nil {
				t.Fatal(err)
			}
			var guard *github.ProcessLock
			switch which {
			case "config edit":
				cfg.MaxParallelism++
				err = config.SaveConfig(path, cfg)
			case "schema edit":
				runner.field = true
			case "worker":
				guard, err = github.AcquireProcessLock(*cfg.GitHubProject)
			case "standalone review":
				guard, err = github.AcquireQAReviewLock(*cfg.GitHubProject, "PVTI_done")
			case "standalone verification":
				guard, err = github.AcquireVerificationOperationLock(*cfg.GitHubProject, "complete")
			case "quarantined cleanup":
				service.processOwnership.RecordCleanupFailure()
			}
			if err != nil {
				t.Fatal(err)
			}
			if guard != nil {
				defer guard.Release()
			}
			before, _ := os.ReadFile(path)
			if err := service.ApplyDeliveryMigration(t.Context(), plan); err == nil {
				t.Fatal("unsafe apply accepted")
			}
			after, _ := os.ReadFile(path)
			if string(before) != string(after) || runner.creates != 0 {
				t.Fatal("refusal mutated state")
			}
		})
	}
}
