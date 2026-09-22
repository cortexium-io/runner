package execution

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/metrics"
	"github.com/cortexium-io/runner/internal/subprocess"
)

// usageStream keeps counters, not a transcript. A single oversized/malformed
// record cannot prevent later usage records from being observed.
type usageStream struct {
	ctx          context.Context
	activity     activityStream
	kind         string
	pending      []byte
	discarding   bool
	observed     bool
	latest       metrics.Usage
	piMessages   map[[32]byte]bool
	piIncomplete bool
}

func observeHarness(ctx context.Context, kind string) (context.Context, *usageStream) {
	stream := &usageStream{kind: kind, ctx: ctx}
	ctx = subprocess.WithStdoutObserver(ctx, stream)
	ctx = subprocess.WithCleanupObserver(ctx, func() func(error) {
		finish := metrics.StartStage(ctx, metrics.StageHarnessCleanup)
		return func(err error) {
			if err != nil {
				finish(metrics.StageOutcomeFailed, string(FailureCleanupUnresolved), string(RetryManual), metrics.Usage{})
				return
			}
			finish(metrics.StageOutcomeSucceeded, "", "", metrics.Usage{})
		}
	})
	return ctx, stream
}

func (s *usageStream) Write(data []byte) (int, error) {
	s.observed = true
	n := len(data)
	if s.kind == config.HarnessClaudeCLI {
		// Claude's JSON mode is one envelope, not JSONL; whitespace/newlines
		// inside that envelope must not discard otherwise valid counters.
		if !s.discarding && len(s.pending)+n <= maxHarnessResultBytes {
			s.pending = append(s.pending, data...)
		} else {
			s.pending = nil
			s.discarding = true
		}
		return n, nil
	}
	for len(data) > 0 {
		index := bytes.IndexByte(data, '\n')
		part := data
		if index >= 0 {
			part = data[:index]
		}
		if !s.discarding {
			if len(s.pending)+len(part) <= maxHarnessResultBytes {
				s.pending = append(s.pending, part...)
			} else {
				s.pending = nil
				s.discarding = true
				s.partial()
				s.activity.summary.Coverage = "partial"
				if s.kind == config.HarnessPiCLI {
					s.piIncomplete = true
				}
			}
		}
		if index < 0 {
			break
		}
		if !s.discarding {
			s.consume(s.pending)
		}
		s.pending = s.pending[:0]
		s.discarding = false
		data = data[index+1:]
	}
	return n, nil
}

func (s *usageStream) partial() {
	if s.latest.Reported() {
		s.latest.Coverage = metrics.UsagePartial
	}
}

func (s *usageStream) finish(fallback string, runErr error) metrics.Usage {
	// Injected runners may return synthetic output without using OSRunner's
	// observer. Production consumes the original stream exactly once.
	live := s.observed
	if !live {
		_, _ = io.WriteString(s, fallback)
	}
	if !s.discarding && len(s.pending) > 0 {
		s.consume(s.pending)
	}
	s.pending = nil
	if !live {
		// Buffered fallback output has no trustworthy event receipt timing.
		s.activity = activityStream{}
	}
	if runErr != nil {
		s.partial()
	}
	if !s.latest.Reported() {
		s.latest.Coverage = metrics.UsageUnavailable
	}
	if s.ctx != nil {
		metrics.RecordHarnessActivity(s.ctx, s.activity.finish())
	}
	return s.latest
}

func (s *usageStream) consume(line []byte) {
	s.activity.consume(s.kind, line, time.Now().UTC())
	var event struct {
		Type     string            `json:"type"`
		Message  json.RawMessage   `json:"message"`
		Messages []json.RawMessage `json:"messages"`
	}
	if json.Unmarshal(line, &event) != nil {
		s.partial()
		if s.kind == config.HarnessPiCLI {
			s.piIncomplete = true
		}
		return
	}
	switch s.kind {
	case config.HarnessCodexCLI:
		if event.Type == "turn.started" || event.Type == "turn.failed" || event.Type == "item.started" {
			s.partial()
		}
		if event.Type != "turn.completed" && event.Type != "event_msg" {
			return
		}
		usage := parseCodexUsage(string(line))
		if usage.Available && metrics.ValidateUsage(usage) == nil {
			s.latest = usage
		} else if usage.Available || event.Type == "turn.completed" {
			s.partial()
		}
	case config.HarnessClaudeCLI:
		var envelope claudeResultEnvelope
		if json.Unmarshal(line, &envelope) != nil {
			return
		}
		usage := usageFromClaudeEnvelope(envelope)
		if metrics.ValidateUsage(usage) == nil {
			usage.Coverage = metrics.UsageComplete
			if !usage.Reported() {
				usage.Coverage = metrics.UsageUnavailable
			}
			s.latest = usage
		} else {
			s.partial()
		}
	case config.HarnessPiCLI:
		if event.Type == "agent_start" {
			s.partial()
		}
		if event.Type == "agent_end" {
			// Each agent loop reports its own messages, not an invocation-wide
			// cumulative total. Include all loops without recounting message_end.
			for _, message := range event.Messages {
				s.consumePiMessage(message)
			}
			if s.latest.Reported() && !s.piIncomplete {
				s.latest.Coverage = metrics.UsageComplete
			}
			return
		}
		if event.Type == "message_end" {
			s.consumePiMessage(event.Message)
		}
	}
}

func (s *usageStream) consumePiMessage(message json.RawMessage) {
	var header struct {
		Role string `json:"role"`
	}
	if json.Unmarshal(message, &header) != nil || header.Role != "assistant" {
		return
	}
	// Pi serializes the same timestamped message in both events. Ignore
	// insignificant whitespace; retain only its digest, never message content.
	var compact bytes.Buffer
	if json.Compact(&compact, message) != nil {
		return
	}
	digest := sha256.Sum256(compact.Bytes())
	if s.piMessages[digest] {
		return
	}
	if len(s.piMessages) >= 4096 {
		s.piIncomplete = true
		s.partial()
		return
	}
	usage, err := usageFromPiEventStream(`{"type":"agent_end","messages":[` + compact.String() + `]}`)
	if err != nil || !usage.Available {
		s.piIncomplete = true
		s.partial()
		return
	}
	if s.piMessages == nil {
		s.piMessages = make(map[[32]byte]bool)
	}
	s.piMessages[digest] = true
	total := s.latest.Add(usage)
	if metrics.ValidateUsage(total) != nil {
		s.piIncomplete = true
		s.partial()
		return
	}
	s.latest = total
	s.partial()
}

func runCodexWithUsage(ctx context.Context, run subprocess.Runner, command string, args []string, dir string, timeout time.Duration, input io.Reader) (subprocess.Result, metrics.Usage, error) {
	ctx, marker, err := subprocess.PrepareHarness(ctx)
	if err != nil {
		return subprocess.Result{}, metrics.Usage{Coverage: metrics.UsageUnavailable}, err
	}
	// Codex can filter inherited variables. Set only this non-secret marker;
	// preserve the operator's policy for every other environment variable.
	args = append(append([]string(nil), args...), "-c", fmt.Sprintf("shell_environment_policy.set.%s=%q", subprocess.OwnershipEnvironmentVariable, marker))
	ctx, stream := observeHarness(ctx, config.HarnessCodexCLI)
	result, err := subprocess.RunBoundedHeadTailInput(ctx, run, command, args, dir, timeout, input, maxHarnessDiagnosticBytes, harnessTruncationMarker)
	return result, stream.finish(result.Stdout, err), err
}
