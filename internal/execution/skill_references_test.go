package execution

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/metrics"
	"github.com/cortexium-io/runner/internal/subprocess"
	bundledskills "github.com/cortexium-io/runner/skills"
)

func TestImplementationAdaptersLaunchWithSkillReferences(t *testing.T) {
	for _, kind := range []string{config.HarnessCodexCLI, config.HarnessClaudeCLI, config.HarnessPiCLI} {
		t.Run(kind, func(t *testing.T) {
			cfg := testWorkspaceWriteConfig(t)
			cfg.Harness.Kind, cfg.Harness.Command = kind, kind
			cfg.Harness.TimeoutSeconds = 7200
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
			defer cancel()
			deadline, _ := ctx.Deadline()
			cfg.Skills = []string{"runner-implementer", "runner-interaction-design"}
			cfg.ReviewEvidencePaths = []string{"test-results/runner-review-evidence"}
			if kind == config.HarnessPiCLI {
				cfg.RoleAccess = config.RoleAccessHost
			}
			root := ""
			run := &implementationReferenceRunner{representationResidueRunner: representationResidueRunner{kind: kind}, inspect: func(prompt string, args []string) {
				if !strings.Contains(prompt, `test-results/runner-review-evidence`) || !strings.Contains(prompt, "Implementation runtime budget:") {
					t.Fatal("native implementation launch omitted configured handoff/budget")
				}
				if !strings.Contains(prompt, "finish by "+deadline.UTC().Format(time.RFC3339)) || strings.Contains(prompt, "2h0m0s from launch") {
					t.Fatal("native implementation launch advertised a fresh budget instead of its inherited deadline")
				}
				_, suffix, found := strings.Cut(prompt, "Runner-pinned skill reference root: ")
				if !found {
					t.Fatal("implementation prompt lost references")
				}
				root, _, _ = strings.Cut(suffix, "\n")
				checkSkillReferenceBytes(t, root)
			}}
			assignment := testPollResponse(testCodexCLIWorkspaceWriteAssignmentSpec()).Assignments[0]
			var output Output
			var err error
			if kind == config.HarnessCodexCLI {
				output, err = NewCodexExecutor(cfg, run).ExecuteWorkspaceWrite(ctx, assignment, nil)
			} else {
				output, err = NewAgentExecutor(kind, cfg, run).ExecuteWorkspaceWrite(ctx, assignment, nil)
			}
			if err != nil || output.Outcome != OutcomeSucceeded || root == "" {
				t.Fatalf("implementation with design references: %#v %v", output, err)
			}
			if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("reference cleanup: %v", err)
			}
		})
	}
}

