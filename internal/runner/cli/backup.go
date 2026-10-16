package cli

import (
	"context"
	"errors"
	"fmt"

	"github.com/cortexium-io/runner/internal/runner/backup"
)

func (c CLI) maintenance(ctx context.Context, path string, args []string) error {
	command := args[0]
	f := c.flags(command)
	var destination *string
	var apply *bool
	if command == "backup" {
		destination = f.String("to", "", "new private backup directory")
	} else {
		destination = f.String("from", "", "complete private backup directory")
		apply = f.Bool("apply", false, "restore to absent original paths; remain drained")
	}
	if err := f.Parse(args[1:]); err != nil {
		return err
	}
	if *destination == "" || f.NArg() != 0 {
		return errors.New("backup requires --to DIRECTORY; restore requires --from DIRECTORY [--apply]")
	}
	var result backup.Report
	var err error
	if command == "backup" {
		result, err = backup.Create(ctx, path, *destination)
	} else {
		result, err = backup.Restore(ctx, *destination, path, *apply)
	}
	if err != nil {
		if command == "restore" && *apply {
			return fmt.Errorf("restore did not complete: %w; inspect status, destination paths and any .runner-restore-* staging directories before moving aside partial copies and retrying; no existing path is replaced", err)
		}
		return err
	}
	return c.json(result)
}
