package main

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

const (
	sourceDigestMethod = "runner-source-snapshot-sha256-v1"
	maxSourceEntries   = 1 << 19
	maxSourcePathBytes = 8 << 20
)

// The digest covers regular source files, not Git metadata or Runner's writable
// scratch. Paths, executable bits and sizes frame the streamed file contents;
// checkout location, timestamps and read/write permission bits are not identity.
type sourceSnapshot struct {
	Identity     string `json:"identity"`
	SHA256       string `json:"sha256"`
	DigestMethod string `json:"digestMethod"`
	Files        int    `json:"files"`
	Bytes        int64  `json:"bytes"`

	root    string
	rootID  os.FileInfo
	entries []sourceEntry
}

type sourceEntry struct {
	path string
	info os.FileInfo
}

func takeSourceSnapshot(ctx context.Context, root string) (sourceSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return sourceSnapshot{}, err
	}
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return sourceSnapshot{}, errors.New("source snapshot requires a clean absolute directory")
	}
	info, err := os.Lstat(root)
	if err != nil {
		return sourceSnapshot{}, err
	}
	if !info.IsDir() {
		return sourceSnapshot{}, errors.New("source snapshot root is not a regular directory")
	}
	dir, err := os.OpenRoot(root)
	if err != nil {
		return sourceSnapshot{}, err
	}
	defer dir.Close()
	opened, err := dir.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		return sourceSnapshot{}, errors.New("source snapshot root changed while opening")
	}
	s := sourceSnapshot{root: root, rootID: info, DigestMethod: sourceDigestMethod}
	digest := sha256.New()
	digest.Write([]byte(sourceDigestMethod + "\x00"))
	err = walkSource(ctx, dir, func(name string, info os.FileInfo) error {
		s.entries = append(s.entries, sourceEntry{path: name, info: info})
		return nil
	})
	if err != nil {
		return sourceSnapshot{}, err
	}
	sort.Slice(s.entries, func(i, j int) bool { return s.entries[i].path < s.entries[j].path })
	buffer := make([]byte, 64<<10)
	for _, entry := range s.entries {
		if entry.info.IsDir() {
			continue
		}
		frameSourceFile(digest, entry.path, entry.info)
		if err = hashSourceFile(ctx, dir, entry.path, entry.info, digest, buffer); err != nil {
			return sourceSnapshot{}, err
		}
		s.Files++
		s.Bytes += entry.info.Size()
	}
	// Catch path, directory and already-read file changes during the first walk,
	// before a caller can use the stamp for its child process.
	if err = s.verify(ctx); err != nil {
		return sourceSnapshot{}, err
	}
	s.SHA256 = hex.EncodeToString(digest.Sum(nil))
	s.Identity = "sha256:" + s.SHA256
	return s, nil
}

func excludedSourcePath(name string) bool {
	switch name {
	case ".runner-tmp", ".git", ".codex", ".agents", ".aws":
		return true
	}
	return false
}

// The entry/path bounds apply to both walks and retain metadata only, never
// another copy of source file payloads.
func walkSource(ctx context.Context, root *os.Root, visit func(string, os.FileInfo) error) error {
	entries, pathBytes := 0, 0
	info, err := root.Stat(".")
	if err != nil {
		return err
	}
	pending := []sourceEntry{{path: ".", info: info}}
	for i := 0; i < len(pending); i++ {
		err := func() error {
			if err := ctx.Err(); err != nil {
				return err
			}
			dir, err := root.OpenRoot(pending[i].path)
			if err != nil {
				return err
			}
			defer dir.Close()
			opened, err := dir.Stat(".")
			if err != nil || !os.SameFile(pending[i].info, opened) {
				return errors.New("source directory changed while opening")
			}
			file, err := dir.Open(".")
			if err != nil {
				return err
			}
			defer file.Close()
			for {
				if err := ctx.Err(); err != nil {
					return err
				}
				// A large directory cannot allocate an unbounded listing before
				// the metadata budget is checked.
				batch, readErr := file.ReadDir(256)
				for _, entry := range batch {
					name := entry.Name()
					if pending[i].path != "." {
						name = pending[i].path + "/" + name
					}
					// Skip denied root metadata before even requesting its stat.
					if excludedSourcePath(name) {
						continue
					}
					entries++
					pathBytes += len(name)
					if entries > maxSourceEntries || pathBytes > maxSourcePathBytes {
						return errors.New("source snapshot metadata exceeds its entry/path bound")
					}
					if !fs.ValidPath(name) {
						return errors.New("source snapshot contains an unsafe relative path")
					}
					info, err := root.Lstat(name)
					if err != nil {
						return err
					}
					if !info.IsDir() && (!info.Mode().IsRegular() || !evidenceUnlinked(info)) {
						return fmt.Errorf("source snapshot requires unlinked regular files: %q", name)
					}
					if err = visit(name, info); err != nil {
						return err
					}
					if info.IsDir() {
						pending = append(pending, sourceEntry{path: name, info: info})
					}
				}
				if errors.Is(readErr, io.EOF) {
					return nil
				}
				if readErr != nil {
					return readErr
				}
			}
		}()
		if err != nil {
			return err
		}
	}
	return ctx.Err()
}

