// Disposable producer-calibration app, never Cortexium product code. The future
// App must implement its thin fixture adapter over normal Genny construction;
// this deliberately small HTTP state machine tests the fixed observer itself.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

type run struct {
	ID, Input, State, Result, Failure string
	Version, Invocations              int
	Accepted                          int
}
type state struct {
	Runs             map[string]*run
	Current          string
	Workspaces       map[string]string
	Member, Grant    bool
	Function, Worker string
}
type app struct {
	sync.Mutex
	s              state
	file, frontend string
}

func main() {
	listen := flag.String("listen", "127.0.0.1:0", "")
	data := flag.String("data-dir", "", "")
	frontend := flag.String("frontend-dir", "", "")
	ready := flag.String("ready-file", "", "")
	fixture := flag.Bool("fixture", false, "")
	flag.Parse()
	if !*fixture || !strings.HasPrefix(*listen, "127.0.0.1:") || !filepath.IsAbs(*data) || !filepath.IsAbs(*frontend) || !filepath.IsAbs(*ready) {
		panic("owned fixture arguments required")
	}
	a := &app{file: filepath.Join(*data, "synthetic.json"), frontend: *frontend, s: state{Runs: map[string]*run{}, Workspaces: map[string]string{}, Member: true, Grant: true, Function: "report-v1", Worker: "fixture-review"}}
	if b, e := os.ReadFile(a.file); e == nil {
		if json.Unmarshal(b, &a.s) != nil {
			panic("invalid retained fixture")
		}
	}
	l, err := net.Listen("tcp", *listen)
	if err != nil {
		panic(err)
	}
	b, _ := json.Marshal(map[string]any{"pid": os.Getpid(), "origin": "http://" + l.Addr().String()})
	if err = os.WriteFile(*ready, b, 0600); err != nil {
		panic(err)
	}
	if err = http.Serve(l, a); err != nil {
		panic(err)
	}
}

func (a *app) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/state" && r.URL.Path != "/action" {
		if strings.HasPrefix(r.URL.Path, "/assets/") {
			http.FileServer(http.Dir(a.frontend)).ServeHTTP(w, r)
			return
		}
		http.ServeFile(w, r, filepath.Join(a.frontend, "index.html"))
		return
	}
	a.Lock()
	defer a.Unlock()
	w.Header().Set("Content-Type", "application/json")
	var in struct {
		Action, Email, Password, Workspace, Input, Function, Worker, Run string
		Version                                                          int
	}
	if r.Method == "POST" {
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&in) != nil {
			http.Error(w, "bad input", 400)
			return
		}
	}
	user := ""
	if c, e := r.Cookie("fixture-session"); e == nil && (c.Value == "alice" || c.Value == "bob") {
		user = c.Value
	}
	if in.Action == "login" {
		user = strings.TrimSuffix(in.Email, "@fixture.invalid")
		if (user != "alice" && user != "bob") || in.Password != "Runner-fixture-only-12" {
			http.Error(w, "denied", 401)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "fixture-session", Value: user, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
	}
	if user == "" {
		http.Error(w, "sign in", 401)
		return
	}
	if a.s.Workspaces[user] == "" {
		a.s.Workspaces[user] = "personal-" + user
	}
	if in.Action == "workspace" {
		a.s.Workspaces[user] = in.Workspace
	}
	if (user == "bob" && a.s.Workspaces[user] == "shared-alpha" && !a.s.Member) || ((r.URL.Query().Get("run") != "" || in.Run != "") && a.s.Workspaces[user] != "shared-alpha") {
		http.Error(w, "tenant access denied", 403)
		return
	}
	if (in.Action == "remove-bob" || in.Action == "revoke-worker-grant" || in.Action == "grant-worker") && (user != "alice" || a.s.Workspaces[user] != "shared-alpha") {
		http.Error(w, "owner required", 403)
		return
	}
	current := a.s.Runs[a.s.Current]
	switch in.Action {
	case "remove-bob":
		a.s.Member = false
	case "revoke-worker-grant":
		a.s.Grant = false
	case "grant-worker":
		a.s.Grant = true
	case "new-flow":
		a.s.Function = "report-v1"
		a.s.Worker = "fixture-review"
		a.s.Current = ""
	case "save-flow":
		a.s.Function = in.Function
		a.s.Worker = in.Worker
	case "run-flow":
		id := "run-" + strconv.Itoa(len(a.s.Runs)+1)
		current = &run{ID: id, Input: in.Input, Version: 1, Invocations: 1, State: "Awaiting acceptance", Result: "Report: " + strings.TrimSpace(in.Input)}
		if !a.s.Grant {
			current.State, current.Failure, current.Invocations = "Blocked", "Worker grant revoked", 0
		} else if a.s.Worker == "fixture-unsupported" {
			current.State, current.Failure, current.Invocations = "Blocked", "unsupported capability", 0
		} else if a.s.Function == "fixture-failure-v1" {
			current.State, current.Failure = "Failed", "named formatter failed"
		} else {
			switch in.Input {
			case "Receipt loss fixture":
				current.State = "Receipt pending"
			case "Pending cancellation fixture":
				current.State = "Running"
			case "Uncertain effect fixture":
				current.State = "Uncertain"
			}
		}
		a.s.Runs[id] = current
		a.s.Current = id
	case "request-revision":
		if current != nil {
			current.Version++
			current.Result = "Revised report: " + current.Input
			current.State = "Awaiting acceptance"
			current.Invocations++
		}
	case "accept-result":
		if current == nil || current.Version != in.Version {
			http.Error(w, "exact version conflict", 409)
			return
		} else if current.State != "Awaiting acceptance" {
			http.Error(w, "invalid state", 409)
			return
		} else {
			current.State = "Accepted"
			current.Accepted = current.Version
		}
	case "recover-receipt":
		if current != nil && current.State == "Receipt pending" {
			current.State = "Awaiting acceptance"
		}
	case "cancel-run":
		if current != nil && current.State == "Running" {
			current.State = "Cancelled"
		}
	case "open-run":
		a.s.Current = in.Run
	}
	if id := r.URL.Query().Get("run"); id != "" {
		a.s.Current = id
	}
	b, err := json.Marshal(a.s)
	if err != nil || os.WriteFile(a.file, b, 0600) != nil {
		http.Error(w, "persist fixture", 500)
		return
	}
	visibleRuns := map[string]*run{}
	var visibleCurrent *run
	if a.s.Workspaces[user] == "shared-alpha" {
		visibleRuns = a.s.Runs
		visibleCurrent = a.s.Runs[a.s.Current]
	}
	out := map[string]any{"User": user, "Workspace": a.s.Workspaces[user], "Member": a.s.Member, "Grant": a.s.Grant, "Function": a.s.Function, "Worker": a.s.Worker, "Current": visibleCurrent, "Runs": visibleRuns}
	if err = json.NewEncoder(w).Encode(out); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
}
