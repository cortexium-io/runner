//go:build darwin || linux

package execution

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
	"github.com/cortexium-io/runner/internal/runner/control"
)

// An explicit owner proof restricts this opt-in to rejoining a saved merge gate.
// It never selects new work or starts a native model turn.
type intakeRejoinProof struct {
	Config        string `json:"config"`
	ConfigSHA256  string `json:"config_sha256"`
	Auth          string `json:"auth"`
	Token         string `json:"token"`
	Request       string `json:"request"`
	RequestSHA256 string `json:"request_sha256"`
	RequestID     string `json:"request_id"`
	RunID         string `json:"run_id"`
	DecisionID    string `json:"decision_id"`
	Head          string `json:"head"`
	Image         string `json:"image"`
	PR            int    `json:"pr"`
	TotalRuns     int    `json:"total_runs"`
	Attempts      int    `json:"attempts"`
}

func TestNativeInstalledIntakeRejoinsQualifiedRun(t *testing.T) {
	file := os.Getenv("RUNNER_INTAKE_REJOIN_PROOF")
	if file == "" {
		t.Skip("explicit installed-intake proof required; zero model turns and exact request replay only")
	}
	read := func(file string, limit int64) []byte {
		t.Helper()
		info, err := os.Stat(file)
		if err != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > limit {
			t.Fatal("intake replay proof input is unavailable or exceeds its bound")
		}
		data, err := os.ReadFile(file)
		if err != nil || int64(len(data)) != info.Size() {
			t.Fatal("intake replay proof input changed while reading")
		}
		return data
	}
	var proof intakeRejoinProof
	if err := json.Unmarshal(read(file, 16<<10), &proof); err != nil {
		t.Fatal("invalid intake replay proof")
	}
	for _, input := range []string{proof.Config, proof.Auth, proof.Token, proof.Request} {
		if !filepath.IsAbs(input) {
			t.Fatal("intake replay proof requires explicit absolute input paths")
		}
	}
	if !publicationDigest.MatchString(proof.ConfigSHA256) || !publicationDigest.MatchString(proof.RequestSHA256) ||
		!publicationID.MatchString(proof.RunID) || proof.RequestID == "" || proof.DecisionID == "" ||
		!publicationCommit.MatchString(proof.Head) || len(proof.Image) != 71 || proof.Image[:7] != "sha256:" ||
		!publicationDigest.MatchString(proof.Image[7:]) || proof.PR < 1 || proof.TotalRuns < 1 || proof.Attempts < 1 {
		t.Fatal("intake replay proof lacks exact saved release identities")
	}
	config := read(proof.Config, 128<<10)
	if captureDigest(config) != proof.ConfigSHA256 || captureDigest(read(proof.Request, 128<<10)) != proof.RequestSHA256 {
		t.Fatal("installed intake configuration or canonical request differs from owner proof")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(filepath.Join(home, ".codex"), "ir-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	clientHome := filepath.Join(root, "client", "home")
	if err = os.MkdirAll(clientHome, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(filepath.Join(root, "client", "empty"), 0500); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(clientHome, "config.toml"), config, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(proof.Auth, filepath.Join(clientHome, "auth.json")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RUNNER_COORDINATOR_TOKEN_FILE", proof.Token)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	qualifyCoordinatorTools(t, ctx, root, os.Getenv("RUNNER_PREFLIGHT_CODEX"), clientHome, proof)
}

func qualifyCoordinatorRejoin(t *testing.T, ctx context.Context, peer codexPeer, threadID string, proof intakeRejoinProof) {
	t.Helper()
	call := func(tool string, args, output any) {
		t.Helper()
		var response struct {
			IsError bool `json:"isError"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		}
		if err := peer.Call(ctx, "mcpServer/tool/call", map[string]any{"threadId": threadID, "server": "runner", "tool": tool, "arguments": args}, &response); err != nil || response.IsError ||
			len(response.Content) != 1 || response.Content[0].Type != "text" || json.Unmarshal([]byte(response.Content[0].Text), output) != nil {
			t.Fatalf("restricted native intake could not verify %s", tool)
		}
	}
	var before, after runner.Status
	call("runner_status", map[string]any{}, &before)
	if before.TotalRuns != proof.TotalRuns || before.ConfigError != "" {
		t.Fatal("live Runner census or selected configuration differs from owner proof")
	}
	check := func(result control.Result) {
		t.Helper()
		if result.ID != proof.RunID || result.Status != "Waiting" || result.Attempts != proof.Attempts || !result.ExecutionSettled || result.Reconciling ||
			result.Decision == nil || result.Decision.ID != proof.DecisionID || result.Decision.Step != "prepare" || result.Decision.Next != "merge" {
			t.Fatal("intake did not rejoin the exact settled protected merge gate")
		}
		receipt := result.DecisionOutputs["receipt"]
		var release struct {
			Head  string `json:"head"`
			Image string `json:"image"`
			PR    int    `json:"pr"`
		}
		if receipt.Truncated || json.Unmarshal([]byte(receipt.Preview), &release) != nil || release.Head != proof.Head || release.Image != proof.Image || release.PR != proof.PR {
			t.Fatal("intake release decision differs from qualified PR, head or image")
		}
	}
	var original control.Result
	call("runner_result", control.ResultArgs{RunID: proof.RunID}, &original)
	check(original)
	data, err := os.ReadFile(proof.Request)
	if err != nil || captureDigest(data) != proof.RequestSHA256 {
		t.Fatal("canonical intake request changed before replay")
	}
	var request struct {
		RequestID string            `json:"request_id"`
		ProjectID string            `json:"project_id"`
		ProcessID string            `json:"process_id"`
		SourceRef string            `json:"source_ref"`
		Inputs    map[string]any    `json:"inputs"`
		Artifacts map[string]string `json:"artifacts"`
	}
	if json.Unmarshal(data, &request) != nil || request.RequestID != proof.RequestID || request.ProjectID == "" || request.ProcessID == "" || !publicationCommit.MatchString(request.SourceRef) {
		t.Fatal("canonical saved request identity is incomplete")
	}
	args := control.StartArgs{RequestID: request.RequestID, Project: request.ProjectID, Process: request.ProcessID, SourceRef: request.SourceRef, Inputs: request.Inputs, Artifacts: request.Artifacts}
	for _, tool := range []string{"runner_run", "runner_result"} {
		var result control.Result
		if tool == "runner_run" {
			call(tool, args, &result)
		} else {
			call(tool, control.ResultArgs{RunID: proof.RunID, Wait: true}, &result)
		}
		check(result)
		if result.Revision != original.Revision || result.Usage != original.Usage || !reflect.DeepEqual(result.DecisionOutputs, original.DecisionOutputs) {
			t.Fatal("exact replay changed saved work, usage or protected decision evidence")
		}
	}
	call("runner_status", map[string]any{}, &after)
	if after.TotalRuns != before.TotalRuns || after.ConfigError != "" || !reflect.DeepEqual(after.Runs, before.Runs) {
		t.Fatal("restricted intake replay created or advanced Runner work")
	}
	t.Logf("fresh restricted intake replayed %s and rejoined %s: runs=%d attempts=%d; protected decision unchanged; zero model turns", proof.RequestID, proof.RunID, proof.TotalRuns, proof.Attempts)
}
