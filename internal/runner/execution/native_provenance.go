package execution

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
)

const maxNativeFacts = 96

func harnessProvenance(a runner.Assignment, binary, work string) runner.NativeProvenance {
	host, _ := os.Hostname()
	p := a.Setup.Profile
	proof := runner.NativeProvenance{
		Machine:          host,
		Platform:         runtime.GOOS + "/" + runtime.GOARCH,
		Harness:          p.Native.Harness,
		Binary:           binary,
		Model:            p.Model,
		Reasoning:        p.Reasoning,
		Access:           p.Access,
		Permitted:        *runner.Clone(p.Native),
		StartedAt:        time.Now().UTC(),
		CleanupAssurance: "cooperative-known-processes",
		UsageScope:       "cli-turn-cumulative",
		MaxOutputBytes:   p.MaxOutput,
		Gaps: []string{
			"Requested model and effort are recorded; CLI telemetry may not attest the resolved provider/model.",
			"Tool arguments, outputs, private reasoning and credentials are deliberately not retained.",
			"Skill discovery or a SKILL.md read does not prove that its instructions caused a decision.",
			"Native MCP tools retain their harness permissions; shell sandbox and network flags do not constrain remote MCP effects.",
			"Inherited harness caches and third-party tool outputs outside Runner's managed root are not an OS disk quota.",
			"The CLI does not export its complete injected context/catalog. request_sha256 covers Runner's supplied task, role instructions and task context only.",
			"Native profiles retain host file reads. input-only means no product workspace; it does not prohibit reading host configuration or files.",
			"Core owns task delegation: native subagent tools are disabled. Codex hooks are disabled; CLI approval_policy=never denies operations needing approval.",
			"CLI subprocess cleanup follows an inherited per-attempt ownership marker and stable process birth identity, including detached groups. This is cooperative resource ownership, not hostile-process containment; deliberately clearing ownership before spawning is unsupported.",
			"Darwin can hide system-process environments. A fast detached fork through an unseen exited parent may be unobservable; unrelated ambiguous host processes are neither terminated nor treated as this task's blocker.",
		},
	}
	if p.Native.Harness == "codex" {
		proof.Gaps = append(proof.Gaps, "Codex exec does not report a complete available tool/skill inventory; observed calls are a partial inventory.")
		if p.Native.Context == "lean" {
			proof.Gaps = append(proof.Gaps, "Lean context keeps normal account/configuration and project guidance, disables apps/plugins/skill discovery and verifies per-configured-MCP enabled=false overrides. Configuration changed after readiness can introduce a new MCP server; this CLI has no global deny switch.")
		}
	} else {
		proof.UsageScope = "cli-result-cumulative"
	}
	home, _ := os.UserHomeDir()
	codexHome := os.Getenv("CODEX_HOME")
	if codexHome == "" {
		codexHome = filepath.Join(home, ".codex")
	}
	paths := []string{filepath.Join(work, "AGENTS.md"), filepath.Join(work, "CLAUDE.md")}
	if p.Native.Harness == "codex" {
		paths = append(paths, filepath.Join(codexHome, "config.toml"), filepath.Join(codexHome, "AGENTS.md"))
	} else {
		paths = append(paths, filepath.Join(home, ".claude", "CLAUDE.md"))
		proof.Gaps = append(proof.Gaps, "Claude --restricted ignores user/project/local settings. Selected file tools receive a private evidence directory of hardlinks to supplied artifacts, not access to coordinator state.")
	}
	for _, path := range paths {
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		info, err := f.Stat()
		if err != nil || !info.Mode().IsRegular() || info.Size() > 4<<20 {
			f.Close()
			continue
		}
		hash := sha256.New()
		_, err = io.Copy(hash, f)
		f.Close()
		if err == nil {
			proof.Configuration = append(proof.Configuration, runner.NativeFingerprint{Path: path, SHA256: hex.EncodeToString(hash.Sum(nil))})
		}
	}
	return proof
}

