package metrics

import (
	"testing"
	"time"
)

func TestUncoveredTimingUsesObservedUnionNotStageSum(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	a := Attempt{Event: Event{StartedAt: start, FinishedAt: start.Add(10 * time.Second), DurationMilliseconds: 10000}, Completed: true,
		Stages: []Stage{
			{Name: StagePlanIntegration, StartedAt: start.Add(time.Second), FinishedAt: start.Add(6 * time.Second), Completed: true},
			{Name: StageAuthorityValidation, StartedAt: start.Add(2 * time.Second), FinishedAt: start.Add(3 * time.Second), Completed: true},
			{Name: StageSnapshotValidation, StartedAt: start.Add(5 * time.Second), FinishedAt: start.Add(8 * time.Second), Completed: true},
			{Name: StageEvidenceCapture, StartedAt: start.Add(8 * time.Second)},
		}}
	if got := uncoveredDuration(a); got != 3000 {
		t.Fatalf("nested/overlapping/incomplete stages misattributed time: %d", got)
	}
	a.Stages = nil
	if got := uncoveredDuration(a); got != 10000 {
		t.Fatalf("unobserved time should remain uncovered: %d", got)
	}
}
