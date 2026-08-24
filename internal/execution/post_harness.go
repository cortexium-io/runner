package execution

import (
	"context"
	"time"
)

const maxPostHarnessVerificationTime = 2 * time.Minute

// postHarnessContext gives Runner a short cleanup and verification window after
// the user cancels a harness. It preserves context values used by metrics while
// deliberately dropping only the canceled signal.
func postHarnessContext(parent context.Context, harnessTimeout time.Duration) (context.Context, context.CancelFunc) {
	timeout := harnessTimeout
	if timeout <= 0 || timeout > maxPostHarnessVerificationTime {
		timeout = maxPostHarnessVerificationTime
	}
	return context.WithTimeout(context.WithoutCancel(parent), timeout)
}
