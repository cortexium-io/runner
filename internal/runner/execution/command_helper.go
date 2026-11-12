package execution

import (
	"errors"
	"io"
	"os"
	"path/filepath"
)

func readPinnedCommandHelper(path, expected, name string, maxHelperBytes int64) ([]byte, error) {
	if !filepath.IsAbs(path) || filepath.Base(path) != name || !publicationDigest.MatchString(expected) {
		return nil, errors.New("pinned command recovery requires the configured helper and exact digest")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 || info.Size() < 1 || info.Size() > maxHelperBytes {
		return nil, errors.New("pinned command helper is unavailable or differs from its qualified identity")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, errors.New("pinned command helper changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(f, maxHelperBytes+1))
	if err != nil {
		return nil, err
	}
	current, err := os.Lstat(path)
	if err != nil || !os.SameFile(info, current) || int64(len(data)) != info.Size() || current.Size() != info.Size() || !current.ModTime().Equal(info.ModTime()) || captureDigest(data) != expected {
		return nil, errors.New("pinned command helper differs from its approved digest")
	}
	return data, nil
}
