package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/runner/config"
	"github.com/cortexium-io/runner/internal/runner/core"
	"github.com/cortexium-io/runner/internal/runner/engine"
	"github.com/cortexium-io/runner/internal/runner/store"
)

type issueCommands struct{ published string }

func (*issueCommands) Request(_ context.Context, _ string) (core.SourceRequest, error) {
	return core.SourceRequest{Open: true, Source: core.Source{Provider: "github-issue", Connection: "owner/repo", ID: "I_request", URL: "https://github.com/owner/repo/issues/3", Revision: "original", Title: "Greeting request", Body: commandBrief}}, nil
}
func (s *issueCommands) Requests(ctx context.Context) ([]core.SourceRequest, error) {
	r, err := s.Request(ctx, "3")
	return []core.SourceRequest{r}, err
}
func (s *issueCommands) ObserveOverview(context.Context, string) (string, error) {
	return s.published, nil
}
func (s *issueCommands) PublishOverview(_ context.Context, _, body string, beforeWrite func() error) (bool, error) {
	if err := beforeWrite(); err != nil {
		return false, err
	}
	s.published = body
	return false, nil
}

func TestGitHubCommandsKeepImportSeparateFromOwnerApproval(t *testing.T) {
	_, path, cfg := commandFixture(t)
	cfg.Issues = &config.GitHubIssues{Repository: "owner/repo", OverviewIssue: 7}
	b, _ := json.Marshal(cfg)
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	h := &commandHarness{}
	source := &issueCommands{}
	run := func(args ...string) string {
		t.Helper()
		var out bytes.Buffer
		if err := (CLI{Out: &out, Harness: h, Sources: source, Progress: source}).Run(t.Context(), append([]string{"--config", path}, args...)); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out.String())
		}
		return out.String()
	}
	run("init", "--check", "test -f greeting.txt", "--apply")
	guard, err := store.LockGuard(cfg.StateDir, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = guard.Seal(strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	guard.Close()
	// An unapplied file edit must not disconnect or redirect the read-only inbox.
	unapplied := cfg
	unapplied.Issues = nil
	edited, _ := json.Marshal(unapplied)
	if err = os.WriteFile(path, edited, 0600); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(store.GuardPath(cfg.StateDir))
	if err != nil {
		t.Fatal(err)
	}
	if inbox := run("github", "inbox", "--json"); !strings.Contains(inbox, "Greeting request") {
		t.Fatal("request missing from inbox", inbox)
	}
	run("status")
	after, err := os.ReadFile(store.GuardPath(cfg.StateDir))
	if err != nil || !bytes.Equal(before, after) || source.published != "" {
		t.Fatal("read-only inbox/status invalidated freshness or published", err)
	}
	if err = os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"github", "import", "--issue", "3"}, {"github", "import", "--issue", "3", "--ready", "--plan"}} {
		if err := (CLI{Out: io.Discard, Harness: h}).Run(t.Context(), append([]string{"--config", path}, args...)); err == nil {
			t.Fatal("ambiguous import mode accepted", args)
		}
	}
	output := run("github", "import", "--issue", "3", "--ready")
	id := strings.Fields(output)[2]
	var view engine.View
	if err = json.Unmarshal([]byte(run("task", "show", id, "--json")), &view); err != nil {
		t.Fatal(err)
	}
	if view.Task.Stage != core.Draft || view.Task.Approval != nil {
		t.Fatal("CLI import approved work")
	}
	if again := run("github", "import", "--issue", "3", "--ready"); !strings.Contains(again, id) {
		t.Fatal("repeated CLI import lost mapping")
	}
	run("github", "sync")
	if !strings.Contains(source.published, "Needs owner") {
		t.Fatal("overview hides the owner gate")
	}
	run("task", "approve", id, "--version", "1", "--preview", view.Preview)
	if err = json.Unmarshal([]byte(run("task", "show", id, "--json")), &view); err != nil {
		t.Fatal(err)
	}
	if view.Task.Approval == nil || h.auditCalls != 0 || h.planCalls != 0 {
		t.Fatal("owner approval or command invocation boundary changed")
	}
}
