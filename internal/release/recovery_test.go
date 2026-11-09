package release

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestBackupFailureRecoversOnlyUnchangedPreMigrationService(t *testing.T) {
	for _, scenario := range []string{
		"recover",
		"lost restart reply",
		"changed database",
		"changed environment",
		"migration exists",
		"unknown backup",
		"recovery disabled",
	} {
		t.Run(scenario, func(t *testing.T) {
			dir := t.TempDir()
			env := []byte("GAMES_IMAGE=old\nPRIVATE=value\n")
			for _, file := range []string{"env", "env.before"} {
				if err := os.WriteFile(filepath.Join(dir, file), env, 0600); err != nil {
					t.Fatal(err)
				}
			}
			backup := filepath.Join(dir, "live.db")
			os.WriteFile(backup, []byte("verified snapshot"), 0600)
			b := Live{Config: Config{
				RecoverBeforeMigration: scenario != "recovery disabled",
				Environment:            filepath.Join(dir, "env"),
				DeployProject:          "games-site",
				DataDir:                "/data",
				BackupDir:              "/backup",
				OffsiteBackupDir:       filepath.Join(dir, "offsite"),
			}}
			j := Job{
				Request: Request{RunID: "run_fixture", Key: "stop_attempt", Action: "stop"},
				Phase:   "stop_requested",
				Receipt: Receipt{
					PriorContainer:    "previous",
					PriorImage:        "old",
					Commit:            "merged",
					Backup:            backup,
					BackupSHA:         Digest([]byte("verified snapshot")),
					EnvironmentBackup: filepath.Join(dir, "env.before"),
				},
			}
			running, starts, fingerprints := false, 0, 0
			b.runCommand = func(_ context.Context, _ []byte, _ string, args ...string) ([]byte, error) {
				joined := strings.Join(args, " ")
				switch {
				case slices.Contains(args, "start"):
					starts++
					running = true
					if scenario == "lost restart reply" {
						return nil, errors.New("transport disconnected")
					}
					return nil, nil
				case slices.Contains(args, "api"):
					return []byte(`{"object":{"sha":"merged"}}`), nil
				case slices.Contains(args, "ps"):
					if strings.Contains(joined, "cortexium.runner.run=") {
						if scenario == "migration exists" {
							return []byte("migration"), nil
						}
						return nil, nil
					}
					return []byte("previous"), nil
				case slices.Contains(args, "inspect"):
					return json.Marshal([]any{map[string]any{
						"Id":     "previous",
						"Image":  "old",
						"Mounts": []any{map[string]any{"Type": "bind", "Source": "/data", "Destination": "/data", "RW": true}},
						"Config": map[string]any{"Labels": map[string]string{"com.docker.compose.project": "games-site", "com.docker.compose.service": "games"}},
						"State":  map[string]any{"Running": running, "Health": map[string]string{"Status": "healthy"}},
					}})
				case strings.Contains(joined, "--fingerprint"):
					fingerprints++
					if scenario == "changed database" && fingerprints > 1 {
						return []byte(strings.Repeat("b", 64)), nil
					}
					return []byte(strings.Repeat("a", 64)), nil
				case strings.Contains(joined, "'test' '-f'"):
					return nil, &commandFailure{program: "ssh", code: 1}
				case strings.Contains(joined, "runner-snapshot"):
					if scenario == "changed environment" {
						os.WriteFile(b.Config.Environment, []byte("GAMES_IMAGE=other\n"), 0600)
					}
					if scenario == "unknown backup" {
						return nil, errors.New("SSH disconnected")
					}
					return nil, &commandFailure{program: "ssh", code: 1}
				default:
					t.Fatalf("unexpected command %v", args)
					return nil, nil
				}
			}
			save := func() error { return writeJSON(filepath.Join(dir, "job.json"), j) }
			err := b.stop(t.Context(), &j, save)
			if scenario == "unknown backup" || scenario == "recovery disabled" {
				if err == nil || starts != 0 || j.Done {
					t.Fatal("unsettled or unauthorized failure recovered", err, j)
				}
				return
			}
			if scenario == "recover" || scenario == "lost restart reply" {
				if starts != 1 {
					t.Fatal("previous service not started", starts, err, j)
				}
				// Reopen the saved journal, as after loss of the owning process.
				data, _ := os.ReadFile(filepath.Join(dir, "job.json"))
				json.Unmarshal(data, &j)
				if err = b.recoverBeforeMigration(t.Context(), &j, save); err != nil || !j.Done || !j.Failed || j.Receipt.Recovery == "" || starts != 1 {
					t.Fatal("recovery did not settle without repeated start", err, j, starts)
				}
			} else if starts != 0 || !j.Failed {
				t.Fatal("unsafe restart", starts, err, j)
			}
		})
	}
}
