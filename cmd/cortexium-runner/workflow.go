package main

import (
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/cortexium-io/runner/internal/config"
)

func runWorkflow(args []string, stdout io.Writer) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprintln(stdout, "Usage: cortexium-runner workflow validate|explain [--config PATH]")
		fmt.Fprintln(stdout, "Validate typed workflow rules or explain their effective event, action, role, and transition behavior.")
		return nil
	}
	switch args[0] {
	case "validate":
		return runWorkflowValidate(args[1:], stdout)
	case "explain":
		return runWorkflowExplain(args[1:], stdout)
	default:
		return fmt.Errorf("unknown workflow command %q; use workflow --help", args[0])
	}
}

func runWorkflowValidate(args []string, stdout io.Writer) error {
	cfg, path, proceed, err := loadWorkflowCommandConfig("workflow validate", args, stdout)
	if err != nil || !proceed {
		return err
	}
	fmt.Fprintf(stdout, "Workflow valid: %d lanes, %d typed rules, %d active role profiles\n", len(cfg.Workflow.Lanes), len(cfg.Workflow.Rules), len(cfg.ExecutionRoleIDs()))
	fmt.Fprintf(stdout, "Configuration: %s\n", path)
	return nil
}

func runWorkflowExplain(args []string, stdout io.Writer) error {
	cfg, path, proceed, err := loadWorkflowCommandConfig("workflow explain", args, stdout)
	if err != nil || !proceed {
		return err
	}
	workflow := cfg.EffectiveWorkflow()
	fmt.Fprintln(stdout, "Workflow")
	fmt.Fprintf(stdout, "  Configuration: %s\n", path)
	fmt.Fprintf(stdout, "  Human intake: %s · approval: %s\n", describeLane(workflow, workflow.IntakeLane), describeLane(workflow, workflow.ApprovalLane))
	fmt.Fprintf(stdout, "  Default Plan: %s · default Ready: %s\n", describeLane(workflow, workflow.PlanLane), describeLane(workflow, workflow.ReadyLane))
	fmt.Fprintf(stdout, "  Claim lane: %s\n", describeLane(workflow, workflow.ActiveLane))

	rules := append([]config.WorkflowRule(nil), workflow.Rules...)
	sort.Slice(rules, func(left, right int) bool { return rules[left].ID < rules[right].ID })
	fmt.Fprintln(stdout, "\nRules")
	for _, rule := range rules {
		fmt.Fprintf(stdout, "  %s: %s -> %s\n", rule.ID, describeTrigger(workflow, rule.Trigger), describeAction(cfg, workflow, rule.Action))
		writeWorkflowTransitions(stdout, workflow, rule.Action.Transitions)
	}

	fmt.Fprintln(stdout, "\nMandatory Runner safety")
	fmt.Fprintln(stdout, "  Authenticated card authority, dependency success, admission limits, and resource conflicts are checked before role work.")
	fmt.Fprintln(stdout, "  Candidate integrity and independent review remain required before pull-request publication; integration is serialized per repository and base branch.")
	return nil
}

func loadWorkflowCommandConfig(command string, args []string, stdout io.Writer) (config.Config, string, bool, error) {
	flags := newFlagSet(command, "cortexium-runner "+command+" [--config PATH]", stdout)
	configPath := flags.String("config", "", "runner config; defaults to .cortexium/runner.json")
	proceed, err := parseFlags(flags, args, command)
	if err != nil || !proceed {
		return config.Config{}, "", proceed, err
	}
	if flags.NArg() != 0 {
		return config.Config{}, "", false, errors.New(command + " does not accept positional arguments")
	}
	*configPath = resolveRunnerConfigPath(*configPath, "")
	cfg, err := config.LoadConfig(*configPath)
	if err != nil {
		return config.Config{}, "", false, fmt.Errorf("load workflow config: %w", err)
	}
	return cfg, *configPath, true, nil
}

func describeLane(workflow config.ResolvedWorkflow, laneID string) string {
	lane := workflow.Lanes[laneID]
	return fmt.Sprintf("%s (%s)", strings.TrimSpace(lane.Name), laneID)
}

func describeTrigger(workflow config.ResolvedWorkflow, trigger config.WorkflowTrigger) string {
	if trigger.Event == config.WorkflowEventLaneEntered {
		return "card enters " + describeLane(workflow, trigger.Lane)
	}
	return trigger.Event
}

func describeAction(cfg config.Config, workflow config.ResolvedWorkflow, action config.WorkflowAction) string {
	switch action.Type {
	case config.WorkflowActionRunRole:
		contract := cfg.RoleContract(action.Role)
		description := fmt.Sprintf("run role %s (%s contract)", action.Role, contract)
		if action.CreatesIn != "" {
			description += "; create approved children in " + describeLane(workflow, action.CreatesIn)
		}
		if action.MaxQARejections > 0 {
			description += fmt.Sprintf("; block on rejection %d", action.MaxQARejections)
		}
		return description
	case config.WorkflowActionTransition:
		return "move card to " + describeLane(workflow, action.To)
	case config.WorkflowActionPublishPR:
		return "publish or reuse the reviewed pull request"
	case config.WorkflowActionUpdateBranch:
		return "refresh the candidate from the latest base and require another review"
	default:
		return action.Type
	}
}

func writeWorkflowTransitions(output io.Writer, workflow config.ResolvedWorkflow, transitions map[string]string) {
	if len(transitions) == 0 {
		return
	}
	outcomes := make([]string, 0, len(transitions))
	for outcome := range transitions {
		outcomes = append(outcomes, outcome)
	}
	sort.Strings(outcomes)
	for _, outcome := range outcomes {
		fmt.Fprintf(output, "    %s -> %s\n", outcome, describeLane(workflow, transitions[outcome]))
	}
}
