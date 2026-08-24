//go:build darwin || linux

package securefs

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"sort"
	"strings"

	"golang.org/x/sys/unix"
)

const discoveryStageDirectoryRead = "directory-read"

type discoveryObserver func(stage, path string)

// PathDiscovery holds every directory descriptor used to discover named
// files. Callers keep it open until the surrounding snapshot is complete so
// that files created in an already traversed directory cannot disappear from
// the integrity contract.
type PathDiscovery struct {
	root        *Directory
	budget      *SnapshotBudget
	directories []discoveredDirectory
	paths       []discoveredPath
}

type discoveredDirectory struct {
	directory *Directory
	entries   []string
	owned     bool
}

type discoveredPath struct {
	directory *Directory
	name      string
	path      string
	digest    []byte
}

// DiscoverFiles finds files with one of the literal names below the pinned
// directory. It never follows symlinks and does not enter excluded directory
// paths or Git administrative directories.
func (d *Directory) DiscoverFiles(names, excludedPaths []string) (*PathDiscovery, error) {
	return d.discoverFiles(names, excludedPaths, nil)
}

func (d *Directory) DiscoverFilesWithBudget(names, excludedPaths []string, budget *SnapshotBudget) (*PathDiscovery, error) {
	return d.discoverFilesWithBudget(names, excludedPaths, budget, nil)
}

func (d *Directory) discoverFiles(names, excludedPaths []string, observe discoveryObserver) (*PathDiscovery, error) {
	return d.discoverFilesWithBudget(names, excludedPaths, nil, observe)
}

func (d *Directory) discoverFilesWithBudget(names, excludedPaths []string, budget *SnapshotBudget, observe discoveryObserver) (*PathDiscovery, error) {
	if d == nil || d.fd < 0 {
		return nil, errors.New("secure directory is closed")
	}
	protected := make(map[string]struct{}, len(names))
	for _, name := range names {
		if err := validateLeaf(name); err != nil {
			return nil, err
		}
		protected[name] = struct{}{}
	}
	if len(protected) == 0 {
		return nil, errors.New("secure file discovery requires at least one name")
	}
	excluded := make(map[string]struct{}, len(excludedPaths))
	for _, relativePath := range excludedPaths {
		if _, err := snapshotPathComponents(relativePath); err != nil {
			return nil, err
		}
		excluded[relativePath] = struct{}{}
	}

	discovery := &PathDiscovery{root: d, budget: budget}
	if err := discovery.walk(d, "", protected, excluded, observe, false); err != nil {
		_ = discovery.Close()
		return nil, err
	}
	sort.Slice(discovery.paths, func(left, right int) bool {
		return discovery.paths[left].path < discovery.paths[right].path
	})
	if err := discovery.Verify(); err != nil {
		_ = discovery.Close()
		return nil, err
	}
	return discovery, nil
}

func (d *PathDiscovery) walk(directory *Directory, relativeDirectory string, protected, excluded map[string]struct{}, observe discoveryObserver, owned bool) error {
	names, err := directoryNames(directory, d.budget)
	if err != nil {
		return err
	}
	if observe != nil {
		observe(discoveryStageDirectoryRead, relativeDirectory)
	}
	sort.Strings(names)
	d.directories = append(d.directories, discoveredDirectory{
		directory: directory,
		entries:   append([]string(nil), names...),
		owned:     owned,
	})
	for _, name := range names {
		if name == "." || name == ".." || strings.ContainsRune(name, '/') {
			return fmt.Errorf("invalid secure directory entry %q", name)
		}
		relativePath := name
		if relativeDirectory != "" {
			relativePath = relativeDirectory + "/" + name
		}
		if _, skip := excluded[relativePath]; skip {
			continue
		}

		var state unix.Stat_t
		if err := unix.Fstatat(directory.fd, name, &state, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return fmt.Errorf("%w while inspecting discovered path %q: %v", ErrChanged, relativePath, err)
		}
		if _, match := protected[name]; match {
			digest, err := directory.HashPathWithBudget(name, d.budget)
			if err != nil {
				return fmt.Errorf("pin discovered path %q: %w", relativePath, err)
			}
			d.paths = append(d.paths, discoveredPath{
				directory: directory,
				name:      name,
				path:      relativePath,
				digest:    digest,
			})
		}
		if state.Mode&unix.S_IFMT != unix.S_IFDIR || name == ".git" {
			continue
		}
		child, err := directory.OpenDir(name)
		if err != nil {
			return fmt.Errorf("open discovered directory %q without following links: %w", relativePath, err)
		}
		tracked := len(d.directories)
		if err := d.walk(child, relativePath, protected, excluded, observe, true); err != nil {
			if len(d.directories) == tracked {
				_ = child.Close()
			}
			return err
		}
	}
	return nil
}

