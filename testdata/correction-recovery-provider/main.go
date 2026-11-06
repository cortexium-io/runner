// Model-free provider for the governed recovery journey. All remote effects are
// disposable records; Git publication still uses Runner's real local CAS path.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/cortexium-io/runner/internal/release"
)

type publication struct {
	RunID   string `json:"run_id"`
	Base    string `json:"base"`
	Head    string `json:"head"`
	PR      int    `json:"pr"`
	Creates int    `json:"creates"`
	Updates int    `json:"updates"`
}

type fixture struct{ root string }

func (f fixture) Advance(_ context.Context, j *release.Job, save func() error) error {
	if j.Phase == "" {
		file, err := os.OpenFile(filepath.Join(f.root, "calls"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		_, err = file.WriteString(j.Request.Action + "\n")
		file.Close()
		if err != nil {
			return err
		}
		j.Phase = "accepted"
		if j.Request.Action == "readiness" {
			j.Receipt.Head = j.Request.Source
		}
		j.Receipt.Image = "fixture-image"
		if j.Request.Action == "publish" {
			var previous release.Receipt
			var p publication
			path := filepath.Join(f.root, "publication.json")
			data, err := os.ReadFile(path)
			if errors.Is(err, os.ErrNotExist) {
				p = publication{RunID: j.Request.RunID, Base: j.Request.Source, PR: 333, Creates: 1}
			} else if err != nil || json.Unmarshal(data, &p) != nil || json.Unmarshal(j.Request.Retry, &previous) != nil || previous.Action != "publish" || previous.Head != p.Head || previous.RunID != p.RunID || p.RunID != j.Request.RunID || p.Base != j.Request.Source {
				return errors.New("existing draft publication identity changed")
			} else {
				p.Updates++
			}
			p.Head = j.Request.Candidate
			data, _ = json.Marshal(p)
			if err = os.WriteFile(path, data, 0600); err != nil {
				return err
			}
			j.Receipt.PR, j.Receipt.Head, j.Receipt.Base, j.Receipt.Source = p.PR, p.Head, p.Base, p.Base
		}
		if err = save(); err != nil {
			return err
		}
	}
	if j.Request.Action == "qualify" {
		file := filepath.Join(f.root, "qualified-once")
		if _, err := os.Stat(file); errors.Is(err, os.ErrNotExist) {
			if err = os.WriteFile(file, []byte(j.Receipt.Head), 0600); err != nil {
				return err
			}
			j.Outcome, j.Reason = "changes_requested", "fixture source correction requested"
			j.Receipt.Recovery, j.Receipt.Note = "source_correction_required", j.Reason
		}
	}
	j.Done = true
	return nil
}

func main() {
	root := os.Getenv("RUNNER_RELEASE_FIXTURE_STATE")
	var q release.Request
	if root == "" || json.NewDecoder(os.Stdin).Decode(&q) != nil {
		os.Exit(2)
	}
	o := release.Operator{Directory: filepath.Join(root, "operations"), PolicyHash: "fixture", Project: "demo", Source: os.Getenv("RUNNER_RELEASE_FIXTURE_HEAD"), Backend: fixture{root}, Delivery: true}
	r, err := o.Call(context.Background(), q)
	if err != nil {
		r = release.Response{Key: q.Key, State: "unknown", Reason: err.Error()}
	}
	if json.NewEncoder(os.Stdout).Encode(r) != nil {
		os.Exit(1)
	}
}
