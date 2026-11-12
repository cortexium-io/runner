package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
)

type commandSpec struct {
	AttemptID    string            `json:"attempt_id"`
	Binary       string            `json:"binary"`
	Args         []string          `json:"args"`
	Env          map[string]string `json:"env"`
	Directory    string            `json:"directory"`
	Control      string            `json:"control"`
	Deadline     time.Time         `json:"deadline"`
	MaxLog       runner.Bytes      `json:"max_log"`
	HelperKind   string            `json:"helper_kind,omitempty"`
	HelperSHA256 string            `json:"helper_sha256,omitempty"`
}

// RunCommandWorker is an internal mode of this Go executable, not a scheduler.
// Its private result is written only after the sandboxed command has settled.
func RunCommandWorker(parent context.Context, file string) error {
	if !commandGroupsSupported {
		return errors.New("command workers require macOS or Linux")
	}
	info, err := os.Lstat(file)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return errors.New("invalid command worker spec")
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	var spec commandSpec
	if err = json.Unmarshal(data, &spec); err != nil {
		return err
	}
	if spec.AttemptID == "" || spec.Control != filepath.Dir(file) || len(spec.Args) == 0 || spec.MaxLog < 1 || spec.MaxLog > 4<<20 {
		return errors.New("incomplete command worker spec")
	}
	claim, err := os.OpenFile(filepath.Join(spec.Control, "started.json"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("command dispatch already claimed or unavailable: %w", err)
	}
	if err = json.NewEncoder(claim).Encode(map[string]any{"pid": os.Getpid(), "attempt_id": spec.AttemptID}); err == nil {
		err = claim.Sync()
	}
	claim.Close()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithDeadline(parent, spec.Deadline)
	defer cancel()
	stopped := func() bool {
		b, e := os.ReadFile(filepath.Join(spec.Control, "stop"))
		return e != nil || len(b) != 1 || b[0] != 0
	}
	if stopped() {
		cancel()
	}
	tail := &diagnosticTail{limit: int(spec.MaxLog)}
	var runErr error
	if spec.HelperSHA256 != "" && ctx.Err() == nil {
		switch spec.HelperKind {
		case "":
			runErr = verifyFrontendCheckHelper(spec.Args[0], spec.HelperSHA256)
		case "guides-import":
			_, runErr = guidesImportHelperBytes(spec.Args[0], spec.HelperSHA256)
		default:
			runErr = errors.New("unknown pinned command helper kind")
		}
	}
	if spec.HelperKind != "" && spec.HelperSHA256 == "" {
		runErr = errors.New("pinned command helper kind lacks its digest")
	}
	if runErr == nil {
		runErr = runWorkerSandbox(ctx, cancel, spec, tail, stopped)
	}
	if errors.Is(runErr, exec.ErrWaitDelay) {
		return errors.New("command descendants did not settle")
	}
	diagnostic := tail.bytes()
	if len(diagnostic) > 480 {
		diagnostic = diagnostic[len(diagnostic)-480:]
	}
	result := commandResult{
		AttemptID:  spec.AttemptID,
		Kind:       "returned",
		Diagnostic: string(diagnostic),
	}
	if ctx.Err() != nil {
		result.Kind = "stopped"
	} else if runErr != nil {
		result.ExitCode = 1
		var exit *exec.ExitError
		if errors.As(runErr, &exit) {
			result.ExitCode = exit.ExitCode()
		}
		if result.Diagnostic == "" {
			result.Diagnostic = runner.Clip(runErr.Error())
		}
	}
	if err = workerWrite(filepath.Join(spec.Control, "log"), tail.bytes()); err != nil {
		return err
	}
	data, err = json.Marshal(result)
	if err != nil {
		return err
	}
	return workerWrite(filepath.Join(spec.Control, "result.json"), data)
}

func runWorkerSandbox(ctx context.Context, cancel context.CancelFunc, spec commandSpec, tail *diagnosticTail, stopped func() bool) error {
	args := append([]string{"sandbox", "-P", "runner-command", "-C", spec.Directory, "--"}, envCommand(spec.Env, spec.Args...)...)
	cmd := exec.CommandContext(ctx, spec.Binary, args...)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "CODEX_HOME=" + filepath.Join(spec.Control, "config")}
	cmd.Dir = spec.Directory
	cmd.Stdout = tail
	cmd.Stderr = tail
	setNativeProcessGroup(cmd)
	cmd.Cancel = func() error { return killCommandGroup(cmd) }
	cmd.WaitDelay = 2 * time.Second
	done := make(chan error, 1)
	go func() { done <- cmd.Run() }()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case err := <-done:
			return err
		case <-ticker.C:
			if stopped() {
				cancel()
			}
		}
	}
}
func workerWrite(file string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(file), ".result-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), file)
}
