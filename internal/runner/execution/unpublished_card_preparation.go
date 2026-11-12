package execution

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"

	"github.com/cortexium-io/runner/internal/runner"
)

const cardRecoveryValidationStage = "validating_card_recovery"

func recoveryPreparationIdentity(j nativeJob) bool {
	return j.Stage == cardRecoveryValidationStage && j.Assignment.UnpublishedCardRecovery != nil &&
		j.Assignment.Setup.Profile.Harness == "codex" && j.Assignment.Setup.Profile.Access == "workspace-write" &&
		j.Workspace == nil && j.ThreadID == "" && j.TurnID == "" && j.Generation == ""
}

func recoveryPreparationOwns(data []byte, a runner.Assignment) bool {
	var j nativeJob
	return json.Unmarshal(data, &j) == nil && recoveryPreparationIdentity(j) &&
		j.End == nil && !j.StopRequested && reflect.DeepEqual(j.Assignment, a)
}

// The ordinary native job owns this read-only preparation and its original
// deadline. Start/Observe only join it; no separate intent, scheduler, cached
// authorization or model visit is introduced. Restart repeats authentication
// only while this durable phase proves no thread/workspace/turn was dispatched.
func (c *Codex) prepareRecoveredCard(ctx context.Context, j nativeJob) error {
	if !recoveryPreparationIdentity(j) || c.validateCardRecovery == nil || c.workspaces == nil {
		return errors.New("recovered-card preparation lacks its exact pre-dispatch identity/validator")
	}
	_, lease, err := c.workspaces.taskLease(j.Assignment)
	if err != nil {
		return err
	}
	defer lease.Close()
	ctx = context.WithValue(ctx, captureLeaseKey{}, lease)
	if j.StopRequested || j.End != nil || ctx.Err() != nil {
		return errors.New("recovered-card preparation stopped before validation")
	}
	if err := c.validateCardRecovery(ctx, j.Assignment); err != nil {
		return err
	}
	return c.mutate(j.Assignment.AttemptID, func(current *nativeJob) {
		if current.End != nil {
			return
		}
		if current.StopRequested || c.closing || ctx.Err() != nil {
			current.End = &runner.ExecutionEnd{Kind: "stopped", Reason: "recovered-card validation settled before native dispatch"}
			return
		}
		current.Stage = "allocated"
		current.Reason = ""
	})
}

func (c *Codex) reconcileRecoveryStop(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.preparing[id] && !c.closing {
		c.preparing[id] = true
		c.wg.Add(1)
		go c.launch(id)
	}
}
