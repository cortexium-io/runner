package config

import (
	"path/filepath"
	"testing"
)

// Keep published configurations loadable without coupling mutation tests to them.
// Loading configuration performs no model execution or project checkout.
func TestExampleConfigurations(t *testing.T) {
	for _, name := range []string{"demo", "codex", "workspace"} {
		t.Run(name, func(t *testing.T) {
			loader, err := New(filepath.Join("..", "..", "..", "examples", name, "instance.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err = loader.Load(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
