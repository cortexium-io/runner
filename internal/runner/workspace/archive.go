package workspace

import (
	"archive/tar"
	"bytes"
	"errors"
	"github.com/cortexium-io/runner/internal/securefs"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func extractArchive(root string, data []byte) error {
	r := tar.NewReader(bytes.NewReader(data))
	for {
		h, e := r.Next()
		if e == io.EOF {
			return nil
		}
		if e != nil {
			return e
		}
		name := filepath.Clean(h.Name)
		if filepath.IsAbs(name) || name == ".." || strings.HasPrefix(name, "../") || name == ".git" || strings.HasPrefix(name, ".git/") {
			return errors.New("unsafe archive path")
		}
		path := filepath.Join(root, name)
		switch h.Typeflag {
		case tar.TypeXGlobalHeader:
			continue
		case tar.TypeDir:
			if e = os.MkdirAll(path, 0700); e != nil {
				return e
			}
		case tar.TypeReg:
			if h.Size > 64<<20 {
				return errors.New("archive file too large")
			}
			b, e := io.ReadAll(r)
			if e != nil {
				return e
			}
			if e = os.MkdirAll(filepath.Dir(path), 0700); e != nil {
				return e
			}
			if e = securefs.WriteFileExclusive(path, b, os.FileMode(h.Mode)&0777); e != nil {
				return e
			}
		default:
			return errors.New("baseline archive contains an unsupported link or special file")
		}
	}
}
