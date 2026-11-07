package runner

import "time"

// Cleanup is already one Core-owned operation with its run held inFlight.
// Source verification/deletion must finish under that maintenance context, not
// restart under each short execution-observation timeout. This grants no work
// attempt and remains cancelled by Core.Close.
func cleanupCallTimeout(process Process) time.Duration {
	timeout, err := time.ParseDuration(process.AttemptTimeout)
	if err != nil || timeout <= 0 {
		return 5 * time.Second // Preserve the legacy call bound for absent/invalid data.
	}
	return min(timeout, 5*time.Minute)
}
