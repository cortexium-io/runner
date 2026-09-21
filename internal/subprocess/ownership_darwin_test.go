package subprocess

import (
	"errors"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestProcessSnapshotRetriesOnlyBoundedKernelRaces(t *testing.T) {
	for _, test := range []struct {
		name   string
		errors []error
		calls  int
		want   error
	}{
		{"exec race", []error{unix.EINVAL, nil}, 2, nil},
		{"interrupted read", []error{unix.EINTR, nil}, 2, nil},
		{"persistent invalid snapshot", []error{unix.EINVAL, unix.EINVAL, unix.EINVAL}, 3, unix.EINVAL},
		{"permission", []error{unix.EPERM}, 1, unix.EPERM},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			got, err := processSnapshot("kern.proc.pid", 123, func(name string, ids ...int) ([]unix.KinfoProc, error) {
				if name != "kern.proc.pid" || len(ids) != 1 || ids[0] != 123 {
					t.Fatal("snapshot identity changed")
				}
				err := test.errors[calls]
				calls++
				return []unix.KinfoProc{{}}, err
			})
			if calls != test.calls || !errors.Is(err, test.want) {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
			if err == nil && len(got) != 1 {
				t.Fatal("successful snapshot lost")
			}
			if err != nil && (got != nil || !strings.Contains(err.Error(), "kern.proc.pid(123)")) {
				t.Fatal("failed read treated as absence or lost context")
			}
		})
	}
}
