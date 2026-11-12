// runner-saas-check owns fixed Go/Vue/Genny checks and the Linux build handoff. It is
// invoked only as an existing configured Runner command, never a model tool.
package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

type options struct {
	source, scratch, authority, base, patch string
	patchID                                 string
	goBinary, genny, node, modules, goCheck string
	runtimeFile                             string
	reference                               bool
}

func main() {
	var o options
	flag.StringVar(&o.source, "source", "", "read-only admitted candidate")
	flag.StringVar(&o.runtimeFile, "runtime", "", "operator-configured public runtime paths")
	flag.StringVar(&o.scratch, "scratch", "", "owned Runner command scratch")
	flag.StringVar(&o.authority, "authority", "", "declared frozen reference artifact")
	flag.StringVar(&o.base, "base", "", "Runner saved immutable base")
	flag.StringVar(&o.patch, "patch", "", "declared candidate patch file")
	flag.StringVar(&o.patchID, "patch-id", "", "literal Runner candidate artifact identity")
	flag.StringVar(&o.goBinary, "go", "", "pinned Go1.27.3 executable")
	flag.StringVar(&o.genny, "genny", "", "pinned published Genny v0.4.0 executable")
	flag.StringVar(&o.node, "node", "", "pinned Node executable")
	flag.StringVar(&o.modules, "modules", "", "locked Vue node_modules")
	flag.StringVar(&o.goCheck, "go-check", "", "installed fixed runner-go-check")
	flag.BoolVar(&o.reference, "reference", false, "validate and forward the frozen authority")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 18*time.Minute)
	defer cancel()
	if err := run(ctx, o); err != nil {
		fmt.Fprintln(os.Stderr, "local SaaS qualification:", err)
		os.Exit(2)
	}
}

func absolute(paths ...string) bool {
	for _, p := range paths {
		if !filepath.IsAbs(p) {
			return false
		}
	}
	return true
}

type boundedOutput struct {
	bytes.Buffer
	total int64
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	n := len(p)
	b.total += int64(n)
	if b.Len() < 64<<10 {
		_, _ = b.Buffer.Write(p[:min(len(p), 64<<10-b.Len())])
	}
	return n, nil
}

func (b *boundedOutput) ReadFrom(r io.Reader) (int64, error) {
	return io.Copy(struct{ io.Writer }{b}, r)
}
