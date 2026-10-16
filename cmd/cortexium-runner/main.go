package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"syscall"

	"github.com/cortexium-io/runner/internal/runner/cli"
)

var version = "dev"

func buildVersion() string {
	if value := strings.TrimSpace(version); value != "" && value != "dev" {
		return value
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "dev"
}

func run(ctx context.Context, args []string, in io.Reader, out io.Writer) error {
	if len(args) == 1 && (args[0] == "--version" || args[0] == "version") {
		_, err := fmt.Fprintln(out, "cortexium-runner", buildVersion())
		return err
	}
	return (cli.CLI{In: in, Out: out}).Run(ctx, args)
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:], os.Stdin, os.Stdout); err != nil && !errors.Is(err, flag.ErrHelp) && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, "cortexium-runner:", err)
		os.Exit(1)
	}
}
