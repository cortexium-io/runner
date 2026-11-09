package runner

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
)

// Release reserve applies only to the already validated closed release suffix.
// It neither budgets model work nor traverses arbitrary workflow graphs.
func releaseApprovalReserve(r Run, outcome string, route Route, usedCount ...int) error {
	if r.Step < 0 || r.Step >= len(r.Process.Steps) {
		return unsupportedReleaseBudget()
	}
	step := r.Process.Steps[r.Step]
	profile, err := r.Roles[step.Role].Profile(r.Profiles[step.Role])
	if err != nil {
		return err
	}
	if profile.Harness != "release" {
		return nil
	}
	if a := r.Current(); a != nil {
		actual, err := r.Roles[step.Role].Profile(a.Profile)
		if err != nil || actual.Harness != profile.Harness || actual.Mode != profile.Mode {
			return unsupportedReleaseBudget()
		}
	}
	if _, err := releaseBudgetProfile(r, r.Step); err != nil {
		return err
	}
	targetMode := map[string]string{"prepare": "merge", "backup": "stop", "inspect": "deploy", "inspect-migration": "restore"}[profile.Mode]
	if targetMode == "" || !route.Approval || outcome != "ready" {
		return unsupportedReleaseBudget()
	}
	target := -1
	for i, step := range r.Process.Steps {
		if step.ID == route.Next {
			target = i
			break
		}
	}
	profile, err = releaseBudgetProfile(r, target)
	if err != nil || profile.Harness != "release" || profile.Mode != targetMode {
		return unsupportedReleaseBudget()
	}
	uses := maps.Clone(r.RouteUses)
	if uses == nil {
		uses = map[string]int{}
	}
	key := r.Process.Steps[r.Step].ID + "/" + outcome
	if uses[key] < 0 || route.MaxTimes > 0 && uses[key] >= route.MaxTimes {
		return unsupportedReleaseBudget()
	}
	uses[key]++ // Approval will consume this edge before its target executes.
	return releaseReserve(r, target, uses, 0, usedCount...)
}

// An old owner may already have presented an approval before the reserve guard
// was installed. Retire only that unsafe unapproved prompt, atomically retaining
// its identity/reason in the existing receipt store. The saved result is intact
// and ordinary owner-approved Retry can revisit its read-only prepare step.
func (c *Core) retireUnsafeReleaseDecision(r Run) bool {
	if r.Decision == nil || r.Status != "Waiting" || r.Active() {
		return false
	}
	a := r.Current()
	d := r.Decision
	if a == nil || a.ID != d.AttemptID || a.End == nil || a.End.Kind != "returned" || a.End.Result == nil || a.End.Result.Outcome != d.Outcome {
		return false
	}
	profile, err := releaseBudgetProfile(r, r.Step)
	if err != nil || profile.Harness != "release" {
		return false
	}
	err = releaseApprovalReserve(r, d.Outcome, r.Process.Steps[r.Step].Routes[d.Outcome], c.familyAttempts(r))
	if err == nil {
		return false
	}
	reason := Clip(err.Error())
	args, _ := json.Marshal(struct {
		Decision *Decision `json:"decision"`
		Reason   string    `json:"reason"`
	}{d, reason})
	r.Decision = nil
	r.Reason = reason
	receipt := Receipt{Key: "release-budget-" + d.ID, Kind: "release_budget_wait", Args: args, RunID: r.ID}
	if c.persist(r, nil, &receipt) == nil {
		c.Wake()
	}
	return true
}

func releaseStopReserve(r Run, usedCount ...int) error {
	return releaseStopReserveWithCredit(r, 0, usedCount...)
}

func releaseNotStartedStopReserve(r Run, usedCount ...int) error {
	a := r.Current()
	if a == nil || a.State != NotStarted {
		return nil
	}
	r.NextProfile = a.Profile
	return releaseStopReserveWithCredit(r, 1, usedCount...)
}

func releaseStopReserveWithCredit(r Run, allocated int, usedCount ...int) error {
	profileName := r.NextProfile
	if profileName == "" {
		profileName = r.Profiles[r.Process.Steps[r.Step].Role]
	}
	profile, err := r.Roles[r.Process.Steps[r.Step].Role].Profile(profileName)
	if err != nil {
		return nil // Ordinary assignment validation handles other setup errors.
	}
	frozen, err := r.Roles[r.Process.Steps[r.Step].Role].Profile(r.Profiles[r.Process.Steps[r.Step].Role])
	if err != nil || (profile.Harness == "release" || frozen.Harness == "release") && (profile.Harness != frozen.Harness || profile.Mode != frozen.Mode) {
		return unsupportedReleaseBudget()
	}
	if profile.Harness != "release" || profile.Mode != "stop" {
		return nil
	}
	return releaseReserve(r, r.Step, r.RouteUses, allocated, usedCount...)
}

func releaseReserve(r Run, target int, uses map[string]int, allocated int, usedCount ...int) error {
	need, err := releaseCompletionAttempts(r, target, uses)
	if err != nil {
		return err
	}
	used := len(r.Attempts)
	if len(usedCount) > 1 || len(usedCount) == 1 && usedCount[0] < used {
		return unsupportedReleaseBudget()
	}
	if len(usedCount) == 1 {
		used = usedCount[0]
	}
	available := r.Process.MaxAttempts - used + allocated
	if available < need {
		return fmt.Errorf("release attempt reserve: need %d remaining attempts for configured completion/recovery, have %d; preserve the qualified candidate", need, available)
	}
	return nil
}

