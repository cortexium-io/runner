//go:build darwin || linux

package config

import (
	"fmt"
	"os"

	"github.com/cortexium-io/runner/internal/securefs"
)

func readTrustedConfigFile(path string, limit int64) ([]byte, error) {
	data, _, fileState, err := securefs.ReadFile(path, limit)
	if err != nil {
		return nil, fmt.Errorf("operator config %s must be a regular non-symlinked file readable without following links: %w", path, err)
	}
	if err := securefs.ValidateOwnedRegularFile(fileState, uint32(os.Geteuid())); err != nil {
		return nil, fmt.Errorf("operator config %s: %w", path, err)
	}
	return data, nil
}
