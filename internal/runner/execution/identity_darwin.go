package execution

import (
	"fmt"
	"golang.org/x/sys/unix"
)

func processIdentity(pid int) (string, error) {
	p, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%d:%d:%d", p.Proc.P_pid, p.Proc.P_starttime.Sec, p.Proc.P_starttime.Usec), nil
}
