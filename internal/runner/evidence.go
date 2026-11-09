package runner

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// Reuse is opt-in for model-free read-only checks with text results. Candidate
// source identity replaces a regenerated patch reference; other inputs and the
// selected check/runtime remain part of the key. Reuse never applies to review,
// measurement artifacts or delivery actions.
func (c *Core) evidenceKey(r Run, a Assignment) string {
	p := a.Setup.Profile
	if !r.Process.Steps[r.Step].Reuse || p.Access != "workspace-read" || (p.Harness != "go-check" && p.Harness != "command") || p.Command != nil && p.Command.Network {
		return ""
	}
	for _, kind := range a.OutputTypes {
		if kind != "text" {
			return ""
		}
	}
	inputs := Clone(a.Inputs)
	for name, value := range inputs {
		if value.ArtifactID != "" {
			for _, attempt := range r.Attempts {
				if source := attempt.Source; source != nil && source.PatchID == value.ArtifactID && source.Tree != "" {
					inputs[name] = Text(source.Repository + "/" + source.Base + "/" + source.Tree)
					break
				}
			}
		}
	}
	data, _ := json.Marshal(struct {
		Profile Profile
		Inputs  map[string]Value
		Runtime *CodexSettings
	}{p, inputs, c.snapshot.Instance.Codex})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
func priorEvidence(r Run, key string) *Attempt {
	if key == "" {
		return nil
	}
	for i := len(r.Attempts) - 1; i >= 0; i-- {
		a := r.Attempts[i]
		if a.Step == r.Step && a.EvidenceKey == key && a.End != nil && a.End.Kind == "returned" && a.End.Result != nil && a.End.Result.Outcome == "ready" {
			return &a
		}
	}
	return nil
}
