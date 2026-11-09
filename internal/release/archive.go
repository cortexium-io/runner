package release

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

func tarFileHash(data []byte, name string) (string, error) {
	r := tar.NewReader(bytes.NewReader(data))
	for {
		h, err := r.Next()
		if err != nil {
			return "", err
		}
		if h.Name == name && h.Typeflag == tar.TypeReg {
			return hashReader(io.LimitReader(r, h.Size))
		}
	}
}

func packTree(root string) ([]byte, error) {
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	t := tar.NewWriter(gz)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("asset is not a regular file")
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		h, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		h.Name = filepath.ToSlash(relative)
		if err = t.WriteHeader(h); err != nil {
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		_, err = io.Copy(t, f)
		f.Close()
		if out.Len() > 64<<20 {
			return errors.New("asset archive exceeds allowance")
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	if err = t.Close(); err != nil {
		return nil, err
	}
	if err = gz.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