// harnessEvents reduces streaming notifications to bounded public facts. It
// never saves raw stdout: those events can contain secrets or private reasoning.
type harnessEvents struct {
	Proof            runner.NativeProvenance
	Final            []byte
	FinalOverflow    bool
	Usage            runner.Usage
	Limit            int
	Failed           string
	Terminal         bool
	ExecutionEndedAt time.Time
	commandLog       []byte
}

func (e *harnessEvents) available(name string) {
	if name == "" || slices.Contains(e.Proof.Available, name) {
		return
	}
	if len(e.Proof.Available) < maxNativeFacts {
		e.Proof.Available = append(e.Proof.Available, runner.Clip(name))
	} else {
		e.Proof.Dropped++
	}
}

func (e *harnessEvents) use(kind, name, status, evidence string) {
	if len(e.Proof.Used) >= maxNativeFacts {
		e.Proof.Dropped++
		return
	}
	e.Proof.Used = append(e.Proof.Used, runner.NativeUse{Kind: kind, Name: runner.Clip(name), Status: runner.Clip(status), Evidence: runner.Clip(evidence)})
}

func (e *harnessEvents) final(text []byte) {
	size := len(text)
	e.Proof.MaxOutputBytes = runner.Bytes(e.Limit)
	e.Proof.FinalResultBytes = &size
	e.FinalOverflow = len(text) > e.Limit
	if e.FinalOverflow {
		e.Final = nil
		return
	}
	e.Final = append(e.Final[:0], text...)
}

func (e *harnessEvents) oversizedEvent(limit int) {
	e.Proof.Dropped++
	e.Proof.OversizedEvents++
	e.Proof.StreamEventLimit = runner.Bytes(limit)
	if !e.Terminal {
		// This event could contain a newer final. Its size/type is unknown;
		// an earlier candidate must not survive as authoritative evidence.
		e.Final, e.FinalOverflow = nil, true
		e.Proof.FinalResultBytes = nil
	}
}

