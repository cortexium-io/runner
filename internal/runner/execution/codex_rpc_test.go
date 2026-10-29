//go:build darwin || linux

package execution

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// Owns Unix/WebSocket framing, handshake and refusal of unsolicited requests.
// The lifecycle matrix belongs to codex_test.go and is not repeated here.
func TestCodexWireUsesNativeProtocol(t *testing.T) {
	root, err := os.MkdirTemp("", "runner-wire-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	for _, dir := range []string{"jobs", "native"} {
		if err = os.Mkdir(filepath.Join(root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	socket := filepath.Join(root, "jobs", "codex.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	denied := make(chan struct{}, 1)
	handlerErr := make(chan error, 1)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			handlerErr <- err
			return
		}
		defer c.CloseNow()
		ctx := r.Context()
		for {
			_, b, err := c.Read(ctx)
			if err != nil {
				return
			}
			var m rpcMessage
			if err = json.Unmarshal(b, &m); err != nil {
				handlerErr <- err
				return
			}
			if m.Method == "initialized" {
				continue
			}
			if string(m.ID) == "777" {
				if m.Error == nil || m.Error.Code != -32601 {
					t.Error("native server request was not refused")
				}
				denied <- struct{}{}
				continue
			}
			var out any
			switch m.Method {
			case "initialize":
				home, _ := filepath.EvalSymlinks(filepath.Join(root, "native"))
				out = map[string]string{"codexHome": home}
			case "inspect":
				out = map[string]bool{"ok": true}
			default:
				t.Errorf("unexpected method %s", m.Method)
				return
			}
			encoded, _ := json.Marshal(out)
			reply, _ := json.Marshal(rpcMessage{ID: m.ID, Result: encoded})
			if err = c.Write(ctx, websocket.MessageText, reply); err != nil {
				return
			}
			if m.Method == "inspect" {
				request, _ := json.Marshal(rpcMessage{ID: json.RawMessage(`777`), Method: "item/permissions/requestApproval", Params: json.RawMessage(`{}`)})
				if err = c.Write(ctx, websocket.MessageText, request); err != nil {
					return
				}
			}
		}
	})}
	go func() { _ = server.Serve(listener) }()
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	peer, err := dialCodex(ctx, socket, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	var result struct {
		OK bool `json:"ok"`
	}
	if err = peer.Call(ctx, "inspect", map[string]any{}, &result); err != nil || !result.OK {
		t.Fatalf("wire roundtrip: %v", err)
	}
	select {
	case <-denied:
	case err := <-handlerErr:
		t.Fatal(err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}
