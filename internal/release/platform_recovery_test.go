//go:build darwin || linux

package release

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// No production failure injection: a separate Compose project has synthetic
// settings and a database created by UID 65532 with the real protected modes.
func TestDeliveryPlatformRecovery(t *testing.T) {
	policy := os.Getenv("RUNNER_DELIVERY_PLATFORM_POLICY")
	helper := os.Getenv("RUNNER_DELIVERY_FAULT_HELPER")
	if policy == "" || helper == "" {
		t.Skip("explicit disposable platform fixture required")
	}
	op, err := Load(policy)
	if err != nil {
		t.Fatal(err)
	}
	base := op.Backend.(*Live).Config
	if !strings.Contains(base.StateDir, "-qualification/") || !filepath.IsAbs(helper) {
		t.Fatal("separate qualification policy required")
	}
	for _, mode := range []string{"backup", "migration", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			run := "lab_" + mode + "_" + time.Now().UTC().Format("20060102T150405")
			c := base
			c.DeployProject = strings.ToLower(strings.ReplaceAll(run, "_", "-"))
			remote := c.BackupDir + "/" + run
			c.DataDir = remote + "/data"
			c.StateDir = filepath.Join(base.StateDir, run)
			c.OffsiteBackupDir = filepath.Join(c.StateDir, "backups")
			os.MkdirAll(c.OffsiteBackupDir, 0700)
			c.Environment = filepath.Join(c.StateDir, "fixture.env")
			b := &Live{Config: c}
			live := *b
			b.runCommand = func(ctx context.Context, input []byte, path string, args ...string) ([]byte, error) {
				if path == c.GH && slices.Contains(args, "api") {
					return []byte(`{"object":{"sha":"merged"}}`), nil
				}
				return live.command(ctx, input, path, args...)
			}
			if _, err = b.host(t.Context(), nil, "mkdir", "-m", "0777", remote); err != nil {
				t.Fatal(err)
			}
			gidBytes, err := b.host(t.Context(), nil, "id", "-g")
			if err != nil {
				t.Fatal(err)
			}
			gid := strings.TrimSpace(string(gidBytes))
			oldEnvironment, err := readFile(base.Environment, 64<<10)
			if err != nil {
				t.Fatal(err)
			}
			image := dotenvValue(oldEnvironment, "GAMES_IMAGE")
			report := dotenvValue(oldEnvironment, "GAMES_NONOGRAM_QUALIFICATION_FILE")
			if image == "" || report == "" {
				t.Fatal("qualified image/report identity missing")
			}
			fixtureArgs := []string{
				"run",
				"--rm",
				"--pull=never",
				"--network",
				"none",
				"--read-only",
				"--cap-drop",
				"ALL",
				"--security-opt",
				"no-new-privileges:true",
				"--user",
				"65532:" + gid,
				"--entrypoint",
				"/fixture-helper",
				"--mount",
				"type=bind,src=" + remote + ",dst=/fixture",
				"--mount",
				"type=bind,src=" + helper + ",dst=/fixture-helper,readonly",
				image,
			}
			if _, err = b.docker(t.Context(), append(fixtureArgs, "init")...); err != nil {
				t.Fatal(err)
			}
			env := strings.Join(rehearsalEnvironment("/data/games.db", "/report", "https://rehearsal.example.test"), "\n") + "\nGAMES_IMAGE=" + image + "\n"
			os.WriteFile(c.Environment, []byte(env), 0600)
			os.WriteFile(c.Environment+".before", []byte(env), 0600)
			compose := filepath.Join(c.StateDir, "compose.json")
			definition := map[string]any{"services": map[string]any{"games": map[string]any{
				"image": image, "command": []string{"--migrate"}, "env_file": []string{c.Environment},
				"network_mode": "none", "read_only": true, "cap_drop": []string{"ALL"},
				"mem_limit": "1g", "cpus": 2, "pids_limit": 64,
				"security_opt": []string{"no-new-privileges:true"}, "user": "65532:65532",
				"volumes":     []string{c.DataDir + ":/data", report + ":/report:ro"},
				"tmpfs":       []string{"/tmp:size=16m,mode=1777,noexec,nosuid"},
				"healthcheck": map[string]any{"test": []string{"CMD", "/app/server", "healthcheck"}, "interval": "1s", "timeout": "5s", "retries": 30},
			}}}
			data, _ := json.Marshal(definition)
			os.WriteFile(compose, data, 0600)
			if _, err = b.docker(t.Context(), "compose", "-p", c.DeployProject, "-f", compose, "up", "-d", "--no-build", "--pull", "never"); err != nil {
				t.Fatal(err)
			}
			ids, err := b.docker(t.Context(), "ps", "-aq", "--no-trunc", "--filter", "label=com.docker.compose.project="+c.DeployProject)
			if err != nil || len(strings.Fields(string(ids))) != 1 {
				t.Fatal("disposable service identity", err)
			}
			id := strings.TrimSpace(string(ids))
			j := Job{Request: Request{RunID: run, Key: run + "_stop", Action: "stop"}, Receipt: Receipt{
				PriorContainer: id, PriorImage: image, Image: image, Commit: "merged", Report: report,
				EnvironmentBackup: c.Environment + ".before", Remote: remote,
			}}
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
				defer cancel()
				for _, name := range []string{"runner-migration-" + run, "runner-restore-" + run + "_restore"} {
					if x, e := b.inspect(ctx, name); e == nil && x.Config.Labels["cortexium.runner.run"] == run && !x.State.Running {
						_, _ = b.docker(ctx, "rm", x.ID)
					}
				}
				_, _ = b.docker(ctx, "compose", "-p", c.DeployProject, "-f", compose, "down", "--timeout", "10")
				_, _ = b.docker(ctx, append(fixtureArgs, "clean")...)
				_, _ = b.host(ctx, nil, "rm", "-rf", "--", remote)
			})
			until := func(action func() (bool, error)) {
				t.Helper()
				ticker := time.NewTicker(time.Second)
				defer ticker.Stop()
				for {
					ready, e := action()
					if e != nil {
						t.Fatal(e)
					}
					if ready {
						return
					}
					select {
					case <-t.Context().Done():
						t.Fatal(t.Context().Err())
					case <-ticker.C:
					}
				}
			}
			until(func() (bool, error) { x, e := b.inspect(t.Context(), id); return x.State.Health.Status == "healthy", e })
			backupSHA, err := b.capture(t.Context(), run+"-before", false, id, image)
			if err != nil {
				t.Fatal(err)
			}
			j.Receipt.Backup, j.Receipt.BackupSHA = filepath.Join(c.OffsiteBackupDir, run+"-before.db"), backupSHA
			if mode == "backup" {
				b.Config.OffsiteBackupDir = filepath.Join(c.StateDir, "blocked-backup")
				if err = os.WriteFile(b.Config.OffsiteBackupDir, []byte("injected backup destination failure"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			save := func() error { return writeJSON(filepath.Join(c.StateDir, j.Request.Key+".json"), j) }
			until(func() (bool, error) {
				var e error
				if j.Recovery != "" {
					e = b.recoverBeforeMigration(t.Context(), &j, save)
				} else {
					e = b.stop(t.Context(), &j, save)
				}
				return j.Done, e
			})
			if mode == "backup" {
				if !j.Failed || j.Receipt.Recovery == "" {
					t.Fatal("backup failure did not recover", j.Reason)
				}
				return
			}
			if j.Failed {
				t.Fatal(j.Reason)
			}
			if mode == "cancel" {
				j.Request.Action, j.Request.Key = "recover-stop", run+"_cancel"
				j.Done = false
				j.Recovery = "needed"
				j.StoppedSHA = j.Receipt.DataSHA
				j.Failure = "release cancelled before migration"
				until(func() (bool, error) { e := b.recoverBeforeMigration(t.Context(), &j, save); return j.Done, e })
				if !j.Failed || j.Receipt.Recovery == "" {
					t.Fatal("cancelled stopped service was not recovered", j.Reason)
				}
				return
			}
			name := "runner-migration-" + run
			args := []string{
				"create",
				"--name",
				name,
				"--label",
				"cortexium.runner.run=" + run,
				"--pull=never",
				"--network",
				"none",
				"--read-only",
				"--cap-drop",
				"ALL",
				"--user",
				"65532:" + gid,
				"--entrypoint",
				"/fixture-helper",
				"--mount",
				"type=bind,src=" + remote + ",dst=/fixture",
				"--mount",
				"type=bind,src=" + helper + ",dst=/fixture-helper,readonly",
				image,
				"mutate",
			}
			if _, err = b.docker(t.Context(), args...); err != nil {
				t.Fatal(err)
			}
			if _, err = b.docker(t.Context(), "start", "-a", name); !knownFailure(err) {
				t.Fatal("injected migration did not fail conclusively", err)
			}
			j.Request.Action = "migrate"
			j.Request.Key = run + "_migrate"
			j.Done = false
			j.Phase = "start_requested"
			if err = b.migrate(t.Context(), &j, save); err != nil || j.Outcome != "recovery_required" {
				t.Fatal("migration recovery route", err, j.Reason)
			}
			j.Request.Action = "inspect-migration"
			j.Request.Key = run + "_inspect"
			j.Done = false
			j.Phase = ""
			j.Outcome = ""
			if err = b.inspectMigration(t.Context(), &j); err != nil || j.Failed || j.Receipt.DataSHA == "" {
				t.Fatal("migration inspection", err, j.Reason)
			}
			// The normal core fixture separately proves the saved owner decision.
			j.Request.Action = "restore"
			j.Request.Key = run + "_restore"
			j.Done = false
			until(func() (bool, error) { e := b.restorePrevious(t.Context(), &j, save); return j.Done, e })
			if j.Failed || j.Outcome != "restored" {
				t.Fatal("approved restoration", j.Reason)
			}
			if err = save(); err != nil {
				t.Fatal(err)
			}
			t.Log("Disposable protected-data restoration passed:", c.StateDir)
		})
	}
}
