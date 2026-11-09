package execution

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
)

func measurementHistoryReports(t *testing.T, data []byte) [][]byte {
	t.Helper()
	var history struct {
		Kind    string
		Entries []struct {
			Sequence, Bytes     int
			SHA256, Measurement string
		}
	}
	if err := json.Unmarshal(data, &history); err != nil || history.Kind != "runner-measure-history-v1" || len(history.Entries) < 1 || len(history.Entries) > 8 {
		t.Fatal("complete ordered measurement history missing", err)
	}
	reports := make([][]byte, len(history.Entries))
	for i, entry := range history.Entries {
		reports[i] = []byte(entry.Measurement)
		sum := sha256.Sum256(reports[i])
		if entry.Sequence != i+1 || entry.Bytes != len(reports[i]) || entry.SHA256 != hex.EncodeToString(sum[:]) {
			t.Fatal("history changed report bytes, order or digest", i)
		}
	}
	return reports
}
