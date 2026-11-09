//go:build darwin

package execution

import (
	"golang.org/x/sys/unix"
	"net"
)

func nativePeerPID(c *net.UnixConn) (int, error) {
	raw, err := c.SyscallConn()
	if err != nil {
		return 0, err
	}
	var pid int
	var inner error
	err = raw.Control(func(fd uintptr) { pid, inner = unix.GetsockoptInt(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERPID) })
	if err != nil {
		return 0, err
	}
	return pid, inner
}
