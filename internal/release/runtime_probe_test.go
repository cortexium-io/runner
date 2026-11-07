package release

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func nativeProbeFixture(t *testing.T) []byte {
	t.Helper()
	observations := make([]map[string]any, 239)
	for i := range observations {
		observations[i] = map[string]any{
			"choice":       nativeProbeChoice{Game: "binary", Family: "bin2", Size: i + 1, Band: "easy"},
			"milliseconds": 1, "identicalReopen": true, "path": "/binary/fixture",
		}
	}
	for i, c := range []nativeProbeChoice{
		{Game: "nonogram", Family: "ngm2", Size: 15, Band: "hard"},
		{Game: "nonogram", Family: "ngm2", Size: 25, Band: "hard"},
		{Game: "kakuro", Family: "kkr2", Size: 30, Band: "easy"},
	} {
		observations[i] = map[string]any{"choice": c, "milliseconds": 10000 + i%2, "identicalReopen": false, "error": "/api/games/next HTTP 408, wanted 200"}
	}
	raw, err := json.Marshal(map[string]any{
		"status": "failed", "choices": 239, "passed": 236, "maxMs": 10001, "p95Ms": 1,
		"publicRoutes": []string{}, "retiredEndpoint404": true, "foreignOrigin403": true, "observations": observations,
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestFailedNativeProbeReportFactsAndConsistency(t *testing.T) {
	raw := nativeProbeFixture(t)
	r, err := validateFailedNativeProbe(raw)
	if err != nil {
		t.Fatal(err)
	}
	note := failedNativeProbeNote(r, Digest(raw))
	for _, want := range []string{"236/239", "nonogram/ngm2 size15 hard HTTP 408 10000ms", "nonogram/ngm2 size25 hard HTTP 408 10001ms", "kakuro/kkr2 size30 easy HTTP 408 10000ms", Digest(raw), "cause/scope unestablished"} {
		if !strings.Contains(note, want) {
			t.Fatal("missing sanitized failed-probe fact", want, note)
		}
	}
	if len(note) > 512 {
		t.Fatal("failed-probe note exceeds model receipt allowance", len(note))
	}
	mutations := map[string]func(map[string]any){
		"passed count":     func(m map[string]any) { m["passed"] = 237 },
		"choice count":     func(m map[string]any) { m["choices"] = 238 },
		"maximum":          func(m map[string]any) { m["maxMs"] = 9999 },
		"percentile":       func(m map[string]any) { m["p95Ms"] = 2 },
		"status":           func(m map[string]any) { m["status"] = "passed" },
		"missing status":   func(m map[string]any) { delete(m, "status") },
		"missing routes":   func(m map[string]any) { delete(m, "publicRoutes") },
		"missing security": func(m map[string]any) { delete(m, "foreignOrigin403") },
		"unknown field":    func(m map[string]any) { m["untrusted"] = true },
		"failed security":  func(m map[string]any) { m["retiredEndpoint404"] = false },
		"duplicate choice": func(m map[string]any) { a := m["observations"].([]any); a[1] = a[0] },
		"missing identity": func(m map[string]any) {
			delete(m["observations"].([]any)[0].(map[string]any)["choice"].(map[string]any), "maximum")
		},
		"null identity": func(m map[string]any) {
			m["observations"].([]any)[0].(map[string]any)["choice"].(map[string]any)["maximum"] = nil
		},
		"foreign game": func(m map[string]any) {
			m["observations"].([]any)[0].(map[string]any)["choice"].(map[string]any)["game"] = "foreign"
		},
		"negative duration": func(m map[string]any) { m["observations"].([]any)[0].(map[string]any)["milliseconds"] = -1 },
		"missing duration":  func(m map[string]any) { delete(m["observations"].([]any)[0].(map[string]any), "milliseconds") },
		"missing reopen":    func(m map[string]any) { delete(m["observations"].([]any)[0].(map[string]any), "identicalReopen") },
		"failed reopened":   func(m map[string]any) { m["observations"].([]any)[0].(map[string]any)["identicalReopen"] = true },
		"path identity":     func(m map[string]any) { m["observations"].([]any)[3].(map[string]any)["path"] = "/nonogram/foreign" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			var m map[string]any
			if err := json.Unmarshal(raw, &m); err != nil {
				t.Fatal(err)
			}
			mutate(m)
			data, _ := json.Marshal(m)
			if _, err := validateFailedNativeProbe(data); err == nil {
				t.Fatal("incomplete/inconsistent failed report accepted")
			}
		})
	}
	for _, data := range [][]byte{raw[:len(raw)-1], append(bytes.Clone(raw), []byte("{}")...), bytes.Repeat([]byte("x"), runtimeProbeOutputLimit+1)} {
		if _, err := validateFailedNativeProbe(data); err == nil {
			t.Fatal("truncated, trailing or oversized report accepted")
		}
	}
}

type runtimeProbeHarness struct {
	b         Live
	j         Job
	raw       []byte
	runErr    error
	inspect   runtimeProbeInspection
	runs      int
	removes   int
	queries   int
	removeErr error
}

func newRuntimeProbeHarness(t *testing.T) *runtimeProbeHarness {
	t.Helper()
	h := &runtimeProbeHarness{
		j:      Job{Request: Request{RunID: "own_run", Key: "own_attempt"}, Receipt: Receipt{Package: t.TempDir(), Image: "sha256:" + strings.Repeat("a", 64)}},
		raw:    nativeProbeFixture(t),
		runErr: &commandFailure{program: "docker", code: 1},
	}
	h.b.Config = Config{Docker: "docker", Delivery: &DeliveryConfig{Probe: "/owned/probe"}}
	h.inspect.ID, h.inspect.Name, h.inspect.Image = strings.Repeat("b", 64), "/runner-runtime-own_attempt", h.j.Receipt.Image
	h.inspect.Config.Labels = map[string]string{"cortexium.runner.run": h.j.Request.RunID, "cortexium.runner.attempt": h.j.Request.Key}
	running, exit := false, 1
	h.inspect.State.Status, h.inspect.State.Running, h.inspect.State.ExitCode = "exited", &running, &exit
	h.b.runCommand = func(_ context.Context, _ []byte, _ string, args ...string) ([]byte, error) {
		switch {
		case slices.Contains(args, "run"):
			h.runs++
			if !slices.Contains(args, "cortexium.runner.attempt="+h.j.Request.Key) || !slices.Contains(args, "container:runner-rehearse-server-own_attempt") || !slices.Contains(args, "type=bind,src=/owned/probe,dst=/probe,readonly") {
				t.Fatal("probe authority or fixed command changed", args)
			}
			return h.raw, h.runErr
		case slices.Contains(args, "inspect"):
			h.queries++
			data, _ := json.Marshal([]runtimeProbeInspection{h.inspect})
			return data, nil
		case slices.Contains(args, "rm"):
			h.removes++
			if args[len(args)-1] != h.inspect.ID {
				t.Fatal("cleanup did not use positively owned immutable ID", args)
			}
			return nil, h.removeErr
		default:
			t.Fatal("unexpected probe operation", args)
			return nil, nil
		}
	}
	return h
}

func TestRuntimeProbeRetainsSettledFailureWithoutRetryOrSourceCorrection(t *testing.T) {
	h := newRuntimeProbeHarness(t)
	err := h.b.runtimeProbe(t.Context(), &h.j, "runner-rehearse-server-own_attempt")
	var source *sourceFailure
	var uncertain *commandUncertain
	if err == nil || errors.As(err, &source) || errors.As(err, &uncertain) || !strings.Contains(err.Error(), "236/239") || h.runs != 1 || h.removes != 1 {
		t.Fatal("settled app failure must remain blocked, with no source routing or rerun", err, h.runs, h.removes)
	}
	for _, name := range []string{"runtime-http.json", "runtime-http.stdout.log"} {
		data, readErr := os.ReadFile(filepath.Join(h.j.Receipt.Package, name))
		if readErr != nil || !bytes.Equal(data, h.raw) {
			t.Fatal("complete failed runtime report was not retained", name, readErr)
		}
	}
	var metadata struct {
		Bytes    int
		SHA256   string
		Complete bool
	}
	data, _ := os.ReadFile(filepath.Join(h.j.Receipt.Package, "runtime-http.stdout.json"))
	if json.Unmarshal(data, &metadata) != nil || metadata.Bytes != len(h.raw) || metadata.SHA256 != Digest(h.raw) || !metadata.Complete {
		t.Fatal("missing complete failed stdout identity", string(data))
	}
}

func TestRuntimeProbePreservesSuccessContract(t *testing.T) {
	h := newRuntimeProbeHarness(t)
	h.raw, h.runErr = []byte(`{"status":"passed","choices":239,"passed":239}`), nil
	*h.inspect.State.ExitCode = 0
	if err := h.b.runtimeProbe(t.Context(), &h.j, "runner-rehearse-server-own_attempt"); err != nil || h.runs != 1 || h.removes != 1 {
		t.Fatal("existing pass contract changed", err, h.runs, h.removes)
	}
}

func TestRuntimeProbeRejectsUnestablishedEvidenceAndPreservesCleanupUncertainty(t *testing.T) {
	cases := map[string]func(*runtimeProbeHarness){
		"foreign run":       func(h *runtimeProbeHarness) { h.inspect.Config.Labels["cortexium.runner.run"] = "foreign" },
		"foreign attempt":   func(h *runtimeProbeHarness) { h.inspect.Config.Labels["cortexium.runner.attempt"] = "foreign" },
		"missing attempt":   func(h *runtimeProbeHarness) { delete(h.inspect.Config.Labels, "cortexium.runner.attempt") },
		"foreign image":     func(h *runtimeProbeHarness) { h.inspect.Image = "sha256:" + strings.Repeat("c", 64) },
		"foreign name":      func(h *runtimeProbeHarness) { h.inspect.Name = "/foreign" },
		"running":           func(h *runtimeProbeHarness) { *h.inspect.State.Running = true },
		"wrong exit":        func(h *runtimeProbeHarness) { *h.inspect.State.ExitCode = 0 },
		"missing exit":      func(h *runtimeProbeHarness) { h.inspect.State.ExitCode = nil },
		"transport":         func(h *runtimeProbeHarness) { h.runErr = &commandUncertain{program: "docker"} },
		"malformed report":  func(h *runtimeProbeHarness) { h.raw = h.raw[:len(h.raw)-1] },
		"exit1 pass report": func(h *runtimeProbeHarness) { h.raw = []byte(`{"status":"passed","choices":239,"passed":239}`) },
		"truncated bound": func(h *runtimeProbeHarness) {
			h.raw = append(h.raw, bytes.Repeat([]byte(" "), runtimeProbeOutputLimit)...)
		},
		"cleanup": func(h *runtimeProbeHarness) { h.removeErr = &commandFailure{program: "docker", code: 1} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			h := newRuntimeProbeHarness(t)
			mutate(h)
			err := h.b.runtimeProbe(t.Context(), &h.j, "runner-rehearse-server-own_attempt")
			var source *sourceFailure
			if err == nil || errors.As(err, &source) || h.runs != 1 {
				t.Fatal("unestablished probe must block without retry/source correction", err, h.runs)
			}
			if name == "cleanup" {
				var uncertain *commandUncertain
				if !errors.As(err, &uncertain) || h.removes != 1 {
					t.Fatal("failed cleanup lost uncertainty dominance", err)
				}
			} else {
				if _, statErr := os.Stat(filepath.Join(h.j.Receipt.Package, "runtime-http.json")); !errors.Is(statErr, os.ErrNotExist) {
					t.Fatal("unestablished bytes became accepted report evidence", statErr)
				}
			}
			if strings.HasPrefix(name, "foreign") || name == "missing attempt" || name == "running" || name == "wrong exit" || name == "missing exit" || name == "transport" {
				if h.removes != 0 {
					t.Fatal("unowned/unsettled resource deleted", h.removes)
				}
			}
			if name == "transport" {
				if h.queries != 0 {
					t.Fatal("transport bytes were adopted through probe inspection")
				}
				var metadata struct{ Complete bool }
				data, _ := os.ReadFile(filepath.Join(h.j.Receipt.Package, "runtime-http.stdout.json"))
				if json.Unmarshal(data, &metadata) != nil || metadata.Complete {
					t.Fatal("uncertain full bytes mislabeled complete evidence", string(data))
				}
			}
		})
	}
}

func TestFailedNativeProbeNoteBoundAndSanitization(t *testing.T) {
	r, err := validateFailedNativeProbe(nativeProbeFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	for i := range r.Observations {
		r.Observations[i].Error = "private response body and credentials"
		r.Observations[i].Choice.Family = strings.Repeat("x", 64)
		r.Observations[i].Choice.Band = strings.Repeat("y", 64)
	}
	*r.Passed = 0
	note := failedNativeProbeNote(r, strings.Repeat("a", 64))
	if len("qualify failed; bounded private diagnostics and output retained: "+note) > 512 || strings.Contains(note, "credentials") || !strings.Contains(note, "further failures retained") || !strings.Contains(note, strings.Repeat("a", 64)) {
		t.Fatal("blocked note leaked errors, omitted digest or exceeded bound", len(note), note)
	}
}
