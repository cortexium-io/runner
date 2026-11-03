package release

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

func (b *Live) deliveryReadiness(ctx context.Context, j *Job) error {
	c := b.Config
	engine, err := b.docker(ctx, "info", "--format", "{{.ID}}")
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(engine)) != c.Engine {
		return errors.New("delivery target engine identity differs")
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
	script := filepath.Join(work, "browser-probe.mjs")
	probe := []byte("import {pathToFileURL} from 'node:url';const r=await fetch('https://registry.npmjs.org/ws');if(!r.ok)throw new Error('npm TLS readiness failed');await r.arrayBuffer();const {chromium}=await import(pathToFileURL(process.env.RUNNER_PLAYWRIGHT_MODULE).href);const b=await chromium.launch({executablePath:process.env.RUNNER_BROWSER_EXECUTABLE,headless:true,args:['--disable-gpu','--single-process','--no-zygote']});await b.close();console.log('npm TLS and headless browser runtime ready');\n")
	if err = writeExclusive(script, probe); err != nil {
		return err
	}
	if _, err = b.sandbox(ctx, j, []string{work}, "/usr/bin/env", "RUNNER_PLAYWRIGHT_MODULE="+c.Delivery.BrowserModule, "RUNNER_BROWSER_EXECUTABLE="+c.Delivery.BrowserExecutable, c.Delivery.Node, script); err != nil {
		return err
	}
	j.Receipt.Source, j.Receipt.Head = j.Request.Source, j.Request.Source
	return done(j, "Source/runtime admission, actual protected-WAL backup probe and isolated headless browser readiness passed; no model or production mutation")
}
