//go:build darwin || linux

package release

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This opt-in test executes the actual installed provider, sandbox, package
// builder, Docker qualification and headless browser. Its synthetic publication
// journal lives in a separate qualification directory. It never publishes,
// merges, stops production, migrates production or calls the deploy controller.
func TestDeliveryPlatformQualification(t *testing.T) {
	policy := os.Getenv("RUNNER_DELIVERY_PLATFORM_POLICY")
	if policy == "" {
		t.Skip("explicit private qualification policy required; no model turns")
	}
	op, err := Load(policy)
	if err != nil {
		t.Fatal(err)
	}
	c := op.Backend.(*Live).Config
	if c.Delivery == nil || !strings.Contains(c.StateDir, "-qualification/") || !strings.Contains(c.Delivery.Root, "-qualification/") {
		t.Fatal("platform test requires separate qualification storage")
	}
	binary := os.Getenv("RUNNER_DELIVERY_PLATFORM_BINARY")
	if !filepath.IsAbs(binary) {
		t.Fatal("explicit qualified operator executable required")
	}
	run := "lab_" + time.Now().UTC().Format("20060102T150405.000000000")
	run = strings.ReplaceAll(run, ".", "_")
	call := func(q Request) Response {
		t.Helper()
		data, _ := json.Marshal(q)
		cmd := exec.CommandContext(t.Context(), binary, "--config", policy)
		cmd.Stdin = bytes.NewReader(data)
		var output, diagnostic bytes.Buffer
		cmd.Stdout, cmd.Stderr = &output, &diagnostic
		if err := cmd.Run(); err != nil {
			t.Fatal("operator transport", err, diagnostic.String())
		}
		var response Response
		if json.Unmarshal(output.Bytes(), &response) != nil || response.Key != q.Key {
			t.Fatal("operator response differs", output.String())
		}
		return response
	}
	wait := func(q Request) Response {
		t.Helper()
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			r := call(q)
			if r.State == "finished" {
				if r.Failed || r.Outcome != "" {
					t.Fatal(q.Action, r.Reason, "private journal:", filepath.Join(c.StateDir, q.Key+".json"))
				}
				return r
			}
			if r.State != "running" {
				t.Fatal(q.Action, r.State, r.Reason)
			}
			select {
			case <-t.Context().Done():
				t.Fatal(t.Context().Err())
			case <-ticker.C:
			}
		}
	}
	readiness := Request{Project: c.Project, RunID: run, Key: run + "_admit", Action: "readiness", Source: c.Head}
	wait(readiness)
	if os.Getenv("RUNNER_DELIVERY_PLATFORM_READINESS_ONLY") == "1" {
		return
	}
	publication := Receipt{
		Key: run + "_published", RunID: run, Action: "publish", Policy: op.PolicyHash,
		Head: c.Head, Base: c.Base, Source: c.Base, PR: c.PR,
	}
	if err = writeJSON(filepath.Join(c.StateDir, publication.Key+".json"), Job{
		Request: Request{Project: c.Project, RunID: run, Key: publication.Key, Action: "publish", Source: c.Base, Candidate: c.Head},
		Receipt: publication, Done: true,
	}); err != nil {
		t.Fatal(err)
	}
	previous, _ := json.Marshal(publicReceipt(publication, true))
	qualified := wait(Request{Project: c.Project, RunID: run, Key: run + "_qualify", Action: "qualify", Source: c.Base, Previous: previous})
	var result Receipt
	if json.Unmarshal(qualified.Receipt, &result) != nil {
		t.Fatal("qualification identity differs")
	}
	data, err := readFile(filepath.Join(c.StateDir, result.Key+".json"), 32<<10)
	var saved Job
	if err != nil || json.Unmarshal(data, &saved) != nil || saved.Receipt.Head != c.Head || saved.Receipt.Image == "" {
		t.Fatal("private qualification identity differs", err)
	}
	t.Log("Actual qualification passed; retained operator journal:", filepath.Join(c.StateDir, result.Key+".json"))
}
