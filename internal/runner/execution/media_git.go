package execution

import (
	"bytes"
	"compress/zlib"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cortexium-io/runner/internal/guidesmedia"
	"github.com/cortexium-io/runner/internal/runner"
)

// Git media objects remain in counted Core-owned storage. Source repositories
// receive hard links, never an unaccounted second media copy. No alternates,
// global Git settings, garbage collection or existing object replacement occurs.
func (w *Workspaces) mediaGitObject(ctx context.Context, r workspaceRecord, dir string, env []string, data []byte) (string, error) {
	if r.GuidesMedia == nil || r.GuidesMedia.ManifestSHA256 != guidesmedia.ManifestSHA256 {
		return "", errors.New("media Git object lacks whole-set authority")
	}
	body := append([]byte(fmt.Sprintf("blob %d%c", len(data), 0)), data...)
	h := sha1.Sum(body)
	id := hex.EncodeToString(h[:])
	rel := "work/guides-media-objects/" + guidesmedia.ManifestSHA256
	if err := captureDirectory(w.disk.Root, "work", "guides-media-objects", guidesmedia.ManifestSHA256); err != nil {
		return "", err
	}

	leasePath := filepath.Join(w.disk.Root, rel, "lease")
	prior, err := os.Lstat(leasePath)
	flags := os.O_RDWR
	if errors.Is(err, os.ErrNotExist) {
		flags |= os.O_CREATE | os.O_EXCL
	} else if err != nil || !prior.Mode().IsRegular() || prior.Size() != 0 {
		return "", errors.New("counted media object lease ownership differs")
	}
	lease, err := os.OpenFile(leasePath, flags, 0600)
	if err != nil {
		return "", err
	}
	defer lease.Close()
	if prior != nil {
		opened, err := lease.Stat()
		if err != nil || !os.SameFile(prior, opened) {
			return "", errors.New("counted media object lease changed while opening")
		}
	}
	for {
		owned, err := tryCaptureLease(lease)
		if err != nil {
			return "", err
		}
		if owned {
			break
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
	object := rel + "/" + id
	compressed, err := w.disk.Read(object, runner.Bytes(captureObjectBound(len(data))))
	if errors.Is(err, os.ErrNotExist) {
		var output bytes.Buffer
		z := zlib.NewWriter(&output)
		if _, err = z.Write(body); err == nil {
			err = z.Close()
		} else {
			_ = z.Close()
		}
		if err != nil {
			return "", err
		}
		compressed = output.Bytes()
		if err := w.disk.Write(object, compressed, runner.Bytes(captureObjectBound(len(data)))); err != nil {
			return "", err
		}
	} else if err != nil {
		return "", err
	}
	check, err := zlib.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return "", err
	}
	decoded, err := io.ReadAll(io.LimitReader(check, int64(len(body))+1))
	closeErr := check.Close()
	if err != nil || closeErr != nil || !bytes.Equal(decoded, body) {
		return "", errors.New("counted immutable media Git object changed")
	}
	objectDir, err := git(ctx, dir, env, nil, 4096, "rev-parse", "--path-format=absolute", "--git-path", "objects")
	if err != nil {
		return "", err
	}
	objects := strings.TrimSpace(string(objectDir))
	info, err := os.Lstat(objects)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !filepath.IsAbs(objects) {
		return "", errors.New("owned Git object directory differs")
	}
	shard := filepath.Join(objects, id[:2])
	if err := os.Mkdir(shard, 0755); err != nil && !errors.Is(err, os.ErrExist) {
		return "", err
	}
	info, err = os.Lstat(shard)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("owned Git object shard differs")
	}
	canonical := filepath.Join(w.disk.Root, object)
	target := filepath.Join(shard, id[2:])
	if err := os.Link(canonical, target); err != nil && !errors.Is(err, os.ErrExist) {
		return "", fmt.Errorf("counted media Git hard link is unavailable; no unaccounted copy fallback: %w", err)
	}
	stored, err := os.Lstat(canonical)
	linked, linkErr := os.Lstat(target)
	if err != nil || linkErr != nil || !stored.Mode().IsRegular() || !linked.Mode().IsRegular() || !os.SameFile(stored, linked) {
		return "", errors.New("media Git object is not the counted owned hard link; preserve existing source object")
	}
	return id, nil
}
