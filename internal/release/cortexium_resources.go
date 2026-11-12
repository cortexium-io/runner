package release

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type cortexiumResourceUsage struct {
	Role         string `json:"role"`
	Generation   string `json:"generation"`
	PeakBytes    int64  `json:"peak_bytes"`
	MemoryLimit  int64  `json:"memory_limit"`
	Failures     int64  `json:"memory_failures"`
	ZeroSwapHost bool   `json:"zero_swap_host"`
}

// The selected installed engine uses cgroup v1. Read only this owned container's
// controller; missing/unknown metrics refuse qualification, never change setup.
func (b *Live) recordCortexiumResources(ctx context.Context, j *Job, id, role, generation string) error {
	data, err := b.docker(ctx, "exec", "--user", "1000:1000", id, "/bin/cat", "/sys/fs/cgroup/memory/memory.max_usage_in_bytes", "/sys/fs/cgroup/memory/memory.failcnt")
	if err != nil {
		return err
	}
	values := strings.Fields(string(data))
	if len(values) != 2 {
		return errors.New("Cortexium cgroup v1 resource proof incomplete")
	}
	peak, err := strconv.ParseInt(values[0], 10, 64)
	if err != nil {
		return err
	}
	failures, err := strconv.ParseInt(values[1], 10, 64)
	if err != nil {
		return err
	}
	limit, _, _ := cortexiumResources(role)
	if peak < 1 || peak > limit || failures != 0 {
		return errors.New("Cortexium resource peak exceeded or reached its qualified memory bound")
	}
	file := filepath.Join(filepath.Dir(j.Worker), "resource-usage.json")
	records := []cortexiumResourceUsage{}
	if existing, err := readFile(file, 128<<10); err == nil {
		if json.Unmarshal(existing, &records) != nil {
			return errors.New("Cortexium retained resource proof malformed")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if len(records) >= 4 {
		return errors.New("Cortexium resource proof exceeds fixed lifecycle")
	}
	records = append(records, cortexiumResourceUsage{
		Role:         role,
		Generation:   generation,
		PeakBytes:    peak,
		MemoryLimit:  limit,
		Failures:     failures,
		ZeroSwapHost: true,
	})
	return writeJSON(file, records)
}

// Capture only the exact owned fixture App's bounded diagnostics before its
// container is removed. Neither operator environment nor host logs are copied.
func (b *Live) captureCortexiumAppDiagnostics(ctx context.Context, j *Job, id string) error {
	file := filepath.Join(filepath.Dir(j.Worker), "app-diagnostics.json")
	if _, err := os.Stat(file); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	call, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(call, b.Config.Docker, "--host", b.Config.DockerHost, "logs", id)
	configureReleaseCommand(cmd)
	if lease, ok := ctx.Value(leaseKey{}).(*os.File); ok {
		cmd.ExtraFiles = []*os.File{lease}
	}
	output := &limitedOutput{limit: 64 << 10}
	cmd.Stdout, cmd.Stderr = output, output
	err := cmd.Run()
	complete := err == nil && releaseCommandSettled(cmd) && !output.exceeded
	text := string(output.Bytes())
	if err != nil {
		// Transport stderr belongs to the operator, not the fixture App. Keep
		// it private; it may contain host/account paths and cannot reach models.
		_ = writeExclusive(filepath.Join(filepath.Dir(j.Worker), "app-diagnostics.private.log"), output.Bytes())
		text = ""
	}
	return writeJSON(file, map[string]any{"output": text, "complete": complete, "truncated": output.exceeded})
}
