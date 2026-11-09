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
	project := flag.String("project", "games-site", "expected application Compose project")
	fingerprint := flag.Bool("fingerprint", false, "observe closed database bytes without creating a snapshot")
	selfTest := flag.String("self-test", "", "probe closed-WAL permissions in an owned temporary directory")
	restore := flag.String("restore-from", "", "internal approved recovery snapshot")
	targetSHA := flag.String("expected-source-sha", "", "closed data hash approved for replacement")
	backupSHA := flag.String("backup-sha", "", "approved recovery snapshot hash")
	flag.Parse()
	if *restore != "" && filepath.IsAbs(*restore) && filepath.IsAbs(*source) && *destination == "" && flag.NArg() == 0 {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		if err := release.RestoreSnapshot(ctx, *source, *restore, *targetSHA, *backupSHA); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Println("approved SQLite snapshot restored atomically")
		return
	}
	if *selfTest != "" && filepath.IsAbs(*selfTest) && flag.NArg() == 0 {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		if err := release.SnapshotSelfTest(ctx, *selfTest); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Println("protected closed-WAL snapshot permission probe passed")
		return
	}
	if !filepath.IsAbs(*source) || (!*fingerprint && !filepath.IsAbs(*destination)) || (*fingerprint && *destination != "") || filepath.Base(*source) != "games.db" || *engine == "" || *container == "" || *image == "" || flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "explicit source/destination/engine/container/image are required")
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	stopped := func() error {
		return release.CheckSnapshotWriterStoppedForProject(ctx, *engine, *container, *image, *source, *project)
	}
	if *fingerprint {
		hash, err := release.QuiescedFingerprint(*source, stopped)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Println(hash)
		return
	}
	err := release.QuiescedSnapshot(ctx, *source, *destination, stopped)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("quiesced SQLite API backup verified; source unchanged")
}
