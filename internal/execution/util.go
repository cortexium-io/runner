package execution

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/cortexium-io/runner/internal/metrics"
)

const (
	maxHarnessDiagnosticBytes = 1 << 20
	maxHarnessResultBytes     = 4 << 20
	harnessTruncationMarker   = "\n... [output truncated by Runner]"
)

func validateSemanticResultEvidence(outcome string, summary string, workDone []string, blocker *string) error {
	switch strings.TrimSpace(outcome) {
	case OutcomeSucceeded, OutcomeNeedsInput, OutcomeBlocked:
	default:
		return fmt.Errorf("unsupported outcome %q", strings.TrimSpace(outcome))
	}
	if strings.TrimSpace(summary) == "" {
		return errors.New("summary is required")
	}
	if workDone == nil {
		return errors.New("work_done is required")
	}
	for index, item := range workDone {
		if strings.TrimSpace(item) == "" {
			return fmt.Errorf("work_done[%d] cannot be empty", index)
		}
	}
	if (outcome == OutcomeNeedsInput || outcome == OutcomeBlocked) && (blocker == nil || strings.TrimSpace(*blocker) == "") {
		return errors.New("blocker is required for needs_input or blocked outcomes")
	}
	return nil
}

func pathInsideOrEqual(path string, parent string) bool {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	absParent, err := filepath.Abs(parent)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(absParent, absPath)
	if err != nil {
		return false
	}
	return rel == "." || (!strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel))
}

func stringPtr(value string) *string { return &value }

func finishStageFromOutput(finish metrics.FinishStage, output Output, err error, usage metrics.Usage) {
	outcome := metrics.StageOutcomeSucceeded
	class := string(output.FailureClass)
	retry := string(output.RetryDisposition)
	if err != nil {
		outcome = metrics.StageOutcomeFailed
		if class == "" {
			class = string(FailureUnknown)
		}
	} else if output.Outcome != "" && output.Outcome != OutcomeSucceeded {
		outcome = metrics.StageOutcomeBlocked
	}
	finish(outcome, class, retry, usage)
}
