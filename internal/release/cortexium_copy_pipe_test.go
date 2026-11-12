//go:build darwin || linux

package release

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This is an owned local pipe fixture, never a Docker/SSH/App/browser process.
func TestCortexiumCopyPipeWorker(t *testing.T) {
	mode := os.Getenv("RUNNER_CORTEXIUM_COPY_PIPE")
	if mode == "" {
		return
	}
	root := os.Getenv("RUNNER_CORTEXIUM_COPY_PIPE_ROOT")
	data := bytes.Repeat([]byte("x"), 4<<20)
	var archive bytes.Buffer
	w := tar.NewWriter(&archive)
	_ = w.WriteHeader(&tar.Header{Name: "server", Mode: 0666, Size: int64(len(data)), Typeflag: tar.TypeReg})
	_, _ = w.Write(data)
	_ = w.Close()
	if mode == "stall" || mode == "cancel" {
		if _, err := os.Stdout.Write(archive.Bytes()[:512]); err != nil {
			os.Exit(3)
		}
		_ = os.WriteFile(filepath.Join(root, "header-sent"), []byte("header\n"), 0600)
		// Deliberate stalled producer. The consumer's own refusal deadline
		// must terminate it; this is not a synchronization grace sleep.
		time.Sleep(time.Hour)
		os.Exit(4)
	}
	if _, err := os.Stdout.Write(archive.Bytes()); err != nil {
		os.Exit(3)
	}
	if mode == "overflow" {
		if _, err := io.CopyN(os.Stdout, bytes.NewReader(make([]byte, 33<<20)), 33<<20); err != nil {
			os.Exit(3)
		}
	}
	_ = os.WriteFile(filepath.Join(root, "stream-finished"), []byte("complete\n"), 0600)
	os.Exit(0)
}

func TestCortexiumReadbackRefusesMetadataWhileSettlingBoundedPipe(t *testing.T) {
	for _, mode := range []string{"complete", "overflow", "stall", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			binary, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			program := filepath.Join(root, "fixed-copy-fixture")
			body := "#!/bin/sh\nexec " + quote(binary) + " -test.run=^TestCortexiumCopyPipeWorker$ -test.timeout=20s -- \"$@\"\n"
			if err := os.WriteFile(program, []byte(body), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("RUNNER_CORTEXIUM_COPY_PIPE", mode)
			t.Setenv("RUNNER_CORTEXIUM_COPY_PIPE_ROOT", root)
			b := Live{Config: Config{Docker: program, DockerHost: "fixture-only"}}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			ready := make(chan error, 1)
			if mode == "cancel" {
				go func() {
					deadline := time.Now().Add(5 * time.Second)
					for {
						if _, err := os.Stat(filepath.Join(root, "header-sent")); err == nil {
							cancel()
							ready <- nil
							return
						}
						if time.Now().After(deadline) {
							ready <- errors.New("owned producer did not publish its header")
							cancel()
							return
						}
						time.Sleep(5 * time.Millisecond) // Observe actual header readiness.
					}
				}()
			}
			started := time.Now()
			err = b.verifyCortexiumFile(ctx, strings.Repeat("a", 64), "server", 4<<20, Digest(bytes.Repeat([]byte("x"), 4<<20)), 0555)
			if err == nil || !strings.Contains(err.Error(), "metadata differs") {
				t.Fatal("primary refusal lost", err)
			}
			var uncertain *commandUncertain
			if errors.As(err, &uncertain) {
				t.Fatal("owned local process group did not settle", err)
			}
			_, completed := os.Stat(filepath.Join(root, "stream-finished"))
			if mode == "complete" && completed != nil {
				t.Fatal("refusal canceled before bounded archive drained", err)
			}
			if mode != "complete" && !os.IsNotExist(completed) {
				t.Fatal("overflow/stalled producer was not stopped", completed)
			}
			if mode == "cancel" {
				if signal := <-ready; signal != nil || !errors.Is(err, context.Canceled) {
					t.Fatal("caller cancellation failed to settle original pipe", signal, err)
				}
			}
			if mode == "overflow" && !strings.Contains(err.Error(), "32MiB") {
				t.Fatal("overflow boundary was not retained", err)
			}
			if mode == "stall" && (!errors.Is(err, context.DeadlineExceeded) || time.Since(started) > 8*time.Second) {
				t.Fatal("refusal drain exceeded its own bounded deadline", time.Since(started), err)
			}
		})
	}
}