func directoryNames(directory *Directory, budget *SnapshotBudget) ([]string, error) {
	fd, err := unix.Openat(directory.fd, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open pinned directory for discovery: %w", err)
	}
	var opened unix.Stat_t
	if err := unix.Fstat(fd, &opened); err != nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("inspect pinned directory for discovery: %w", err)
	}
	if !sameFileMetadata(stateFromStat(opened), directory.initial) {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("%w before reading directory %s", ErrChanged, directory.path)
	}
	handle := os.NewFile(uintptr(fd), directory.path)
	if handle == nil {
		_ = unix.Close(fd)
		return nil, errors.New("create pinned directory discovery handle")
	}
	names := make([]string, 0)
	var readErr error
	for {
		var batch []string
		batch, readErr = handle.Readdirnames(256)
		for _, name := range batch {
			if err := budget.addEntry(directory.path, name); err != nil {
				_ = handle.Close()
				return nil, err
			}
			names = append(names, name)
		}
		if errors.Is(readErr, io.EOF) {
			readErr = nil
			break
		}
		if readErr != nil {
			break
		}
	}
	closeErr := handle.Close()
	if readErr != nil {
		return nil, fmt.Errorf("read pinned directory entries: %w", readErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close pinned directory discovery handle: %w", closeErr)
	}
	return names, nil
}

// Paths returns the sorted repository-relative names found by the discovery.
func (d *PathDiscovery) Paths() []string {
	if d == nil {
		return nil
	}
	paths := make([]string, 0, len(d.paths))
	for _, record := range d.paths {
		paths = append(paths, record.path)
	}
	return paths
}

// Digests returns the initial no-follow digest for every discovered path.
// Callers use these values rather than re-reading the paths after commands
// that may consume the control files.
func (d *PathDiscovery) Digests() map[string][]byte {
	if d == nil {
		return nil
	}
	digests := make(map[string][]byte, len(d.paths))
	for _, record := range d.paths {
		digests[record.path] = append([]byte(nil), record.digest...)
	}
	return digests
}

// Verify proves that every traversed directory still has the identity and
// metadata it had during discovery.
func (d *PathDiscovery) Verify() error {
	if d == nil || d.root == nil {
		return errors.New("secure path discovery is closed")
	}
	for _, record := range d.paths {
		current, err := record.directory.HashPathWithBudget(record.name, d.budget)
		if err != nil {
			return fmt.Errorf("verify discovered path %q: %w", record.path, err)
		}
		if !bytes.Equal(current, record.digest) {
			return fmt.Errorf("%w while verifying discovered path %q", ErrChanged, record.path)
		}
	}
	for index := len(d.directories) - 1; index >= 0; index-- {
		record := d.directories[index]
		entries, err := directoryNames(record.directory, d.budget)
		if err != nil {
			return err
		}
		sort.Strings(entries)
		if !slices.Equal(entries, record.entries) {
			return fmt.Errorf("%w while verifying directory entries under %s", ErrChanged, record.directory.path)
		}
		if err := record.directory.Verify(); err != nil {
			return err
		}
	}
	return nil
}

// Close releases the child directory descriptors retained by the discovery.
// The caller continues to own the root descriptor.
func (d *PathDiscovery) Close() error {
	if d == nil {
		return nil
	}
	var first error
	for index := len(d.directories) - 1; index >= 0; index-- {
		record := d.directories[index]
		if !record.owned {
			continue
		}
		if err := record.directory.Close(); err != nil && first == nil {
			first = err
		}
	}
	d.directories = nil
	d.paths = nil
	d.root = nil
	return first
}