func frameSourceFile(digest hash.Hash, name string, info os.FileInfo) {
	var number [8]byte
	binary.BigEndian.PutUint64(number[:], uint64(len(name)))
	digest.Write(number[:])
	digest.Write([]byte(name))
	mode := byte(0)
	if info.Mode()&0111 != 0 {
		mode = 1
	}
	digest.Write([]byte{mode})
	binary.BigEndian.PutUint64(number[:], uint64(info.Size()))
	digest.Write(number[:])
}

type sourceReader struct {
	ctx  context.Context
	file *os.File
}

func (r sourceReader) Read(data []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.file.Read(data)
}

func sameSourceFile(before, after os.FileInfo) bool {
	return before != nil && after != nil && os.SameFile(before, after) &&
		before.Mode() == after.Mode() && before.Size() == after.Size() &&
		before.ModTime().Equal(after.ModTime()) && evidenceUnlinked(after)
}

func hashSourceFile(ctx context.Context, root *os.Root, name string, before os.FileInfo, digest hash.Hash, buffer []byte) error {
	file, err := root.Open(name)
	if err != nil {
		return err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !sameSourceFile(before, opened) {
		return fmt.Errorf("source file changed while opening: %q", name)
	}
	n, err := io.CopyBuffer(digest, io.LimitReader(sourceReader{ctx: ctx, file: file}, before.Size()+1), buffer)
	if err != nil {
		return err
	}
	after, err := file.Stat()
	if err != nil || n != before.Size() || !sameSourceFile(before, after) {
		return fmt.Errorf("source file changed while hashing: %q", name)
	}
	return nil
}

// Source is read-only to the measured child. Verification detects ordinary
// concurrent host changes using path/inode/mode/size/time metadata; it is not a
// second byte hash or protection against a hostile owner restoring all metadata.
func (s sourceSnapshot) verify(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := os.Lstat(s.root)
	if err != nil || !info.IsDir() || !os.SameFile(s.rootID, info) {
		return errors.New("source snapshot root changed")
	}
	root, err := os.OpenRoot(s.root)
	if err != nil {
		return err
	}
	defer root.Close()
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(s.rootID, opened) {
		return errors.New("source snapshot root changed while opening")
	}
	seen := 0
	err = walkSource(ctx, root, func(name string, info os.FileInfo) error {
		i := sort.Search(len(s.entries), func(i int) bool { return s.entries[i].path >= name })
		if i >= len(s.entries) || s.entries[i].path != name {
			return errors.New("source snapshot path inventory changed")
		}
		old := s.entries[i].info
		seen++
		if info.IsDir() {
			if !old.IsDir() || !os.SameFile(old, info) {
				return fmt.Errorf("source directory changed: %q", name)
			}
		} else if !sameSourceFile(old, info) {
			return fmt.Errorf("source file changed after snapshot: %q", name)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if seen != len(s.entries) {
		return errors.New("source snapshot path inventory changed")
	}
	return ctx.Err()
}
