package release

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// ArchiveRetention applies only to reproducible raw source archives. Operational
// journals, reports, Git history, compiled artifacts and recovery data are never
// collected by this policy.
type ArchiveRetention struct {
	RawArchives int `json:"raw_archives"`
}

func (p *ArchiveRetention) validate() error {
	if p != nil && (p.RawArchives < 2 || p.RawArchives > 8) {
		return errors.New("retention.raw_archives must retain between two and eight recent source packages")
	}
	return nil
}

type retainedPackage struct {
	Job      Job
	Config   Config
	Manifest packageManifest
	Changed  time.Time
}

// retainedPackages authenticates package ownership through both the private
// operator journal and its captured worker policy. This also admits old policy
// versions after an operator upgrade without trusting a candidate manifest alone.
func (b *Live) retainedPackages() ([]retainedPackage, []Job, error) {
	entries, err := os.ReadDir(b.Config.StateDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	var packages []retainedPackage
	var jobs []Job
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		key := strings.TrimSuffix(entry.Name(), ".json")
		if !safeID.MatchString(key) {
			continue
		}
		data, err := readPackageFile(b.Config.StateDir, entry.Name(), 32<<10)
		if err != nil {
			return nil, nil, err
		}
		var j Job
		if json.Unmarshal(data, &j) != nil || j.Request.Key != key || j.Receipt.Key != key || !safeID.MatchString(j.Request.RunID) {
			return nil, nil, errors.New("retention encountered an unverified operator journal")
		}
		if j.Request.Project != b.Config.Project {
			continue
		}
		jobs = append(jobs, j)
		if j.Request.Action != "qualify" || !j.Done {
			continue
		}
		root := filepath.Join(b.Config.Delivery.Root, j.Request.RunID, key)
		if j.Worker != filepath.Join(root, "work.json") || j.Receipt.Package != filepath.Join(root, "output") {
			return nil, nil, errors.New("retained package is outside its owned attempt")
		}
		for _, dir := range []string{b.Config.Delivery.Root, filepath.Dir(root), root} {
			info, err := os.Lstat(dir)
			if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return nil, nil, errors.New("retained attempt directory ownership is not established")
			}
		}
		data, err = readPackageFile(root, "work.json", 32<<10)
		if err != nil {
			return nil, nil, err
		}
		var work deliveryWork
		if json.Unmarshal(data, &work) != nil || work.Config.Delivery == nil {
			return nil, nil, errors.New("retained package has no captured operator policy")
		}
		canonical, _ := json.Marshal(work.Config)
		if Digest(canonical) != j.Receipt.Policy || work.Config.Project != b.Config.Project || work.Config.Repository != b.Config.Repository || work.Config.StateDir != b.Config.StateDir || work.Config.Delivery.Root != b.Config.Delivery.Root || work.Job.Request.Key != key || work.Job.Request.RunID != j.Request.RunID || work.Job.Receipt.Head != j.Receipt.Head {
			return nil, nil, errors.New("retained package policy or publication binding differs")
		}
		data, err = readPackageFile(j.Receipt.Package, "manifest.json", 8<<20)
		if errors.Is(err, os.ErrNotExist) {
			continue // Qualification may have failed before a package was created.
		}
		if err != nil {
			return nil, nil, err
		}
		var manifest packageManifest
		if json.Unmarshal(data, &manifest) != nil || !manifest.Clean || manifest.Revision != j.Receipt.Head || !strings.HasPrefix(manifest.Identity, "source-sha256:") || len(manifest.Artifacts["source.tar.gz"]) != 64 {
			return nil, nil, errors.New("retained package source identity differs")
		}
		data, err = readPackageFile(j.Receipt.Package, "reproduction.json", 8<<10)
		if errors.Is(err, os.ErrNotExist) {
			continue // Never collect a raw package whose independent proof did not finish.
		}
		if err != nil {
			return nil, nil, err
		}
		var proof struct {
			Status string `json:"status"`
			Commit string `json:"commit"`
			Server string `json:"serverSHA256"`
		}
		if json.Unmarshal(data, &proof) != nil || proof.Status != "passed" || proof.Commit != j.Receipt.Head || proof.Server == "" || proof.Server != j.Receipt.ServerSHA || proof.Server != manifest.Artifacts["context/server"] {
			return nil, nil, errors.New("retained package independent reproduction is not established")
		}
		archive, err := packagePath(j.Receipt.Package, "source.tar.gz")
		if errors.Is(err, os.ErrNotExist) {
			continue // An earlier authorized collection already removed this raw copy.
		}
		if err != nil {
			return nil, nil, err
		}
		info, err := os.Stat(archive)
		if err != nil {
			return nil, nil, err
		}
		packages = append(packages, retainedPackage{Job: j, Config: work.Config, Manifest: manifest, Changed: info.ModTime()})
	}
	slices.SortFunc(packages, func(a, c retainedPackage) int {
		if n := c.Changed.Compare(a.Changed); n != 0 {
			return n
		}
		return strings.Compare(a.Job.Request.Key, c.Job.Request.Key)
	})
	return packages, jobs, nil
}

