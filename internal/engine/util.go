package engine

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/cortexium-io/runner/internal/subprocess"
)

func stringPtr(value string) *string { return &value }

func safeRefComponent(value string) string {
	var result strings.Builder
	for _, char := range strings.ToLower(strings.TrimSpace(value)) {
		switch {
		case char >= 'a' && char <= 'z', char >= '0' && char <= '9', char == '-', char == '_':
			result.WriteRune(char)
		default:
			result.WriteByte('-')
		}
	}
	value = strings.Trim(result.String(), "-_.")
	if value == "" {
		return "assignment"
	}
	return value
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value)
}

func commandFailure(err error, result subprocess.Result) error {
	detail := strings.TrimSpace(result.Stderr)
	if detail == "" {
		detail = strings.TrimSpace(result.Stdout)
	}
	if detail == "" {
		return err
	}
	if len(detail) > 1000 {
		detail = detail[:1000]
	}
	return fmt.Errorf("%w: %s", err, detail)
}

func (s *Engine) git(ctx context.Context, args []string, dir string, timeout time.Duration) (subprocess.Result, error) {
	return subprocess.RunGit(ctx, s.run, args, dir, timeout)
}
