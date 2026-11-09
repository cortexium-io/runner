package main

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
)

func safeParent(path string) (string, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || filepath.Base(path) == "." || filepath.Base(path) == string(filepath.Separator) {
		return "", errors.New("explicit clean absolute file paths required")
	}
	parent := filepath.Dir(path)
	canonical, err := filepath.EvalSymlinks(parent)
	if err != nil || canonical != parent {
		return "", errors.New("file parents must exist without symbolic links")
	}
	info, err := os.Lstat(parent)
	if err != nil || !info.IsDir() {
		return "", errors.New("file parent is not a regular directory")
	}
	return parent, nil
}

func readBounded(path string) ([]byte, error) {
	parent, err := safeParent(path)
	if err != nil {
		return nil, err
	}
	parentInfo, err := os.Lstat(parent)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(parent)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	openedParent, err := root.Stat(".")
	if err != nil || !os.SameFile(parentInfo, openedParent) {
		return nil, errors.New("input parent changed while opening")
	}
	info, err := root.Lstat(filepath.Base(path))
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || !unlinked(info) || info.Size() > maxBytes {
		return nil, errors.New("input must be a regular unlinked file within 16MiB")
	}
	file, err := root.Open(filepath.Base(path))
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) || !opened.Mode().IsRegular() || !unlinked(opened) {
		return nil, errors.New("input changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, err
	}
	after, err := file.Stat()
	if err != nil || len(data) > maxBytes || int64(len(data)) != info.Size() || !os.SameFile(info, after) || info.Mode() != after.Mode() || info.Size() != after.Size() || !info.ModTime().Equal(after.ModTime()) || !unlinked(after) {
		return nil, errors.New("input changed or exceeded its bound during reading")
	}
	return data, nil
}

func writeHistory(path string, h history) error {
	parent, err := safeParent(path)
	if err != nil {
		return err
	}
	var data bytes.Buffer
	encoder := json.NewEncoder(&data)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(h); err != nil {
		return err
	}
	if data.Len() > maxBytes {
		return errors.New("measurement history exceeds 16MiB output bound")
	}
	parentInfo, err := os.Lstat(parent)
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(parent)
	if err != nil {
		return err
	}
	defer root.Close()
	openedParent, err := root.Stat(".")
	if err != nil || !os.SameFile(parentInfo, openedParent) {
		return errors.New("output parent changed while opening")
	}
	if info, err := root.Lstat(filepath.Base(path)); err == nil {
		if !info.Mode().IsRegular() || !unlinked(info) {
			return errors.New("output must be a regular unlinked file")
		}
		existing, err := readBounded(path)
		if err != nil {
			return err
		}
		if !bytes.Equal(existing, data.Bytes()) {
			return errors.New("existing output differs; preserve it and choose a fresh output")
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	temporary := ".measurement-history-" + rand.Text()
	file, err := root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer root.Remove(temporary)
	_, err = file.Write(data.Bytes())
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err := root.Rename(temporary, filepath.Base(path)); err != nil {
		return err
	}
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
