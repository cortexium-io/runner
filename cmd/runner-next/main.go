package main

import (
	"context"
	"fmt"
	"github.com/cortexium-io/runner/internal/runner/cli"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := (cli.CLI{Out: os.Stdout}).Run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "runner-next:", err)
		os.Exit(1)
	}
}
