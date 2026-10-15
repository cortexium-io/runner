package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/cortexium-io/runner/internal/runner/cli"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := (cli.CLI{In: os.Stdin, Out: os.Stdout}).Run(ctx, os.Args[1:]); err != nil && !errors.Is(err, flag.ErrHelp) && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, "runner-next:", err)
		os.Exit(1)
	}
}