func unsupportedReleaseBudget() error {
	return errors.New("release attempt reserve: closed release suffix or remaining route allowance is unsupported")
}

func releaseBudgetProfile(r Run, index int) (Profile, error) {
	if index < 0 || index >= len(r.Process.Steps) {
		return Profile{}, unsupportedReleaseBudget()
	}
	step := r.Process.Steps[index]
	if step.ForEach != nil || step.Reuse || step.Escalation != nil {
		return Profile{}, unsupportedReleaseBudget()
	}
	profile, err := r.Roles[step.Role].Profile(r.Profiles[step.Role])
	if err != nil {
		return Profile{}, unsupportedReleaseBudget()
	}
	return profile, nil
}

func releaseCompletionAttempts(r Run, target int, uses map[string]int) (int, error) {
	stop := -1
	for i := range r.Process.Steps {
		profile, err := releaseBudgetProfile(r, i)
		if err == nil && profile.Harness == "release" && profile.Mode == "stop" {
			if stop != -1 {
				return 0, unsupportedReleaseBudget()
			}
			stop = i
		}
	}
	if stop < 0 {
		return 0, unsupportedReleaseBudget()
	}
	var modes []string
	positions := map[string]int{}
	for i := stop; i < len(r.Process.Steps); i++ {
		profile, err := releaseBudgetProfile(r, i)
		if err != nil || profile.Harness != "release" {
			return 0, unsupportedReleaseBudget()
		}
		modes = append(modes, profile.Mode)
		positions[profile.Mode] = i
	}
	standalone := []string{"stop", "migrate", "pin", "deploy", "verify"}
	connected := []string{"stop", "migrate", "inspect-migration", "restore", "pin", "deploy", "inspect", "verify"}
	recovery := reflect.DeepEqual(modes, connected)
	if !recovery && !reflect.DeepEqual(modes, standalone) {
		return 0, unsupportedReleaseBudget()
	}
	next := func(mode string) string { return r.Process.Steps[positions[mode]].ID }
	routes := map[string]map[string]Route{
		"stop":    {"ready": {Next: next("migrate")}, "blocked": {Next: "$fail"}},
		"migrate": {"ready": {Next: next("pin")}, "blocked": {Next: "$fail"}},
		"pin":     {"ready": {Next: next("deploy")}, "blocked": {Next: "$fail"}},
		"deploy":  {"ready": {Next: next("verify")}, "blocked": {Next: "$fail"}},
		"verify":  {"ready": {Next: "$complete"}, "blocked": {Next: "$fail"}},
	}
	if recovery {
		routes["migrate"]["recovery_required"] = Route{Next: next("inspect-migration")}
		routes["inspect-migration"] = map[string]Route{"ready": {Next: next("restore"), Approval: true}, "blocked": {Next: "$fail"}}
		routes["restore"] = map[string]Route{"restored": {Next: "$fail"}, "blocked": {Next: "$fail"}}
		routes["deploy"]["retry_safe"] = Route{Next: next("deploy"), MaxTimes: 1}
		routes["deploy"]["recovery_required"] = Route{Next: next("inspect")}
		routes["inspect"] = map[string]Route{"ready": {Next: next("deploy"), Approval: true, MaxTimes: 1}, "blocked": {Next: "$fail"}}
	}
	for mode, expected := range routes {
		if !reflect.DeepEqual(r.Process.Steps[positions[mode]].Routes, expected) {
			return 0, unsupportedReleaseBudget()
		}
	}
	deploy := 2 // One deployment and live verification, including the current one.
	if recovery {
		for key, cost := range map[string]int{next("deploy") + "/retry_safe": 1, next("inspect") + "/ready": 2} {
			if uses[key] < 0 || uses[key] > 1 {
				return 0, unsupportedReleaseBudget()
			}
			deploy += (1 - uses[key]) * cost
		}
	}
	profile, err := releaseBudgetProfile(r, target)
	if err != nil || profile.Harness != "release" {
		return 0, unsupportedReleaseBudget()
	}
	switch profile.Mode {
	case "stop":
		if target == stop {
			return 3 + deploy, nil // Stop, migration and pin precede deployment.
		}
	case "deploy":
		if target == positions["deploy"] {
			return deploy, nil
		}
	case "restore":
		if recovery && target == positions["restore"] {
			return 1, nil
		}
	case "merge":
		if target == stop-2 {
			backup, err := releaseBudgetProfile(r, stop-1)
			mergeRoute := map[string]Route{"ready": {Next: r.Process.Steps[stop-1].ID}, "blocked": {Next: "$fail"}}
			backupRoute := map[string]Route{"ready": {Next: next("stop"), Approval: true}, "blocked": {Next: "$fail"}}
			if err == nil && backup.Harness == "release" && backup.Mode == "backup" && reflect.DeepEqual(r.Process.Steps[target].Routes, mergeRoute) && reflect.DeepEqual(r.Process.Steps[stop-1].Routes, backupRoute) {
				return 5 + deploy, nil
			}
		}
	}
	return 0, unsupportedReleaseBudget()
}
