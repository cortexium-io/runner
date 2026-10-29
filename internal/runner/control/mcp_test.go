package control

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
	"github.com/cortexium-io/runner/internal/runner/config"
	"github.com/cortexium-io/runner/internal/runner/execution"
	"github.com/cortexium-io/runner/internal/runner/sqlite"
)

// Owns the actual MCP wire/client contract and local access boundary. Execution
// failures/recovery are tested once in the core integration suite.
func TestMCPControlRoundTrip(t *testing.T) {
	loader, err := config.New("../../../examples/demo/instance.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := loader.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Instance.Storage.MinFree = 0
	dir := filepath.Join(t.TempDir(), "state")
	store, err := sqlite.Open(dir, cfg.Instance.Storage)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	disk, err := runner.OpenDisk(dir, cfg.Instance.Storage)
	if err != nil {
		t.Fatal(err)
	}
	backend := execution.New(disk, func(ctx context.Context, id string) ([]byte, error) {
		return disk.Read("artifacts/"+id, disk.Policy.MaxArtifact)
	})
	core, err := runner.New(context.Background(), store, backend, disk, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer core.Close()
	token := strings.Repeat("x", 52)
	server := httptest.NewServer(Handler(core, token, func(file, field string, value json.RawMessage) error {
		if file != "instance.yaml" || field != "concurrency" || string(value) != "3" {
			return errors.New("configuration callback changed arguments")
		}
		return nil
	}))
	defer server.Close()
	for _, tc := range []struct {
		name, auth, host, origin string
		want                     int
	}{
		{"missing token", "", "", "", 401},
		{"bare token", token, "", "", 401},
		{"foreign host", "Bearer " + token, "evil.example:1234", "", 403},
		{"foreign origin", "Bearer " + token, "", "https://evil.example", 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodPost, server.URL+"/mcp", strings.NewReader(`{}`))
			req.Header.Set("Authorization", tc.auth)
			req.Header.Set("Origin", tc.origin)
			if tc.host != "" {
				req.Host = tc.host
			}
			response, err := server.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if response.StatusCode != tc.want {
				t.Fatalf("got HTTP %d", response.StatusCode)
			}
		})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	session, err := Connect(ctx, server.URL+"/mcp", token)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	args := StartArgs{RequestID: "wire-start", Project: "demo", Inputs: map[string]any{"name": "Hello over MCP"}}
	var started runner.Summary
	call := func(name string, args, out any) {
		t.Helper()
		b, err := Call(ctx, session, name, args)
		if err != nil {
			t.Fatal(name, err)
		}
		if err = json.Unmarshal(b, out); err != nil {
			t.Fatal(err)
		}
	}
	call("runner_start", args, &started)
	var replay runner.Summary
	call("runner_start", args, &replay)
	if replay.ID != started.ID {
		t.Fatal("wire replay created another run")
	}
	for !startedStatusTerminal(started.Status) {
		call("runner_wait", WaitArgs{RunID: started.ID, After: started.Revision}, &started)
	}
	var result runner.Run
	call("runner_inspect", RunArgs{RunID: started.ID}, &result)
	if result.Status != "Completed" || string(result.Outputs["result"].Inline) != `"Hello over MCP"` || len(result.Attempts) != 2 {
		t.Fatalf("bad result: %+v", result)
	}
	var status runner.Status
	call("runner_status", struct{}{}, &status)
	if status.TotalRuns != 1 {
		t.Fatal("status disagrees with core")
	}
	var edited map[string]string
	call("runner_configure", SetArgs{File: "instance.yaml", Field: "concurrency", Value: 3}, &edited)
	if edited["file"] != "instance.yaml" {
		t.Fatal("configuration response missing")
	}
}

func startedStatusTerminal(status string) bool {
	return status == "Completed" || status == "Failed" || status == "Cancelled" || status == "Waiting"
}
