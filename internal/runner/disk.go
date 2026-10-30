package runner

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Disk accounts controlled files. SQLite's whole allowance is reserved separately.
// Native writers are rescanned separately; their stop threshold is not an OS quota.
type Disk struct {
	mu         sync.Mutex
	Root       string
	Policy     StoragePolicy
	used       int64
	peak       int64
	native     int64
	nativePeak int64
}

func OpenDisk(root string, p StoragePolicy) (*Disk, error) {
	d := &Disk{Root: root, Policy: p}
	for _, dir := range []string{"jobs", "work", "artifacts", "logs", "native"} {
		path := filepath.Join(root, dir)
		if e := os.MkdirAll(path, 0700); e != nil {
			return nil, e
		}
	}
	canonical, e := filepath.EvalSymlinks(root)
	if e != nil {
		return nil, e
	}
	d.Root = canonical
	if e := d.Refresh(); e != nil {
		return nil, e
	}
	return d, nil
}

// Refresh measures controlled/native content without following links. The
// native links supply account/runtime resources. They are counted as links, never
// followed or accepted as artifacts. Codex also exposes its private socket via an
// alias; the RPC handshake verifies its instance home before any task is sent.
func (d *Disk) Refresh() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	var total, native int64
	for _, dir := range []string{"jobs", "work", "artifacts", "logs", "native"} {
		e := filepath.WalkDir(filepath.Join(d.Root, dir), func(path string, ent fs.DirEntry, err error) error {
			if err != nil {
				if (dir == "native" || dir == "work") && errors.Is(err, os.ErrNotExist) {
					return nil
				}
				return err
			}
			if ent.Type()&os.ModeSymlink != 0 {
				nativeLink := dir == "native" && path != filepath.Join(d.Root, "native")
				if !nativeLink && dir != "work" && path != filepath.Join(d.Root, "jobs", "codex.sock") {
					return fmt.Errorf("managed path is a symlink: %s", path)
				}
			}
			// The instance lock is held before opening Disk. Incomplete atomic
			// replacements are private writer scratch, never retained outputs.
			if dir != "native" && dir != "work" && !ent.IsDir() && strings.HasPrefix(ent.Name(), ".write-") {
				return os.Remove(path)
			}
			if !ent.IsDir() {
				i, e := ent.Info()
				if e != nil {
					if (dir == "native" || dir == "work") && errors.Is(e, os.ErrNotExist) {
						return nil
					}
					return e
				}
				total += i.Size()
				if dir == "native" {
					native += i.Size()
				}
			}
			return nil
		})
		if e != nil {
			return e
		}
	}
	d.used, d.native = total, native
	d.peak = max(d.peak, total)
	d.nativePeak = max(d.nativePeak, native)
	return nil
}

func (d *Disk) NativeUsed() int64 { d.mu.Lock(); defer d.mu.Unlock(); return d.native }
func (d *Disk) NativePeak() int64 { d.mu.Lock(); defer d.mu.Unlock(); return d.nativePeak }
func (d *Disk) path(name string) (string, error) {
	if !fs.ValidPath(name) || !(strings.HasPrefix(name, "jobs/") || strings.HasPrefix(name, "work/") || strings.HasPrefix(name, "artifacts/") || strings.HasPrefix(name, "logs/") || strings.HasPrefix(name, "native/")) {
		return "", errors.New("invalid managed path")
	}
	path := filepath.Join(d.Root, filepath.FromSlash(name))
	for parent := filepath.Dir(path); parent != d.Root; parent = filepath.Dir(parent) {
		if parent == filepath.Dir(parent) {
			return "", errors.New("managed path is outside its root")
		}
		if info, err := os.Lstat(parent); err == nil && info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("managed paths must not traverse symlinks")
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
	}
	return path, nil
}
func (d *Disk) Used() int64 { d.mu.Lock(); defer d.mu.Unlock(); return d.used }
func (d *Disk) Write(name string, b []byte, limit Bytes) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if int64(len(b)) > int64(limit) {
		return fmt.Errorf("%w: file exceeds %d bytes", ErrCapacity, limit)
	}
	path, e := d.path(name)
	if e != nil {
		return e
	}
	free, e := FreeBytes(d.Root)
	if e != nil {
		return e
	}
	if free < int64(d.Policy.MinFree)+int64(len(b)) {
		return fmt.Errorf("%w: minimum free space", ErrCapacity)
	}
	if d.used+int64(len(b)) > int64(d.Policy.MaxManaged-d.Policy.Database.MaxSize)-4096 {
		return fmt.Errorf("%w: managed files", ErrCapacity)
	}
	if e = os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return e
	}
	var old int64
	if i, e := os.Lstat(path); e == nil {
		if !i.Mode().IsRegular() {
			return errors.New("managed target is not a regular file")
		}
		old = i.Size()
	} else if !errors.Is(e, os.ErrNotExist) {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(path), ".write-")
	if e != nil {
		return e
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if e = f.Chmod(0600); e == nil {
		_, e = f.Write(b)
	}
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e == nil {
		e = ce
	}
	if e != nil {
		return e
	}
	if e = os.Rename(tmp, path); e != nil {
		return e
	}
	d.used += int64(len(b)) - old
	d.peak = max(d.peak, d.used)
	return nil
}
func (d *Disk) Read(name string, limit Bytes) ([]byte, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	p, e := d.path(name)
	if e != nil {
		return nil, e
	}
	i, e := os.Lstat(p)
	if e != nil {
		return nil, e
	}
	if !i.Mode().IsRegular() || i.Size() > int64(limit) {
		return nil, errors.New("invalid or oversized managed file")
	}
	return os.ReadFile(p)
}
func (d *Disk) Remove(name string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	p, e := d.path(name)
	if e != nil {
		return e
	}
	var n int64
	e = filepath.WalkDir(p, func(path string, ent fs.DirEntry, err error) error {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if !ent.IsDir() {
			i, e := ent.Info()
			if e != nil {
				return e
			}
			n += i.Size()
		}
		return nil
	})
	if e != nil {
		return e
	}
	if e = os.RemoveAll(p); e != nil {
		return e
	}
	d.used -= n
	return nil
}
func (d *Disk) ArtifactSize(id string) (int64, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	p, e := d.path("artifacts/" + id)
	if e != nil {
		return 0, e
	}
	i, e := os.Lstat(p)
	if e != nil {
		return 0, e
	}
	if !i.Mode().IsRegular() || i.Size() > int64(d.Policy.MaxArtifact) {
		return 0, errors.New("invalid or oversized artifact")
	}
	return i.Size(), nil
}

// Peak reports observed managed content; SQLite capacity is reserved separately.
func (d *Disk) Peak() int64 { d.mu.Lock(); defer d.mu.Unlock(); return d.peak }
