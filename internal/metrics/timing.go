package metrics

import (
	"sort"
	"time"
)

// Stages may overlap or nest. Only their observed interval union covers an
// attempt; the remainder is deliberately unattributed, not assigned to Git,
// networking, model thinking, or any other guessed source.
func uncoveredDuration(attempt Attempt) int64 {
	if !attempt.Completed {
		return 0
	}
	total := attempt.DurationMilliseconds
	if attempt.StartedAt.IsZero() || attempt.FinishedAt.Before(attempt.StartedAt) {
		return total
	}
	type interval struct{ start, end time.Time }
	var spans []interval
	for _, stage := range attempt.Stages {
		if !stage.Completed || stage.StartedAt.IsZero() || stage.FinishedAt.Before(stage.StartedAt) {
			continue
		}
		start, end := stage.StartedAt, stage.FinishedAt
		if start.Before(attempt.StartedAt) {
			start = attempt.StartedAt
		}
		if end.After(attempt.FinishedAt) {
			end = attempt.FinishedAt
		}
		if end.After(start) {
			spans = append(spans, interval{start, end})
		}
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].start.Before(spans[j].start) })
	var covered time.Duration
	var end time.Time
	for _, span := range spans {
		start := span.start
		if start.Before(end) {
			start = end
		}
		if span.end.After(start) {
			covered += span.end.Sub(start)
			end = span.end
		}
	}
	return max(0, total-covered.Milliseconds())
}
