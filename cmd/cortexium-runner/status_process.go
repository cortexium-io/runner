package main

import (
	"bufio"
	"context"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/github"
)

type runnerSubprocess struct {
	PID            int    `json:"pid"`
	State          string `json:"state"`
	Health         string `json:"health"`
	ElapsedSeconds int64  `json:"elapsed_seconds"`
	Command        string `json:"command"`
	Harness        string `json:"harness,omitempty"`
	Role           string `json:"role,omitempty"`
	ItemTitle      string `json:"item_title,omitempty"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
	parentPID      int
}

func inspectRunnerSubprocesses(ctx context.Context, runnerPID int, harnesses []config.HarnessConfig) ([]runnerSubprocess, error) {
	output, err := exec.CommandContext(ctx, "ps", "-axo", "pid=,ppid=,state=,etime=,comm=").Output()
	if err != nil {
		return nil, err
	}
	return parseRunnerSubprocesses(string(output), runnerPID, harnesses), nil
}

func parseRunnerSubprocesses(output string, runnerPID int, harnesses []config.HarnessConfig) []runnerSubprocess {
	harnessByCommand := map[string]string{}
	for _, harness := range harnesses {
		if harness.Enabled != nil && !*harness.Enabled {
			continue
		}
		command := filepath.Base(strings.TrimSpace(harness.Command))
		if command != "" {
			harnessByCommand[command] = strings.TrimSpace(harness.Kind)
		}
	}

	processes := map[int]runnerSubprocess{}
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 5 {
			continue
		}
		pid, pidErr := strconv.Atoi(fields[0])
		parentPID, parentErr := strconv.Atoi(fields[1])
		if pidErr != nil || parentErr != nil || pid <= 0 {
			continue
		}
		command := filepath.Base(strings.Join(fields[4:], " "))
		elapsed, _ := parseProcessElapsed(fields[3])
		processes[pid] = runnerSubprocess{
			PID: pid, parentPID: parentPID, State: fields[2], Health: processHealth(fields[2]),
			ElapsedSeconds: int64(elapsed / time.Second), Command: command, Harness: harnessByCommand[command],
		}
	}

	result := make([]runnerSubprocess, 0)
	for _, process := range processes {
		// Runner starts each harness or external command as a direct child.
		// Descendants belong to that invocation and can legitimately include
		// another executable with the same basename (for example a Codex tool
		// launching ChatGPT's bundled codex). Reporting those as independent
		// harness attempts is misleading.
		if process.parentPID != runnerPID {
			continue
		}
		result = append(result, process)
	}
	sort.Slice(result, func(i, j int) bool {
		if (result[i].Harness != "") != (result[j].Harness != "") {
			return result[i].Harness != ""
		}
		return result[i].PID < result[j].PID
	})
	return result
}

func parseProcessElapsed(value string) (time.Duration, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	days := 0
	if prefix, remainder, found := strings.Cut(value, "-"); found {
		parsed, err := strconv.Atoi(prefix)
		if err != nil || parsed < 0 {
			return 0, false
		}
		days, value = parsed, remainder
	}
	parts := strings.Split(value, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, false
	}
	values := make([]int, len(parts))
	for index, part := range parts {
		parsed, err := strconv.Atoi(part)
		if err != nil || parsed < 0 {
			return 0, false
		}
		values[index] = parsed
	}
	hours, minutes, seconds := 0, 0, 0
	if len(values) == 2 {
		minutes, seconds = values[0], values[1]
	} else {
		hours, minutes, seconds = values[0], values[1], values[2]
	}
	if minutes >= 60 || seconds >= 60 {
		return 0, false
	}
	return time.Duration(days*24+hours)*time.Hour + time.Duration(minutes)*time.Minute + time.Duration(seconds)*time.Second, true
}

func processHealth(state string) string {
	state = strings.ToUpper(strings.TrimSpace(state))
	if strings.HasPrefix(state, "T") {
		return "stopped"
	}
	if strings.HasPrefix(state, "Z") {
		return "zombie"
	}
	return "alive"
}

func annotateRunnerSubprocesses(processes []runnerSubprocess, cfg config.Config, active []github.WorkItem) []runnerSubprocess {
	if len(active) != 1 {
		return processes
	}
	item := active[0]
	attemptRole := cfg.AttemptRole(item.Role, item.QAFailures)
	profile, exists := cfg.RoleProfile(attemptRole)
	if !exists {
		return processes
	}
	for index := range processes {
		if processes[index].Harness != profile.Harness {
			continue
		}
		processes[index].Role = attemptRole
		processes[index].ItemTitle = item.Title
		processes[index].TimeoutSeconds = profile.TimeoutSeconds
	}
	return processes
}
