package release

import (
	"context"
	_ "embed"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

//go:embed browser_probe.mjs
var browserProbeScript []byte

func (b *Live) deliveryReadiness(ctx context.Context, j *Job) error {
	c := b.Config
	engine, err := b.docker(ctx, "info", "--format", "{{.ID}}")
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(engine)) != c.Engine {
		return errors.New("delivery target engine identity differs")
	}
	if err = b.imageBuilderReadiness(ctx, j); err != nil {
		return err
	}
	if _, err = b.host(ctx, nil, filepath.Join(filepath.Dir(c.BackupHelper), "runner-snapshot"), "--self-test", c.BackupDir); err != nil {
		return err
	}
	if _, err = b.host(ctx, nil, c.Delivery.Probe, "--help"); err != nil {
		return err
	}
	for _, runtime := range []struct {
		args   []string
		prefix string
	}{
		{[]string{c.Delivery.Go, "version"}, "go version go1.26.7 "},
		{[]string{c.Delivery.Node, "--version"}, "v24.21.0"},
		{[]string{c.Delivery.Python, "--version"}, "Python 3.12."},
	} {
		version, err := b.command(ctx, nil, runtime.args[0], runtime.args[1:]...)
		if err != nil {
			return err
		}
		if !strings.HasPrefix(string(version), runtime.prefix) {
			return errors.New("delivery runtime version differs from the qualified build contract")
		}
	}
	if _, err = b.command(ctx, nil, c.Delivery.Ownership[0], append(c.Delivery.Ownership[1:], "--help")...); err != nil {
		return err
	}
	work := filepath.Join(filepath.Dir(j.Worker), "work")
	if err = os.MkdirAll(work, 0700); err != nil {
		return err
	}
	if c.Delivery.BrowserImage != "" {
		// The frontend builder still uses the host Node trust bundle; keep its
		// original registry TLS admission independent of browser placement.
		if _, err = b.sandbox(ctx, j, []string{work}, c.Delivery.Node, "--input-type=module", "--eval", `const r=await fetch('https://registry.npmjs.org/ws');if(!r.ok)throw new Error('npm TLS readiness failed');await r.arrayBuffer();`); err != nil {
			return err
		}
		if err = b.browserContainerReadiness(ctx, j); err != nil {
			return err
		}
		j.Receipt.Source, j.Receipt.Head = j.Request.Source, j.Request.Source
		return done(j, "Source/runtime admission, protected backup probe and isolated Chromium/WebKit container acceptance passed; no model or production mutation")
	}
	script := filepath.Join(work, "browser-probe.mjs")
	if err = writeExclusive(script, browserProbeScript); err != nil {
		return err
	}
	if _, err = b.sandbox(ctx, j, []string{work}, "/usr/bin/env", "RUNNER_PLAYWRIGHT_MODULE="+c.Delivery.BrowserModule, "RUNNER_BROWSER_EXECUTABLE="+c.Delivery.BrowserExecutable, "RUNNER_WEBKIT_EXECUTABLE="+c.Delivery.WebKitExecutable, c.Delivery.Node, script); err != nil {
		return err
	}
	j.Receipt.Source, j.Receipt.Head = j.Request.Source, j.Request.Source
	return done(j, "Source/runtime admission, actual protected-WAL backup probe and isolated headless browser readiness passed; no model or production mutation")
}

// Exercise the real image builder and its helper lookup before spending model
// turns. The probe has no product source, data, RUN command or base-image pull.
func (b *Live) imageBuilderReadiness(ctx context.Context, j *Job) error {
	work := filepath.Join(filepath.Dir(j.Worker), "work", "image-builder")
	if err := os.MkdirAll(work, 0700); err != nil {
		return err
	}
	if err := writeExclusive(filepath.Join(work, "Dockerfile"), []byte("FROM scratch\nCOPY readiness /readiness\n")); err != nil {
		return err
	}
	if err := writeExclusive(filepath.Join(work, "readiness"), []byte("Runner image-builder readiness\n")); err != nil {
		return err
	}
	tag := "runner-readiness:" + strings.ToLower(j.Request.Key)
	if _, err := b.docker(ctx, "build", "--network=none", "--platform", "linux/amd64", "--label", "cortexium.runner.run="+j.Request.RunID, "--label", "cortexium.runner.attempt="+j.Request.Key, "--tag", tag, work); err != nil {
		return err
	}
	owner, err := b.docker(ctx, "image", "inspect", tag, "--format", "{{index .Config.Labels \"cortexium.runner.attempt\"}}")
	if err != nil || strings.TrimSpace(string(owner)) != j.Request.Key {
		return errors.New("image-builder probe ownership is not established; retain its tag")
	}
	cleanup, cancel := context.WithTimeout(ctx, 10*time.Second)
	_, removeErr := b.docker(cleanup, "image", "rm", tag)
	cancel()
	// Some SSH/engine combinations lose the image-delete acknowledgement.
	// A successful listing proves absence; an exit code alone cannot do so.
	remaining, inspectErr := b.docker(ctx, "image", "ls", "--filter", "reference="+tag, "--format", "{{.ID}}")
	if inspectErr == nil && strings.TrimSpace(string(remaining)) == "" {
		return nil
	}
	return errors.Join(errors.New("owned image-builder probe cleanup is not established; retain its tag"), removeErr, inspectErr)
}
