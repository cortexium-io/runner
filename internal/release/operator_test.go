package release

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

type scriptedBackend struct {
	calls      map[string]int
	disconnect bool
}

func (b *scriptedBackend) Advance(_ context.Context, j *Job, save func() error) error {
	if j.Phase == "" {
		b.calls[j.Request.Action]++
		j.Phase = "intent"
		j.Receipt.Head = j.Request.Source
		if err := save(); err != nil {
			return err
		}
		if j.Request.Action == "merge" && b.disconnect {
			return errors.New("lost acknowledgement")
		}
	}
	return done(j, "fixture accepted")
}
func TestOperatorRetainsIdentityAndReconcilesWithoutRepeatingMutation(t *testing.T) {
	b := &scriptedBackend{calls: map[string]int{}, disconnect: true}
	o := Operator{
		Directory:  filepath.Join(t.TempDir(), "journal"),
		PolicyHash: "policy",
		Project:    "demo",
		Source:     "head",
		Backend:    b,
	}
	q := Request{Project: "demo", RunID: "run_fixture", Key: "prepare_1", Action: "prepare", Source: "head"}
	p, err := o.Call(t.Context(), q)
	if err != nil || p.State != "finished" {
		t.Fatal(p, err)
	}
	if _, err = o.Call(t.Context(), q); err != nil || b.calls["prepare"] != 1 {
		t.Fatal("completed action repeated", err)
	}
	merge := Request{Project: q.Project, RunID: q.RunID, Key: "merge_1", Action: "merge", Source: q.Source, Previous: p.Receipt}
	m, err := o.Call(t.Context(), merge)
	if err != nil || m.State != "unknown" {
		t.Fatal(m, err)
	}
	m, err = o.Call(t.Context(), merge)
	if err != nil || m.State != "finished" || b.calls["merge"] != 1 {
		t.Fatal("mutation was repeated after lost acknowledgement", m, err, b.calls)
	}
	for _, change := range []func(*Request){
		func(q *Request) { q.Source = "other" },
		func(q *Request) { q.RunID = "other" },
		func(q *Request) { q.Action = "deploy" },
		func(q *Request) { q.Key = "../escape" },
	} {
		bad := merge
		change(&bad)
		if _, err = o.Call(t.Context(), bad); err == nil {
			t.Fatal("changed identity accepted", bad)
		}
	}
	var forged Receipt
	json.Unmarshal(p.Receipt, &forged)
	forged.Image = "unreviewed"
	data, _ := json.Marshal(forged)
	merge.Key = "forged_merge"
	merge.Previous = data
	if _, err = o.Call(t.Context(), merge); err == nil {
		t.Fatal("forged predecessor accepted")
	}
	o.PolicyHash = "changed"
	if _, err = o.Call(t.Context(), q); err == nil {
		t.Fatal("saved action reinterpreted under changed operator policy")
	}
}

func TestPinPreservesPrivateEnvironmentAndRejectsDrift(t *testing.T) {
	for _, drift := range []bool{false, true} {
		t.Run(map[bool]string{false: "accepted", true: "drift"}[drift], func(t *testing.T) {
			dir := t.TempDir()
			file := filepath.Join(dir, "production.env")
			backup := filepath.Join(dir, "backup.env")
			original := []byte("GAMES_IMAGE=old\nGAMES_NONOGRAM_QUALIFICATION_FILE=/old/report\nPRIVATE_FIXTURE=value\n")
			os.WriteFile(backup, original, 0600)
			current := original
			if drift {
				current = append(append([]byte{}, original...), []byte("OPERATOR_CHANGE=1\n")...)
			}
			os.WriteFile(file, current, 0600)
			b := Live{Config: Config{Environment: file, DeployProject: "games-site", DataDir: "/data"}}
			b.runCommand = func(_ context.Context, _ []byte, _ string, args ...string) ([]byte, error) {
				if slices.Contains(args, "ps") {
					return []byte("prior\n"), nil
				}
				return []byte(`[{"Id":"prior","Image":"old","Mounts":[{"Type":"bind","Source":"/data","Destination":"/data","RW":true}],"Config":{"Labels":{"com.docker.compose.project":"games-site","com.docker.compose.service":"games"}}}]`), nil
			}
			j := Job{Receipt: Receipt{
				EnvironmentBackup: backup,
				Image:             "approved",
				Report:            "/approved/report",
				PriorContainer:    "prior",
				PriorImage:        "old",
			}}
			if err := b.pin(t.Context(), &j, func() error { return nil }); err != nil {
				t.Fatal(err)
			}
			after, _ := os.ReadFile(file)
			if drift {
				if !j.Failed || string(after) != string(current) {
					t.Fatal("operator drift overwritten")
				}
				return
			}
			if j.Failed || dotenvValue(after, "GAMES_IMAGE") != "approved" || dotenvValue(after, "PRIVATE_FIXTURE") != "value" {
				t.Fatal("bindings or unrelated values changed")
			}
			if err := b.pin(t.Context(), &j, func() error { return nil }); err != nil {
				t.Fatal("idempotent pin failed", err)
			}
		})
	}
}

