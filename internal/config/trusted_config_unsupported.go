//go:build !darwin && !linux

package config

import "errors"

func readTrustedConfigFile(string, int64) ([]byte, error) {
	return nil, errors.New("operator config provenance enforcement is unsupported on this platform")
}
