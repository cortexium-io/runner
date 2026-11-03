//go:build darwin || linux

package release

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSSHReconnectsOnlyBeforeRemoteDispatch(t *testing.T) {
	for _, mode := range []string{"startup timeout", "lost acknowledgement"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			program := filepath.Join(root, "ssh")
			marker := filepath.Join(root, "calls")
			message := "Connection timed out during banner exchange"
			if mode == "lost acknowledgement" {
				message = "Connection lost after remote session started"
			}
			script := "#!/bin/sh\nif [ -e " + quote(marker) + " ]; then printf 'second\\n' >> " + quote(marker) + "; printf 'connected\\n'; exit 0; fi\nprintf 'first\\n' > " + quote(marker) + "\nprintf '%s\\n' " + quote(message) + " >&2\nexit 255\n"
			if err := os.WriteFile(program, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			b := Live{Config: Config{SSH: program, Host: "fixture"}}
			out, err := b.host(t.Context(), nil, "fixed-operation")
			calls, _ := os.ReadFile(marker)
			if mode == "startup timeout" {
				if err != nil || string(out) != "connected\n" || strings.Count(string(calls), "\n") != 2 {
					t.Fatal("startup did not reconnect once", err, string(calls))
				}
			} else if err == nil || strings.Count(string(calls), "\n") != 1 {
				t.Fatal("uncertain remote command was repeated", err, string(calls))
			}
		})
	}
}