func TestControllerReusesRequestAfterLostAcknowledgementAndStopsAfterPartialFailure(t *testing.T) {
	var keys []string
	starts := 0
	lost := true
	partial := false
	b := Live{Config: Config{DeployProject: "games-site", Branch: "main"}}
	b.callTool = func(_ context.Context, name string, args any, target any) error {
		if name == "deploy" {
			m := args.(map[string]any)
			key := m["request_id"].(string)
			keys = append(keys, key)
			if starts == 0 {
				starts++
			}
			if lost {
				lost = false
				return errors.New("disconnect")
			}
			data, _ := json.Marshal(controllerJob{ID: "durable"})
			return json.Unmarshal(data, target)
		}
		status := "succeeded"
		if partial {
			status = "failed"
		}
		data, _ := json.Marshal(controllerJob{ID: "durable", Status: status, Commit: "merged", Started: true})
		return json.Unmarshal(data, target)
	}
	j := Job{Request: Request{Key: "attempt_1"}, Receipt: Receipt{Commit: "merged"}}
	if _, err := b.controller(t.Context(), &j, func() error { return nil }, false); err == nil {
		t.Fatal("expected lost acknowledgement")
	}
	ready, err := b.controller(t.Context(), &j, func() error { return nil }, false)
	if err != nil || !ready || len(keys) != 2 || keys[0] != keys[1] || starts != 1 {
		t.Fatal("controller submission identity changed", keys, err)
	}
	partial = true
	j.Done = false
	ready, err = b.controller(t.Context(), &j, func() error { return nil }, false)
	if err == nil || ready || j.Done || len(keys) != 2 {
		t.Fatal("partial deployment did not block without replacement", j, err)
	}
}

func TestMergeRejectsChangedHeadBeforeMutation(t *testing.T) {
	b := Live{Config: Config{PR: 330, Repository: "techbase/jacks-break", Branch: "main"}}
	writes := 0
	b.runCommand = func(_ context.Context, _ []byte, _ string, args ...string) ([]byte, error) {
		for _, a := range args {
			if a == "PUT" || a == "ready" {
				writes++
			}
		}
		return []byte(`{"head":{"sha":"changed"},"base":{"ref":"main","repo":{"full_name":"techbase/jacks-break"}}}`), nil
	}
	j := Job{Receipt: Receipt{Head: "approved"}}
	if err := b.merge(t.Context(), &j, func() error { return nil }); err != nil || !j.Failed || writes != 0 {
		t.Fatal(j, err, writes)
	}
}

func TestMigrationDoesNotRepeatAnUncertainContainerStart(t *testing.T) {
	file := filepath.Join(t.TempDir(), "snapshot.db")
	data := []byte("fixture snapshot")
	if err := os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	b := Live{Config: Config{DeployProject: "games-site", DataDir: "/data"}}
	starts := 0
	b.runCommand = func(_ context.Context, _ []byte, _ string, args ...string) ([]byte, error) {
		if slices.Contains(args, "start") {
			starts++
			return nil, nil
		}
		if slices.Contains(args, "ps") {
			return []byte("prior\n"), nil
		}
		if args[len(args)-1] == "prior" {
			return []byte(`[{"Id":"prior","Image":"old","Mounts":[{"Type":"bind","Source":"/data","Destination":"/data","RW":true}],"Config":{"Labels":{"com.docker.compose.project":"games-site","com.docker.compose.service":"games"}},"State":{"Status":"exited","Running":false}}]`), nil
		}
		return []byte(`[{"Id":"migration","Image":"approved","Config":{"Labels":{"cortexium.runner.run":"run_fixture"}},"State":{"Status":"created","Running":false}}]`), nil
	}
	j := Job{
		Request: Request{RunID: "run_fixture"},
		Phase:   "start_requested",
		Receipt: Receipt{
			PriorContainer: "prior",
			PriorImage:     "old",
			Image:          "approved",
			FinalBackup:    file,
			FinalBackupSHA: Digest(data),
		},
	}
	if err := b.migrate(t.Context(), &j, func() error { return nil }); err == nil || starts != 0 || j.Done {
		t.Fatal("uncertain migration was repeated or falsely settled", err, starts, j.Done)
	}
}
