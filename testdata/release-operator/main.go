// Isolated model-free release provider: never invokes GitHub, Docker or SSH.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/cortexium-io/runner/internal/release"
)

type fixture struct{ root string }

func (f fixture) Advance(_ context.Context, j *release.Job, save func() error) error {
	if j.Phase == "" {
		file, err := os.OpenFile(filepath.Join(f.root, "calls"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		_, err = file.WriteString(j.Request.Action + "\n")
		file.Close()
		if err != nil {
			return err
		}
		j.Phase = "accepted"
		if os.Getenv("RUNNER_RELEASE_FIXTURE_CONNECTED") != "1" || j.Request.Action == "readiness" {
			j.Receipt.Head = j.Request.Source
		}
		if os.Getenv("RUNNER_RELEASE_FIXTURE_CONNECTED") == "1" && j.Request.Action == "publish" {
			j.Receipt.Head = j.Request.Candidate
			j.Receipt.Base = j.Request.Source
			j.Receipt.Source = j.Request.Source
			j.Receipt.PR = 1
		}
		j.Receipt.Image = "fixture-image"
		if os.Getenv("RUNNER_RELEASE_FIXTURE_MODE") == "all" {
			j.Receipt.Image = "sha256:" + strings.Repeat("a", 64)
			j.Receipt.ServerSHA, j.Receipt.ReportSHA = strings.Repeat("b", 64), strings.Repeat("c", 64)
			j.Receipt.PriorImage = "sha256:" + strings.Repeat("d", 64)
			j.Receipt.Backup, j.Receipt.FinalBackup = "/private/operator/backups/"+strings.Repeat("retained-", 20)+"live.db", "/private/operator/backups/"+strings.Repeat("retained-", 20)+"stopped.db"
			j.Receipt.BackupSHA, j.Receipt.FinalBackupSHA = strings.Repeat("e", 64), strings.Repeat("f", 64)
			j.Receipt.Commit = strings.Repeat("a", 40)
		}
		if err = save(); err != nil {
			return err
		}
		if j.Request.Action == "merge" {
			return errors.New("fixture lost merge acknowledgement")
		}
	}
	if os.Getenv("RUNNER_RELEASE_FIXTURE_CONNECTED") == "1" {
		data, _ := os.ReadFile(filepath.Join(f.root, "calls"))
		calls := string(data)
		mode := os.Getenv("RUNNER_RELEASE_FIXTURE_MODE")
		switch {
		case (mode == "qa" || mode == "all") && j.Request.Action == "qualify" && strings.Count(calls, "qualify\n") == 1:
			j.Outcome = "changes_requested"
			j.Reason = "fixture source correction requested"
			j.Receipt.Note = j.Reason
		case mode == "backup" && j.Request.Action == "stop":
			j.Failed = true
			j.Receipt.Recovery = "previous service restored healthy"
			j.Reason = j.Receipt.Recovery
		case mode == "migration" && j.Request.Action == "migrate":
			j.Outcome = "recovery_required"
			j.Reason = "fixture migration failed; approval required to restore retained backup"
		case j.Request.Action == "restore":
			j.Outcome = "restored"
			j.Receipt.Recovery = "approved backup restored; previous service healthy"
			j.Reason = j.Receipt.Recovery
		case j.Request.Action == "recover-stop":
			j.Failed = true
			j.Receipt.Recovery = "unchanged previous service restored healthy before migration"
			j.Reason = j.Receipt.Recovery
		case (mode == "retry" || mode == "all") && j.Request.Action == "deploy" && strings.Count(calls, "deploy\n") == 1:
			j.Outcome = "retry_safe"
			j.Receipt.Deployment = "fixture-before-effects"
			j.Receipt.Inspection = "no deployment effects"
		case (mode == "approval" && strings.Count(calls, "deploy\n") == 1 || mode == "all" && strings.Count(calls, "deploy\n") == 2) && j.Request.Action == "deploy":
			j.Outcome = "recovery_required"
			j.Receipt.Deployment = "fixture-after-effects"
		case j.Request.Action == "inspect":
			j.Receipt.Inspection = "fixture container image/state observed"
			j.Reason = "approve one same-image recovery"
			if mode == "all" {
				j.Receipt.Inspection = strings.Repeat("observed fixed image/service identity; ", 8)
			}
		case mode == "uncertain" && j.Request.Action == "deploy":
			if _, err := os.Stat(filepath.Join(f.root, "settle")); err != nil {
				return errors.New("fixture deployment response lost; same operation unresolved")
			}
		}
	}
	if os.Getenv("RUNNER_RELEASE_FIXTURE_MODE") == "all" && j.Reason == "" {
		j.Reason = strings.Repeat("Observed operation proof retained. ", 6)
		j.Receipt.Note = j.Reason
	}
	if j.Request.Action == "migrate" && os.Getenv("RUNNER_RELEASE_FIXTURE_FAIL") == "1" {
		j.Failed = true
		j.Reason = "fixture migration failed; recovery needs owner decision"
	}
	j.Done = true
	return nil
}
func main() {
	root := os.Getenv("RUNNER_RELEASE_FIXTURE_STATE")
	if root == "" {
		os.Exit(2)
	}
	var q release.Request
	if json.NewDecoder(os.Stdin).Decode(&q) != nil {
		os.Exit(2)
	}
	o := release.Operator{
		Directory:  filepath.Join(root, "operations"),
		PolicyHash: "fixture",
		Project:    "demo",
		Source:     os.Getenv("RUNNER_RELEASE_FIXTURE_HEAD"),
		Backend:    fixture{root},
		Delivery:   os.Getenv("RUNNER_RELEASE_FIXTURE_CONNECTED") == "1",
	}
	r, err := o.Call(context.Background(), q)
	if err != nil {
		r = release.Response{Key: q.Key, State: "unknown", Reason: err.Error()}
	}
	if json.NewEncoder(os.Stdout).Encode(r) != nil {
		os.Exit(1)
	}
}
