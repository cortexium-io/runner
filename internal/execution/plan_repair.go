package execution

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// PlanRepairTarget describes a defect owner, never a grant of new work.
// The coordinator must independently validate current authority, complete
// failure coverage, and the remaining rejection allowance before admission.
type PlanRepairTarget struct {
	CheckKey string `json:"check_key"`
	ItemID   string `json:"item_id"`
	Finding  string `json:"finding"`
	InScope  bool   `json:"in_scope"`
}

func (target *PlanRepairTarget) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	if string(fields["in_scope"]) != "true" && string(fields["in_scope"]) != "false" {
		return errors.New("repair target in_scope must be an explicit boolean")
	}
	type plain PlanRepairTarget
	return decodeRequiredJSONObject(data, (*plain)(target), "check_key", "item_id", "finding", "in_scope")
}

// Routing is optional content, not a representation-only concession. Keep
// canonicalization strict for every other field and preserve empty/missing
// routing for an assessment which cannot safely name an existing owner.
func canonicalizeReviewerResult(value string, fields ...string) (string, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal([]byte(NormalizeStructuredResult(value)), &object); err != nil {
		return "", err
	}
	if raw, present := object["repair_targets"]; present {
		if string(raw) == "null" {
			return "", errors.New("repair_targets must be an array")
		}
		fields = append(fields, "repair_targets")
	}
	return CanonicalizeStructuredResult(value, fields...)
}

func validatePlanRepairTargets(spec Spec, targets []PlanRepairTarget, statuses map[string]string) error {
	if len(targets) == 0 {
		return nil
	}
	if spec.PlanContext == nil || spec.ReviewScope != ReviewScopePlan || !spec.ReviewRequired || spec.ItemID != spec.PlanContext.ID {
		return errors.New("repair_targets are only valid for an authenticated whole-plan review")
	}
	if err := ValidateAssignmentContext(spec); err != nil {
		return err
	}
	if len(targets) > maxReviewerEntries {
		return errors.New("too many plan repair targets")
	}
	members := map[string]bool{}
	for _, id := range spec.PlanContext.MemberIDs {
		members[id] = true
	}
	seen := map[string]bool{}
	for _, target := range targets {
		if statuses[target.CheckKey] != "failed" {
			return fmt.Errorf("repair target %q must identify a failed check in this review stage", target.CheckKey)
		}
		if target.ItemID == spec.PlanContext.ID || !members[target.ItemID] {
			return errors.New("repair target is not an approved plan member")
		}
		if strings.TrimSpace(target.Finding) == "" || len(target.Finding) > 8192 || strings.ContainsRune(target.Finding, 0) {
			return errors.New("repair target requires a bounded nonempty finding")
		}
		key := target.CheckKey + "\x00" + target.ItemID
		if seen[key] {
			return errors.New("duplicate plan repair target")
		}
		seen[key] = true
	}
	return nil
}

func addPlanRepairSchema(schema map[string]any, keys []string, specs []Spec) {
	if len(specs) == 0 || specs[0].ReviewScope != ReviewScopePlan || specs[0].PlanContext == nil {
		return
	}
	schema["properties"].(map[string]any)["repair_targets"] = map[string]any{
		"type": "array", "maxItems": maxReviewerEntries, "items": map[string]any{
			"type": "object", "required": []string{"check_key", "item_id", "finding", "in_scope"}, "additionalProperties": false,
			"properties": map[string]any{
				"check_key": map[string]any{"type": "string", "enum": keys},
				"item_id":   map[string]any{"type": "string", "enum": specs[0].PlanContext.MemberIDs},
				"finding":   map[string]any{"type": "string", "minLength": 1, "maxLength": 8192},
				"in_scope":  map[string]any{"type": "boolean"},
			},
		},
	}
	// Strict structured-output schemas require every declared property. An
	// empty list is a legitimate assessment, but cannot authorize a repair.
	schema["required"] = append(schema["required"].([]string), "repair_targets")
}

func planRepairPrompt(spec Spec) string {
	if spec.ReviewScope != ReviewScopePlan {
		return ""
	}
	briefs, _ := json.Marshal(spec.PlanMemberBriefs)
	return "\nFor failed checks only, return repair_targets with this stage's check_key, an exact approved member item_id, the concrete finding and explicit in_scope. Use authenticated member ownership context to identify existing owning cards. Do not invent cards, requirements, or ownership. If ownership is unclear, leave the finding unrouted. If a consequential new decision or expanded scope is needed, in_scope must be false. Empty or out-of-scope routing preserves the defect verdict but requires operator input before repair. Never route a passed, blocked, or check_required check.\nFrozen approved member ownership context (not permission for extra work):\n" + string(briefs) + "\n"
}
