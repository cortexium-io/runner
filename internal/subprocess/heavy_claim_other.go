//go:build !darwin && !linux

package subprocess

import (
	"context"
	"errors"
)

type HeavyClaim struct{}

func AcquireHeavyVerification(ctx context.Context) (context.Context, *HeavyClaim, error) {
	return ctx, nil, errors.New("heavy verification ownership requires macOS or Linux")
}
func (*HeavyClaim) Finish(error) error {
	return errors.New("heavy verification ownership is unsupported")
}
func checkHeavyClaim(string, string) error { return nil }

type HeavyRecovery struct {
	Present     bool
	Recoverable bool
	Token       string
	Cleared     bool
	Reason      string
}

func RecoverHeavyVerification(context.Context, string, bool) (HeavyRecovery, error) {
	return HeavyRecovery{}, errors.New("heavy verification recovery requires macOS or Linux")
}
