package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/execution"
	"github.com/cortexium-io/runner/internal/setup"
	"github.com/cortexium-io/runner/internal/subprocess"
)

const liveHarnessProbeToken = "cortexium-runner-live-probe-v1"

var liveHarnessProbeSchema = []byte(`{
  "type": "object",
  "required": ["status", "token"],
  "properties": {
    "status": {"type": "string", "enum": ["ready"]},
    "token": {"type": "string", "enum": ["cortexium-runner-live-probe-v1"]}
  },
  "additionalProperties": false
}`)

func runDoctor(ctx context.Context, args []string, stdout io.Writer) error {
	flags := newFlagSet("doctor", "cortexium-runner doctor [--config PATH] [--fix] [--offline] [--probe-harnesses] [--json]", stdout)
	configPath := flags.String("config", "", "trusted operator config path; defaults to .cortexium/runner.json")
	projectDir := flags.String("project-dir", "", "project Git repository to inspect; overrides project_dir from config")
	fix := flags.Bool("fix", false, "replace differing bundled Runner-managed role skills")
	offline := flags.Bool("offline", false, "validate only the config and bundled skills without network access")
	probeHarnesses := flags.Bool("probe-harnesses", false, "make one minimal live model call per distinct configured harness profile")
	jsonOutput := flags.Bool("json", false, "write the complete capability report as JSON")
	proceed, err := parseFlags(flags, args, "doctor")
	if err != nil || !proceed {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("doctor does not accept positional arguments")
	}
	if *offline && *probeHarnesses {
		return errors.New("--probe-harnesses cannot be combined with --offline")
	}
	if *fix && *jsonOutput {
		return errors.New("--fix cannot be combined with --json because repair progress is human-readable")
	}
	*configPath = resolveRunnerConfigPath(*configPath, *projectDir)

	cfg := config.Config{}
	var staticReport *config.StaticCheckReport
	configExplicit := false
	flags.Visit(func(value *flag.Flag) {
		if value.Name == "config" {
			configExplicit = true
		}
	})
	_, statErr := os.Stat(*configPath)
	if statErr == nil {
		loaded, report, checkErr := config.CheckTrustedConfig(*configPath)
		staticReport = &report
		if !*jsonOutput {
			writeDoctorConfiguration(stdout, report)
		}
		if checkErr != nil {
			if *jsonOutput {
				if err := writeDoctorJSON(stdout, staticReport, nil, nil); err != nil {
					return err
				}
			}
			return fmt.Errorf("configuration is not ready: %w", checkErr)
		}
		cfg = loaded
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return fmt.Errorf("inspect doctor config: %w", statErr)
	} else if configExplicit || *fix || *offline || *probeHarnesses {
		return fmt.Errorf("doctor config %s does not exist", *configPath)
	}
	if *fix {
		fmt.Fprintln(stdout, "Bundled Runner role skills")
		if err := installInitSkills(cfg, *configPath, true, false, stdout); err != nil {
			return fmt.Errorf("repair bundled Runner role skills: %w", err)
		}
	}
	localConfigReady := true
	if *offline {
		if *jsonOutput {
			if err := writeDoctorJSON(stdout, staticReport, nil, nil); err != nil {
				return err
			}
		} else if localConfigReady {
			fmt.Fprintln(stdout, "Configuration ready: yes")
		} else {
			writeStateLine(stdout, toneFailure, "Configuration ready: no")
		}
		if !localConfigReady {
			return errors.New("local Runner config is not safely excluded from Git; follow the doctor recommendation above")
		}
		return nil
	}
	if strings.TrimSpace(*projectDir) != "" {
		cfg.ProjectDir = *projectDir
	} else if strings.TrimSpace(cfg.ProjectDir) == "" {
		cfg.ProjectDir = "."
	}
	if !*jsonOutput {
		writeProgress(stdout, "Checking local tools, GitHub, workspace, and Project readiness…")
	}
	report := setup.NewInspector(cfg, nil).Inspect(ctx, setup.InspectionRequest{
		ProjectDir: cfg.ProjectDir, Requirements: cfg.EffectiveDoctorRequirements(),
	})
	report.Ready = report.Ready && localConfigReady
	var probes []liveHarnessProbe
	if *probeHarnesses {
		if !*jsonOutput {
			writeProgress(stdout, "Running live harness probes. This may take a moment…")
		}
		probes, err = probeConfiguredHarnesses(ctx, cfg, nil)
		if err != nil {
			return err
		}
		for _, probe := range probes {
			if !probe.Ready {
				report.Ready = false
			}
		}
	}
	if *jsonOutput {
		if err := writeDoctorJSON(stdout, staticReport, &report, probes); err != nil {
			return err
		}
	} else {
		writeDoctorReport(stdout, report, probes)
	}
	if !report.Ready {
		return errors.New("runner is not ready; follow the doctor recommendations above")
	}
	return nil
}

