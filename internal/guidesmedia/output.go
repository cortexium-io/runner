package guidesmedia

import (
	"context"
	"errors"
	"io"
	"os"
)

func ReadOutputAsset(ctx context.Context, root string, a Asset) ([]byte, error) {
	return readFile(ctx, root, File{
		RelativePath: OutputName(a.Game, a.Role),
		Bytes:        a.Bytes,
		SHA256:       a.SHA256,
	})
}

func ReadOutputIndex(root string) ([]byte, Index, error) {
	var index Index
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, index, errors.New("guide output directory differs")
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, index, err
	}
	defer r.Close()
	openedRoot, err := r.Stat(".")
	if err != nil || !os.SameFile(info, openedRoot) {
		return nil, index, errors.New("guide output directory changed")
	}
	before, err := r.Lstat("media-index.json")
	if err != nil || !before.Mode().IsRegular() || before.Size() < 1 || before.Size() > 32<<10 {
		return nil, index, errors.New("guide output index type/size differs")
	}
	f, err := r.Open("media-index.json")
	if err != nil {
		return nil, index, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(before, opened) || !opened.Mode().IsRegular() {
		return nil, index, errors.New("guide index changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(f, (32<<10)+1))
	if err != nil {
		return nil, index, err
	}
	after, err := f.Stat()
	named, namedErr := r.Lstat("media-index.json")
	currentRoot, rootErr := os.Lstat(root)
	if err != nil || namedErr != nil || rootErr != nil || !os.SameFile(opened, after) || !os.SameFile(opened, named) || !named.Mode().IsRegular() || !os.SameFile(info, currentRoot) || !currentRoot.IsDir() || currentRoot.Mode()&os.ModeSymlink != 0 || int64(len(data)) != before.Size() || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		return nil, index, errors.New("guide index bytes or directory changed")
	}
	index, err = DecodeIndex(data)
	return data, index, err
}
