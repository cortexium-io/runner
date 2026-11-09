//go:build darwin || linux

package release

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestRuntimeProbeCommandPreservesKnownFailedStdoutOnly(t *testing.T) {
	for _, code := range []string{"1", "255"} {
		t.Run(code, func(t *testing.T) {
			root := t.TempDir()
			program := filepath.Join(root, "probe")
			if err := os.WriteFile(program, []byte("#!/bin/sh\nhead -c 49646 /dev/zero\nprintf 'bounded warning\\n' >&2\nexit "+code+"\n"), 0700); err != nil {
				t.Fatal(err)
			}
			b := Live{}
			ctx := context.WithValue(t.Context(), deliveryDiagnosticKey{}, root)
			raw, err := b.command(ctx, nil, program)
			var failure *commandFailure
			var uncertain *commandUncertain
			known := code == "1"
			if err == nil || errors.As(err, &failure) != known || errors.As(err, &uncertain) == known || (known && !bytes.Equal(raw, make([]byte, 49646))) || (!known && raw != nil) {
				t.Fatal("failed-command stdout or transport classification changed", code, len(raw), err)
			}
			files, globErr := filepath.Glob(filepath.Join(root, "diagnostic-*.stdout.json"))
			if globErr != nil || len(files) != 1 {
				t.Fatal("failed stdout metadata missing", files, globErr)
			}
			var metadata struct {
				Bytes, CapturedBytes       int
				SHA256, CapturedSHA256     string
				Complete, CapturedComplete bool
			}
			data, readErr := os.ReadFile(files[0])
			if readErr != nil || json.Unmarshal(data, &metadata) != nil || metadata.Bytes != 49646 || metadata.CapturedBytes != 49646 || metadata.SHA256 != Digest(make([]byte, 49646)) || metadata.CapturedSHA256 != metadata.SHA256 || metadata.Complete != known || metadata.CapturedComplete != known {
				t.Fatal("failed stdout identity/completeness lost", string(data), readErr)
			}
		})
	}
}
