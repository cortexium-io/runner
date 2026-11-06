package main

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/runner/config"
	"github.com/cortexium-io/runner/internal/runner/sqlite"
)

func TestServeRejectsCanonicalNativeStatePathBeforeCoreAdmission(t *testing.T) {
	configRoot := t.TempDir()
	fixture := filepath.Join("..", "..", "testdata", "config", "codex")
	if err := filepath.WalkDir(fixture, func(source string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(fixture, source)
		if err != nil {
			return err
		}
		target := filepath.Join(configRoot, rel)
		if entry.IsDir() {
			return os.MkdirAll(target, 0700)
		}
		data, err := os.ReadFile(source)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0600)
	}); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(configRoot, "native-must-not-run")
	binary := filepath.Join(configRoot, "codex-fixture")
	script := "#!/bin/sh\nprintf 'unexpected native start\\n' > '" + strings.ReplaceAll(marker, "'", "'\\''") + "'\nexit 91\n"
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	instance := filepath.Join(configRoot, "instance.yaml")
	data, err := os.ReadFile(instance)
	if err != nil {
		t.Fatal(err)
	}
	quotedBinary, _ := json.Marshal(binary)
	quotedAuth, _ := json.Marshal(filepath.Join(configRoot, "unused-auth.json"))
	data = []byte(strings.Replace(string(data), "binary: codex", "binary: "+string(quotedBinary)+"\n  auth_file: "+string(quotedAuth), 1))
	if err = os.WriteFile(instance, data, 0600); err != nil {
		t.Fatal(err)
	}
	loader, err := config.New(instance)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := loader.Load()
	if err != nil {
		t.Fatal("startup fixture is invalid", err)
	}
	longParent := filepath.Join(t.TempDir(), strings.Repeat("nested-", 20))
	realState := filepath.Join(longParent, "state")
	if err = os.MkdirAll(realState, 0700); err != nil {
		t.Fatal(err)
	}
	shortParent, err := os.MkdirTemp("/tmp", "runner-native-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(shortParent) })
	if err = os.Symlink(longParent, filepath.Join(shortParent, "parent")); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(shortParent, "parent", "state")
	if len(filepath.Join(state, "jobs", "codex.sock")) > 100 {
		t.Fatal("fixture must use a short visible state path")
	}
	// An already-canceled caller prevents an old implementation from serving
	// indefinitely. The static path prerequisite must still precede Core startup.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err = serve(ctx, instance, state, "127.0.0.1:0")
	if err == nil || !strings.Contains(err.Error(), "maximum path 100 bytes") {
		t.Fatal("native path was accepted until Core/task execution", err)
	}
	store, err := sqlite.Open(state, snapshot.Instance.Storage)
	if err != nil {
		t.Fatal("startup rejection retained the state owner lock", err)
	}
	defer store.Close()
	runs, err := store.Load(t.Context())
	if err != nil || len(runs) != 0 {
		t.Fatal("startup rejection accepted work", runs, err)
	}
	jobs, err := os.ReadDir(filepath.Join(realState, "jobs"))
	if err != nil || len(jobs) != 0 {
		t.Fatal("startup rejection dispatched an attempt", jobs, err)
	}
	for _, path := range []string{marker, filepath.Join(realState, "owner.token")} {
		if _, err = os.Lstat(path); !os.IsNotExist(err) {
			t.Fatal("invalid startup created native/owner state", path, err)
		}
	}
}
