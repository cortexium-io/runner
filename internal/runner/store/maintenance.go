package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/cortexium-io/runner/internal/runner/config"
	"github.com/cortexium-io/runner/internal/runner/core"
	"github.com/cortexium-io/runner/internal/securefs"
	"golang.org/x/sys/unix"
)

// The guard is deliberately outside the backed-up state. Its inode and token
// establish continuity, not task authority. Never restore it from a snapshot.
type Guard struct {
	file      *os.File
	exclusive bool
}

func GuardPath(dir string) string { path, _ := securefs.AbsolutePath(dir + ".guard"); return path }

func LockGuard(dir string, exclusive, create bool) (*Guard, error) {
	path := GuardPath(dir)
	if err := config.NoSymlinks(path); err != nil {
		return nil, err
	}
	flags := os.O_RDWR | unix.O_NOFOLLOW
	if create {
		flags |= os.O_CREATE
	}
	f, err := os.OpenFile(path, flags, 0600)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*Guard, error) { f.Close(); return nil, err }
	i, err := f.Stat()
	if err != nil {
		return fail(err)
	}
	s := i.Sys().(*syscall.Stat_t)
	if !i.Mode().IsRegular() || i.Mode().Perm() != 0600 || s.Uid != uint32(os.Geteuid()) || s.Nlink != 1 {
		return fail(errors.New("maintenance guard must be private, owned and single-linked"))
	}
	mode := unix.LOCK_SH
	if exclusive {
		mode = unix.LOCK_EX
	}
	if err = unix.Flock(int(f.Fd()), mode|unix.LOCK_NB); err != nil {
		return fail(errors.New("installation is in use; finish commands and drain the coordinator before backup or restore"))
	}
	return &Guard{file: f, exclusive: exclusive}, nil
}

func (g *Guard) Close() error { return g.file.Close() }
func (g *Guard) Identity() (string, error) {
	i, err := g.file.Stat()
	if err != nil {
		return "", err
	}
	p, err := os.Lstat(g.file.Name())
	if err != nil || !os.SameFile(i, p) {
		return "", errors.New("maintenance guard was replaced")
	}
	s := i.Sys().(*syscall.Stat_t)
	return core.Digest([]uint64{uint64(s.Dev), s.Ino, uint64(s.Uid)}), nil
}
func (g *Guard) Token() (string, error) {
	b := make([]byte, 129)
	n, err := g.file.ReadAt(b, 0)
	if err != nil && err != io.EOF {
		return "", err
	}
	if n > 128 {
		return "", errors.New("invalid maintenance guard token")
	}
	return string(b[:n]), nil
}
func (g *Guard) Invalidate() error { return g.write("") }
func (g *Guard) Seal(token string) error {
	if !g.exclusive || len(token) != 64 {
		return errors.New("snapshot sealing requires exclusive ownership and a digest")
	}
	return g.write(token)
}
func (g *Guard) write(token string) error {
	if _, err := g.Identity(); err != nil {
		return err
	}
	if err := g.file.Truncate(0); err != nil {
		return err
	}
	if token != "" {
		if _, err := g.file.WriteAt([]byte(token), 0); err != nil {
			return err
		}
	}
	if err := g.file.Sync(); err != nil {
		return err
	}
	d, err := os.Open(filepath.Dir(g.file.Name()))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

type Image struct {
	Settings     config.Settings
	Drained      bool
	Tasks        []core.Task
	Attempts     []core.Attempt
	Publications []core.Publication
	Qualities    []config.Quality
	Reports      []config.QualityReport
}

// ReadImage inspects a detached backup database without creating installation
// ownership. It never exposes a writable Store to the caller.
func ReadImage(ctx context.Context, dir string) (Image, error) {
	s, err := open(ctx, dir, true)
	if err != nil {
		return Image{}, err
	}
	defer s.Close()
	return s.image(ctx)
}
func (s *Store) image(ctx context.Context) (v Image, err error) {
	var check string
	if err = s.db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&check); err != nil {
		return
	}
	if check != "ok" {
		return v, errors.New("backup database integrity check failed")
	}
	rows, err := s.db.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return v, err
	}
	bad := rows.Next()
	err = rows.Err()
	rows.Close()
	if err != nil {
		return v, err
	}
	if bad {
		return v, errors.New("backup database has broken references")
	}
	v.Settings, v.Drained, err = s.Settings(ctx)
	if err != nil {
		return
	}
	v.Tasks, err = s.Tasks(ctx)
	if err != nil {
		return
	}
	v.Attempts, err = s.Attempts(ctx, "")
	if err != nil {
		return
	}
	v.Publications, err = s.Publications(ctx, "")
	if err != nil {
		return
	}
	for _, table := range []string{"quality_history", "quality_reports"} {
		rows, err = s.db.QueryContext(ctx, "SELECT data FROM "+table)
		if err != nil {
			return v, err
		}
		for rows.Next() {
			var b []byte
			if err = rows.Scan(&b); err != nil {
				break
			}
			if table == "quality_history" {
				var q config.Quality
				err = json.Unmarshal(b, &q)
				v.Qualities = append(v.Qualities, q)
			} else {
				var q config.QualityReport
				err = json.Unmarshal(b, &q)
				v.Reports = append(v.Reports, q)
			}
			if err != nil {
				break
			}
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			return v, err
		}
	}
	return
}

