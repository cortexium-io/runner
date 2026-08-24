//go:build darwin || linux

package securefs

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

var ErrChanged = errors.New("secure filesystem object changed")

type Directory struct {
	fd                         int
	path                       string
	initial                    FileState
	beforeOpenDirForTest       func()
	beforeReplaceCommitForTest func()
}

type FileState struct {
	Exists        bool
	dev           uint64
	ino           uint64
	uid           uint32
	mode          uint32
	nlink         uint64
	size          int64
	mtime         int64
	ctime         int64
	contentDigest [sha256.Size]byte
	hasContent    bool
}

// ValidateOwnedRegularFile validates metadata captured from the descriptor
// that ReadFile actually opened. Callers use this instead of a path-level
// stat so provenance checks cannot be separated from the bytes they decode.
func ValidateOwnedRegularFile(state FileState, uid uint32) error {
	if !state.Exists || state.mode&unix.S_IFMT != unix.S_IFREG {
		return errors.New("file must be a regular non-symlinked file")
	}
	if state.uid != uid {
		return fmt.Errorf("file must be owned by effective uid %d", uid)
	}
	if state.mode&0o022 != 0 {
		return errors.New("file must not be writable by group or other users")
	}
	if state.nlink != 1 {
		return errors.New("file must have exactly one filesystem link")
	}
	return nil
}

type PinnedFile struct {
	file    *os.File
	parent  *Directory
	name    string
	initial FileState
}

func EnsurePrivateDir(path string) error {
	directory, err := openAbsoluteDir(path, true, true)
	if err != nil {
		return err
	}
	return directory.Close()
}

func ValidatePrivateDir(path string) error {
	directory, err := openAbsoluteDir(path, false, true)
	if err != nil {
		return err
	}
	return directory.Close()
}

func OpenDir(path string) (*Directory, error) {
	return openAbsoluteDir(path, false, false)
}

// OpenDir opens one child directory relative to the pinned directory without
// following links or resolving the child through an absolute path.
func (d *Directory) OpenDir(name string) (*Directory, error) {
	if d == nil || d.fd < 0 {
		return nil, errors.New("secure directory is closed")
	}
	if err := validateLeaf(name); err != nil {
		return nil, err
	}
	var before unix.Stat_t
	if err := unix.Fstatat(d.fd, name, &before, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return nil, err
	}
	if before.Mode&unix.S_IFMT != unix.S_IFDIR {
		return nil, fmt.Errorf("secure directory child %q is not a directory or is a symlink", name)
	}
	if d.beforeOpenDirForTest != nil {
		d.beforeOpenDirForTest()
	}
	fd, err := unix.Openat(d.fd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	var opened unix.Stat_t
	if err := unix.Fstat(fd, &opened); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	if !sameObject(before, opened) {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("%w while opening child directory %q", ErrChanged, name)
	}
	if err := validateDirectory(opened, false); err != nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("unsafe child directory %q: %w", name, err)
	}
	return &Directory{
		fd:      fd,
		path:    filepath.Join(d.path, name),
		initial: stateFromStat(opened),
	}, nil
}

func AbsolutePath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	absolute = filepath.Clean(absolute)
	absolute = platformAbsolutePath(absolute)
	return absolute, nil
}

