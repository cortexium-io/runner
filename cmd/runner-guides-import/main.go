// runner-guides-import is a closed, hash-bound raw media importer. It has no
// caller-selected source root, URL, command or arbitrary upload interface.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/cortexium-io/runner/internal/guidesmedia"
)

func main() {
	output := flag.String("output", "", "owned empty Runner command scratch directory")
	run := flag.String("run", "", "owning Runner import run")
	attempt := flag.String("attempt", "", "owning Runner import attempt")
	flag.Parse()
	if flag.NArg() != 0 || *output == "" || *run == "" || *attempt == "" {
		fmt.Fprintln(os.Stderr, "exact Runner-owned output/run/attempt required")
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	executable, err := os.Executable()
	if err == nil {
		var f *os.File
		f, err = os.Open(executable)
		if err == nil {
			var data []byte
			data, err = io.ReadAll(io.LimitReader(f, (16<<20)+1))
			_ = f.Close()
			if err == nil && len(data) <= 16<<20 {
				_, err = guidesmedia.Import(ctx, *output, *run, *attempt, guidesmedia.Digest(data))
			} else if err == nil {
				err = fmt.Errorf("import helper exceeds its immutable executable bound")
			}
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "approved guide import failed:", err)
		os.Exit(1)
	}
	fmt.Println("Authenticated the complete immutable36-file guide asset set; no product source modified.")
}
