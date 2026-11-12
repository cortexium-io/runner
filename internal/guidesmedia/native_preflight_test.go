package guidesmedia

import (
	"encoding/json"
	"os"
	"testing"
)

func TestNativeInventoryDoesNotClaimSessionAuthority(t *testing.T) {
	drivers, err := NativeDrivers([]byte("123 1 /usr/bin/safaridriver\n124 7 /foreign/WebDriverAgentRunner\n125 1 /Applications/Safari.app/Contents/MacOS/Safari\n"))
	if err != nil || len(drivers) != 2 || drivers[0].PID != 123 {
		t.Fatal(drivers, err)
	}
	for _, raw := range []string{"bad 1 /usr/bin/safaridriver", "123 -1 driver", "123 1"} {
		if _, err := NativeDrivers([]byte(raw)); err == nil {
			t.Fatal("unknown custody accepted")
		}
	}
}

func TestGuidesNativeReadOnlyAdmission(t *testing.T) {
	if os.Getenv("RUNNER_GUIDE_NATIVE_PREFLIGHT") != "1" {
		t.Skip("explicit non-mutating native admission observation")
	}
	report, err := ReadNativePreflight(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if report.Eligible || report.SessionAttempted || report.DeviceCreated || report.Authorization != "unverified" {
		t.Fatal("inventory became native permission")
	}
	data, _ := json.MarshalIndent(report, "", "  ")
	t.Log(string(data))
}
