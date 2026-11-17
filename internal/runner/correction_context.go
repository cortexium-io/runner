package runner

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
)

const MaxCorrectionContext Bytes = 16 << 10

// CorrectionContextInput is owner-supplied data, retained once as an artifact.
// Content is omitted from the durable idempotency arguments; its exact hash and
// size authenticate the bytes supplied on every call, including replay.
type CorrectionContextInput struct {
	Content string   `json:"content"`
	Bytes   int64    `json:"bytes"`
	SHA256  string   `json:"sha256"`
	Roles   []string `json:"roles"`
}

type CorrectionContext struct {
	ID     string   `json:"id"`
	Bytes  int64    `json:"bytes"`
	SHA256 string   `json:"sha256"`
	Roles  []string `json:"roles"`
}

func correctionDigest(data []byte) string {
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

func validCorrectionSHA(s string) bool {
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == sha256.Size && hex.EncodeToString(b) == s
}

func correctionRecoveryArgs(req RecoverCorrectionsRequest) ([]byte, error) {
	if input := req.Context; input != nil {
		if !json.Valid([]byte(input.Content)) || input.Bytes < 1 || input.Bytes > int64(MaxCorrectionContext) || input.Bytes != int64(len(input.Content)) || input.SHA256 != correctionDigest([]byte(input.Content)) {
			return nil, errors.New("supplemental correction context needs exact bounded JSON bytes and SHA256")
		}
		req = Clone(req)
		req.Context.Content = ""
	}
	return json.Marshal(req)
}

func validateCorrectionContext(r Run, input *CorrectionContextInput, limit Bytes) error {
	if input.Bytes > int64(limit) || len(input.Roles) == 0 {
		return errors.New("supplemental context needs an artifact within the configured bound and explicit existing role recipients")
	}
	seen := map[string]bool{}
	for _, id := range input.Roles {
		role, exists := r.Roles[id]
		profile, err := role.Profile(r.Profiles[id])
		if !exists || err != nil || seen[id] || (profile.Harness != "codex" && profile.Harness != "native") || (profile.Access != "workspace-read" && profile.Access != "workspace-write") {
			return errors.New("supplemental context recipients must be unique existing source or review roles")
		}
		seen[id] = true
	}
	return nil
}

func (c *Core) correctionArtifactDigest(r Run, id, expected string) error {
	if err := c.recoveryArtifact(r, id); err != nil {
		return err
	}
	data, err := c.disk.Read("artifacts/"+id, c.disk.Policy.MaxArtifact)
	if err != nil || correctionDigest(data) != expected {
		return errors.New("retained correction artifact SHA256 differs")
	}
	return nil
}

// A lost commit acknowledgement must not leave the in-memory failed snapshot
// authorising another operation over a durably committed recovery.
func (c *Core) reconcileCorrectionReplay(r Run, req RecoverCorrectionsRequest) (Run, error) {
	// Always consult durable state. An earlier receipt may be replayed after a
	// newer grant committed without acknowledgement, leaving memory behind.
	rows, err := c.store.Load(c.ctx)
	if err != nil {
		return Run{}, fmt.Errorf("committed correction recovery needs reconciliation: %w", err)
	}
	for _, saved := range rows {
		if saved.ID != req.RunID {
			continue
		}
		grant := correctionGrant(saved.CorrectionRecovery, req.RequestID)
		additional := 0
		if req.AdditionalUses != nil {
			additional = *req.AdditionalUses
		}
		if grant == nil || grant.AttemptID != req.AttemptID || grant.MaxUses != req.MaxUses || grant.AdditionalUses != additional || grant.CurrentRoute != req.CurrentRoute || saved.Revision < r.Revision {
			return Run{}, errors.New("committed correction recovery does not match its receipt")
		}
		if saved.Revision > r.Revision {
			c.runs[saved.ID] = saved
			c.maintenanceError = ""
			close(c.changed)
			c.changed = make(chan struct{})
			c.Wake()
		}
		if req.CurrentRoute {
			return c.outputRun(saved), nil
		}
		return Clone(saved), nil
	}
	return Run{}, errors.New("committed correction recovery is unavailable; reopen the same state")
}

func correctionGrant(grant *CorrectionRecovery, requestID string) *CorrectionRecovery {
	for ; grant != nil; grant = grant.Previous {
		if grant.RequestID == requestID {
			return grant
		}
	}
	return nil
}

func (c *Core) correctionAssignmentContext(r Run, a Attempt) (*CorrectionContext, error) {
	grant := r.CorrectionRecovery
	if grant == nil || grant.Context == nil || !grant.CurrentRoute || a.Visit < grant.FromVisit || !slices.Contains(grant.Context.Roles, r.Process.Steps[a.Step].Role) {
		return nil, nil
	}
	proof := grant.Context
	if proof.ID != r.ID+"/correction-context-"+proof.SHA256+".json" || proof.Bytes < 1 || proof.Bytes > int64(MaxCorrectionContext) {
		return nil, errors.New("supplemental correction context identity differs")
	}
	artifact, err := c.store.Artifact(c.ctx, proof.ID)
	if err != nil || artifact.RunID != r.ID || artifact.ProjectID != r.ProjectID || artifact.Bytes != proof.Bytes {
		return nil, errors.New("supplemental correction context ownership differs")
	}
	data, err := c.disk.Read("artifacts/"+proof.ID, MaxCorrectionContext)
	if err != nil || int64(len(data)) != proof.Bytes || correctionDigest(data) != proof.SHA256 {
		return nil, errors.New("supplemental correction context bytes differ")
	}
	return Clone(proof), nil
}
