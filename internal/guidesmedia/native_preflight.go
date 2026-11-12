package guidesmedia

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type NativeProcess struct {
	PID        int    `json:"pid"`
	Parent     int    `json:"parent"`
	Executable string `json:"executable"`
}

type NativePreflight struct {
	At               time.Time       `json:"at"`
	ManifestSHA256   string          `json:"manifest_sha256"`
	Drivers          []NativeProcess `json:"existing_driver_processes"`
	Eligible         bool            `json:"eligible"`
	Reason           string          `json:"reason"`
	Authorization    string          `json:"automation_authorization"`
	SessionAttempted bool            `json:"session_attempted"`
	DeviceCreated    bool            `json:"device_created"`
}

// ReadNativePreflight performs only a fixed process inventory. Driver presence
// does not establish an active WebDriver session; it prevents us establishing
// exclusive custody. It never probes endpoints, browsers, prefs or simulators.
// Absence is also not authorization or producer-preservation qualification.
func ReadNativePreflight(ctx context.Context) (NativePreflight, error) {
	var report NativePreflight
	cmd := exec.CommandContext(ctx, "/bin/ps", "-axo", "pid=,ppid=,ucomm=")
	var output limitedInventory
	cmd.Stdout = &output
	if err := cmd.Run(); err != nil {
		return report, err
	}
	drivers, err := NativeDrivers(output.Bytes())
	if err != nil {
		return report, err
	}
	report = NativePreflight{
		At:             time.Now().UTC(),
		ManifestSHA256: ManifestSHA256,
		Drivers:        drivers,
		Authorization:  "unverified",
		Reason:         "native producer preservation, exclusive session custody and cancellation/restart cleanup are not qualified",
	}
	if len(drivers) > 0 {
		report.Reason = "pre-existing Safari/WebDriver/Appium/XCUITest driver custody is unresolved; no session or device attempt is permitted"
	}
	return report, nil
}

type limitedInventory struct{ bytes.Buffer }

func (b *limitedInventory) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 128<<10 {
		return 0, errors.New("native process inventory exceeds bound")
	}
	return b.Buffer.Write(p)
}

func NativeDrivers(data []byte) ([]NativeProcess, error) {
	if len(data) > 128<<10 {
		return nil, errors.New("native process inventory exceeds bound")
	}
	drivers := []NativeProcess{}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			return nil, errors.New("native inventory has malformed process identity")
		}
		pid, e1 := strconv.Atoi(fields[0])
		parent, e2 := strconv.Atoi(fields[1])
		if e1 != nil || e2 != nil || pid < 1 || parent < 0 {
			return nil, errors.New("native inventory has invalid process identity")
		}
		executable := strings.Join(fields[2:], " ")
		name := strings.ToLower(filepath.Base(executable))
		if name == "safaridriver" || name == "appium" || strings.Contains(name, "webdriveragent") || strings.Contains(name, "xctest") {
			drivers = append(drivers, NativeProcess{PID: pid, Parent: parent, Executable: executable})
		}
	}
	return drivers, nil
}
