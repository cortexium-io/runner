//go:build unix

package execution

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDevelopmentToolPathPreservesOperatorExecutableSelection(t *testing.T) {
	for _, scenario := range []struct {
		name          string
		operatorOrder []string
	}{
		{name: "Go directory before Node directory containing older Go", operatorOrder: []string{"go", "node"}},
		{name: "Node directory before Go directory containing older Node", operatorOrder: []string{"node", "go"}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			root := t.TempDir()
			gitDirectory := filepath.Join(root, "xcode")
			goDirectory := filepath.Join(root, "go")
			nodeDirectory := filepath.Join(root, "node")
			unrelatedDirectory := filepath.Join(root, "unrelated")
			for _, directory := range []string{gitDirectory, goDirectory, nodeDirectory, unrelatedDirectory} {
				if err := os.Mkdir(directory, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			writeExecutableMarker(t, filepath.Join(gitDirectory, "git"), "xcode-git")
			writeExecutableMarker(t, filepath.Join(goDirectory, "go"), "operator-go")
			writeExecutableMarker(t, filepath.Join(nodeDirectory, "node"), "operator-node")
			writeExecutableMarker(t, filepath.Join(goDirectory, "git"), "competing-git")
			writeExecutableMarker(t, filepath.Join(nodeDirectory, "git"), "competing-git")
			if scenario.operatorOrder[0] == "go" {
				writeExecutableMarker(t, filepath.Join(nodeDirectory, "go"), "competing-go")
			} else {
				writeExecutableMarker(t, filepath.Join(goDirectory, "node"), "competing-node")
			}

			directories := map[string]string{"go": goDirectory, "node": nodeDirectory}
			operatorPath := strings.Join([]string{
				directories[scenario.operatorOrder[0]], unrelatedDirectory,
				directories[scenario.operatorOrder[1]],
			}, string(os.PathListSeparator))
			t.Setenv("PATH", operatorPath)
			path := developmentToolPathWith(exec.LookPath, gitDirectory, operatorPath)
			if contains(filepath.SplitList(path), unrelatedDirectory) {
				t.Fatalf("sandbox PATH inherited unrelated operator directory: %s", path)
			}

			t.Setenv("PATH", path)
			for tool, want := range map[string]string{"git": "xcode-git", "go": "operator-go", "node": "operator-node"} {
				command := exec.Command(tool)
				output, err := command.CombinedOutput()
				if err != nil {
					t.Fatalf("run selected %s executable: %v: %s", tool, err, output)
				}
				if got := strings.TrimSpace(string(output)); got != want {
					t.Fatalf("selected %s executable reported %q, want %q (path %s)", tool, got, want, path)
				}
			}
		})
	}
}

func writeExecutableMarker(t *testing.T, path, marker string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("#!/bin/sh\nprintf '%s\\n' '"+marker+"'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
}
