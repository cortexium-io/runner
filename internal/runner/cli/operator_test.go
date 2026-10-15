package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner/engine"
	"github.com/cortexium-io/runner/internal/runner/store"
)

type readerFunc func([]byte) (int, error)

func (f readerFunc) Read(b []byte) (int, error) { return f(b) }

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(b []byte) (int, error) { return f(b) }

func previewValue(t *testing.T, output, label string) string {
	t.Helper()
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, label+": ") {
			return strings.TrimPrefix(line, label+": ")
		}
	}
	t.Fatal("missing preview", label, output)
	return ""
}

func TestConfigurationEditorPreviewCancellationConflictAndApply(t *testing.T) {
	_, path, cfg := commandFixture(t)
	h := &commandHarness{}
	run := func(input io.Reader, args ...string) (string, error) {
		t.Helper()
		var out bytes.Buffer
		err := (CLI{In: input, Out: &out, Harness: h}).Run(t.Context(), append([]string{"--config", path}, args...))
		return out.String(), err
	}
	if _, err := run(nil, "init", "--apply"); err != nil {
		t.Fatal(err)
	}
	original, _ := os.ReadFile(path)
	dbPath := filepath.Join(cfg.StateDir, "runner.db")
	originalDB, _ := os.ReadFile(dbPath)
	unchanged := func() {
		t.Helper()
		file, _ := os.ReadFile(path)
		db, _ := os.ReadFile(dbPath)
		if !bytes.Equal(original, file) || !bytes.Equal(originalDB, db) {
			t.Fatal("preview/cancellation changed file or authority")
		}
	}
	changes := []string{"config", "edit", "--set", "parallelism=2", "--set", "profile.careful=controlled high 2m", "--set", "repairs=1", "--set", "base-refreshes=2", "--set", "escalation=careful", "--set", "planner=build", "--set", "invocations=5"}
	out, err := run(nil, changes...)
	if err != nil {
		t.Fatal(err)
	}
	preview := previewValue(t, out, "Save preview")
	unchanged()
	for _, input := range []string{"parallelism\n3\ncancel\n", "parallelism\n3\ndone\nn\n", "parallelism\n3\n"} {
		if _, err := run(strings.NewReader(input), "config", "edit"); err != nil {
			t.Fatal(err)
		}
		unchanged()
	}
	// Interrupting an idle prompt must return even when stdin has not closed.
	pipe, inputWriter := io.Pipe()
	defer pipe.Close()
	defer inputWriter.Close()
	ctx, cancel := context.WithCancel(t.Context())
	prompt := writerFunc(func(b []byte) (int, error) {
		if strings.Contains(string(b), "Setting: ") {
			cancel()
		}
		return len(b), nil
	})
	if err := (CLI{In: pipe, Out: prompt, Harness: h}).Run(ctx, []string{"--config", path, "config", "edit"}); !errors.Is(err, context.Canceled) {
		t.Fatal("idle prompt did not cancel", err)
	}
	unchanged()
	for _, bad := range []string{"parallelism=0", "repairs=-1", "base-refreshes=-1", "invocations=0", "profile.build=remove", "profile.new=controlled high bad", "escalation=missing", "repository.path=/tmp/elsewhere", "delivery=github"} {
		if _, err := run(nil, "config", "edit", "--set", bad, "--save", "--preview", "stale"); err == nil {
			t.Fatal("invalid setting accepted", bad)
		}
		unchanged()
	}
	if _, err := run(nil, append(append([]string{}, changes...), "--save", "--preview", "stale")...); err == nil {
		t.Fatal("stale preview accepted")
	}
	unchanged()
	// The final conditional replacement must reject an edit made while prompting.
	input := strings.NewReader("parallelism\n2\ndone\ny\n")
	concurrent := append(append([]byte{}, original...), '\n')
	reader := readerFunc(func(b []byte) (int, error) {
		if err := os.WriteFile(path, concurrent, 0600); err != nil {
			t.Fatal(err)
		}
		return input.Read(b)
	})
	if _, err := run(reader, "config", "edit"); err == nil {
		t.Fatal("concurrent file edit overwritten")
	}
	file, _ := os.ReadFile(path)
	if !bytes.Equal(file, concurrent) {
		t.Fatal("lost concurrent edit")
	}
	if _, err := run(nil, append(append([]string{}, changes...), "--save", "--preview", preview)...); err == nil {
		t.Fatal("changed file reused old preview")
	}
	out, err = run(nil, changes...)
	if err != nil {
		t.Fatal(err)
	}
	preview = previewValue(t, out, "Save preview")
	if _, err = run(nil, append(append([]string{}, changes...), "--save", "--preview", preview)...); err != nil {
		t.Fatal(err)
	}
	db, _ := os.ReadFile(dbPath)
	if !bytes.Equal(originalDB, db) {
		t.Fatal("saving implicitly applied policy")
	}
	out, err = run(nil, "config", "explain")
	if err != nil || (!strings.Contains(out, "build -> careful") || !strings.Contains(out, "base refreshes: 2")) {
		t.Fatal("policy not explained", err, out)
	}
	if _, err = run(nil, "config", "apply", "--preview", previewValue(t, out, "Apply preview")); err != nil {
		t.Fatal(err)
	}
	// Interactive save uses the same settings and preserves explicit application.
	if _, err = run(strings.NewReader("parallelism\n3\ndone\ny\n"), "config", "edit"); err != nil {
		t.Fatal(err)
	}
	out, err = run(nil, "status", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var status engine.Status
	if err = json.Unmarshal([]byte(out), &status); err != nil || status.Parallelism != 2 || !status.Unapplied {
		t.Fatal("save changed running policy", status, err)
	}
}

func TestStatusWatchRefreshesAppliedPolicyWithoutWriting(t *testing.T) {
	_, path, cfg := commandFixture(t)
	h := &commandHarness{}
	if err := (CLI{Out: io.Discard, Harness: h}).Run(t.Context(), []string{"--config", path, "init", "--apply"}); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(t.Context(), cfg.StateDir, false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	count := 0
	var expectedDB []byte
	writer := writerFunc(func(b []byte) (int, error) {
		count++
		var status engine.Status
		if err := json.Unmarshal(b, &status); err != nil {
			t.Fatal("watch did not emit one complete JSON object", err)
		}
		if count == 1 {
			if status.Parallelism != 1 || status.Running {
				t.Fatal(status)
			}
			s, _, err := db.Settings(ctx)
			if err != nil {
				t.Fatal(err)
			}
			prior := s.Config.Revision()
			s.Config.Policy.MaxParallelism = 2
			if err = db.ApplySettings(ctx, prior, s); err != nil {
				t.Fatal(err)
			}
			expectedDB, err = os.ReadFile(filepath.Join(cfg.StateDir, "runner.db"))
			if err != nil {
				t.Fatal(err)
			}
		} else {
			if status.Parallelism != 2 || !status.Unapplied || status.Running {
				t.Fatal("watch used stale settings", status)
			}
			cancel()
		}
		return len(b), nil
	})
	err = (CLI{Out: writer, Harness: h}).Run(ctx, []string{"--config", path, "status", "--watch", "1s", "--json"})
	if !errors.Is(err, context.Canceled) || count != 2 {
		t.Fatal("watch cancellation failed", err, count)
	}
	after, _ := os.ReadFile(filepath.Join(cfg.StateDir, "runner.db"))
	if !bytes.Equal(expectedDB, after) {
		t.Fatal("watch wrote control state")
	}
}
