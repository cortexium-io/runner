package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/coder/websocket"
	"net"
	"net/http"
	"path/filepath"
	"sync"
)

type codexPeer interface {
	Call(context.Context, string, any, any) error
	Close()
	Done() <-chan struct{}
}
type rpcMessage struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *rpcError       `json:"error,omitempty"`
}
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("Codex RPC %d: %s", e.Code, e.Message) }

// wire multiplexes bounded native messages. It keeps no transcript and never
// retries a request: a lost turn/start response belongs to reconciliation.
type wire struct {
	conn    *websocket.Conn
	mu      sync.Mutex
	write   sync.Mutex
	next    int64
	pending map[string]chan rpcMessage
	done    chan struct{}
	onEvent func(rpcMessage)
	failure error
}

func dialCodex(ctx context.Context, socket string, onEvent func(rpcMessage)) (codexPeer, error) {
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	c, _, err := websocket.Dial(ctx, "ws://localhost/", &websocket.DialOptions{HTTPClient: &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}})
	if err != nil {
		transport.CloseIdleConnections()
		return nil, err
	}
	c.SetReadLimit(1 << 20)
	w := &wire{conn: c, pending: map[string]chan rpcMessage{}, done: make(chan struct{}), onEvent: onEvent}
	go w.read()
	var init struct {
		CodexHome string `json:"codexHome"`
	}
	if err = w.Call(ctx, "initialize", map[string]any{"clientInfo": map[string]string{"name": "cortexium_runner", "version": "0.1.0"}, "capabilities": map[string]bool{"experimentalApi": true}}, &init); err != nil {
		w.Close()
		return nil, err
	}
	expected, e := filepath.EvalSymlinks(filepath.Join(filepath.Dir(filepath.Dir(socket)), "native"))
	if e != nil || init.CodexHome != expected {
		w.Close()
		return nil, errors.New("native connection did not confirm this instance's private Codex home")
	}
	if err = w.send(ctx, rpcMessage{Method: "initialized", Params: json.RawMessage(`{}`)}); err != nil {
		w.Close()
		return nil, err
	}
	return w, nil
}
func (w *wire) Done() <-chan struct{} { return w.done }
func (w *wire) Close()                { _ = w.conn.CloseNow() }
func (w *wire) send(ctx context.Context, m rpcMessage) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	w.write.Lock()
	defer w.write.Unlock()
	return w.conn.Write(ctx, websocket.MessageText, b)
}
func (w *wire) Call(ctx context.Context, method string, params, out any) error {
	b, err := json.Marshal(params)
	if err != nil {
		return err
	}
	w.mu.Lock()
	w.next++
	id := fmt.Sprint(w.next)
	ch := make(chan rpcMessage, 1)
	w.pending[id] = ch
	w.mu.Unlock()
	defer func() { w.mu.Lock(); delete(w.pending, id); w.mu.Unlock() }()
	if err = w.send(ctx, rpcMessage{ID: json.RawMessage(id), Method: method, Params: b}); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-w.done:
		w.mu.Lock()
		cause := w.failure
		w.mu.Unlock()
		if cause != nil {
			return fmt.Errorf("Codex connection closed: %w", cause)
		}
		return errors.New("Codex connection closed; execution outcome requires reconciliation")
	case m := <-ch:
		if m.Error != nil {
			return m.Error
		}
		if out == nil {
			return nil
		}
		return json.Unmarshal(m.Result, out)
	}
}
func (w *wire) read() {
	defer close(w.done)
	defer w.Close()
	for {
		kind, b, err := w.conn.Read(context.Background())
		if err != nil {
			w.mu.Lock()
			w.failure = err
			w.mu.Unlock()
			return
		}
		if kind != websocket.MessageText {
			w.mu.Lock()
			w.failure = fmt.Errorf("unexpected native frame type %d", kind)
			w.mu.Unlock()
			return
		}
		var m rpcMessage
		if json.Unmarshal(b, &m) != nil {
			w.mu.Lock()
			w.failure = errors.New("native message does not match JSON-RPC envelope")
			w.mu.Unlock()
			return
		}
		if len(m.ID) > 0 && m.Method != "" {
			// Unattended input-only execution never grants an unsolicited tool,
			// approval or credential request. Native managed auth refreshes itself.
			if err = w.send(context.Background(), rpcMessage{ID: m.ID, Error: &rpcError{-32601, "Runner input-only profile does not permit server requests"}}); err != nil {
				return
			}
		} else if len(m.ID) > 0 {
			w.mu.Lock()
			ch := w.pending[string(m.ID)]
			w.mu.Unlock()
			if ch != nil {
				select {
				case ch <- m:
				default:
					return
				}
			}
		} else if w.onEvent != nil {
			w.onEvent(m)
		}
	}
}
