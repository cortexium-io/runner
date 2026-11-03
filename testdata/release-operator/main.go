// Isolated model-free release provider: never invokes GitHub, Docker or SSH.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

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
		j.Receipt.Head = j.Request.Source
		j.Receipt.Image = "fixture-image"
		if err = save(); err != nil {
			return err
		}
		if j.Request.Action == "merge" {
			return errors.New("fixture lost merge acknowledgement")
		}
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
	}
	r, err := o.Call(context.Background(), q)
	if err != nil {
		r = release.Response{Key: q.Key, State: "unknown", Reason: err.Error()}
	}
	if json.NewEncoder(os.Stdout).Encode(r) != nil {
		os.Exit(1)
	}
}
