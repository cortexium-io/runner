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
	Round          int                        `json:"round"`
	Package        string                     `json:"package"`
	Selector       string                     `json:"selector"`
	Started        time.Time                  `json:"started"`
	Seconds        float64                    `json:"seconds"`
	Output         string                     `json:"output"`
	Skipped        bool                       `json:"skipped"`
	Baseline       bool                       `json:"baseline,omitempty"`
	Failed         bool                       `json:"failed,omitempty"`
	Error          string                     `json:"error,omitempty"`
	Evidence       map[string]json.RawMessage `json:"evidence,omitempty"`
	EvidenceStatus string                     `json:"evidence_status"`
	EvidenceNote   string                     `json:"evidence_note,omitempty"`
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
	baseline := flag.Bool("baseline", false, "retain an unsuccessful benchmark as investigation feedback")
	flag.Parse()
	if err := run(*goBinary, *source, *scratch, *pkg, *selector, *previous, *baseline); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(goBinary, source, scratch, pkg, selector, previous string, baseline bool) error {
	round, err := selection(pkg, selector, previous)
	if err != nil {
		return err
	}
	if !filepath.IsAbs(goBinary) || !filepath.IsAbs(source) || !filepath.IsAbs(scratch) {
		return errors.New("explicit absolute runtime/source/scratch paths required")
	}
	if baseline && (selector == "none" || previous != "") {
		return errors.New("baseline needs an actual named benchmark and no preceding round")
	}
	m := measurement{
		Round:          round,
		Package:        pkg,
		Selector:       selector,
		Started:        time.Now().UTC(),
		Skipped:        selector == "none",
		Baseline:       baseline,
		EvidenceStatus: "not_requested",
	}
	if err = os.MkdirAll(scratch, 0700); err != nil {
		return err
	}
	outcome := "ready"
	if !m.Skipped {
		evidence := filepath.Join(scratch, "evidence")
		if err = os.Mkdir(evidence, 0700); err != nil {
			return fmt.Errorf("create fresh measurement evidence directory: %w", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, goBinary, "test", "-mod=readonly", "-run", "^$", "-bench", selector, "-benchtime=1x", "-count=1", "./"+pkg)
		cmd.Dir = source
		for _, value := range os.Environ() {
			if !strings.HasPrefix(value, "RUNNER_MEASURE_EVIDENCE=") {
				cmd.Env = append(cmd.Env, value)
			}
		}
		cmd.Env = append(cmd.Env, "RUNNER_MEASURE_EVIDENCE="+evidence)
		var output boundedOutput
		cmd.Stdout = &output
		cmd.Stderr = &output
		err = cmd.Run()
		m.Output = output.String()
		m.Seconds = time.Since(m.Started).Seconds()
		if err != nil {
			var exit *exec.ExitError
			if ctx.Err() != nil || !errors.As(err, &exit) || exit.ExitCode() != 1 {
				return errors.New("configured measurement failed; " + m.Output)
			}
			m.Failed, m.Error = true, "Go benchmark failed; inspect retained output before claiming a performance baseline"
		} else if !regexp.MustCompile(`(?m)^Benchmark\S+\s+\d+\s+`).MatchString(m.Output) {
			m.Failed, m.Error = true, "benchmark selector executed no measurement"
		}
		m.Evidence, err = collectEvidence(evidence)
		switch {
		case err != nil:
			m.Failed, m.EvidenceStatus = true, "invalid"
			if m.Error != "" {
				m.Error += "; "
			}
			m.Error += "structured benchmark evidence rejected: " + err.Error()
		case len(m.Evidence) == 0:
			m.EvidenceStatus = "incomplete"
			m.EvidenceNote = "No structured request/phase evidence was provided; stdout may be truncated."
		default:
			m.EvidenceStatus = "provided"
			m.EvidenceNote = "Declared files retained; request/phase completeness and acceptance require review."
		}
		if round == 1 || m.Failed {
			outcome = "continue"
		}
	}
	reason := "Bounded measurement; Runner owns the next owner/review stage"
	if m.Failed {
		reason = m.Error
	}
	if len(reason) > 480 {
		reason = strings.ToValidUTF8(reason[:480], "?") + " [see measurements.json]"
	}
	for name, value := range map[string]any{
		"measurements.json": m,
		"domain.json":       map[string]string{"outcome": outcome, "reason": reason},
	} {
		data, err := json.Marshal(value)
		if err != nil {
			return err
		}
		if len(data) > 16<<20 {
			return errors.New("measurement report exceeds 16MiB artifact ceiling")
		}
		if err = os.WriteFile(filepath.Join(scratch, name), data, 0600); err != nil {
			return err
		}
	}
	if m.Failed {
		fmt.Println(m.Error)
	}
	if baseline || m.Failed {
		text := m.Output
		if len(text) > 1024 {
			text = text[:512] + "\n[truncated; complete output retained in measurements.json]\n" + text[len(text)-512:]
		}
		fmt.Println("measurement output:\n" + strings.ToValidUTF8(text, "?"))
	} else {
		for _, line := range strings.Split(m.Output, "\n") {
			if strings.HasPrefix(line, "Benchmark") {
				fmt.Println(line)
			}
		}
	}
	// Command results retain the diagnostic tail. Keep routing metadata last so
	// a long cohort cannot erase the round required by the next measurement.
	fmt.Printf("runner-measure round=%d selector=%s skipped=%t baseline=%t failed=%t seconds=%.6f; actual output retained in measurements.json\n", round, selector, m.Skipped, m.Baseline, m.Failed, m.Seconds)
	return nil
}

type boundedOutput struct{ bytes.Buffer }

func (b *boundedOutput) Write(p []byte) (int, error) {
	if len(p) > 256*1024-b.Len() {
		return 0, errors.New("measurement output exceeds 256KiB")
	}
	return b.Buffer.Write(p)
}