func (b *Live) collectArchives(ctx context.Context) error {
	if b.Config.Delivery.Retention == nil {
		return nil
	}
	lock := filepath.Join(b.Config.StateDir, "archive-retention.lock")
	if info, err := os.Lstat(lock); err == nil && !info.Mode().IsRegular() {
		return errors.New("archive retention lock ownership is not established")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	lease, err := os.OpenFile(lock, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lease.Close()
	if err = lockOperator(lease); errors.Is(err, errOperatorBusy) {
		return nil
	} else if err != nil {
		return err
	}
	defer unlockOperator(lease)
	packages, jobs, err := b.retainedPackages()
	if err != nil {
		return err
	}
	protected := map[string]bool{}
	complete := map[string]bool{}
	var verified []Job
	for _, j := range jobs {
		if j.Request.Action == "verify" && j.Done && !j.Failed {
			complete[j.Request.RunID] = true
			verified = append(verified, j)
		}
	}
	// Successful verify journals form the owning release history. Protect the
	// current and previous release package even when they are no longer recent.
	slices.SortFunc(verified, func(a, c Job) int {
		ai, ae := os.Stat(filepath.Join(b.Config.StateDir, a.Request.Key+".json"))
		ci, ce := os.Stat(filepath.Join(b.Config.StateDir, c.Request.Key+".json"))
		if ae != nil || ce != nil {
			return strings.Compare(a.Request.Key, c.Request.Key)
		}
		return ci.ModTime().Compare(ai.ModTime())
	})
	for _, j := range verified[:min(2, len(verified))] {
		protected[j.Receipt.Package] = true
	}
	for _, j := range jobs {
		if !j.Done || !complete[j.Request.RunID] && (j.Request.Action != "qualify" || !j.Failed && j.Outcome == "") {
			protected[j.Receipt.Package] = true // Active work or an unconsumed release/recovery dependency.
		}
	}
	for i, p := range packages {
		if i < b.Config.Delivery.Retention.RawArchives || protected[p.Job.Receipt.Package] {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := b.removeRawArchive(p); err != nil {
			return err
		}
	}
	return nil
}

func (b *Live) removeRawArchive(p retainedPackage) error {
	root := filepath.Dir(p.Job.Worker)
	lock, err := packagePath(root, "worker.lock")
	if err != nil {
		return err
	}
	lease, err := os.OpenFile(lock, os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lease.Close()
	if err = lockOperator(lease); errors.Is(err, errOperatorBusy) {
		return nil // A settled-looking journal never overrides actual worker ownership.
	} else if err != nil {
		return err
	}
	defer unlockOperator(lease)
	file, err := packagePath(p.Job.Receipt.Package, "source.tar.gz")
	if err != nil {
		return err
	}
	before, err := os.Stat(file)
	if err != nil {
		return err
	}
	hash, err := fileHash(file)
	if err != nil || hash != p.Manifest.Artifacts["source.tar.gz"] {
		return errors.New("raw archive differs from its retained source-bound manifest; preserve it")
	}
	journal := filepath.Join(b.Config.StateDir, "archive-retention")
	if err = os.MkdirAll(journal, 0700); err != nil {
		return err
	}
	if info, err := os.Lstat(journal); err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return errors.New("archive retention receipt directory must be owned and private")
	}
	receipt := struct {
		Key      string `json:"key"`
		Archive  string `json:"archive"`
		SHA256   string `json:"sha256"`
		Bytes    int64  `json:"bytes"`
		Complete bool   `json:"complete"`
	}{p.Job.Request.Key, file, hash, before.Size(), false}
	path := filepath.Join(journal, p.Job.Request.Key+".json")
	if err = writeJSON(path, receipt); err != nil {
		return err
	}
	if _, err = packagePath(p.Job.Receipt.Package, "source.tar.gz"); err != nil {
		return err
	}
	current, err := os.Lstat(file)
	if err != nil || !current.Mode().IsRegular() || !os.SameFile(before, current) || before.Size() != current.Size() || !before.ModTime().Equal(current.ModTime()) {
		return errors.New("raw archive changed while retention was being prepared; preserve it")
	}
	if err = os.Remove(file); err != nil {
		return fmt.Errorf("owned raw archive retention: %w", err)
	}
	receipt.Complete = true
	return writeJSON(path, receipt)
}
