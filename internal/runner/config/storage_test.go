package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRetainedRunCountIsOptionalWithoutChangingBytePolicy(t *testing.T) {
	l, dir := fixture(t)
	path := filepath.Join(dir, "instance.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	write(t, path, strings.Replace(string(data), "    max_runs: 20\n", "", 1))
	s, err := l.Load()
	if err != nil || s.Instance.Storage.Database.MaxRuns != 0 {
		t.Fatal("omitted max_runs must disable the count guard", err)
	}
	bytePolicy := s.Instance.Storage
	for _, value := range []string{"128", "0", "-1", "10001"} {
		write(t, path, strings.Replace(string(data), "max_runs: 20", "max_runs: "+value, 1))
		loaded, err := l.Load()
		if value == "-1" || value == "10001" {
			if err == nil {
				t.Fatal("invalid count admitted", value)
			}
			continue
		}
		if err != nil {
			t.Fatal(value, err)
		}
		got := loaded.Instance.Storage
		if value == "128" && got.Database.MaxRuns != 128 || value == "0" && got.Database.MaxRuns != 0 {
			t.Fatal("count setting not preserved", value, got.Database.MaxRuns)
		}
		got.Database.MaxRuns = 0
		if got != bytePolicy {
			t.Fatal("count policy changed byte, record or retention safeguards")
		}
	}
}