func TestSkillReferenceWorkspaceIsSelectedReadOnlyAndDisposable(t *testing.T) {
	for _, role := range []RoleContract{RolePlanner, RoleImplementer, RoleReviewer, RoleSynthesis, RoleProbe} {
		t.Run(string(role), func(t *testing.T) {
			profile, _ := ProfileForRole(role)
			cfg := config.ExecutionConfig{Skills: []string{"runner-interaction-design"}}
			w, err := prepareExecutionWorkspace(t.Context(), subprocess.OSRunner{}, profile, t.TempDir(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer w.cleanup()
			if role == RoleSynthesis || role == RoleProbe {
				if w.SkillReferenceRoot != "" {
					t.Fatal("tool-free stage gained references")
				}
				return
			}
			checkSkillReferenceBytes(t, w.SkillReferenceRoot)
			for _, writable := range append(sandboxAdditionalWritePaths(w), w.Dir, w.ReadRoot) {
				if writable != "" && pathInsideOrEqual(w.SkillReferenceRoot, writable) {
					t.Fatalf("references inside writable/root %s", writable)
				}
			}
			if !slices.Contains(sandboxAdditionalReadPaths(w, false), w.SkillReferenceRoot) || slices.Contains(sandboxAdditionalReadPaths(w, true), w.TrustedToolDir) {
				t.Fatal("reference grant missing or exposes trusted runtime parent")
			}
			args := strings.Join(codexProfileArgs(profile, w, false), " ")
			if !strings.Contains(args, quoteJSON(w.SkillReferenceRoot)+`="read"`) || strings.Contains(args, quoteJSON(w.SkillReferenceRoot)+`="write"`) {
				t.Fatalf("Codex reference grant: %s", args)
			}
			var settings struct {
				Sandbox struct {
					Filesystem struct {
						AllowRead  []string `json:"allowRead"`
						AllowWrite []string `json:"allowWrite"`
						DenyWrite  []string `json:"denyWrite"`
					} `json:"filesystem"`
				} `json:"sandbox"`
			}
			if err := json.Unmarshal([]byte(claudeSandboxSettings(profile, w, false)), &settings); err != nil {
				t.Fatal(err)
			}
			fs := settings.Sandbox.Filesystem
			if !slices.Contains(fs.AllowRead, w.SkillReferenceRoot) || !slices.Contains(fs.DenyWrite, w.SkillReferenceRoot) || slices.Contains(fs.AllowWrite, w.SkillReferenceRoot) {
				t.Fatalf("Claude reference grants: %#v", fs)
			}
			if err := w.cleanup(); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(w.SkillReferenceRoot); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("references not removed: %v", err)
			}
		})
	}
	profile, _ := ProfileForRole(RolePlanner)
	w, err := prepareExecutionWorkspace(t.Context(), subprocess.OSRunner{}, profile, t.TempDir(), config.ExecutionConfig{Skills: []string{"runner-planner"}})
	if err != nil {
		t.Fatal(err)
	}
	defer w.cleanup()
	if w.SkillReferenceRoot != "" || strings.Contains(profileRepositoryInstruction(w), "skill reference") {
		t.Fatal("non-UI role gained design references")
	}
}

func checkSkillReferenceBytes(t *testing.T, root string) {
	t.Helper()
	if root == "" {
		t.Fatal("no reference root at launch")
	}
	skill, _ := (bundledskills.EmbeddedCatalog{}).Get("runner-interaction-design")
	for _, file := range skill.References {
		path := filepath.Join(root, skill.ID, file.Path)
		got, err := os.ReadFile(path)
		if err != nil || string(got) != string(file.Content) {
			t.Fatalf("reference %s: %v", file.Path, err)
		}
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm()&0222 != 0 {
			t.Fatalf("reference is writable: %s %v", path, err)
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 1 || entries[0].Name() != skill.ID {
		t.Fatalf("unselected files exposed: %v %v", entries, err)
	}
}

// An explicit failure at the model boundary proves references exist before a
// launch and are cleaned up on failure, without paid provider calls.
type skillReferenceFailureRunner struct {
	t      *testing.T
	root   string
	prompt string
}

func (r *skillReferenceFailureRunner) Run(context.Context, string, []string, string, time.Duration) (subprocess.Result, error) {
	return subprocess.Result{}, errors.New("unexpected non-input call")
}
func (r *skillReferenceFailureRunner) capture(input io.Reader) (subprocess.Result, error) {
	data, err := io.ReadAll(input)
	if err != nil {
		return subprocess.Result{}, err
	}
	r.prompt = string(data)
	_, suffix, found := strings.Cut(r.prompt, "Runner-pinned skill reference root: ")
	if !found {
		r.t.Fatal("actual harness launch omitted reference location")
	}
	r.root, _, _ = strings.Cut(suffix, "\n")
	checkSkillReferenceBytes(r.t, r.root)
	return subprocess.Result{}, errors.New("fixture model boundary failure")
}
func (r *skillReferenceFailureRunner) RunBoundedInput(_ context.Context, _ string, _ []string, _ string, _ time.Duration, input io.Reader, _ int, _ string) (subprocess.Result, error) {
	return r.capture(input)
}
func (r *skillReferenceFailureRunner) RunBoundedHeadTailInput(_ context.Context, _ string, _ []string, _ string, _ time.Duration, input io.Reader, _ int, _ string) (subprocess.Result, error) {
	return r.capture(input)
}
func (r *skillReferenceFailureRunner) RunLineFilteredInput(_ context.Context, _ string, _ []string, _ string, _ time.Duration, input io.Reader, _ int, _ string, _ subprocess.LineFilter) (subprocess.Result, error) {
	return r.capture(input)
}

func TestStructuredStagesLaunchWithSelectedSkillReferences(t *testing.T) {
	for _, kind := range []string{config.HarnessCodexCLI, config.HarnessClaudeCLI, config.HarnessPiCLI} {
		for _, role := range []RoleContract{RolePlanner, RoleReviewer} {
			t.Run(kind+"/"+string(role), func(t *testing.T) {
				run := &skillReferenceFailureRunner{t: t}
				cfg := config.ExecutionConfig{Skills: []string{"runner-interaction-design"}, Harness: config.HarnessConfig{Kind: kind, Command: kind, WorkingDir: t.TempDir(), TimeoutSeconds: 10}}
				if kind == config.HarnessPiCLI {
					cfg.RoleAccess = config.RoleAccessHost
				}
				stage := metrics.StageReviewerAudit
				if role == RolePlanner {
					stage = metrics.StagePlannerOutline
				}
				_, err := runStructuredHarness(t.Context(), role, kind, cfg, cfg.Harness.WorkingDir, "Inspect the retained evidence only.", []byte(`{"type":"object"}`), "prefer", stage, run)
				if err == nil || run.root == "" {
					t.Fatalf("fixture did not reach launch: %v", err)
				}
				if _, err := os.Stat(run.root); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("failed launch leaked references: %v", err)
				}
			})
		}
	}
}

func TestDesignGuidanceKeepsReferencesOutOfStablePrompt(t *testing.T) {
	cfg := config.ExecutionConfig{Skills: []string{"runner-reviewer", "runner-interaction-design"}}
	skill, _ := (bundledskills.EmbeddedCatalog{}).Get("runner-interaction-design")
	for _, kind := range []string{config.HarnessCodexCLI, config.HarnessClaudeCLI, config.HarnessPiCLI} {
		guidance := harnessGuidance(kind, cfg, true)
		for _, ref := range skill.References {
			if !strings.Contains(guidance, ref.Path+" sha256:"+ref.SHA256) || strings.Contains(guidance, string(ref.Content)) {
				t.Fatal("reference missing from stable manifest or eagerly loaded")
			}
		}
		if strings.Contains(harnessGuidance(kind, cfg, false), string(skill.Content)) {
			t.Fatal("tool-free stage received design skill")
		}
		for _, path := range []string{"/private/first", "/private/second"} {
			prompt := guidance + reviewerAuditPrompt(reviewerAssignment(), reviewerHarnessDisplayName(kind)) + profileReferenceInstruction(profileWorkspace{SkillReferenceRoot: path})
			if !strings.HasPrefix(prompt, guidance) || strings.Index(prompt, path) < len(guidance) || !strings.Contains(prompt, "Do not run tests") {
				t.Fatal("dynamic reference location changed prefix or static stage boundary")
			}
		}
	}
}

func TestNativeCodexSkillReferenceContainment(t *testing.T) {
	if os.Getenv("CORTEXIUM_RUNNER_NATIVE_REFERENCE_CHECK") == "" {
		t.Skip("opt in to the installed Codex sandbox; no model invocation")
	}
	profile, _ := ProfileForRole(RoleImplementer)
	w, err := prepareExecutionWorkspace(t.Context(), subprocess.OSRunner{}, profile, t.TempDir(), config.ExecutionConfig{Skills: []string{"runner-interaction-design"}})
	if err != nil {
		t.Fatal(err)
	}
	defer w.cleanup()
	secret := filepath.Join(w.TrustedToolDir, "unrelated-runtime")
	if err := os.WriteFile(secret, []byte("private fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"sandbox", "-P", codexImplementationWritePermissionProfile, "-C", w.Dir}
	policy := codexProfileArgs(profile, w, false)
	for i := 0; i+1 < len(policy); i++ {
		if policy[i] == "--config" && strings.HasPrefix(policy[i+1], "permissions.") {
			args = append(args, "--config", policy[i+1])
		}
	}
	ref := filepath.Join(w.SkillReferenceRoot, "runner-interaction-design", "references", "evaluation.md")
	args = append(args, "--", "/bin/sh", "-c", `set -eu
cat "$1" > observed-reference.md
if (chmod u+w "$1" && echo forbidden > "$1") 2>/dev/null; then exit 10; fi
if (echo forbidden > "$2/new.md") 2>/dev/null; then exit 11; fi
if cat "$3" > /dev/null 2>&1; then exit 12; fi
test -s observed-reference.md
`, "reference-check", ref, w.SkillReferenceRoot, secret)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	if output, err := exec.CommandContext(ctx, "codex", args...).CombinedOutput(); err != nil {
		t.Fatalf("native reference containment: %v\n%s", err, output)
	}
	checkSkillReferenceBytes(t, w.SkillReferenceRoot)
}
