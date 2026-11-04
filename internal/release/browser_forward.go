package release

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type forwardDiagnostic struct {
	output diagnosticOutput
	ready  chan struct{}
	marker []byte
	bound  bool
}

func (d *forwardDiagnostic) Write(p []byte) (int, error) {
	n, err := d.output.Write(p)
	if !d.bound && bytes.Contains(d.output.Bytes(), d.marker) {
		d.bound = true
		close(d.ready)
	}
	return n, err
}

// Only startup may reconnect, and only a confirmed pre-authentication timeout.
// Browser actions never run until this SSH process reports its own local bind.
func (b *Live) browserForward(ctx context.Context, directory string, local int, remote string) (func() error, error) {
	for attempt := 1; attempt <= 2; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		childCtx, cancel := context.WithCancel(ctx)
		port := strconv.Itoa(local)
		diagnostic := &forwardDiagnostic{
			output: diagnosticOutput{limit: 64 << 10},
			ready:  make(chan struct{}),
			marker: []byte("Local forwarding listening on 127.0.0.1 port " + port + "."),
		}
		cmd := exec.CommandContext(childCtx, b.Config.SSH, "-N", "-v", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes", "-o", "ConnectTimeout=10", "-o", "ConnectionAttempts=1", "-o", "ControlMaster=no", "-o", "ControlPath=none", "-o", "ExitOnForwardFailure=yes", "-L", "127.0.0.1:"+port+":127.0.0.1:"+remote, b.Config.Host)
		cmd.WaitDelay = 2 * time.Second
		cmd.Stdout, cmd.Stderr = io.Discard, diagnostic
		if err := cmd.Start(); err != nil {
			cancel()
			return nil, err
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		log := filepath.Join(directory, fmt.Sprintf("browser-forward-%d.log", attempt))
		timer := time.NewTimer(20 * time.Second)
		var exited error
		select {
		case <-diagnostic.ready:
			timer.Stop()
			return func() error {
				cancel()
				<-done
				return writeExclusive(log, diagnostic.output.Bytes())
			}, nil
		case exited = <-done:
		case <-ctx.Done():
			cancel()
			<-done
			exited = ctx.Err()
		case <-timer.C:
			cancel()
			<-done
			exited = errors.New("browser SSH forward did not bind within its startup limit")
		}
		timer.Stop()
		cancel()
		if err := writeExclusive(log, diagnostic.output.Bytes()); err != nil {
			return nil, err
		}
		var exit *exec.ExitError
		if attempt == 1 && ctx.Err() == nil && !diagnostic.bound && errors.As(exited, &exit) && exit.ExitCode() == 255 && strings.Contains(diagnostic.output.String(), "Connection timed out during banner exchange") {
			continue
		}
		return nil, fmt.Errorf("candidate browser forward failed (%v); retained %s", exited, filepath.Base(log))
	}
	return nil, errors.New("browser SSH startup allowance exhausted")
}