// Snapshot is called only while the caller owns the exclusive guard. VACUUM
// INTO copies a consistent SQLite image, including committed journal contents.
func (g *Guard) Snapshot(ctx context.Context, dir, destination string) (Image, error) {
	if !g.exclusive || g.file.Name() != GuardPath(dir) {
		return Image{}, errors.New("snapshot requires exclusive installation ownership")
	}
	if _, err := g.Identity(); err != nil {
		return Image{}, err
	}
	s, err := open(ctx, dir, true)
	if err != nil {
		return Image{}, err
	}
	defer s.Close()
	v, err := s.image(ctx)
	if err != nil {
		return v, err
	}
	if !v.Drained {
		return v, errors.New("drain before backup")
	}
	for _, a := range v.Attempts {
		if a.Occupying {
			return v, errors.New("backup requires all attempt ownership to be resolved")
		}
	}
	for _, p := range v.Publications {
		if p.Occupying {
			return v, errors.New("backup requires all publication ownership to be resolved")
		}
	}
	_, err = s.db.ExecContext(ctx, "VACUUM INTO ?", destination)
	if err != nil {
		return v, err
	}
	if err = os.Chmod(destination, 0600); err != nil {
		return v, err
	}
	f, err := os.OpenFile(destination, os.O_RDWR|unix.O_NOFOLLOW, 0)
	if err != nil {
		return v, err
	}
	err = f.Sync()
	return v, errors.Join(err, f.Close())
}

// BindRestored changes only physical binding and recovery disposition. Logical
// task/quality identities, approval records, counters and receipts stay intact.
func (g *Guard) BindRestored(ctx context.Context, dir string, r config.Restoration) error {
	if !g.exclusive {
		return errors.New("restore requires exclusive installation ownership")
	}
	if _, err := g.Identity(); err != nil {
		return err
	}
	s, err := open(ctx, dir, false)
	if err != nil {
		return err
	}
	defer s.Close()
	v, _, err := s.Settings(ctx)
	if err != nil {
		return err
	}
	v.Restoration = &r
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, "UPDATE metadata SET settings=?,drained=1 WHERE id=1", b)
	return err
}

func (s *Store) AllowMutation(ctx context.Context) error {
	v, _, err := s.Settings(ctx)
	if err != nil {
		return err
	}
	if v.Restoration != nil && v.Restoration.InspectionOnly {
		return fmt.Errorf("restored snapshot %s is inspection-only: later history is unknown; reconcile evidence and initialize fresh authority at distinct paths", v.Restoration.Snapshot)
	}
	if s.guard == nil {
		return errors.New("mutation requires installation ownership")
	}
	return s.guard.Invalidate()
}
