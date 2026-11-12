package execution

import (
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"strings"

	"github.com/cortexium-io/runner/internal/runner"
)

const gennyModuleSum = "h1:b6+9XLiWo/LcK5lwUK1Xme9HnrtdB4KhLpiH/XQJGuE="

func pinnedExecutable(path, expected string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 || len(expected) != 64 {
		return errors.New("pinned runtime executable is unavailable")
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	if hex.EncodeToString(h.Sum(nil)) != expected {
		return errors.New("pinned runtime executable changed")
	}
	return nil
}

func (c *Codex) validateGennyRuntime() error {
	if c.cfg == nil || c.cfg.Genny == nil {
		return errors.New("genny-source runtime is not configured")
	}
	g := c.cfg.Genny
	if err := pinnedExecutable(g.Binary, g.SHA256); err != nil {
		return err
	}
	if err := pinnedExecutable(g.GoBinary, g.GoSHA256); err != nil {
		return err
	}
	goInfo, err := buildinfo.ReadFile(g.GoBinary)
	if err != nil || goInfo.GoVersion != "go1.27.3" {
		return errors.New("Genny source generation requires the pinned Go1.27.3 toolchain")
	}
	info, err := buildinfo.ReadFile(g.Binary)
	if err != nil || info.Main.Path != "github.com/cortexium-io/genny" || info.Main.Version != "v0.4.0" || info.Main.Sum != gennyModuleSum || info.GoVersion != "go1.27.3" {
		return errors.New("Genny must be the pinned published v0.4.0 build from source 86fe571, compiled with Go1.27.3")
	}
	return nil
}

func sourcePermission(p runner.Profile) string {
	if p.Mode == "genny-source" && p.Access == "workspace-write" {
		return "runner-genny-edit"
	}
	if p.Access == "workspace-read" {
		return "runner-check"
	}
	return "runner-edit"
}

func sourceInstructions(p runner.Profile) string {
	if p.Mode != "genny-source" {
		return workspaceBase
	}
	base := strings.Replace(workspaceBase, "This profile provides Go and basic system commands;", "This profile provides the pinned Genny v0.4.0 generator, its Go1.27.3 compiler and basic system commands;", 1)
	return strings.Replace(base, "Do not use Git, add dependencies, change protected files, use Python or start other work.", "Use only RUNNER_GENNY for the admitted recipe, without --force, in the owned empty application directory. Do not hand-edit generated glue or invent adoption metadata. Preserve the selected Go/npm manifests; only the declared pinned foundation dependencies are authorized. Use ordinary app-owned extension points. Your assignment is source generation/editing, not checks: only later configured checks and independent review can establish acceptance. Do not use Git, change protected files, use Python or start other work.", 1)
}
