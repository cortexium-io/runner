package execution

import (
	"context"
	"errors"
	"fmt"
	"time"
)

const nativeOwnerVariable = "RUNNER_NATIVE_OWNER"

type nativeChild struct {
	PID      int
	Identity string // PID plus kernel birth identity, never command arguments.
}

// settleNativeChildren owns cooperating subprocesses through a unique inherited
// marker. Groups/parent PIDs alone lose detached servers and double forks. This
// is resource ownership, not hostile-process containment: tools must preserve
// their ownership marker rather than deliberately laundering their environment.
func settleNativeChildren(owner string, since time.Time, known map[string]bool, observed func(nativeChild)) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	seen := map[string]bool{}
	return settleNativeInventory(ctx, func() ([]nativeChild, error) {
		return ownedNativeProcesses(owner, since, known)
	}, func(child nativeChild) error {
		if err := killOwnedNativeProcess(child, owner, known); err != nil {
			return err
		}
		if !seen[child.Identity] {
			seen[child.Identity] = true
			observed(child)
		}
		return nil
	})
}

func settleNativeInventory(ctx context.Context, inventory func() ([]nativeChild, error), stop func(nativeChild) error) error {
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	empty := false
	for {
		children, inventoryErr := inventory()
		if inventoryErr != nil {
			return fmt.Errorf("native owned-process inventory unavailable: %w", inventoryErr)
		}
		if len(children) == 0 {
			if empty {
				return nil
			}
			empty = true
		} else {
			empty = false
			for _, child := range children {
				if err := stop(child); err != nil {
					return fmt.Errorf("native owned-process identity or termination unconfirmed: %w", err)
				}
			}
		}
		select {
		case <-ctx.Done():
			return errors.New("native owned processes did not settle within cleanup allowance")
		case <-ticker.C:
		}
	}
}
