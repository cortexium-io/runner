// This model-free harness fixture deliberately leaves detached descendants.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--version" {
		fmt.Println("native-child-fixture 1")
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "--fork" {
		spawnLeaf()
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "--hold" {
		time.Sleep(time.Minute)
		return
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
	if strings.HasPrefix(os.Getenv("RUNNER_CHILD_FIXTURE"), "double-fork") {
		cmd := exec.Command(os.Args[0], "--fork")
		if strings.HasSuffix(os.Getenv("RUNNER_CHILD_FIXTURE"), "-stdout") {
			cmd.Stdout = os.Stdout
		}
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := cmd.Run(); err != nil {
			panic(err)
		}
	} else {
		spawnLeaf()
	}
	if os.Getenv("RUNNER_CHILD_FIXTURE") == "cancel" {
		for {
			time.Sleep(time.Hour)
		}
	}
	if os.Getenv("RUNNER_CHILD_FIXTURE") == "gated" {
		for {
			if _, err := os.Stat(filepath.Join(os.Getenv("TMPDIR"), "go")); err == nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	final, _ := json.Marshal(map[string]any{"outcome": "ready", "outputs": map[string]string{"article": "Complete fixture draft"}, "reason": ""})
	_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"type": "item.completed", "item": map[string]any{"type": "agent_message", "text": string(final)}})
}

func spawnLeaf() {
	cmd := exec.Command("/bin/sleep", "60")
	if os.Getenv("RUNNER_CHILD_FIXTURE") == "double-fork" || os.Getenv("RUNNER_CHILD_FIXTURE") == "double-fork-stdout" {
		cmd = exec.Command(os.Args[0], "--hold")
	}
	if strings.HasSuffix(os.Getenv("RUNNER_CHILD_FIXTURE"), "-stdout") {
		cmd.Stdout = os.Stdout
	}
	if os.Getenv("RUNNER_CHILD_FIXTURE") == "stderr" {
		cmd.Stderr = os.Stderr
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		panic(err)
	}
	if err := os.WriteFile(filepath.Join(os.Getenv("TMPDIR"), "escaped.pid"), []byte(strconv.Itoa(cmd.Process.Pid)), 0600); err != nil {
		panic(err)
	}
	// Do not wait: the caller must clean this deliberately detached resource.
}
