package release

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
)

// Reuse only an independently reproduced package for the same publication and
// unchanged operator inputs. Authorization, qualification and runtime evidence
// are not cached. The caller independently reproduces the executable again.
func (b *Live) reusePackage(ctx context.Context, j *Job) (bool, error) {
	if b.Config.Delivery.Retention == nil {
		return false, nil
	}
	packages, _, err := b.retainedPackages()
	if err != nil {
		return false, err
	}
	for _, p := range packages {
		old := p.Job.Receipt
		if p.Job.Request.Key == j.Request.Key || old.Head != j.Receipt.Head || old.Base != j.Receipt.Base || old.Source != j.Receipt.Source || old.PR != j.Receipt.PR || !samePackagePolicy(p.Config, b.Config) {
			continue
		}
		lock, err := packagePath(filepath.Dir(p.Job.Worker), "worker.lock")
		if err != nil {
			return false, err
		}
		lease, err := os.OpenFile(lock, os.O_RDWR, 0600)
		if err != nil {
			return false, err
		}
		if err = lockOperator(lease); errors.Is(err, errOperatorBusy) {
			lease.Close()
			continue
		} else if err != nil {
			lease.Close()
			return false, err
		}
		if _, err = packagePath(p.Job.Receipt.Package, "source.tar.gz"); errors.Is(err, os.ErrNotExist) {
			unlockOperator(lease)
			lease.Close()
			continue // A collector may have won the lease before this copy.
		} else if err != nil {
			unlockOperator(lease)
			lease.Close()
			return false, err
		}
		err = copyPackage(ctx, p, j.Receipt.Package)
		unlockOperator(lease)
		lease.Close()
		if err != nil {
			return false, err
		}
		return true, nil
	}
	return false, nil
}

func samePackagePolicy(a, b Config) bool {
	// Retention changes disposal, not build inputs. Every other policy input,
	// including installed runtimes, is compared conservatively.
	ac, bc := *a.Delivery, *b.Delivery
	ac.Retention, bc.Retention = nil, nil
	a.Delivery, b.Delivery = &ac, &bc
	// Advance's receipt-derived publication/package fields do not change build
	// inputs. The exact candidate and publication were compared by the caller.
	a, b = staticDeliveryPolicy(a), staticDeliveryPolicy(b)
	aj, _ := json.Marshal(a)
	bj, _ := json.Marshal(b)
	return string(aj) == string(bj)
}

func copyPackage(ctx context.Context, p retainedPackage, output string) error {
	if err := os.Mkdir(output, 0700); err != nil {
		return err // Never merge reused bytes into a pre-existing output tree.
	}
	names := make([]string, 0, len(p.Manifest.Artifacts))
	for name := range p.Manifest.Artifacts {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return err
		}
		from, err := packagePath(p.Job.Receipt.Package, name)
		if err != nil {
			return err
		}
		if err = copyPackageArtifact(from, filepath.Join(output, name), p.Manifest.Artifacts[name]); err != nil {
			return err
		}
	}
	manifest, err := readPackageFile(p.Job.Receipt.Package, "manifest.json", 8<<20)
	if err != nil {
		return err
	}
	if err = writeExclusive(filepath.Join(output, "manifest.json"), manifest); err != nil {
		return err
	}
	return writeJSON(filepath.Join(output, "package-reuse.json"), map[string]string{
		"origin_key":      p.Job.Request.Key,
		"origin_run":      p.Job.Request.RunID,
		"head":            p.Job.Receipt.Head,
		"manifest_sha256": Digest(manifest),
	})
}

func copyPackageArtifact(from, to, want string) error {
	source, err := os.Open(from)
	if err != nil {
		return err
	}
	defer source.Close()
	info, err := source.Stat()
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(to), 0700); err != nil {
		return err
	}
	target, err := os.OpenFile(to, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
	if err != nil {
		return err
	}
	defer target.Close()
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(target, hash), io.LimitReader(source, (4<<30)+1))
	if err != nil {
		return err
	}
	if n > 4<<30 || hex.EncodeToString(hash.Sum(nil)) != want {
		return errors.New("reused artifact differs from its source-bound manifest")
	}
	return target.Sync()
}
