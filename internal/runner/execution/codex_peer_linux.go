//go:build linux

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
	err = raw.Control(func(fd uintptr) {
		v, e := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
		inner = e
		if e == nil {
			pid = int(v.Pid)
		}
	})
	if err != nil {
		return 0, err
	}
	return pid, inner
}
