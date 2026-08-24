//go:build darwin || linux

package securefs

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

const (
	snapshotStageAncestorOpened  = "ancestor-opened"
	snapshotStageMissingObserved = "missing-observed"
	snapshotStageRegularOpened   = "regular-opened"
	snapshotStageRegularRead     = "regular-read"
	snapshotStageSymlinkRead     = "symlink-read"
)

type snapshotObserver func(stage string)

type snapshotDirectory struct {
	parentFD int
	name     string
	fd       int
	initial  FileState
}

// HashPath hashes one repository-relative filesystem object without ever
// resolving a path component outside the pinned directory descriptor.
func (d *Directory) HashPath(relativePath string) ([]byte, error) {
	return d.hashPath(relativePath, nil)
}

func (d *Directory) HashPathWithBudget(relativePath string, budget *SnapshotBudget) ([]byte, error) {
	return d.hashPathWithBudget(relativePath, budget, nil)
}

func (d *Directory) hashPath(relativePath string, observe snapshotObserver) ([]byte, error) {
	return d.hashPathWithBudget(relativePath, nil, observe)
}

func (d *Directory) hashPathWithBudget(relativePath string, budget *SnapshotBudget, observe snapshotObserver) ([]byte, error) {
	components, err := snapshotPathComponents(relativePath)
	if err != nil {
		return nil, err
	}
	budgetPath := filepath.Join(d.path, filepath.FromSlash(relativePath))
	if err := budget.AddEntry(budgetPath); err != nil {
		return nil, err
	}
	if err := d.Verify(); err != nil {
		return nil, err
	}

	parentFD := d.fd
	directories := make([]snapshotDirectory, 0, len(components)-1)
	defer func() {
		for index := len(directories) - 1; index >= 0; index-- {
			_ = unix.Close(directories[index].fd)
		}
	}()

	for _, component := range components[:len(components)-1] {
		var before unix.Stat_t
		if err := unix.Fstatat(parentFD, component, &before, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			if errors.Is(err, unix.ENOENT) {
				return d.hashMissingPath(parentFD, component, directories, observe)
			}
			return nil, err
		}
		if before.Mode&unix.S_IFMT != unix.S_IFDIR {
			return nil, fmt.Errorf("snapshot path ancestor %q is not a no-follow directory", component)
		}
		fd, err := unix.Openat(parentFD, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return nil, err
		}
		var opened unix.Stat_t
		if err := unix.Fstat(fd, &opened); err != nil {
			_ = unix.Close(fd)
			return nil, err
		}
		if !sameFileMetadata(stateFromStat(opened), stateFromStat(before)) {
			_ = unix.Close(fd)
			return nil, fmt.Errorf("%w while opening snapshot ancestor %q", ErrChanged, component)
		}
		directories = append(directories, snapshotDirectory{
			parentFD: parentFD,
			name:     component,
			fd:       fd,
			initial:  stateFromStat(opened),
		})
		parentFD = fd
		callSnapshotObserver(observe, snapshotStageAncestorOpened)
	}

	leaf := components[len(components)-1]
	var before unix.Stat_t
	if err := unix.Fstatat(parentFD, leaf, &before, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return d.hashMissingPath(parentFD, leaf, directories, observe)
		}
		return nil, err
	}

	var digest []byte
	switch before.Mode & unix.S_IFMT {
	case unix.S_IFREG:
		digest, err = hashRegularAt(parentFD, leaf, budgetPath, before, budget, observe)
	case unix.S_IFLNK:
		digest, err = hashSymlinkAt(parentFD, leaf, budgetPath, before, budget, observe)
	case unix.S_IFDIR:
		digest, err = hashDirectoryAt(parentFD, leaf, before)
	default:
		return nil, fmt.Errorf("snapshot path %q has unsupported file type %06o", relativePath, before.Mode&unix.S_IFMT)
	}
	if err != nil {
		return nil, err
	}
	if err := verifySnapshotDirectories(d, directories); err != nil {
		return nil, err
	}
	return digest, nil
}

func hashDirectoryAt(parentFD int, name string, before unix.Stat_t) ([]byte, error) {
	fd, err := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	defer unix.Close(fd)
	var opened unix.Stat_t
	if err := unix.Fstat(fd, &opened); err != nil {
		return nil, err
	}
	initial := stateFromStat(opened)
	if !sameFileMetadata(initial, stateFromStat(before)) {
		return nil, fmt.Errorf("%w while opening snapshot directory %q", ErrChanged, name)
	}
	if err := verifyOpenedAndNamedObject(fd, parentFD, name, initial); err != nil {
		return nil, err
	}
	digest := sha256.New()
	writeSecureSnapshotPart(digest, "type", []byte("directory"))
	writeSecureSnapshotPart(digest, "mode", uint32Bytes(initial.mode))
	writeSecureSnapshotPart(digest, "device", uint64Bytes(initial.dev))
	writeSecureSnapshotPart(digest, "inode", uint64Bytes(initial.ino))
	return digest.Sum(nil), nil
}

