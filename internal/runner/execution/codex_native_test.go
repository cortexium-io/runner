//go:build darwin || linux

package execution

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
)

// Explicitly paid qualification only. The default suite never starts Codex.
// Three task executions total; no model-selection fallback or automatic repair.
type qualificationPeer struct {
	codexPeer
	calls *atomic.Int32
	lose  *atomic.Bool
}

func (p *qualificationPeer) Call(ctx context.Context, method string, args, out any) error {
	if method == "turn/start" {
		for {
			n := p.calls.Load()
			if n >= 3 {
				return errors.New("native qualification allowance exhausted")
			}
			if p.calls.CompareAndSwap(n, n+1) {
				break
			}
		}
	}
	err := p.codexPeer.Call(ctx, method, args, out)
	if method == "turn/start" && err == nil && p.lose != nil && p.lose.CompareAndSwap(true, false) {
		p.codexPeer.Close()
		return io.ErrUnexpectedEOF
	}
	return err
}
func instrumentNative(c *Codex, calls *atomic.Int32, lose *atomic.Bool) {
	c.connect = func(ctx context.Context, path string, notify func(rpcMessage)) (codexPeer, error) {
		p, err := dialCodex(ctx, path, notify)
		if err != nil {
			return nil, err
		}
		return &qualificationPeer{p, calls, lose}, nil
	}
}
func TestNativeCodexQualification(t *testing.T) {
	if os.Getenv("RUNNER_NATIVE_QUALIFY") != "1" {
		t.Skip("explicit opt-in required; up to three native Codex task executions")
	}
	userHome, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	cfg := nativeSnapshot()
	cfg.Instance.Codex.AuthFile = filepath.Join(userHome, ".codex", "auth.json")
	cfg.Instance.Storage.MinFree = 32 << 20
	var calls atomic.Int32
	for _, name := range []string{"success", "lost acknowledgement and reconnect", "cancellation and Runner restart"} {
		t.Run(name, func(t *testing.T) {
			root, err := os.MkdirTemp("/tmp", "runner-native-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(root)
			var lose atomic.Bool
			lose.Store(name == "lost acknowledgement and reconnect")
			rig := openNativeRig(t, filepath.Join(root, "state"), cfg, nil, func(s *Service) { instrumentNative(s.Codex, &calls, &lose) })
			current := rig
			defer func() { current.close() }()
			before := calls.Load()
			started := time.Now()
			run, err := rig.core.Start(runner.StartRequest{
				RequestID: name,
				Project:   "demo",
				Inputs:    map[string]runner.Value{"document": runner.Text("Booking Desk opens Tuesday at noon. Every reservation requires email confirmation.")},
			})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 110*time.Second)
			defer cancel()
			if name == "cancellation and Runner restart" {
				for {
					rig.service.Codex.mu.Lock()
					known := false
					for _, j := range rig.service.Codex.jobs {
						known = known || j.TurnID != ""
					}
					changed := rig.service.Codex.changed
					rig.service.Codex.mu.Unlock()
					if known {
						break
					}
					select {
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					case <-changed:
					}
				}
				if _, err = rig.core.Cancel(run.ID); err != nil {
					t.Fatal(err)
				}
				rig.close()
				current = openNativeRig(t, filepath.Join(root, "state"), cfg, nil, func(s *Service) { instrumentNative(s.Codex, &calls, nil) })
			}
			var done runner.Run
			for {
				done, err = current.core.Get(run.ID)
				if err != nil {
					t.Fatal(err)
				}
				if done.Terminal() && (allReleased(done) || done.CleanupError != "") {
					break
				}
				if _, err = current.core.Wait(ctx, run.ID, done.Revision); err != nil {
					t.Fatalf("native qualification: %v; last %s: %s", err, done.Status, done.Reason)
				}
			}
			if done.CleanupError != "" {
				t.Fatal("native cleanup failed:", done.CleanupError)
			}
			if name == "cancellation and Runner restart" {
				if done.Status != "Cancelled" || len(done.Attempts) != 1 {
					t.Fatalf("restart/cancellation changed execution: %s/%d", done.Status, len(done.Attempts))
				}
			} else {
				if done.Status != "Completed" {
					t.Logf("bounded native diagnostic: %s", current.service.Codex.tail.bytes())
					t.Fatal("native execution failed:", done.Reason)
				}
				var text string
				_ = json.Unmarshal(done.Outputs["result"].Inline, &text)
				for _, fact := range []string{"tuesday", "noon", "email"} {
					if !strings.Contains(strings.ToLower(text), fact) {
						t.Fatalf("summary lost %q: %s", fact, text)
					}
				}
			}
			if calls.Load()-before != 1 {
				t.Fatalf("case submitted %d native turns", calls.Load()-before)
			}
			a := done.Attempts[0]
			if a.NativeHandle == "" || a.Effective == nil || a.Effective.Model != "gpt-6.1-sol" || a.Effective.Reasoning != "xhigh" {
				t.Fatal("native execution identity/effective settings missing")
			}
			if name == "lost acknowledgement and reconnect" && lose.Load() {
				t.Fatal("lost-acknowledgement fault was not exercised")
			}
			_ = current.disk.Refresh()
			report := struct {
				Case           string       `json:"case"`
				WallMS         int64        `json:"wall_ms"`
				SubmittedTurns int32        `json:"submitted_turns"`
				Usage          runner.Usage `json:"usage"`
				NativePeak     int64        `json:"native_store_peak_bytes"`
				RetainedNative int64        `json:"native_bytes_before_service_shutdown"`
				Status         string       `json:"status"`
			}{
				name,
				time.Since(started).Milliseconds(),
				calls.Load() - before,
				a.Usage,
				a.NativePeakBytes,
				current.disk.NativeUsed(),
				done.Status,
			}
			b, _ := json.Marshal(report)
			t.Log(string(b))
		})
		if t.Failed() {
			break
		} // Do not spend the remaining allowance diagnosing a failure.
	}
	t.Logf("native task submissions: %d/3", calls.Load())
}