func openAbsoluteDir(path string, create, privateTarget bool) (*Directory, error) {
	absolute, err := AbsolutePath(path)
	if err != nil {
		return nil, err
	}
	if !filepath.IsAbs(absolute) {
		return nil, fmt.Errorf("secure directory path %q is not absolute", path)
	}
	current, err := unix.Open(string(os.PathSeparator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open filesystem root: %w", err)
	}
	closeCurrent := true
	defer func() {
		if closeCurrent {
			_ = unix.Close(current)
		}
	}()

	components := strings.Split(strings.TrimPrefix(absolute, string(os.PathSeparator)), string(os.PathSeparator))
	if absolute == string(os.PathSeparator) {
		components = nil
	}
	for index, component := range components {
		if component == "" || component == "." || component == ".." {
			return nil, fmt.Errorf("secure directory path %q contains an invalid component", absolute)
		}
		var before unix.Stat_t
		statErr := unix.Fstatat(current, component, &before, unix.AT_SYMLINK_NOFOLLOW)
		if errors.Is(statErr, unix.ENOENT) && create {
			if err := unix.Mkdirat(current, component, 0o700); err != nil && !errors.Is(err, unix.EEXIST) {
				return nil, fmt.Errorf("create secure directory component %q: %w", component, err)
			}
			statErr = unix.Fstatat(current, component, &before, unix.AT_SYMLINK_NOFOLLOW)
		}
		if statErr != nil {
			return nil, fmt.Errorf("inspect secure directory component %q: %w", component, statErr)
		}
		if before.Mode&unix.S_IFMT != unix.S_IFDIR {
			return nil, fmt.Errorf("secure directory component %q is not a directory or is a symlink", component)
		}
		next, err := unix.Openat(current, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return nil, fmt.Errorf("open secure directory component %q without following links: %w", component, err)
		}
		var after unix.Stat_t
		if err := unix.Fstat(next, &after); err != nil {
			_ = unix.Close(next)
			return nil, fmt.Errorf("verify secure directory component %q: %w", component, err)
		}
		if before.Dev != after.Dev || before.Ino != after.Ino || after.Mode&unix.S_IFMT != unix.S_IFDIR {
			_ = unix.Close(next)
			return nil, fmt.Errorf("%w while opening directory component %q", ErrChanged, component)
		}
		isTarget := index == len(components)-1
		if err := validateDirectory(after, privateTarget && isTarget); err != nil {
			_ = unix.Close(next)
			return nil, fmt.Errorf("unsafe directory component %q: %w", component, err)
		}
		_ = unix.Close(current)
		current = next
	}
	if len(components) == 0 && privateTarget {
		return nil, errors.New("filesystem root cannot be used as a private workspace root")
	}
	var opened unix.Stat_t
	if err := unix.Fstat(current, &opened); err != nil {
		return nil, fmt.Errorf("record secure directory state: %w", err)
	}
	closeCurrent = false
	return &Directory{fd: current, path: absolute, initial: stateFromStat(opened)}, nil
}

func validateDirectory(stat unix.Stat_t, private bool) error {
	permissions := stat.Mode & 0o7777
	uid := uint32(os.Geteuid())
	if stat.Uid != 0 && stat.Uid != uid {
		return fmt.Errorf("directory owner uid %d is neither root nor the effective uid %d", stat.Uid, uid)
	}
	if permissions&0o022 != 0 && permissions&unix.S_ISVTX == 0 {
		return fmt.Errorf("directory mode %04o permits replacement by another local user", permissions)
	}
	if private && (stat.Uid != uid || permissions != 0o700) {
		return fmt.Errorf("private directory must be owned by effective uid %d with mode 0700; got uid %d mode %04o", uid, stat.Uid, permissions)
	}
	return nil
}

func (d *Directory) Close() error {
	if d == nil || d.fd < 0 {
		return nil
	}
	err := unix.Close(d.fd)
	d.fd = -1
	return err
}

func (d *Directory) Verify() error {
	if d == nil || d.fd < 0 {
		return errors.New("secure directory is closed")
	}
	var current unix.Stat_t
	if err := unix.Fstat(d.fd, &current); err != nil {
		return err
	}
	if stateFromStat(current) != d.initial {
		return fmt.Errorf("%w while using directory %s", ErrChanged, d.path)
	}
	named, err := openAbsoluteDir(d.path, false, false)
	if err != nil {
		return fmt.Errorf("%w while re-opening directory %s: %v", ErrChanged, d.path, err)
	}
	defer named.Close()
	if named.initial != d.initial {
		return fmt.Errorf("%w while resolving directory %s", ErrChanged, d.path)
	}
	return nil
}

// VerifyIdentity proves that the pinned directory still resolves to the same
// directory without requiring its contents or timestamps to remain unchanged.
// Callers must separately verify every security-sensitive child they use.
func (d *Directory) VerifyIdentity() error {
	return d.verifyIdentity(false)
}

func (d *Directory) verifyIdentity(private bool) error {
	if d == nil || d.fd < 0 {
		return errors.New("secure directory is closed")
	}
	var opened unix.Stat_t
	if err := unix.Fstat(d.fd, &opened); err != nil {
		return err
	}
	if err := validateDirectory(opened, private); err != nil {
		return err
	}
	named, err := openAbsoluteDir(d.path, false, private)
	if err != nil {
		return fmt.Errorf("%w while re-opening directory %s: %v", ErrChanged, d.path, err)
	}
	defer named.Close()
	openedState := stateFromStat(opened)
	if openedState.dev != named.initial.dev || openedState.ino != named.initial.ino || openedState.mode&unix.S_IFMT != named.initial.mode&unix.S_IFMT {
		return fmt.Errorf("%w while resolving directory %s", ErrChanged, d.path)
	}
	return nil
}

// VerifyEmpty proves through the pinned descriptor that a directory contains
// no entries and then revalidates its identity and named path.
func (d *Directory) VerifyEmpty() error {
	if d == nil || d.fd < 0 {
		return errors.New("secure directory is closed")
	}
	fd, err := unix.Openat(d.fd, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("open pinned secure directory descriptor: %w", err)
	}
	var opened unix.Stat_t
	if err := unix.Fstat(fd, &opened); err != nil {
		_ = unix.Close(fd)
		return fmt.Errorf("inspect pinned secure directory descriptor: %w", err)
	}
	if stateFromStat(opened) != d.initial {
		_ = unix.Close(fd)
		return fmt.Errorf("%w while opening directory %s for empty-state verification", ErrChanged, d.path)
	}
	directory := os.NewFile(uintptr(fd), d.path)
	if directory == nil {
		_ = unix.Close(fd)
		return errors.New("create secure directory handle")
	}
	entries, readErr := directory.Readdirnames(1)
	closeErr := directory.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return fmt.Errorf("read secure directory entries: %w", readErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close secure directory handle: %w", closeErr)
	}
	if len(entries) != 0 {
		return errors.New("secure directory is not empty")
	}
	return d.Verify()
}

func (d *Directory) VerifyFile(name string, expected FileState) error {
	if err := validateLeaf(name); err != nil {
		return err
	}
	return d.verifyState(name, expected)
}

func (d *Directory) OpenFile(name string) (*PinnedFile, error) {
	if err := validateLeaf(name); err != nil {
		return nil, err
	}
	var before unix.Stat_t
	if err := unix.Fstatat(d.fd, name, &before, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return nil, err
	}
	if before.Mode&unix.S_IFMT != unix.S_IFREG {
		return nil, fmt.Errorf("%s is not a regular file", filepath.Join(d.path, name))
	}
	fd, err := unix.Openat(d.fd, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	var opened unix.Stat_t
	if err := unix.Fstat(fd, &opened); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	if !sameObject(before, opened) {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("%w while opening %s", ErrChanged, filepath.Join(d.path, name))
	}
	file := os.NewFile(uintptr(fd), filepath.Join(d.path, name))
	if file == nil {
		_ = unix.Close(fd)
		return nil, errors.New("create pinned file handle")
	}
	return &PinnedFile{file: file, parent: d, name: name, initial: stateFromStat(opened)}, nil
}

func (f *PinnedFile) Close() error {
	if f == nil || f.file == nil {
		return nil
	}
	err := f.file.Close()
	f.file = nil
	return err
}

func (f *PinnedFile) ReadAll(limit int64) ([]byte, error) {
	if limit <= 0 {
		return nil, errors.New("secure file read limit must be positive")
	}
	read := func() ([]byte, error) {
		if f == nil || f.file == nil {
			return nil, errors.New("pinned file is closed")
		}
		if _, err := f.file.Seek(0, io.SeekStart); err != nil {
			return nil, err
		}
		content, err := io.ReadAll(io.LimitReader(f.file, limit+1))
		if err != nil {
			return nil, err
		}
		if int64(len(content)) > limit {
			return nil, fmt.Errorf("secure file exceeds %d bytes", limit)
		}
		return content, nil
	}
	content, err := read()
	if err != nil {
		return nil, err
	}
	verified, err := read()
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(content, verified) {
		return nil, fmt.Errorf("%w while reading %s", ErrChanged, f.file.Name())
	}
	if err := f.Verify(); err != nil {
		return nil, err
	}
	return content, nil
}

// ReadAllMutable permits content changes made through the pre-created inode,
// while still proving its identity, type, owner, mode, and stable readback.
func (f *PinnedFile) ReadAllMutable(limit int64) ([]byte, error) {
	if limit <= 0 {
		return nil, errors.New("secure file read limit must be positive")
	}
	read := func() ([]byte, FileState, error) {
		if f == nil || f.file == nil {
			return nil, FileState{}, errors.New("pinned file is closed")
		}
		var opened, named unix.Stat_t
		if err := unix.Fstat(int(f.file.Fd()), &opened); err != nil {
			return nil, FileState{}, err
		}
		if err := validateArtifactFile(opened); err != nil {
			return nil, FileState{}, err
		}
		if err := unix.Fstatat(f.parent.fd, f.name, &named, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return nil, FileState{}, fmt.Errorf("%w: %v", ErrChanged, err)
		}
		if !sameObject(opened, named) {
			return nil, FileState{}, fmt.Errorf("%w while reading %s", ErrChanged, filepath.Join(f.parent.path, f.name))
		}
		if err := validateArtifactFile(named); err != nil {
			return nil, FileState{}, err
		}
		if uint64(opened.Dev) != f.initial.dev || opened.Ino != f.initial.ino {
			return nil, FileState{}, fmt.Errorf("%w while reading %s", ErrChanged, filepath.Join(f.parent.path, f.name))
		}
		if _, err := f.file.Seek(0, io.SeekStart); err != nil {
			return nil, FileState{}, err
		}
		content, err := io.ReadAll(io.LimitReader(f.file, limit+1))
		if err != nil {
			return nil, FileState{}, err
		}
		if int64(len(content)) > limit {
			return nil, FileState{}, fmt.Errorf("secure file exceeds %d bytes", limit)
		}
		var finished, namedAfter unix.Stat_t
		if err := unix.Fstat(int(f.file.Fd()), &finished); err != nil {
			return nil, FileState{}, err
		}
		if err := unix.Fstatat(f.parent.fd, f.name, &namedAfter, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return nil, FileState{}, fmt.Errorf("%w: %v", ErrChanged, err)
		}
		if err := validateArtifactFile(finished); err != nil {
			return nil, FileState{}, err
		}
		beforeState, afterState := stateFromStat(opened), stateFromStat(finished)
		if beforeState != afterState || !sameObject(finished, namedAfter) {
			return nil, FileState{}, fmt.Errorf("%w while reading %s", ErrChanged, filepath.Join(f.parent.path, f.name))
		}
		return content, afterState, nil
	}
	content, before, err := read()
	if err != nil {
		return nil, err
	}
	verified, after, err := read()
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(content, verified) || before != after {
		return nil, fmt.Errorf("%w while reading %s", ErrChanged, f.file.Name())
	}
	return content, nil
}

func (f *PinnedFile) Verify() error {
	if f == nil || f.file == nil {
		return errors.New("pinned file is closed")
	}
	var opened, named unix.Stat_t
	if err := unix.Fstat(int(f.file.Fd()), &opened); err != nil {
		return err
	}
	if err := unix.Fstatat(f.parent.fd, f.name, &named, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return fmt.Errorf("%w: %v", ErrChanged, err)
	}
	if stateFromStat(opened) != f.initial || stateFromStat(named) != f.initial {
		return fmt.Errorf("%w while reading %s", ErrChanged, filepath.Join(f.parent.path, f.name))
	}
	return nil
}

func (d *Directory) ReadFile(name string, limit int64) ([]byte, os.FileMode, FileState, error) {
	pinned, err := d.OpenFile(name)
	if errors.Is(err, unix.ENOENT) {
		return nil, 0o644, FileState{Exists: false}, nil
	}
	if err != nil {
		return nil, 0, FileState{}, err
	}
	defer pinned.Close()
	content, err := pinned.ReadAll(limit)
	state := pinned.initial
	if err == nil {
		state.contentDigest = sha256.Sum256(content)
		state.hasContent = true
	}
	return content, os.FileMode(pinned.initial.mode & 0o777), state, err
}

func ReadFile(path string, limit int64) ([]byte, os.FileMode, FileState, error) {
	directory, err := OpenDir(filepath.Dir(path))
	if errors.Is(err, unix.ENOENT) {
		return nil, 0o644, FileState{Exists: false}, nil
	}
	if err != nil {
		return nil, 0, FileState{}, err
	}
	defer directory.Close()
	return directory.ReadFile(filepath.Base(path), limit)
}

func (d *Directory) ReplaceFile(name string, content []byte, mode os.FileMode, expected FileState) error {
	if err := validateLeaf(name); err != nil {
		return err
	}
	temporaryName, err := randomTemporaryName()
	if err != nil {
		return err
	}
	fd, err := unix.Openat(d.fd, temporaryName, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, uint32(mode.Perm()))
	if err != nil {
		return err
	}
	defer func() {
		_ = unix.Unlinkat(d.fd, temporaryName, 0)
	}()
	file := os.NewFile(uintptr(fd), filepath.Join(d.path, temporaryName))
	if file == nil {
		_ = unix.Close(fd)
		return errors.New("create secure temporary file handle")
	}
	if err := file.Chmod(mode.Perm()); err != nil {
		_ = file.Close()
		return err
	}
	if _, err := file.Write(content); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := d.verifyState(name, expected); err != nil {
		return err
	}
	if d.beforeReplaceCommitForTest != nil {
		d.beforeReplaceCommitForTest()
	}
	if err := conditionalReplaceAt(d.fd, temporaryName, name, expected); err != nil {
		return err
	}
	return unix.Fsync(d.fd)
}

func WriteFileExclusive(path string, content []byte, mode os.FileMode) error {
	directory, err := OpenDir(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer directory.Close()
	name := filepath.Base(path)
	if err := validateLeaf(name); err != nil {
		return err
	}
	fd, err := unix.Openat(directory.fd, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, uint32(mode.Perm()))
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return errors.New("create secure file handle")
	}
	if _, err := file.Write(content); err != nil {
		_ = file.Close()
		_ = unix.Unlinkat(directory.fd, name, 0)
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = unix.Unlinkat(directory.fd, name, 0)
		return err
	}
	if err := file.Close(); err != nil {
		_ = unix.Unlinkat(directory.fd, name, 0)
		return err
	}
	return unix.Fsync(directory.fd)
}

func LinkFile(source, destination string) error {
	sourceDir, err := OpenDir(filepath.Dir(source))
	if err != nil {
		return err
	}
	defer sourceDir.Close()
	destinationDir, err := OpenDir(filepath.Dir(destination))
	if err != nil {
		return err
	}
	defer destinationDir.Close()
	sourceName, destinationName := filepath.Base(source), filepath.Base(destination)
	if err := validateLeaf(sourceName); err != nil {
		return err
	}
	if err := validateLeaf(destinationName); err != nil {
		return err
	}
	var sourceState unix.Stat_t
	if err := unix.Fstatat(sourceDir.fd, sourceName, &sourceState, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	if sourceState.Mode&unix.S_IFMT != unix.S_IFREG {
		return errors.New("secure link source is not a regular file")
	}
	if err := unix.Linkat(sourceDir.fd, sourceName, destinationDir.fd, destinationName, 0); err != nil {
		return err
	}
	var linked unix.Stat_t
	if err := unix.Fstatat(destinationDir.fd, destinationName, &linked, unix.AT_SYMLINK_NOFOLLOW); err != nil || !sameObject(sourceState, linked) {
		_ = unix.Unlinkat(destinationDir.fd, destinationName, 0)
		return fmt.Errorf("%w while linking %s", ErrChanged, source)
	}
	return nil
}

func RemoveFile(path string) error {
	directory, err := OpenDir(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer directory.Close()
	name := filepath.Base(path)
	if err := validateLeaf(name); err != nil {
		return err
	}
	var state unix.Stat_t
	if err := unix.Fstatat(directory.fd, name, &state, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	if state.Mode&unix.S_IFMT != unix.S_IFREG {
		return errors.New("secure remove target is not a regular file")
	}
	return unix.Unlinkat(directory.fd, name, 0)
}

func (d *Directory) verifyState(name string, expected FileState) error {
	var current unix.Stat_t
	err := unix.Fstatat(d.fd, name, &current, unix.AT_SYMLINK_NOFOLLOW)
	if !expected.Exists {
		if errors.Is(err, unix.ENOENT) {
			return nil
		}
		if err != nil {
			return err
		}
		return fmt.Errorf("%w: %s appeared before replacement", ErrChanged, filepath.Join(d.path, name))
	}
	if err != nil {
		return fmt.Errorf("%w: %v", ErrChanged, err)
	}
	if !sameFileMetadata(stateFromStat(current), expected) {
		return fmt.Errorf("%w before replacing %s", ErrChanged, filepath.Join(d.path, name))
	}
	return nil
}

func validateLeaf(name string) error {
	if name == "" || name == "." || name == ".." || filepath.Base(name) != name || strings.ContainsRune(name, os.PathSeparator) {
		return fmt.Errorf("invalid secure filesystem leaf %q", name)
	}
	return nil
}

func randomTemporaryName() (string, error) {
	buffer := make([]byte, 12)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return ".runner-secure-" + hex.EncodeToString(buffer) + ".tmp", nil
}

func stateFromStat(stat unix.Stat_t) FileState {
	mtime, ctime := statTimes(stat)
	return FileState{Exists: true, dev: uint64(stat.Dev), ino: stat.Ino, uid: stat.Uid, mode: uint32(stat.Mode), nlink: uint64(stat.Nlink), size: stat.Size, mtime: mtime, ctime: ctime}
}

func sameFileMetadata(current, expected FileState) bool {
	return current.Exists == expected.Exists && current.dev == expected.dev && current.ino == expected.ino &&
		current.uid == expected.uid && current.mode == expected.mode && current.nlink == expected.nlink && current.size == expected.size && current.mtime == expected.mtime && current.ctime == expected.ctime
}

func statTimes(stat unix.Stat_t) (int64, int64) {
	return stat.Mtim.Sec*1_000_000_000 + stat.Mtim.Nsec, stat.Ctim.Sec*1_000_000_000 + stat.Ctim.Nsec
}

func sameObject(left, right unix.Stat_t) bool {
	return left.Dev == right.Dev && left.Ino == right.Ino && left.Mode&unix.S_IFMT == right.Mode&unix.S_IFMT
}
