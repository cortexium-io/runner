// runner-measure runs one configured Go measurement. Runner owns repetition.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type measurement struct {
	Round    int       `json:"round"`
	Package  string    `json:"package"`
	Selector string    `json:"selector"`
	Started  time.Time `json:"started"`
	Seconds  float64   `json:"seconds"`
	Output   string    `json:"output"`
	Skipped  bool      `json:"skipped"`
}

func selection(pkg, selector, previous string) (int, error) {
	if pkg != "..." && (!regexp.MustCompile(`^[A-Za-z0-9_.-]+(/[A-Za-z0-9_.-]+)*$`).MatchString(pkg) || strings.Contains(pkg, "..")) {
		return 0, errors.New("measurement package must be repository-relative")
	}
	if selector != "none" {
		if len(selector) > 256 || !strings.HasPrefix(selector, "^Benchmark") {
			return 0, errors.New("measurement needs a named anchored benchmark selector or none")
		}
		if _, err := regexp.Compile(selector); err != nil {
			return 0, err
		}
	}
	if previous == "" {
		return 1, nil
	}
	match := regexp.MustCompile(`runner-measure round=([0-9]+) `).FindStringSubmatch(previous)
	if len(match) == 2 {
		n, err := strconv.Atoi(match[1])
		if err == nil && n >= 1 && n < 128 {
			return n + 1, nil
		}
	}
	return 0, errors.New("measurement feedback is not a preceding bounded round")
}

func main() {
	goBinary := flag.String("go", "", "configured Go binary")
	source := flag.String("source", "", "read-only source")
	scratch := flag.String("scratch", "", "owned output")
	pkg := flag.String("package", "", "relative package")
	selector := flag.String("benchmark", "none", "anchored benchmark selector or none")
	previous := flag.String("previous", "", "retained prior check")
	flag.Parse()
	if err := run(*goBinary, *source, *scratch, *pkg, *selector, *previous); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(goBinary, source, scratch, pkg, selector, previous string) error {
	round, err := selection(pkg, selector, previous)
	if err != nil {
		return err
	}
	if !filepath.IsAbs(goBinary) || !filepath.IsAbs(source) || !filepath.IsAbs(scratch) {
		return errors.New("explicit absolute runtime/source/scratch paths required")
	}
	m := measurement{Round: round, Package: pkg, Selector: selector, Started: time.Now().UTC(), Skipped: selector == "none"}
	outcome := "ready"
	if !m.Skipped {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, goBinary, "test", "-mod=readonly", "-run", "^$", "-bench", selector, "-benchtime=1x", "-count=1", "./"+pkg)
		cmd.Dir = source
		var output boundedOutput
		cmd.Stdout = &output
		cmd.Stderr = &output
		if err = cmd.Run(); err != nil {
			return errors.New("configured measurement failed; " + output.String())
		}
		m.Output = output.String()
		m.Seconds = time.Since(m.Started).Seconds()
		if !regexp.MustCompile(`(?m)^Benchmark\S+\s+\d+\s+`).MatchString(m.Output) {
			return errors.New("benchmark selector executed no measurement")
		}
		if round == 1 {
			outcome = "continue"
		}
	}
	if err = os.MkdirAll(scratch, 0700); err != nil {
		return err
	}
	for name, value := range map[string]any{
		"measurements.json": m,
		"domain.json":       map[string]string{"outcome": outcome, "reason": "Bounded measurement; Runner owns the next owner/review stage"},
	} {
		data, err := json.Marshal(value)
		if err != nil {
			return err
		}
		if err = os.WriteFile(filepath.Join(scratch, name), data, 0600); err != nil {
			return err
		}
	}
	fmt.Printf("runner-measure round=%d selector=%s skipped=%t seconds=%.6f; actual output retained in measurements.json\n", round, selector, m.Skipped, m.Seconds)
	for _, line := range strings.Split(m.Output, "\n") {
		if strings.HasPrefix(line, "Benchmark") {
			fmt.Println(line)
		}
	}
	return nil
}

type boundedOutput struct{ bytes.Buffer }

func (b *boundedOutput) Write(p []byte) (int, error) {
	if len(p) > 256*1024-b.Len() {
		return 0, errors.New("measurement output exceeds 256KiB")
	}
	return b.Buffer.Write(p)
}
