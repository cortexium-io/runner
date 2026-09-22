package securefs

import (
	"context"
	"os"
)

// RuntimeReadPolicyVersion participates in verification environment identity.
const RuntimeReadPolicyVersion = "external-runtime-read-v1"

// ReadOnlyDirectory exposes only bounded observation of pinned objects. It has
// no write methods or conversion to Directory. The underlying descriptor and
// its permission policy remain private to securefs.
type ReadOnlyDirectory struct{ directory *Directory }

// OpenReadOnlyDir retains OpenDir's strict protected-path permission policy.
func OpenReadOnlyDir(path string) (*ReadOnlyDirectory, error) {
	directory, err := OpenDir(path)
	if err != nil {
		return nil, err
	}
	return &ReadOnlyDirectory{directory}, nil
}

// OpenRuntimeDir observes explicitly selected external runtime installations,
// not authority files. On Darwin it trusts root/euid-owned admin-group (80)
// writable packages. Operators must trust those maintainers and inspect ACL
// grants; POSIX mode checks are not an ACL or hostile-administrator boundary.
// No-follow identity, stable listings/reads and runtime closure remain required.
func OpenRuntimeDir(path string) (*ReadOnlyDirectory, error) {
	directory, err := openExternalRuntimeDir(path)
	if err != nil {
		return nil, err
	}
	return &ReadOnlyDirectory{directory}, nil
}

func (d *ReadOnlyDirectory) Close() error  { return d.directory.Close() }
func (d *ReadOnlyDirectory) Verify() error { return d.directory.Verify() }

func (d *ReadOnlyDirectory) OpenDir(name string) (*ReadOnlyDirectory, error) {
	child, err := d.directory.OpenDir(name)
	if err != nil {
		return nil, err
	}
	return &ReadOnlyDirectory{child}, nil
}

func (d *ReadOnlyDirectory) ReadDirNamesWithBudget(budget *SnapshotBudget) ([]string, error) {
	return d.directory.ReadDirNamesWithBudget(budget)
}

func (d *ReadOnlyDirectory) HashFileContent(ctx context.Context, name string, budget *SnapshotBudget) ([]byte, os.FileMode, error) {
	return d.directory.HashFileContent(ctx, name, budget)
}
