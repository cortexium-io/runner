package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/cortexium-io/runner/internal/release"
	"os"
	"path/filepath"
	"time"
)

func main() {
	source := flag.String("source", "", "closed application database")
	destination := flag.String("destination", "", "new exclusive backup")
	engine := flag.String("engine", "", "expected local Docker engine")
	container := flag.String("container", "", "stopped Games container identity")
	image := flag.String("image", "", "expected previous image")
	flag.Parse()
	if !filepath.IsAbs(*source) || !filepath.IsAbs(*destination) || filepath.Base(*source) != "games.db" || *engine == "" || *container == "" || *image == "" || flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "explicit source/destination/engine/container/image are required")
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	err := release.QuiescedSnapshot(ctx, *source, *destination, func() error { return release.CheckSnapshotWriterStopped(ctx, *engine, *container, *image, *source) })
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("quiesced SQLite API backup verified; source unchanged")
}
