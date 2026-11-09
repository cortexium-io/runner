// The qualification fixture's fixed benchmark/build/browser commands. This is
// test tooling, not a scheduler: Runner owns every transition and allowance.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	if len(os.Args) < 4 {
		return fmt.Errorf("mode, Go binary and scratch are required")
	}
	mode, goBinary, scratch := os.Args[1], os.Args[2], os.Args[3]
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	execute := func(args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, goBinary, args...)
		return cmd.CombinedOutput()
	}
	switch mode {
	case "measure":
		prior := ""
		if len(os.Args) > 4 {
			prior = os.Args[4]
		}
		started := time.Now()
		out, err := execute("test", "-mod=readonly", "-run=^$", "-bench=^BenchmarkGreeting$", "-benchtime=20ms", "-count=1", "-benchmem", ".")
		if err != nil {
			return fmt.Errorf("benchmark: %w: %s", err, out)
		}
		match := regexp.MustCompile(`([0-9.]+) ns/op`).FindSubmatch(out)
		if len(match) != 2 {
			return fmt.Errorf("missing actual benchmark sample: %s", out)
		}
		ns, err := strconv.ParseFloat(string(match[1]), 64)
		if err != nil {
			return err
		}
		sample := map[string]any{"ns_per_op": ns, "elapsed_seconds": time.Since(started).Seconds(), "round": 1, "prior": prior}
		outcome := "continue"
		if strings.Contains(prior, `"round":1`) {
			outcome = "ready"
			sample["round"] = 2
		}
		data, _ := json.Marshal(sample)
		if err = os.WriteFile(filepath.Join(scratch, "measurements.json"), data, 0600); err != nil {
			return err
		}
		domain, _ := json.Marshal(map[string]string{"outcome": outcome, "reason": "Measured the candidate; retain both rounds. No performance improvement claim is required."})
		if err = os.WriteFile(filepath.Join(scratch, "domain.json"), domain, 0600); err != nil {
			return err
		}
		fmt.Println(string(data))
		return nil
	case "package":
		out, err := execute("build", "-mod=readonly", "-o", filepath.Join(scratch, "website"), ".")
		if err != nil {
			return fmt.Errorf("build: %w: %s", err, out)
		}
		fmt.Println("Packaged the verified candidate executable.")
		return nil
	case "smoke":
		if len(os.Args) != 5 {
			return fmt.Errorf("retained executable required")
		}
		executable := filepath.Join(scratch, "website")
		data, err := os.ReadFile(os.Args[4])
		if err != nil {
			return err
		}
		if err = os.WriteFile(executable, data, 0700); err != nil {
			return err
		}
		cmd := exec.CommandContext(ctx, executable)
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			return err
		}
		cmd.Stderr = os.Stderr
		if err = cmd.Start(); err != nil {
			return err
		}
		defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 1024), 4096)
		if !scanner.Scan() {
			return fmt.Errorf("website did not report its address")
		}
		address := scanner.Text()
		browser := exec.CommandContext(ctx, os.Getenv("RUNNER_NODE"), os.Getenv("RUNNER_BROWSER_SCRIPT"), address)
		browser.Stdout = os.Stdout
		browser.Stderr = os.Stderr
		return browser.Run()
	default:
		return fmt.Errorf("unsupported fixture command %q", mode)
	}
}
