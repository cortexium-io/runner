package execution

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
)

type heldCommandPeer struct {
	done, entered, terminate, settled chan struct{}
	once                              sync.Once
}

func (p *heldCommandPeer) Done() <-chan struct{} { return p.done }
func (p *heldCommandPeer) Close()                { p.once.Do(func() { close(p.done) }) }
func (p *heldCommandPeer) Call(ctx context.Context, method string, args, out any) error {
	switch method {
	case "command/exec":
		close(p.entered)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-p.done:
			return errors.New("connection lost")
		case <-p.settled:
		}
		b := []byte(`{"exitCode":0}`)
		return json.Unmarshal(b, out)
	case "command/exec/terminate":
		close(p.terminate)
		return nil
	}
	return errors.New("unexpected command method")
}

// Acknowledged termination and a dead connection must never be mistaken for
// observed command settlement; that would permit cleanup under a live writer.
func TestNativeCommandSettlement(t *testing.T) {
	for _, lost := range []bool{false, true} {
		name := "termination acknowledged"
		if lost {
			name = "connection lost"
		}
		t.Run(name, func(t *testing.T) {
			d := workspaceDisk(t)
			c := NewCodex(d, &runner.CodexSettings{StorageLimit: 16 << 20})
			defer func() {
				ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
				defer cancel()
				_ = c.Close(ctx)
			}()
			id := "attempt_command"
			j := nativeJob{
				Assignment: runner.Assignment{
					AttemptID: id,
					Setup:     runner.ExecutionSetup{Profile: runner.Profile{Harness: "go-check", Access: "workspace-read"}},
				},
				Stage: "checking",
			}
			c.mu.Lock()
			e := c.saveLocked(j)
			c.mu.Unlock()
			if e != nil {
				t.Fatal(e)
			}
			p := &heldCommandPeer{
				done:      make(chan struct{}),
				entered:   make(chan struct{}),
				terminate: make(chan struct{}),
				settled:   make(chan struct{}),
			}
			defer p.Close()
			result := make(chan error, 1)
			go func() {
				var out map[string]any
				result <- c.command(context.Background(), id, p, map[string]any{}, &out)
			}()
			<-p.entered
			if lost {
				p.Close()
				if e = <-result; e == nil {
					t.Fatal("lost response returned success")
				}
				if e = c.Stop(context.Background(), id); e == nil {
					t.Fatal("lost command claimed stoppable")
				}
			} else {
				if e = c.Stop(context.Background(), id); e != nil {
					t.Fatal(e)
				}
				<-p.terminate
			}
			c.mu.Lock()
			saved, _ := c.loadLocked(id)
			c.mu.Unlock()
			if saved.End != nil {
				t.Fatal("command ended before settlement")
			}
			if !lost {
				close(p.settled)
				if e = <-result; !errors.Is(e, context.Canceled) {
					t.Fatal(e)
				}
				c.mu.Lock()
				saved, _ = c.loadLocked(id)
				c.mu.Unlock()
				if saved.End == nil || saved.End.Kind != "stopped" {
					t.Fatal("settlement not retained")
				}
			}
		})
	}
}

// Historical terminal state from a replacement server cannot prove that the old
// server's children stopped. Even a new interrupt event must preserve uncertainty.
func TestWritableServiceReplacementDoesNotSettleOriginalChildren(t *testing.T) {
	d := workspaceDisk(t)
	c := NewCodex(d, &runner.CodexSettings{StorageLimit: 16 << 20})
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		_ = c.Close(ctx)
	}()
	c.generation = "replacement"
	id := "attempt_original"
	j := nativeJob{
		Assignment: runner.Assignment{AttemptID: id},
		Workspace:  &workspaceRecord{WorkDir: "owned-source"},
		Stage:      "active",
		Generation: "original",
		ThreadID:   "old-thread",
		TurnID:     "old-turn",
	}
	c.mu.Lock()
	e := c.saveLocked(j)
	c.mu.Unlock()
	if e != nil {
		t.Fatal(e)
	}
	c.acceptTurn(id, nativeTurn{ID: "old-turn", Status: "interrupted"})
	c.mu.Lock()
	saved, _ := c.loadLocked(id)
	c.mu.Unlock()
	if saved.End != nil || saved.Reason == "" {
		t.Fatal("replacement server settled an original child")
	}
}
