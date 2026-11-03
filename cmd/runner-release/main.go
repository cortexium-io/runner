// runner-release executes one model-free, journaled release operation. It is
// invoked over the existing operator SSH connection, not a scheduler/service.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/cortexium-io/runner/internal/release"
)

func main() {
	config := flag.String("config", "", "private immutable operator policy")
	flag.Parse()
	if *config == "" || flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "--config is required")
		os.Exit(2)
	}
	operator, err := release.Load(*config)
	if err != nil {
		fmt.Fprintln(os.Stderr, "release policy unavailable or invalid")
		os.Exit(1)
	}
	var request release.Request
	d := json.NewDecoder(io.LimitReader(os.Stdin, 32<<10))
	d.DisallowUnknownFields()
	if err = d.Decode(&request); err != nil {
		fmt.Fprintln(os.Stderr, "invalid release request")
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	response, err := operator.Call(ctx, request)
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
