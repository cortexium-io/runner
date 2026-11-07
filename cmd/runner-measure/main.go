// runner-measure runs one configured Go measurement. Runner owns repetition.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"hash"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type measurement struct {
	Round                 int                        `json:"round"`
	Package               string                     `json:"package"`
	Selector              string                     `json:"selector"`
	Started               time.Time                  `json:"started"`
	Seconds               float64                    `json:"seconds"`
	Output                string                     `json:"output"`
	OutputBytes           int64                      `json:"output_bytes"`
	OutputSHA256          string                     `json:"output_sha256"`
	OutputComplete        bool                       `json:"output_complete"`
	CollectionFailed      bool                       `json:"collection_failed,omitempty"`
	PreviousCompleteRound int                        `json:"previous_complete_round"`
	Skipped               bool                       `json:"skipped"`
	Baseline              bool                       `json:"baseline,omitempty"`
	Failed                bool                       `json:"failed,omitempty"`
	Error                 string                     `json:"error,omitempty"`
	Evidence              map[string]json.RawMessage `json:"evidence,omitempty"`
	EvidenceStatus        string                     `json:"evidence_status"`
	EvidenceNote          string                     `json:"evidence_note,omitempty"`
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
	matches := regexp.MustCompile(`(?m)^runner-measure (?:round=([0-9]+) |collection-incomplete previous_complete_round=([0-9]+)(?:\s|$))`).FindAllStringSubmatch(previous, -1)
	if len(matches) > 0 {
		match := matches[len(matches)-1]
		value, minimum := match[1], 1
		if match[2] != "" {
			value, minimum = match[2], 0
		}
		n, err := strconv.Atoi(value)
		if err == nil && n >= minimum && n < 128 {
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
	repairCollection := flag.Bool("repair-collection", false, "request bounded source correction for known stdout collection overflow")
	flag.Parse()
	if err := run(*goBinary, *source, *scratch, *pkg, *selector, *previous, *baseline, *repairCollection); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(goBinary, source, scratch, pkg, selector, previous string, baseline, repairCollection bool) error {
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
	if repairCollection && (baseline || selector == "none") {
		return errors.New("collection repair requires an actual candidate benchmark")
	}
	m := measurement{
		Round:                 round,
		Package:               pkg,
		Selector:              selector,
		Started:               time.Now().UTC(),
		Skipped:               selector == "none",
		Baseline:              baseline,
		EvidenceStatus:        "not_requested",
		PreviousCompleteRound: round - 1,
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
		// Verbose benchmarks stream B.Log output instead of Go's ten-line
		// buffered-log preview. The same output ceiling still applies.
		cmd := exec.CommandContext(ctx, goBinary, "test", "-v", "-mod=readonly", "-run", "^$", "-bench", selector, "-benchtime=1x", "-count=1", "./"+pkg)
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
		m.OutputBytes, m.OutputSHA256, m.OutputComplete = output.total, output.sha256(), !output.exceeded
		m.Seconds = time.Since(m.Started).Seconds()
		knownExit := err == nil && cmd.ProcessState != nil && cmd.ProcessState.Exited() && cmd.ProcessState.ExitCode() == 0 && ctx.Err() == nil
		if err != nil {
			var exit *exec.ExitError
			if ctx.Err() != nil || !errors.As(err, &exit) || exit.ExitCode() != 1 {
				knownExit = false
				if output.exceeded {
					m.Failed, m.Error = true, "Go measurement did not exit normally; retained output is incomplete"
				} else {
					return errors.New("configured measurement failed; " + m.Output)
				}
			} else {
				m.Failed, m.Error = true, "Go benchmark failed; inspect retained output before claiming a performance baseline"
			}
		} else if !output.exceeded && !regexp.MustCompile(`(?m)^Benchmark\S+\s+\d+\s+`).MatchString(m.Output) {
			m.Failed, m.Error = true, "benchmark selector executed no measurement"
		}
		m.Evidence, err = collectEvidence(evidence)
		switch {
		case err != nil:
			m.Failed, m.CollectionFailed, m.EvidenceStatus = true, true, "invalid"
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
		if output.exceeded {
			m.Failed, m.CollectionFailed = true, true
			if m.Error != "" {
				m.Error += "; "
			}
			m.Error += "measurement output exceeds 256KiB; retained prefix is incomplete"
		}
		if m.CollectionFailed {
			outcome = "blocked"
			if repairCollection && output.exceeded && knownExit && err == nil {
				outcome = "changes_requested"
			}
		} else if round == 1 || m.Failed {
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
	for _, report := range []struct {
		name  string
		value any
	}{
		{"measurements.json", m},
		{"domain.json", map[string]string{"outcome": outcome, "reason": reason}},
	} {
		var encoded bytes.Buffer
		encoder := json.NewEncoder(&encoded)
		encoder.SetEscapeHTML(false) // These are JSON files, not embedded HTML.
		if err := encoder.Encode(report.value); err != nil {
			return err
		}
		if encoded.Len() > 16<<20 {
			return errors.New("measurement report exceeds 16MiB artifact ceiling")
		}
		if err = os.WriteFile(filepath.Join(scratch, report.name), encoded.Bytes(), 0600); err != nil {
			return err
		}
	}
	if m.CollectionFailed {
		fmt.Println("Measurement collection incomplete; inspect the bounded diagnostic artifact")
		fmt.Printf("runner-measure collection-incomplete previous_complete_round=%d\n", m.PreviousCompleteRound)
		return nil
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

type boundedOutput struct {
	bytes.Buffer
	exceeded bool
	total    int64
	digest   hash.Hash
}

func (b *boundedOutput) ReadFrom(r io.Reader) (int64, error) {
	return io.Copy(struct{ io.Writer }{b}, r)
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	if b.digest == nil {
		b.digest = sha256.New()
	}
	b.total += int64(len(p))
	_, _ = b.digest.Write(p)
	b.exceeded = b.total > 256<<10
	_, _ = b.Buffer.Write(p[:min(len(p), (256<<10)-b.Len())])
	return len(p), nil
}

func (b *boundedOutput) sha256() string {
	if b.digest == nil {
		return hex.EncodeToString(sha256.New().Sum(nil))
	}
	return hex.EncodeToString(b.digest.Sum(nil))
}
