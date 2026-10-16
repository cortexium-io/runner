package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/cortexium-io/runner/internal/runner/config"
	"golang.org/x/sys/unix"
)

type Entry struct {
	Root   int    `json:"root"`
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Mode   uint32 `json:"mode"`
	Size   int64  `json:"size,omitempty"`
	Digest string `json:"sha256,omitempty"`
	Link   string `json:"link,omitempty"`
}

func dataPath(bundle string, i int) string {
	return filepath.Join(bundle, "data", fmt.Sprintf("%04d", i))
}
func cleanAbsolute(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path && path != "/"
}
func excludedState(name string) bool {
	switch name {
	case "runner.db", "runner.db-wal", "runner.db-shm", "runner.db-journal", "coordinator.lock":
		return true
	}
	return false
}

// scan never follows links. copyTo, when supplied, is a new private tree.
func scan(ctx context.Context, root, copyTo string, index int, skipState bool) ([]Entry, error) {
	var entries []Entry
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if skipState && excludedState(rel) {
			if d.IsDir() {
				return errors.New("unexpected directory in database slot")
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		e := Entry{Root: index, Name: rel, Mode: uint32(info.Mode().Perm())}
		dest := ""
		if copyTo != "" {
			dest = filepath.Join(copyTo, rel)
		}
		switch {
		case info.IsDir():
			e.Kind = "directory"
			if dest != "" {
				if err = os.Mkdir(dest, 0700); err != nil {
					return err
				}
			}
		case info.Mode().IsRegular():
			e.Kind = "file"
			e.Size = info.Size()
			e.Digest, err = copyFile(ctx, path, dest, info.Mode().Perm())
		case info.Mode()&os.ModeSymlink != 0:
			e.Kind = "link"
			// Link permissions are platform-dependent; preserve the literal
			// target, never follow it or apply the referent's permissions.
			e.Mode = 0
			e.Link, err = os.Readlink(path)
			if err == nil && dest != "" {
				err = os.Symlink(e.Link, dest)
			}
		default:
			return fmt.Errorf("backup cannot copy special file %s", path)
		}
		if err != nil {
			return err
		}
		entries = append(entries, e)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if copyTo != "" {
		if err = finishDirectories(copyTo, entries); err != nil {
			return nil, err
		}
	}
	return entries, nil
}

func copyFile(ctx context.Context, source, dest string, mode os.FileMode) (string, error) {
	f, err := os.OpenFile(source, os.O_RDONLY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return "", err
	}
	defer f.Close()
	before, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !before.Mode().IsRegular() {
		return "", errors.New("backup source is not a regular file")
	}
	h := sha256.New()
	var out *os.File
	var w io.Writer = h
	if dest != "" {
		out, err = os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL|unix.O_NOFOLLOW, 0600)
		if err != nil {
			return "", err
		}
		defer out.Close()
		w = io.MultiWriter(h, out)
	}
	buf := make([]byte, 128*1024)
	for {
		if err = ctx.Err(); err != nil {
			return "", err
		}
		n, readErr := f.Read(buf)
		if n > 0 {
			if _, err = w.Write(buf[:n]); err != nil {
				return "", err
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return "", readErr
		}
	}
	after, err := f.Stat()
	if err != nil {
		return "", err
	}
	if before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return "", errors.New("file changed during backup")
	}
	if out != nil {
		if err = out.Chmod(mode); err != nil {
			return "", err
		}
		if err = out.Sync(); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func finishDirectories(root string, entries []Entry) error {
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		if e.Kind != "directory" {
			continue
		}
		p := filepath.Join(root, e.Name)
		if err := os.Chmod(p, os.FileMode(e.Mode)); err != nil {
			return err
		}
		if err := syncDir(p); err != nil {
			return err
		}
	}
	return nil
}
func syncDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

func validateEntries(m Manifest) error {
	if !cleanAbsolute(m.State) || !cleanAbsolute(m.Config) {
		return errors.New("invalid authority paths")
	}
	seen := map[string]string{}
	for _, e := range m.Entries {
		if e.Root < 0 || e.Root >= len(m.Roots) || e.Name == "" || filepath.Clean(e.Name) != e.Name || filepath.IsAbs(e.Name) || e.Name == ".." || strings.HasPrefix(e.Name, "../") || e.Mode&^0777 != 0 {
			return errors.New("invalid backup entry path or mode")
		}
		key := fmt.Sprintf("%d/%s", e.Root, e.Name)
		if seen[key] != "" {
			return errors.New("duplicate backup entry")
		}
		if e.Name != "." && seen[fmt.Sprintf("%d/%s", e.Root, filepath.Dir(e.Name))] != "directory" {
			return errors.New("backup entry parent is not a recorded directory")
		}
		switch e.Kind {
		case "file":
			if len(e.Digest) != 64 || e.Size < 0 {
				return errors.New("invalid backup file digest")
			}
		case "directory":
		case "link":
			if e.Name == "." || e.Link == "" {
				return errors.New("invalid backup link")
			}
		default:
			return errors.New("unsupported backup entry")
		}
		seen[key] = e.Kind
	}
	stateRoot := false
	for i, p := range m.Roots {
		if p == m.State {
			stateRoot = seen[fmt.Sprintf("%d/.", i)] == "directory"
		}
		if !cleanAbsolute(p) || seen[fmt.Sprintf("%d/.", i)] == "" {
			return errors.New("invalid or missing backup root")
		}
		for _, q := range m.Roots[:i] {
			if config.Within(p, q) || config.Within(q, p) {
				return errors.New("overlapping backup roots")
			}
		}
	}
	if !stateRoot {
		return errors.New("state must be a separate directory root")
	}
	return nil
}
