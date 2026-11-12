package guidesmedia

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// ReadApproved reads only one of the frozen manifest entries. Each directory is
// held open and authenticated; no symlink or changed file becomes an input.
func ReadApproved(ctx context.Context, f File) ([]byte, error) {
	m, err := Approved()
	if err != nil {
		return nil, err
	}
	for _, g := range m.Guides {
		for _, expected := range g.Files {
			if f == expected {
				return readFile(ctx, SourceRoot, f)
			}
		}
	}
	return nil, errors.New("file is not a declared approved guide asset")
}

func readFile(ctx context.Context, root string, entry File) ([]byte, error) {
	if !fs.ValidPath(entry.RelativePath) || strings.ContainsAny(entry.RelativePath, "\\:\x00") || entry.Bytes < 0 || entry.Bytes > 16<<20 || !digestPattern.MatchString(entry.SHA256) {
		return nil, errors.New("invalid raw media file contract")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	initial, err := os.Lstat(root)
	if err != nil || !initial.IsDir() || initial.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("guide root is not an owned regular directory")
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	type directory struct {
		parent *os.Root
		name   string
		info   os.FileInfo
	}
	directories := []directory{}
	current := r
	parts := strings.Split(entry.RelativePath, "/")
	for _, part := range parts[:len(parts)-1] {
		info, err := current.Lstat(part)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("guide asset parent is not a regular directory")
		}
		next, err := current.OpenRoot(part)
		if err != nil {
			return nil, err
		}
		defer next.Close()
		opened, err := next.Stat(".")
		if err != nil || !os.SameFile(info, opened) {
			return nil, errors.New("guide asset parent changed while opening")
		}
		directories = append(directories, directory{parent: current, name: part, info: info})
		current = next
	}
	name := parts[len(parts)-1]
	before, err := current.Lstat(name)
	if err != nil || !before.Mode().IsRegular() || before.Size() != entry.Bytes {
		return nil, errors.New("guide asset type or length differs")
	}
	f, err := current.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(before, opened) || !opened.Mode().IsRegular() {
		return nil, errors.New("guide asset changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(f, entry.Bytes+1))
	if err != nil {
		return nil, err
	}
	after, err := f.Stat()
	currentInfo, currentErr := current.Lstat(name)
	rootInfo, rootErr := os.Lstat(root)
	if err != nil || currentErr != nil || rootErr != nil || !os.SameFile(opened, after) || !os.SameFile(opened, currentInfo) || !os.SameFile(initial, rootInfo) || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) || !currentInfo.Mode().IsRegular() || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 || int64(len(data)) != entry.Bytes || Digest(data) != entry.SHA256 {
		return nil, errors.New("guide asset bytes or ownership changed")
	}
	for _, d := range directories {
		info, err := d.parent.Lstat(d.name)
		if err != nil || !os.SameFile(info, d.info) || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("guide asset directory changed")
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return data, nil
}

// WriteNew is restricted to a fresh owned output path; drift is never repaired
// by replacing a pre-existing file or link.
func WriteNew(root, name string, data []byte) error {
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("guide output is not a regular directory")
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer r.Close()
	opened, err := r.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		return errors.New("guide output changed while opening")
	}
	return writeNewRoot(r, name, data)
}

func writeNewRoot(r *os.Root, name string, data []byte) error {
	if filepath.Base(name) != name || name == "." || name == ".." {
		return errors.New("invalid guide output name")
	}
	f, err := r.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	return errors.Join(err, f.Close())
}