func snapshotPathComponents(relativePath string) ([]string, error) {
	if relativePath == "" || strings.HasPrefix(relativePath, "/") || strings.ContainsRune(relativePath, '\x00') {
		return nil, fmt.Errorf("invalid snapshot path %q", relativePath)
	}
	components := strings.Split(relativePath, "/")
	for _, component := range components {
		if component == "" || component == "." || component == ".." {
			return nil, fmt.Errorf("invalid snapshot path %q", relativePath)
		}
	}
	return components, nil
}

func (d *Directory) hashMissingPath(parentFD int, name string, directories []snapshotDirectory, observe snapshotObserver) ([]byte, error) {
	callSnapshotObserver(observe, snapshotStageMissingObserved)
	var appeared unix.Stat_t
	if err := unix.Fstatat(parentFD, name, &appeared, unix.AT_SYMLINK_NOFOLLOW); !errors.Is(err, unix.ENOENT) {
		if err == nil {
			return nil, fmt.Errorf("%w: missing snapshot path component %q appeared", ErrChanged, name)
		}
		return nil, fmt.Errorf("verify missing snapshot path component %q: %w", name, err)
	}
	if err := verifySnapshotDirectories(d, directories); err != nil {
		return nil, err
	}
	digest := sha256.New()
	writeSecureSnapshotPart(digest, "type", []byte("missing"))
	return digest.Sum(nil), nil
}

func hashRegularAt(parentFD int, name, budgetPath string, before unix.Stat_t, budget *SnapshotBudget, observe snapshotObserver) ([]byte, error) {
	fd, err := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), name)
	if file == nil {
		_ = unix.Close(fd)
		return nil, errors.New("create pinned snapshot file handle")
	}
	defer file.Close()

	var opened unix.Stat_t
	if err := unix.Fstat(fd, &opened); err != nil {
		return nil, err
	}
	initial := stateFromStat(opened)
	if opened.Mode&unix.S_IFMT != unix.S_IFREG || !sameFileMetadata(initial, stateFromStat(before)) {
		return nil, fmt.Errorf("%w while opening snapshot file %q", ErrChanged, name)
	}
	callSnapshotObserver(observe, snapshotStageRegularOpened)

	allowance, err := budget.payloadAllowance(budgetPath, opened.Size)
	if err != nil {
		return nil, err
	}
	contentDigest := sha256.New()
	read, err := io.Copy(contentDigest, io.LimitReader(file, allowance+1))
	if err != nil {
		return nil, err
	}
	if read > allowance {
		return nil, budget.overflowError(budgetPath, read)
	}
	if err := budget.chargePayload(budgetPath, read); err != nil {
		return nil, err
	}
	callSnapshotObserver(observe, snapshotStageRegularRead)
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	verifiedContent := sha256.New()
	verifiedRead, err := io.Copy(verifiedContent, io.LimitReader(file, allowance+1))
	if err != nil {
		return nil, err
	}
	if verifiedRead > allowance {
		return nil, budget.overflowError(budgetPath, verifiedRead)
	}
	if err := budget.chargePayload(budgetPath, verifiedRead); err != nil {
		return nil, err
	}
	if !bytes.Equal(contentDigest.Sum(nil), verifiedContent.Sum(nil)) {
		return nil, fmt.Errorf("%w while reading snapshot file %q", ErrChanged, name)
	}
	if err := verifyOpenedAndNamedObject(fd, parentFD, name, initial); err != nil {
		return nil, err
	}
	digest := sha256.New()
	writeSecureSnapshotPart(digest, "type", []byte("regular"))
	writeSecureSnapshotPart(digest, "mode", uint32Bytes(initial.mode))
	writeSecureSnapshotPart(digest, "device", uint64Bytes(initial.dev))
	writeSecureSnapshotPart(digest, "inode", uint64Bytes(initial.ino))
	writeSecureSnapshotPart(digest, "content", contentDigest.Sum(nil))
	return digest.Sum(nil), nil
}

