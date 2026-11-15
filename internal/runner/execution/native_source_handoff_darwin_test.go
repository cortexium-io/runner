//go:build darwin

package execution

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
)

func TestHarnessMaterializesRetainedSourceWithCanonicalArtifactGrants(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "runner-source-handoff")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", binary, "./cmd/runner-source-handoff")
	build.Dir = "../../.."
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build owned handoff fixture: %v %s", err, output)
	}
	d := workspaceDisk(t)
	repo := t.TempDir()
	invoke := func(args ...string) string {
		t.Helper()
		data, err := git(t.Context(), repo, nil, nil, 1<<20, args...)
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(data))
	}
	invoke("init", "--quiet")
	if err := os.WriteFile(filepath.Join(repo, "source.txt"), []byte("base"), 0644); err != nil {
		t.Fatal(err)
	}
	invoke("add", "source.txt")
	invoke("-c", "user.name=Fixture", "-c", "user.email=fixture@localhost", "commit", "--quiet", "-m", "base")
	base := invoke("rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(repo, "source.txt"), []byte("retained source"), 0644); err != nil {
		t.Fatal(err)
	}
	invoke("add", "source.txt")
	tree := invoke("write-tree")
	patch, err := git(t.Context(), repo, nil, nil, 1<<20, "diff", "--binary", "--full-index", "--cached", base)
	if err != nil {
		t.Fatal(err)
	}
	check := []byte(`{"status":"passed","executed":393}`)
	for name, data := range map[string][]byte{"previous_source/changes.patch": patch, "previous_check/result": check} {
		if err := d.Write("artifacts/"+name, data, 1<<20); err != nil {
			t.Fatal(err)
		}
	}
	a := sharedCommandAssignment(t)
	a.Deadline = time.Now().Add(30 * time.Second)
	a.Setup.Profile.Access = "workspace-write"
	a.TaskContext.WritePaths = []string{"."}
	a.Inputs = map[string]runner.Value{
		"patch":  {ArtifactID: "previous_source/changes.patch"},
		"checks": {ArtifactID: "previous_check/result"},
	}
	a.Setup.Profile.Command = &runner.Command{
		Args: []string{
			binary,
			"--repository", repo,
			"--base", base,
			"--tree", tree,
			"--patch", "{artifact.patch}",
			"--patch-sha256", fmt.Sprintf("%x", sha256.Sum256(patch)),
			"--checks", "{artifact.checks}",
			"--check-sha256", fmt.Sprintf("%x", sha256.Sum256(check)),
			"--destination", "{workspace}",
			"--state", d.Root,
			"--output", "{scratch}",
		},
		Result:   "domain.json",
		Artifact: "handoff.json",
	}
	proof := runSharedCommandProof(t, d, a)
	if proof.Access != "workspace-write" {
		t.Fatal("source importer lost its explicit access fact")
	}
	if data, err := os.ReadFile(filepath.Join(a.WorkDir, "source.txt")); err != nil || string(data) != "retained source" {
		t.Fatal("real fixed handoff could not reconstruct under the worker sandbox", string(data), err)
	}
	if invoke("rev-parse", "HEAD") != base {
		t.Fatal("source materialization changed the old repository")
	}
}

func TestHarnessSharedArtifactBindingsKeepOnlyAuthorizedCanonicalFiles(t *testing.T) {
	a := sharedCommandAssignment(t)
	a.OutputDir = filepath.Join(t.TempDir(), "artifacts", a.AttemptID)
	a.Inputs = map[string]runner.Value{"checks": {ArtifactID: "previous_check/result"}}
	a.TaskContext.Results = []runner.RoleTaskResult{
		{
			ID: "import", RunID: "import_run", Role: "fixed-importer", Status: "Completed",
			Outputs: map[string]runner.Value{
				"artifact": {ArtifactID: "import_attempt/result"},
				"report":   runner.Text("do not duplicate this full report"),
			},
		},
		{ID: "unselected", Status: "Completed", Outputs: map[string]runner.Value{"artifact": {ArtifactID: "unselected/result"}}},
	}
	a.Setup.Profile.Command = &runner.Command{Args: []string{"/usr/bin/cat", "{artifact.checks}", "{dependency.import.artifact}"}}
	normalized, err := sharedAssignment(a)
	if err != nil {
		t.Fatal(err)
	}
	if len(normalized.TaskContext.Results) != 1 || len(normalized.TaskContext.Results[0].Outputs) != 1 {
		t.Fatal("fixed command duplicated unrelated results", normalized.TaskContext.Results)
	}
	spec := harnessSpec{Assignment: normalized}
	files := harnessArtifactFiles(normalized)
	if sharedValue("{artifact.checks}", spec) != files["previous_check/result"] || sharedValue("{dependency.import.artifact}", spec) != files["import_attempt/result"] || files["unselected/result"] != "" {
		t.Fatal("fixed file substitutions changed artifact authority", files)
	}
	for _, mode := range []string{"unfinished", "missing", "inline", "ambiguous", "compound"} {
		t.Run(mode, func(t *testing.T) {
			changed := runner.Clone(a)
			switch mode {
			case "unfinished":
				changed.TaskContext.Results[0].Status = "Running"
			case "missing":
				changed.TaskContext.Results = nil
			case "inline":
				changed.TaskContext.Results[0].Outputs["artifact"] = runner.Text("not a canonical file")
			case "ambiguous":
				changed.TaskContext.Results = append(changed.TaskContext.Results, changed.TaskContext.Results[0])
			case "compound":
				changed.Setup.Profile.Command.Args[2] += "/../secret"
			}
			if _, err := sharedAssignment(changed); err == nil {
				t.Fatal("invalid command file binding gained authority")
			}
		})
	}
}
