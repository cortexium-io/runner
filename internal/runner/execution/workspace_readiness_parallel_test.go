package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
)

type readinessProbePeer func(context.Context, map[string]any) error

func (p readinessProbePeer) Call(ctx context.Context, method string, params, out any) error {
	args, ok := params.(map[string]any)
	if method != "command/exec" || !ok {
		return fmt.Errorf("unexpected readiness RPC %q", method)
	}
	if err := p(ctx, args); err != nil {
		return err
	}
	return json.Unmarshal([]byte(`{"exitCode":0}`), out)
}

func (readinessProbePeer) Close()                {}
func (readinessProbePeer) Done() <-chan struct{} { return nil }

func TestParallelWorkspaceReadinessKeepsOwnedProbeFiles(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	d := workspaceDisk(t)
	c := &Codex{
		ctx:      ctx,
		disk:     d,
		cfg:      &runner.CodexSettings{GoBinary: filepath.Join(runtime.GOROOT(), "bin", "go")},
		jobs:     make(map[string]nativeJob),
		commands: make(map[string]nativeCommand),
		changed:  make(chan struct{}),
	}
	newJob := func(id string) nativeJob {
		workDir := filepath.Join(d.Root, "work", id, "source")
		if err := os.MkdirAll(workDir, 0700); err != nil {
			t.Fatal(err)
		}
		j := nativeJob{Assignment: runner.Assignment{AttemptID: id}, Workspace: &workspaceRecord{WorkDir: workDir}}
		if err := c.saveLocked(j); err != nil {
			t.Fatal(err)
		}
		return j
	}
	first := newJob("attempt_readiness_a")
	second := newJob("attempt_readiness_b")

	firstEntered := make(chan [2]string, 1)
	releaseFirst := make(chan struct{})
	var secondPaths [2]string
	checkPresent := func(paths [2]string) error {
		var err error
		for _, path := range paths {
			if info, statErr := os.Lstat(path); statErr != nil {
				err = errors.Join(err, statErr)
			} else if !info.Mode().IsRegular() {
				err = errors.Join(err, fmt.Errorf("probe target %q is not a regular file", path))
			}
		}
		return err
	}
	peer := readinessProbePeer(func(ctx context.Context, args map[string]any) error {
		argv, _ := args["command"].([]string)
		run := slices.Index(argv, "run")
		if run < 0 || run+3 >= len(argv) {
			return errors.New("readiness command omitted its private probe path")
		}
		paths := [2]string{argv[run+3], ""}
		for _, arg := range argv[:run] {
			if path, ok := strings.CutPrefix(arg, "RUNNER_ARTIFACT_STORE_PROBE="); ok {
				paths[1] = path
				break
			}
		}
		if err := checkPresent(paths); err != nil {
			return err
		}
		if args["permissionProfile"] != "runner-edit" {
			return nil
		}
		switch args["processId"] {
		case first.Assignment.AttemptID:
			firstEntered <- paths
			select {
			case <-releaseFirst:
				return checkPresent(paths)
			case <-ctx.Done():
				return ctx.Err()
			}
		case second.Assignment.AttemptID:
			secondPaths = paths
			return nil
		default:
			return fmt.Errorf("unexpected readiness process %v", args["processId"])
		}
	})

	firstDone := make(chan struct{})
	var firstErr error
	go func() {
		defer close(firstDone)
		firstErr = c.workspaceReadiness(ctx, peer, first)
	}()
	defer func() {
		cancel()
		<-firstDone
	}()

	var firstPaths [2]string
	select {
	case firstPaths = <-firstEntered:
	case <-firstDone:
		t.Fatalf("first readiness ended before its edit probe: %v", firstErr)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := c.workspaceReadiness(ctx, peer, second); err != nil {
		t.Fatal(err)
	}
	if err := checkPresent(firstPaths); err != nil {
		t.Errorf("second readiness removed the first readiness probe files: %v", err)
	}
	checkRemoved := func(paths [2]string) {
		t.Helper()
		for _, path := range paths {
			if _, err := os.Lstat(path); path == "" || !errors.Is(err, os.ErrNotExist) {
				t.Errorf("settled readiness did not remove probe target %q: %v", path, err)
			}
		}
	}
	checkRemoved(secondPaths)
	close(releaseFirst)
	<-firstDone
	if firstErr != nil {
		t.Errorf("first readiness could not finish: %v", firstErr)
	}
	checkRemoved(firstPaths)
}
