// runner-release executes one model-free, journaled release operation. It is
// invoked over the existing operator SSH connection, not a scheduler/service.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/cortexium-io/runner/internal/release"
)

func main() {
	config := flag.String("config", "", "private immutable operator policy")
	worker := flag.String("delivery-worker", "", "internal private delivery work")
	flag.Parse()
	if *worker != "" && *config == "" && flag.NArg() == 0 {
		if err := release.RunDeliveryWorker(*worker); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if *config == "" || flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "--config is required")
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = os.Stdin.Close()
		case <-done:
		}
	}()
	request, body, err := release.ReadRequest(os.Stdin)
	if err != nil {
		fmt.Fprintln(os.Stderr, "invalid release request")
		os.Exit(2)
	}
	operator, err := release.Load(*config)
	if err != nil {
		fmt.Fprintln(os.Stderr, "release policy unavailable or invalid")
		os.Exit(1)
	}
	response, err := operator.CallInput(ctx, request, body)
	if err != nil {
		response = release.Response{
			Key:    request.Key,
			State:  "unknown",
			Reason: "operator cannot reconcile the saved release; inspect its private journal",
		}
	}
	if err = json.NewEncoder(os.Stdout).Encode(response); err != nil {
		os.Exit(1)
	}
}
