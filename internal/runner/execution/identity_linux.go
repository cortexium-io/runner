package execution

import (
	"fmt"
	"os"
	"strings"
)

func processIdentity(pid int) (string, error) {
	b, e := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if e != nil {
		return "", e
	}
	fields := strings.Fields(string(b[strings.LastIndex(string(b), ")")+1:]))
	if len(fields) < 20 {
		return "", fmt.Errorf("invalid process identity")
	}
	boot, e := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if e != nil {
		return "", e
	}
	return strings.TrimSpace(string(boot)) + ":" + fields[19], nil
}
