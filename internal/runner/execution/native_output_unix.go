//go:build darwin || linux

package execution

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

func nativeOutputPath(control string) string { return filepath.Join(control, "output.fifo") }

func createNativeOutput(control string) (*nativeOutputIdentity, error) {
	path := nativeOutputPath(control)
	if err := unix.Mkfifo(path, 0600); err != nil {
		return nil, err
	}
	var stat unix.Stat_t
	if err := unix.Lstat(path, &stat); err != nil {
		return nil, err
	}
	return &nativeOutputIdentity{Device: uint64(stat.Dev), Inode: stat.Ino}, nil
}

func openNativeOutputRead(control string, expected *nativeOutputIdentity) (int, error) {
	if expected == nil {
		return -1, errors.New("native output has no recoverable transport identity")
	}
	fd, err := unix.Open(nativeOutputPath(control), unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFIFO || uint64(stat.Dev) != expected.Device || stat.Ino != expected.Inode || stat.Uid != uint32(os.Getuid()) || stat.Mode&0077 != 0 {
		unix.Close(fd)
		return -1, errors.New("native output transport identity or ownership changed")
	}
	return fd, nil
}

// Poll the nonblocking FIFO directly: EOF is a kernel read result, rather
// than a runtime poller notification whose FIFO hangup behavior varies by host.
type nativeFIFOReader struct {
	mu     sync.Mutex
	fd     int
	closed chan struct{}
}

func (r *nativeFIFOReader) Read(buffer []byte) (int, error) {
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		r.mu.Lock()
		if r.fd < 0 {
			r.mu.Unlock()
			return 0, os.ErrClosed
		}
		n, err := unix.Read(r.fd, buffer)
		r.mu.Unlock()
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EWOULDBLOCK) {
			select {
			case <-r.closed:
				return 0, os.ErrClosed
			case <-ticker.C:
				continue
			}
		}
		if err != nil {
			return 0, err
		}
		if n == 0 {
			return 0, io.EOF
		}
		return n, nil
	}
}

func (r *nativeFIFOReader) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fd < 0 {
		return nil
	}
	err := unix.Close(r.fd)
	r.fd = -1
	close(r.closed)
	return err
}

func openNativeOutput(control string, expected *nativeOutputIdentity) (io.ReadCloser, *os.File, error) {
	fd, err := openNativeOutputRead(control, expected)
	if err != nil {
		return nil, nil, err
	}
	read := &nativeFIFOReader{fd: fd, closed: make(chan struct{})}
	writeFD, err := unix.Open(nativeOutputPath(control), unix.O_WRONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		read.Close()
		return nil, nil, err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(writeFD, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFIFO || uint64(stat.Dev) != expected.Device || stat.Ino != expected.Inode {
		read.Close()
		unix.Close(writeFD)
		return nil, nil, errors.New("native output writer identity changed")
	}
	if err := unix.SetNonblock(writeFD, false); err != nil {
		read.Close()
		unix.Close(writeFD)
		return nil, nil, err
	}
	write := os.NewFile(uintptr(writeFD), nativeOutputPath(control))
	// The only write end is inherited by the launched command. Reopening the
	// pathname for writing is unnecessary and must not mask later EOF.
	if err := write.Chmod(0400); err != nil {
		read.Close()
		write.Close()
		return nil, nil, err
	}
	return read, write, nil
}

// A bounded nonblocking drain observes the same FIFO after its original reader
// has exited. EAGAIN is an open idle writer; only read==0 establishes EOF.
func drainNativeOutput(control string, expected *nativeOutputIdentity) (bool, []byte, error) {
	fd, err := openNativeOutputRead(control, expected)
	if err != nil {
		return false, nil, err
	}
	defer unix.Close(fd)
	buffer := make([]byte, 64<<10)
	used := 0
	for used < len(buffer) {
		n, err := unix.Read(fd, buffer[used:])
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EWOULDBLOCK) {
			return false, buffer[:used], nil
		}
		if err != nil {
			return false, nil, err
		}
		if n == 0 {
			return true, buffer[:used], nil
		}
		used += n
	}
	return false, buffer, nil
}