type doctorJSONReport struct {
	Configuration *config.StaticCheckReport `json:"configuration,omitempty"`
	Readiness     *setup.InspectionReport   `json:"readiness,omitempty"`
	HarnessProbes []liveHarnessProbe        `json:"harness_probes,omitempty"`
}

type liveHarnessProbe struct {
	Harness string   `json:"harness"`
	Command string   `json:"command"`
	Model   string   `json:"model,omitempty"`
	Roles   []string `json:"roles"`
	Skills  []string `json:"skills"`
	Ready   bool     `json:"ready"`
	Detail  string   `json:"detail"`
}

type liveHarnessProbeResult struct {
	Status string `json:"status"`
	Token  string `json:"token"`
}

type liveHarnessProbeCandidate struct {
	harness string
	command string
	model   string
	roles   []string
	skills  []string
	exec    config.ExecutionConfig
}

func probeConfiguredHarnesses(ctx context.Context, cfg config.Config, run subprocess.Runner) ([]liveHarnessProbe, error) {
	runtimeCfg, err := cfg.Resolve()
	if err != nil {
		return nil, fmt.Errorf("resolve config for live harness probes: %w", err)
	}
	candidates := map[string]*liveHarnessProbeCandidate{}
	for _, role := range runtimeCfg.ExecutionRoleIDs() {
		profile, exists := runtimeCfg.RoleProfile(role)
		if !exists {
			return nil, fmt.Errorf("resolve role %q for live harness probe", role)
		}
		execCfg := runtimeCfg.Execution(role, profile.Harness, runtimeCfg.ProjectDir)
		harness := execCfg.Harness
		model := ""
		if harness.Model != nil {
			model = strings.TrimSpace(*harness.Model)
		}
		key := strings.Join([]string{profile.Harness, strings.TrimSpace(harness.Command), model, strings.TrimSpace(harness.ReasoningEffort)}, "\x00")
		candidate := candidates[key]
		if candidate == nil {
			candidate = &liveHarnessProbeCandidate{
				harness: profile.Harness, command: strings.TrimSpace(harness.Command), model: model,
				exec: execCfg,
			}
			candidates[key] = candidate
		}
		candidate.roles = appendUnique(candidate.roles, role)
		for _, skill := range profile.Skills {
			candidate.skills = appendUnique(candidate.skills, strings.TrimSpace(skill))
		}
	}
	keys := make([]string, 0, len(candidates))
	for key := range candidates {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	probes := make([]liveHarnessProbe, 0, len(keys))
	for _, key := range keys {
		candidate := candidates[key]
		sort.Strings(candidate.roles)
		sort.Strings(candidate.skills)
		probe := liveHarnessProbe{
			Harness: candidate.harness, Command: candidate.command, Model: candidate.model,
			Roles: candidate.roles, Skills: candidate.skills,
		}
		prompt := "This is an explicit Cortexium Runner live-readiness probe. Do not edit files or run commands. " +
			"Prove that the configured CLI can authenticate, invoke its selected model, and return the required structured result. " +
			"Return status ready and token " + liveHarnessProbeToken + ". Return only the required JSON object. " +
			"The configured Runner role skills for this execution profile are: " + strings.Join(candidate.skills, ", ") + "."
		structured, runErr := execution.RunProbeWithUsage(ctx, candidate.harness, candidate.exec, prompt, liveHarnessProbeSchema, run)
		if runErr != nil {
			probe.Detail = runErr.Error()
			probes = append(probes, probe)
			continue
		}
		var decoded liveHarnessProbeResult
		decoder := json.NewDecoder(strings.NewReader(execution.NormalizeStructuredResult(structured.Message)))
		decoder.DisallowUnknownFields()
		if decodeErr := decoder.Decode(&decoded); decodeErr != nil {
			probe.Detail = "decode live probe result: " + decodeErr.Error()
			probes = append(probes, probe)
			continue
		}
		if decoded.Status != "ready" || decoded.Token != liveHarnessProbeToken {
			probe.Detail = "live probe returned an unexpected status or token"
			probes = append(probes, probe)
			continue
		}
		probe.Ready = true
		probe.Detail = "live model invocation and structured output succeeded"
		probes = append(probes, probe)
	}
	return probes, nil
}

func appendUnique(values []string, value string) []string {
	if value == "" {
		return values
	}
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func writeDoctorJSON(stdout io.Writer, configuration *config.StaticCheckReport, readiness *setup.InspectionReport, probes []liveHarnessProbe) error {
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(doctorJSONReport{Configuration: configuration, Readiness: readiness, HarnessProbes: probes}); err != nil {
		return fmt.Errorf("write doctor report: %w", err)
	}
	return nil
}

func writeDoctorConfiguration(output io.Writer, report config.StaticCheckReport) {
	fmt.Fprintln(output, "Configuration")
	for _, check := range report.Checks {
		marker := "✓"
		tone := toneSuccess
		if check.Status != config.StaticCheckPassed {
			marker = "✗"
			tone = toneFailure
		}
		writeStateLine(output, tone, "  %s %s: %s", marker, check.ID, check.Detail)
	}
	fmt.Fprintln(output)
}

func writeDoctorReport(output io.Writer, report setup.InspectionReport, probes []liveHarnessProbe) {
	fmt.Fprintln(output, "Local Project Runner Doctor")
	fmt.Fprintln(output)
	fmt.Fprintln(output, "Core")
	writeDoctorCapability(output, report.Snapshot.Capabilities, config.CapabilityTypeLocalTool, "git", "git")
	writeDoctorCapability(output, report.Snapshot.Capabilities, config.CapabilityTypeLocalTool, "gh", "GitHub CLI")
	if report.GitHubAuth != nil {
		marker := "✗"
		tone := toneFailure
		if report.GitHubAuth.Status == setup.CapabilityAvailable {
			marker = "✓"
			tone = toneSuccess
		}
		writeStateLine(output, tone, "  %s %s", marker, report.GitHubAuth.Detail)
	}
	fmt.Fprintln(output)
	safeToolIDs := []struct{ id, label string }{{"node", "Node.js"}, {"npm", "npm"}, {"npx", "npx"}, {"chrome", "isolated Chrome/Chromium"}}
	hasSafeTools := false
	for _, tool := range safeToolIDs {
		for _, capability := range report.Snapshot.Capabilities {
			if capability.Type == config.CapabilityTypeLocalTool && capability.ID == tool.id {
				hasSafeTools = true
				break
			}
		}
	}
	if hasSafeTools {
		fmt.Fprintln(output, "Safe development tools")
		for _, tool := range safeToolIDs {
			writeDoctorCapability(output, report.Snapshot.Capabilities, config.CapabilityTypeLocalTool, tool.id, tool.label)
		}
		fmt.Fprintln(output)
	}
	fmt.Fprintln(output, "AI harnesses")
	for _, harness := range report.Harnesses {
		marker := "✗"
		tone := toneFailure
		if harness.Ready {
			marker = "✓"
			tone = toneSuccess
		} else if harness.Status == setup.CapabilityAvailable {
			marker = "!"
			tone = toneWarning
		}
		version := harness.Version
		if version != "" {
			version = " · " + version
		}
		writeStateLine(output, tone, "  %s %s%s", marker, harness.DisplayName, version)
		fmt.Fprintf(output, "    %s\n", harness.Detail)
		if harness.ExecutionPolicy != "" {
			fmt.Fprintf(output, "    Execution policy: %s\n", harness.ExecutionPolicy)
		}
	}
	fmt.Fprintln(output)
	fmt.Fprintln(output, "Role skills")
	installedHarnesses := map[string]bool{}
	for _, harness := range report.Harnesses {
		if harness.Status == setup.CapabilityAvailable {
			installedHarnesses[harness.Kind] = true
		}
	}
	for _, capability := range report.Snapshot.Capabilities {
		if capability.Type != config.CapabilityTypeSkill {
			continue
		}
		harness, _, _ := strings.Cut(capability.ID, "/")
		if !installedHarnesses[harness] {
			continue
		}
		marker := "✗"
		tone := toneFailure
		if capability.Status == setup.CapabilityAvailable {
			marker = "✓"
			tone = toneSuccess
		} else if capability.Status == setup.CapabilityBlocked {
			marker = "!"
			tone = toneWarning
		}
		writeStateLine(output, tone, "  %s %s", marker, capability.ID)
	}
	fmt.Fprintln(output)
	fmt.Fprintln(output, "MCP servers")
	if report.RequiredMCPs == 0 {
		writeStateLine(output, toneMuted, "  ○ No MCP servers required by the current profile")
	} else {
		for _, capability := range report.Snapshot.Capabilities {
			if capability.Type == config.CapabilityTypeMCPServer {
				marker := "✗"
				tone := toneFailure
				if capability.Status == setup.CapabilityAvailable {
					marker = "✓"
					tone = toneSuccess
				} else if capability.Status == setup.CapabilityBlocked {
					marker = "!"
					tone = toneWarning
				}
				writeStateLine(output, tone, "  %s %s", marker, capability.ID)
			}
		}
	}
	if report.Project != nil {
		fmt.Fprintln(output)
		fmt.Fprintln(output, "Workspace")
		marker := "✗"
		tone := toneFailure
		if report.Project.Status == setup.CapabilityAvailable {
			marker = "✓"
			tone = toneSuccess
		} else if report.Project.Status == setup.CapabilityBlocked {
			marker = "!"
			tone = toneWarning
		}
		writeStateLine(output, tone, "  %s %s", marker, report.Project.Detail)
	}
	if report.GitHubRepository != nil {
		repository := report.GitHubRepository
		fmt.Fprintln(output)
		fmt.Fprintln(output, "GitHub repository")
		if repository.WriteAccess {
			writeStateLine(output, toneSuccess, "  ✓ Write access to %s", repository.Repository)
		} else {
			writeStateLine(output, toneFailure, "  ✗ Write access to %s is required", repository.Repository)
		}
		if !repository.AutoMergeRequested {
			writeStateLine(output, toneMuted, "  ○ Automatic merge is disabled in Runner config")
		} else {
			if repository.AutoMergeAllowed {
				writeStateLine(output, toneSuccess, "  ✓ Repository auto-merge is enabled")
			} else {
				writeStateLine(output, toneFailure, "  ✗ Repository auto-merge is disabled")
			}
			if repository.MergeMethodAllowed && !(repository.RequiresLinearHistory && repository.MergeMethod == config.MergeMethodMerge) {
				writeStateLine(output, toneSuccess, "  ✓ Merge method %s is allowed", repository.MergeMethod)
			} else {
				writeStateLine(output, toneFailure, "  ✗ Merge method %s conflicts with repository or branch policy", repository.MergeMethod)
			}
			if repository.ActiveRulesInspected {
				writeStateLine(output, toneSuccess, "  ✓ Active base-branch rulesets were inspected")
			} else {
				writeStateLine(output, toneFailure, "  ✗ Active base-branch rulesets could not be inspected")
			}
		}
		if repository.BaseBranchProtected {
			if repository.ClassicProtection && repository.ProtectionDetailsKnown {
				writeStateLine(output, toneSuccess, "  ✓ Base branch %s protection was inspected", repository.BaseBranch)
			} else if repository.ClassicProtection {
				writeStateLine(output, toneWarning, "  ! Base branch %s is protected; classic protection details require repository administration read access", repository.BaseBranch)
			} else {
				writeStateLine(output, toneSuccess, "  ✓ Base branch %s protection is covered by the inspected rulesets", repository.BaseBranch)
			}
		} else {
			writeStateLine(output, toneMuted, "  ○ Base branch %s is not protected", repository.BaseBranch)
		}
	}
	if report.GitHubProject != nil {
		fmt.Fprintln(output)
		fmt.Fprintln(output, "GitHub Project")
		writeStateLine(output, toneSuccess, "  ✓ Project %s is readable through GitHub CLI", report.GitHubProject.ProjectID)
		if report.GitHubProject.BoardView {
			writeStateLine(output, toneSuccess, "  ✓ Kanban board view is ready")
		} else {
			writeStateLine(output, toneFailure, "  ✗ Kanban board view is missing")
		}
		if report.GitHubProject.BoardLifecycleFields {
			writeStateLine(output, toneSuccess, "  ✓ Runner Activity and QA Failures are visible on board cards")
		} else {
			writeStateLine(output, toneWarning, "  ! Runner Activity or QA Failures is hidden, or internal Runner Phase is visible; rerun init to restore the overview")
		}
		if report.GitHubProject.IntakeRepository && report.GitHubProject.IntakeLabel {
			writeStateLine(output, toneSuccess, "  ✓ Public issue intake repository and assessment label are ready")
		}
		if report.GitHubProject.ApprovalField {
			writeStateLine(output, toneSuccess, "  ✓ Runner Approval field is ready")
		} else {
			writeStateLine(output, toneFailure, "  ✗ Runner Approval field is missing")
		}
		if report.GitHubProject.PhaseField && report.GitHubProject.ActivityField && report.GitHubProject.QAFailuresField && report.GitHubProject.BranchField && report.GitHubProject.PullRequestField && report.GitHubProject.QACommitField {
			writeStateLine(output, toneSuccess, "  ✓ PR lifecycle, branch, and QA retry fields are ready")
		} else {
			writeStateLine(output, toneFailure, "  ✗ PR lifecycle, branch, or QA retry fields are missing")
		}
		writeStateLine(output, toneSuccess, "  ✓ Local process lock prevents a second Runner on this machine")
		writeStateLine(output, toneWarning, "  ! Cross-machine claiming remains unsupported")
	}
	for _, warning := range report.Warnings {
		writeStateLine(output, toneWarning, "  ! %s", warning)
	}
	if len(probes) > 0 {
		fmt.Fprintln(output)
		fmt.Fprintln(output, "Live harness probes")
		for _, probe := range probes {
			marker := "✗"
			tone := toneFailure
			if probe.Ready {
				marker = "✓"
				tone = toneSuccess
			}
			profile := probe.Harness
			if probe.Model != "" {
				profile += " / " + probe.Model
			}
			writeStateLine(output, tone, "  %s %s: %s", marker, profile, probe.Detail)
		}
	}
	if len(report.Recommendations) > 0 {
		fmt.Fprintln(output)
		fmt.Fprintln(output, "Next actions")
		for _, recommendation := range report.Recommendations {
			writeStateLine(output, toneWarning, "  - %s", recommendation)
		}
	}
	fmt.Fprintln(output)
	if report.Ready {
		writeStateLine(output, toneSuccess, "Ready to run: yes")
	} else {
		writeStateLine(output, toneFailure, "Ready to run: no")
	}
}

func writeDoctorCapability(output io.Writer, capabilities []setup.CapabilityState, capabilityType, id, label string) {
	for _, capability := range capabilities {
		if capability.Type != capabilityType || capability.ID != id {
			continue
		}
		marker := "✗"
		tone := toneFailure
		if capability.Status == setup.CapabilityAvailable {
			marker = "✓"
			tone = toneSuccess
		} else if capability.Status == setup.CapabilityBlocked {
			marker = "!"
			tone = toneWarning
		}
		version := ""
		if capability.Version != nil {
			version = " · " + *capability.Version
		}
		writeStateLine(output, tone, "  %s %s%s", marker, label, version)
		return
	}
	writeStateLine(output, toneFailure, "  ✗ %s", label)
}