func hashSymlinkAt(parentFD int, name, budgetPath string, before unix.Stat_t, budget *SnapshotBudget, observe snapshotObserver) ([]byte, error) {
	initial := stateFromStat(before)
	allowance, err := budget.payloadAllowance(budgetPath, initial.size)
	if err != nil {
		return nil, err
	}
	target, read, err := readlinkAt(parentFD, name, initial.size, allowance)
	if err != nil {
		if read > allowance {
			return nil, budget.overflowError(budgetPath, read)
		}
		return nil, err
	}
	if err := budget.chargePayload(budgetPath, read); err != nil {
		return nil, err
	}
	callSnapshotObserver(observe, snapshotStageSymlinkRead)
	var after unix.Stat_t
	if err := unix.Fstatat(parentFD, name, &after, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return nil, fmt.Errorf("%w while verifying snapshot symlink %q: %v", ErrChanged, name, err)
	}
	if !sameFileMetadata(stateFromStat(after), initial) {
		return nil, fmt.Errorf("%w while reading snapshot symlink %q", ErrChanged, name)
	}
	verifiedTarget, verifiedRead, err := readlinkAt(parentFD, name, after.Size, allowance)
	if err != nil {
		if verifiedRead > allowance {
			return nil, budget.overflowError(budgetPath, verifiedRead)
		}
		return nil, fmt.Errorf("%w while verifying snapshot symlink target %q", ErrChanged, name)
	}
	if verifiedTarget != target {
		return nil, fmt.Errorf("%w while verifying snapshot symlink target %q", ErrChanged, name)
	}
	if err := budget.chargePayload(budgetPath, verifiedRead); err != nil {
		return nil, err
	}
	var final unix.Stat_t
	if err := unix.Fstatat(parentFD, name, &final, unix.AT_SYMLINK_NOFOLLOW); err != nil || !sameFileMetadata(stateFromStat(final), initial) {
		return nil, fmt.Errorf("%w after verifying snapshot symlink target %q", ErrChanged, name)
	}
	digest := sha256.New()
	writeSecureSnapshotPart(digest, "type", []byte("symlink"))
	writeSecureSnapshotPart(digest, "mode", uint32Bytes(initial.mode))
	writeSecureSnapshotPart(digest, "device", uint64Bytes(initial.dev))
	writeSecureSnapshotPart(digest, "inode", uint64Bytes(initial.ino))
	writeSecureSnapshotPart(digest, "target", []byte(target))
	return digest.Sum(nil), nil
}

func readlinkAt(parentFD int, name string, size, allowance int64) (string, int64, error) {
	if size < 0 || size >= int64(math.MaxInt) {
		return "", 0, errors.New("invalid snapshot symlink size")
	}
	bufferSize := int(size) + 1
	if bufferSize < 256 {
		bufferSize = 256
	}
	maximum := allowance + 1
	if maximum <= 0 || maximum > int64(math.MaxInt) {
		return "", 0, errors.New("invalid snapshot symlink allowance")
	}
	if int64(bufferSize) > maximum {
		bufferSize = int(maximum)
	}
	for {
		buffer := make([]byte, bufferSize)
		count, err := unix.Readlinkat(parentFD, name, buffer)
		if err != nil {
			return "", 0, err
		}
		if count < len(buffer) {
			return string(buffer[:count]), int64(count), nil
		}
		if int64(bufferSize) == maximum {
			return "", int64(count), fmt.Errorf("snapshot symlink %q exceeded payload allowance of %d bytes", name, allowance)
		}
		next := bufferSize * 2
		if int64(next) > maximum {
			next = int(maximum)
		}
		bufferSize = next
	}
}

func verifyOpenedAndNamedObject(fd, parentFD int, name string, initial FileState) error {
	var opened, named unix.Stat_t
	if err := unix.Fstat(fd, &opened); err != nil {
		return err
	}
	if err := unix.Fstatat(parentFD, name, &named, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return fmt.Errorf("%w while verifying snapshot object %q: %v", ErrChanged, name, err)
	}
	if !sameFileMetadata(stateFromStat(opened), initial) || !sameFileMetadata(stateFromStat(named), initial) {
		return fmt.Errorf("%w while reading snapshot object %q", ErrChanged, name)
	}
	return nil
}

func verifySnapshotDirectories(root *Directory, directories []snapshotDirectory) error {
	for index := len(directories) - 1; index >= 0; index-- {
		directory := directories[index]
		var opened, named unix.Stat_t
		if err := unix.Fstat(directory.fd, &opened); err != nil {
			return err
		}
		if err := unix.Fstatat(directory.parentFD, directory.name, &named, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return fmt.Errorf("%w while verifying snapshot ancestor %q: %v", ErrChanged, directory.name, err)
		}
		if !sameFileMetadata(stateFromStat(opened), directory.initial) || !sameFileMetadata(stateFromStat(named), directory.initial) {
			return fmt.Errorf("%w while traversing snapshot ancestor %q", ErrChanged, directory.name)
		}
	}
	return root.Verify()
}

func callSnapshotObserver(observe snapshotObserver, stage string) {
	if observe != nil {
		observe(stage)
	}
}

func writeSecureSnapshotPart(destination io.Writer, label string, value []byte) {
	_ = binary.Write(destination, binary.BigEndian, uint64(len(label)))
	_, _ = io.WriteString(destination, label)
	_ = binary.Write(destination, binary.BigEndian, uint64(len(value)))
	_, _ = destination.Write(value)
}

func uint32Bytes(value uint32) []byte {
	encoded := make([]byte, 4)
	binary.BigEndian.PutUint32(encoded, value)
	return encoded
}

func uint64Bytes(value uint64) []byte {
	encoded := make([]byte, 8)
	binary.BigEndian.PutUint64(encoded, value)
	return encoded
}
