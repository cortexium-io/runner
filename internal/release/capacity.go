package release

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func (b *Live) deliveryCapacity(ctx context.Context, admission bool) error {
	c := b.Config.Delivery
	var localReserve, remoteReserve int64
	if admission {
		localReserve, remoteReserve = 4<<30, 1<<30
	}
	var used int64
	if err := filepath.WalkDir(c.Root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, os.ErrNotExist) && path != c.Root {
				return nil
			}
			return err
		}
		info, err := entry.Info()
		if err == nil && info.Mode().IsRegular() {
			used += info.Size()
		}
		if errors.Is(err, os.ErrNotExist) {
			return nil // An owned build cache entry may have just been replaced.
		}
		return err
	}); err != nil {
		return err
	}
	free, err := deliveryFree(c.Root)
	if err != nil {
		return err
	}
	if used+localReserve >= c.StorageLimit || free < c.MinFree+localReserve {
		return errors.New("delivery storage admission/reserve exhausted; preserve retained evidence and inspect capacity before new work")
	}
	usage, err := b.host(ctx, nil, "du", "-sk", b.Config.BackupDir)
	if err != nil {
		return err
	}
	fields := strings.Fields(string(usage))
	if len(fields) < 2 {
		return errors.New("delivery target storage usage is not established")
	}
	kib, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil || kib < 0 || kib+remoteReserve/1024 >= c.StorageLimit/1024 {
		return errors.New("delivery target storage bound exhausted; retained backups are preserved")
	}
	space, err := b.host(ctx, nil, "df", "-Pk", b.Config.BackupDir)
	if err != nil {
		return err
	}
	lines := strings.Split(strings.TrimSpace(string(space)), "\n")
	fields = strings.Fields(lines[len(lines)-1])
	if len(fields) < 6 {
		return errors.New("delivery target free space is not established")
	}
	kib, err = strconv.ParseInt(fields[3], 10, 64)
	if err != nil || kib < (c.MinFree+remoteReserve)/1024 {
		return errors.New("delivery target free-space reserve exhausted; no allowances were increased")
	}
	return nil
}
