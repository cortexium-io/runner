//go:build darwin || linux

package securefs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// ArtifactFile describes one file created before an external process starts.
// Mutable files may change content, but never identity or access controls.
type ArtifactFile struct {
	Name    string
	Content []byte
	Mutable bool
}

// ArtifactSet owns one private invocation directory and its pinned files.
type ArtifactSet struct {
	path      string
	directory *Directory
	files     map[string]*PinnedFile
	mutable   map[string]bool
}

// NewArtifactSet creates and pins all invocation artifacts before launch.
func NewArtifactSet(prefix string, specs []ArtifactFile) (*ArtifactSet, error) {
	path, err := os.MkdirTemp("", prefix+".*")
	if err != nil {
		return nil, fmt.Errorf("create private artifact directory: %w", err)
	}
	cleanupPath := true
	defer func() {
		if cleanupPath {
			_ = os.RemoveAll(path)
		}
	}()
	if err := os.Chmod(path, 0o700); err != nil {
		return nil, fmt.Errorf("secure artifact directory: %w", err)
	}
	directory, err := openAbsoluteDir(path, false, true)
	if err != nil {
		return nil, fmt.Errorf("pin private artifact directory: %w", err)
	}
	set := &ArtifactSet{path: path, directory: directory, files: make(map[string]*PinnedFile), mutable: make(map[string]bool)}
	for _, spec := range specs {
		if _, exists := set.files[spec.Name]; exists {
			_ = set.Close()
			return nil, fmt.Errorf("duplicate artifact file %q", spec.Name)
		}
		file, createErr := directory.createPinnedFile(spec.Name, spec.Content)
		if createErr != nil {
			_ = set.Close()
			return nil, fmt.Errorf("create artifact file %q: %w", spec.Name, createErr)
		}
		set.files[spec.Name] = file
		set.mutable[spec.Name] = spec.Mutable
	}
	var pinned unix.Stat_t
	if err := unix.Fstat(directory.fd, &pinned); err != nil {
		_ = set.Close()
		return nil, fmt.Errorf("pin populated artifact directory: %w", err)
	}
	directory.initial = stateFromStat(pinned)
	cleanupPath = false
	return set, nil
}

func (a *ArtifactSet) Path(name string) string {
	if a == nil {
		return ""
	}
	if _, ok := a.files[name]; !ok {
		return ""
	}
	return filepath.Join(a.path, name)
}

// ReadMutable reads a process-owned result through its pre-launch descriptor.
func (a *ArtifactSet) ReadMutable(name string, limit int64) ([]byte, error) {
	file, ok := a.files[name]
	if !ok || !a.mutable[name] {
		return nil, fmt.Errorf("artifact file %q is not mutable", name)
	}
	if err := a.directory.Verify(); err != nil {
		return nil, err
	}
	return file.ReadAllMutable(limit)
}

// VerifyImmutable proves an input artifact retained its original inode and content.
func (a *ArtifactSet) VerifyImmutable(name string) error {
	file, ok := a.files[name]
	if !ok || a.mutable[name] {
		return fmt.Errorf("artifact file %q is not immutable", name)
	}
	if err := a.directory.Verify(); err != nil {
		return err
	}
	return file.Verify()
}

// Close removes only the pinned files from the pinned invocation directory.
// The named directory is removed only if it still resolves to that directory.
func (a *ArtifactSet) Close() error {
	if a == nil || a.directory == nil {
		return nil
	}
	var errs []error
	verified := a.directory.verifyIdentity(true) == nil
	if !verified {
		errs = append(errs, fmt.Errorf("%w before removing artifact directory", ErrChanged))
	}
	for name, file := range a.files {
		if err := file.file.Truncate(0); err != nil {
			errs = append(errs, fmt.Errorf("clear artifact file %q: %w", name, err))
		}
		if err := a.removePinnedAliases(file.initial); err != nil {
			errs = append(errs, fmt.Errorf("remove renamed artifact file %q: %w", name, err))
		}
		if err := file.Close(); err != nil {
			errs = append(errs, err)
		}
		if err := unix.Unlinkat(a.directory.fd, name, 0); err != nil && !errors.Is(err, unix.ENOENT) {
			if directoryErr := unix.Unlinkat(a.directory.fd, name, unix.AT_REMOVEDIR); directoryErr != nil && !errors.Is(directoryErr, unix.ENOENT) {
				errs = append(errs, fmt.Errorf("remove artifact file %q: %w", name, err))
			}
		}
	}
	if err := a.directory.Close(); err != nil {
		errs = append(errs, err)
	}
	if verified {
		if err := os.Remove(a.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, fmt.Errorf("remove artifact directory: %w", err))
		}
	}
	a.directory = nil
	a.files = nil
	return errors.Join(errs...)
}

func (a *ArtifactSet) removePinnedAliases(expected FileState) error {
	fd, err := unix.Openat(a.directory.fd, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	directory := os.NewFile(uintptr(fd), a.path)
	if directory == nil {
		_ = unix.Close(fd)
		return errors.New("create artifact directory cleanup handle")
	}
	names, readErr := directory.Readdirnames(-1)
	closeErr := directory.Close()
	if readErr != nil {
		return readErr
	}
	if closeErr != nil {
		return closeErr
	}
	for _, name := range names {
		if validateLeaf(name) != nil {
			continue
		}
		var current unix.Stat_t
		if err := unix.Fstatat(a.directory.fd, name, &current, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			if errors.Is(err, unix.ENOENT) {
				continue
			}
			return err
		}
		state := stateFromStat(current)
		if state.dev == expected.dev && state.ino == expected.ino && current.Mode&unix.S_IFMT == unix.S_IFREG {
			if err := unix.Unlinkat(a.directory.fd, name, 0); err != nil && !errors.Is(err, unix.ENOENT) {
				return err
			}
		}
	}
	return nil
}

func (d *Directory) createPinnedFile(name string, content []byte) (*PinnedFile, error) {
	if err := validateLeaf(name); err != nil {
		return nil, err
	}
	fd, err := unix.Openat(d.fd, name, unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), filepath.Join(d.path, name))
	if file == nil {
		_ = unix.Close(fd)
		return nil, errors.New("create artifact file handle")
	}
	fail := func(err error) (*PinnedFile, error) {
		_ = file.Close()
		_ = unix.Unlinkat(d.fd, name, 0)
		return nil, err
	}
	if err := file.Chmod(0o600); err != nil {
		return fail(err)
	}
	if _, err := file.Write(content); err != nil {
		return fail(err)
	}
	if err := file.Sync(); err != nil {
		return fail(err)
	}
	if _, err := file.Seek(0, 0); err != nil {
		return fail(err)
	}
	var opened unix.Stat_t
	if err := unix.Fstat(fd, &opened); err != nil {
		return fail(err)
	}
	if err := validateArtifactFile(opened); err != nil {
		return fail(err)
	}
	return &PinnedFile{file: file, parent: d, name: name, initial: stateFromStat(opened)}, nil
}

func validateArtifactFile(stat unix.Stat_t) error {
	if stat.Mode&unix.S_IFMT != unix.S_IFREG {
		return errors.New("artifact must be a regular file")
	}
	if stat.Uid != uint32(os.Geteuid()) {
		return fmt.Errorf("artifact owner uid %d does not match effective uid %d", stat.Uid, os.Geteuid())
	}
	if stat.Mode&0o7777 != 0o600 {
		return fmt.Errorf("artifact mode must be 0600; got %04o", stat.Mode&0o7777)
	}
	if stat.Nlink != 1 {
		return fmt.Errorf("artifact link count must be 1; got %d", stat.Nlink)
	}
	return nil
}