func (e *harnessEvents) accept(line []byte) {
	if e.Terminal {
		e.Proof.Dropped++ // The completed CLI turn owns its final result.
		return
	}
	if e.Proof.FirstEventAt.IsZero() {
		e.Proof.FirstEventAt = time.Now().UTC()
	}
	var event struct {
		Type       string          `json:"type"`
		Subtype    string          `json:"subtype"`
		Model      string          `json:"model"`
		Tools      []string        `json:"tools"`
		Skills     []string        `json:"skills"`
		Result     string          `json:"result"`
		Structured json.RawMessage `json:"structured_output"`
		IsError    bool            `json:"is_error"`
		Item       struct {
			Type      string `json:"type"`
			Name      string `json:"name"`
			Namespace string `json:"namespace"`
			Text      string `json:"text"`
			Command   string `json:"command"`
			Server    string `json:"server"`
			Tool      string `json:"tool"`
			Status    string `json:"status"`
			Message   string `json:"message"`
		} `json:"item"`
		Message json.RawMessage `json:"message"`
		Usage   struct {
			Input        *int64 `json:"input_tokens"`
			Output       *int64 `json:"output_tokens"`
			Cached       *int64 `json:"cached_input_tokens"`
			CacheRead    *int64 `json:"cache_read_input_tokens"`
			CacheCreated *int64 `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(line, &event) != nil {
		e.Proof.Dropped++
		return
	}
	switch event.Type {
	case "item.completed":
		item := event.Item
		switch item.Type {
		case "agent_message":
			e.final([]byte(item.Text))
		case "reasoning": // Never retain reasoning text.
		case "error":
			e.Proof.DiagnosticEvents++
			evidence := ""
			if item.Message != "" {
				digest := sha256.Sum256([]byte(item.Message))
				evidence = "message_sha256 " + hex.EncodeToString(digest[:])
			}
			e.use("diagnostic", "harness_error", "reported", evidence)
		default:
			kind := "event"
			if slices.Contains([]string{"command_execution", "mcp_tool_call", "function_call", "web_search", "file_change", "tool_call"}, item.Type) {
				kind = "tool"
			}
			name := item.Type
			if item.Tool != "" {
				name = item.Server + "/" + item.Tool
			} else if item.Name != "" {
				name = item.Name
				if item.Namespace != "" {
					name = item.Namespace + "/" + name
				}
			}
			evidence := ""
			if item.Command != "" {
				digest := sha256.Sum256([]byte(item.Command))
				evidence = "command sha256:" + hex.EncodeToString(digest[:])
				if strings.Contains(item.Command, "SKILL.md") {
					e.use("skill_reference", "SKILL.md referenced by command; reading is not attested", item.Status, evidence)
					e.skillRead(item.Command, item.Status)
				}
			}
			e.use(kind, name, item.Status, evidence)
		}
	case "turn.completed":
		e.Usage = runner.Usage{InputTokens: event.Usage.Input, OutputTokens: event.Usage.Output, CachedInputTokens: event.Usage.Cached}
		e.Terminal = true
	case "turn.failed", "error":
		e.Proof.DiagnosticEvents++
		e.Failed = "native harness reported an error; raw error omitted because it may contain tool credentials"
		e.Terminal = true
	case "system":
		if event.Subtype == "init" {
			e.Proof.ReportedModel = runner.Clip(event.Model)
			for _, name := range event.Tools {
				e.available("tool:" + name)
			}
			for _, name := range event.Skills {
				e.available("skill:" + name)
			}
		}
	case "assistant":
		var message struct {
			Content []struct {
				Type string `json:"type"`
				Name string `json:"name"`
			} `json:"content"`
		}
		_ = json.Unmarshal(event.Message, &message)
		for _, item := range message.Content {
			if item.Type == "tool_use" {
				kind := "tool"
				if item.Name == "StructuredOutput" {
					kind = "protocol"
				}
				e.use(kind, item.Name, "requested", "")
			}
		}
	case "result":
		e.Terminal = true
		if event.IsError {
			e.Proof.DiagnosticEvents++
			e.Failed = "Claude reported an unsuccessful result"
		}
		if len(event.Structured) > 0 {
			e.final(event.Structured)
		} else if event.Result != "" {
			e.final([]byte(event.Result))
		}
		e.Usage = runner.Usage{OutputTokens: event.Usage.Output, CachedInputTokens: event.Usage.CacheRead}
		if event.Usage.Input != nil && event.Usage.CacheCreated != nil && event.Usage.CacheRead != nil {
			total := *event.Usage.Input + *event.Usage.CacheCreated + *event.Usage.CacheRead
			e.Usage.InputTokens = &total
		}
	}
}

var simpleSkillRead = regexp.MustCompile(`^(?:cat|/bin/cat)\s+["']?(/[^\s"'\x60$*?;|<>]+/SKILL\.md)["']?\s*$`)

// Only an unambiguous single-file cat command is labelled a read. Other command
// references remain references, and no claim about causal influence is made.
func (e *harnessEvents) skillRead(command, status string) {
	match := simpleSkillRead.FindStringSubmatch(strings.TrimSpace(command))
	if len(match) != 2 || status != "completed" {
		return
	}
	f, err := os.Open(match[1])
	if err != nil {
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 4<<20 {
		return
	}
	hash := sha256.New()
	if _, err = io.Copy(hash, f); err == nil {
		e.use("skill_read", match[1], status, "current file sha256:"+hex.EncodeToString(hash.Sum(nil)))
	}
}

func boundedHarnessProof(proof runner.NativeProvenance, limit runner.Bytes) ([]byte, error) {
	for {
		data, err := json.Marshal(proof)
		if err != nil || int64(len(data)) <= int64(limit) {
			return data, err
		}
		proof.Dropped++
		switch {
		case len(proof.Used) > 0:
			proof.Used = proof.Used[:len(proof.Used)-1]
		case len(proof.Available) > 0:
			proof.Available = proof.Available[:len(proof.Available)-1]
		case len(proof.Configuration) > 0:
			proof.Configuration = proof.Configuration[:len(proof.Configuration)-1]
		default:
			return nil, runner.ErrCapacity
		}
	}
}
