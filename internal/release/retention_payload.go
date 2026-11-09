package release

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// This private intent is outside candidate-writable build output. Its hashes
// authenticate the original ownership proof and the complete versioned target
// set; a partial retirement never authorizes a different manifest or package.
type payloadRetentionIntent struct {
	Version              int    `json:"version"`
	Key                  string `json:"key"`
	RunID                string `json:"run_id"`
	Package              string `json:"package"`
	Head                 string `json:"head"`
	Policy               string `json:"policy_sha256"`
	JournalSHA           string `json:"journal_sha256"`
	WorkerSHA            string `json:"worker_sha256"`
	ManifestSHA          string `json:"manifest_sha256"`
	ReproductionSHA      string `json:"reproduction_sha256"`
	TargetsSHA           string `json:"targets_sha256"`
	RawAuthorized        bool   `json:"raw_authorized"`
	CompiledAuthorized   bool   `json:"compiled_authorized"`
	ArchiveAlreadyAbsent bool   `json:"archive_already_absent"`
	Complete             bool   `json:"complete"`
}

type payloadRetentionTarget struct {
	Name string `json:"name"`
	SHA  string `json:"sha256"`
}

func buildPayloadName(name string) bool {
	switch name {
	case "source.tar.gz", "context/server", "server-native", "importer/daily-import-linux-amd64", "importer/daily-import-native":
		return true
	}
	return strings.HasPrefix(name, "frontend_dist/") || strings.HasPrefix(name, "ssr/")
}

func payloadRetentionTargets(p retainedPackage) ([]payloadRetentionTarget, error) {
	var targets []payloadRetentionTarget
	for name, hash := range p.Manifest.Artifacts {
		decoded, err := hex.DecodeString(hash)
		if err != nil || len(decoded) != sha256.Size || !filepath.IsLocal(name) || filepath.Clean(name) != name || name == "." {
			return nil, errors.New("build payload manifest has an invalid target")
		}
		if !buildPayloadName(name) {
			continue
		}
		targets = append(targets, payloadRetentionTarget{Name: name, SHA: hash})
	}
	slices.SortFunc(targets, func(a, b payloadRetentionTarget) int { return strings.Compare(a.Name, b.Name) })
	return targets, nil
}

func payloadIntentIdentity(p retainedPackage, targets []payloadRetentionTarget) payloadRetentionIntent {
	data, _ := json.Marshal(targets)
	return payloadRetentionIntent{
		Version:         1,
		Key:             p.Job.Request.Key,
		RunID:           p.Job.Request.RunID,
		Package:         p.Job.Receipt.Package,
		Head:            p.Job.Receipt.Head,
		Policy:          p.Job.Receipt.Policy,
		JournalSHA:      p.JournalSHA,
		WorkerSHA:       p.WorkerSHA,
		ManifestSHA:     p.ManifestSHA,
		ReproductionSHA: p.ReproductionSHA,
		TargetsSHA:      Digest(data),
	}
}

func (b *Live) payloadIntentDirectory() string {
	return filepath.Join(b.Config.StateDir, "build-payload-retention")
}

func (b *Live) readPayloadIntent(p retainedPackage, targets []payloadRetentionTarget) (*payloadRetentionIntent, error) {
	dir := b.payloadIntentDirectory()
	info, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0700 {
		return nil, errors.New("build payload retention intent directory is not private and owned")
	}
	data, err := readPackageFile(dir, p.Job.Request.Key+".json", 32<<10)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var intent payloadRetentionIntent
	if json.Unmarshal(data, &intent) != nil || !intent.RawAuthorized && !intent.CompiledAuthorized {
		return nil, errors.New("build payload retention intent is invalid")
	}
	want := payloadIntentIdentity(p, targets)
	want.ArchiveAlreadyAbsent, want.Complete = intent.ArchiveAlreadyAbsent, intent.Complete
	want.RawAuthorized, want.CompiledAuthorized = intent.RawAuthorized, intent.CompiledAuthorized
	if intent != want {
		return nil, errors.New("build payload retention intent differs from its authenticated package")
	}
	return &intent, nil
}

func (b *Live) savePayloadIntent(intent payloadRetentionIntent) error {
	dir := b.payloadIntentDirectory()
	if err := os.Mkdir(dir, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0700 {
		return errors.New("build payload retention intent directory is not private and owned")
	}
	// Persist the intent directory's parent entry too: syncing only the new
	// directory would not make its name durable before payload removal.
	if err = syncRetentionDirectory(b.Config.StateDir); err != nil {
		return err
	}
	return writeJSON(filepath.Join(dir, intent.Key+".json"), intent)
}

func syncRetentionDirectory(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	err = f.Sync()
	return errors.Join(err, f.Close())
}

func (b *Live) verifyPayloadEvidence(p retainedPackage) error {
	root := filepath.Dir(p.Job.Worker)
	if !p.Job.Done || p.Job.Request.Action != "qualify" || root != filepath.Join(b.Config.Delivery.Root, p.Job.Request.RunID, p.Job.Request.Key) || p.Job.Receipt.Package != filepath.Join(root, "output") {
		return errors.New("build payload ownership is not established")
	}
	for _, dir := range []string{b.Config.Delivery.Root, filepath.Dir(root), root, p.Job.Receipt.Package} {
		info, err := os.Lstat(dir)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("build payload directory ownership changed")
		}
	}
	for _, proof := range []struct {
		root, name, hash string
		limit            int64
	}{
		{b.Config.StateDir, p.Job.Request.Key + ".json", p.JournalSHA, 32 << 10},
		{root, "work.json", p.WorkerSHA, 32 << 10},
		{p.Job.Receipt.Package, "manifest.json", p.ManifestSHA, 8 << 20},
		{p.Job.Receipt.Package, "reproduction.json", p.ReproductionSHA, 8 << 10},
	} {
		data, err := readPackageFile(proof.root, proof.name, proof.limit)
		if err != nil || proof.hash == "" || Digest(data) != proof.hash {
			return errors.New("build payload ownership or reproduction proof changed")
		}
	}
	return nil
}

// Check the entire selected trees, including unmanifested leaves, before any
// removal. Unknown files and links are preserved rather than recursively erased.
func payloadTreePreflight(ctx context.Context, root string, targets []payloadRetentionTarget) error {
	known := make(map[string]bool, len(targets))
	for _, target := range targets {
		known[target.Name] = true
	}
	for _, name := range []string{"source.tar.gz", "context/server", "server-native", "importer/daily-import-linux-amd64", "importer/daily-import-native"} {
		if _, err := os.Lstat(filepath.Join(root, name)); err == nil && !known[name] {
			return errors.New("unmanifested build payload must be preserved")
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	for _, name := range []string{"frontend_dist", "ssr"} {
		dir := filepath.Join(root, name)
		if _, err := os.Lstat(dir); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return err
		}
		if err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if err = ctx.Err(); err != nil {
				return err
			}
			if entry.IsDir() {
				return nil
			}
			relative, err := filepath.Rel(root, path)
			if err != nil || !entry.Type().IsRegular() || !known[relative] {
				return errors.New("unmanifested or linked build payload must be preserved")
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}

type payloadContextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r payloadContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

func preflightPayloadFiles(ctx context.Context, p retainedPackage, targets []payloadRetentionTarget, intent *payloadRetentionIntent) (map[string]os.FileInfo, error) {
	if err := payloadTreePreflight(ctx, p.Job.Receipt.Package, targets); err != nil {
		return nil, err
	}
	files := make(map[string]os.FileInfo, len(targets))
	for _, target := range targets {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		path, err := packagePath(p.Job.Receipt.Package, target.Name)
		if errors.Is(err, os.ErrNotExist) {
			if target.Name != "source.tar.gz" && (intent == nil || !intent.CompiledAuthorized) {
				return nil, errors.New("compiled build payload is missing without a retained collection intent")
			}
			continue
		}
		if err != nil {
			return nil, err
		}
		if intent != nil && (intent.Complete || target.Name == "source.tar.gz" && intent.ArchiveAlreadyAbsent) {
			return nil, errors.New("retired build payload reappeared; preserve it")
		}
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		before, statErr := f.Stat()
		hash := sha256.New()
		_, hashErr := io.Copy(hash, payloadContextReader{ctx, f})
		closeErr := f.Close()
		current, currentErr := os.Lstat(path)
		if statErr != nil || hashErr != nil || closeErr != nil || currentErr != nil {
			return nil, errors.Join(statErr, hashErr, closeErr, currentErr)
		}
		if !before.Mode().IsRegular() || !current.Mode().IsRegular() || !os.SameFile(before, current) || before.Size() != current.Size() || !before.ModTime().Equal(current.ModTime()) || hex.EncodeToString(hash.Sum(nil)) != target.SHA {
			return nil, errors.New("build payload differs from its source-bound manifest; preserve it")
		}
		files[target.Name] = current
	}
	return files, nil
}

// The caller holds the package worker lease. Keeping this preflight separate
// makes the durable-before-delete boundary explicit and restart-testable.
func (b *Live) preparePayloadRetention(ctx context.Context, p retainedPackage, removeRaw, removeCompiled bool) (payloadRetentionIntent, error) {
	if !removeRaw && !removeCompiled {
		return payloadRetentionIntent{}, errors.New("build payload retention needs a selected component")
	}
	if err := b.verifyPayloadEvidence(p); err != nil {
		return payloadRetentionIntent{}, err
	}
	targets, err := payloadRetentionTargets(p)
	if err != nil {
		return payloadRetentionIntent{}, err
	}
	intent, err := b.readPayloadIntent(p, targets)
	if err != nil {
		return payloadRetentionIntent{}, err
	}
	files, err := preflightPayloadFiles(ctx, p, targets, intent)
	if err != nil {
		return payloadRetentionIntent{}, err
	}
	next := payloadIntentIdentity(p, targets)
	if intent != nil {
		next = *intent
	}
	// A raw-only intent cannot explain missing compiled bytes. Extending the
	// selected components reaches here only after every newly selected compiled
	// target was present and hashed by the preflight above.
	if !next.RawAuthorized && files["source.tar.gz"] == nil {
		next.ArchiveAlreadyAbsent = true
	}
	next.RawAuthorized = next.RawAuthorized || removeRaw
	next.CompiledAuthorized = next.CompiledAuthorized || removeCompiled
	if intent != nil && next == *intent {
		return next, nil
	}
	return next, b.savePayloadIntent(next)
}

func (b *Live) removeBuildPayloads(ctx context.Context, p retainedPackage, removeRaw, removeCompiled bool) error {
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
		return nil
	} else if err != nil {
		return err
	}
	defer unlockOperator(lease)
	intent, err := b.preparePayloadRetention(ctx, p, removeRaw, removeCompiled)
	if err != nil {
		return err
	}
	targets, err := payloadRetentionTargets(p)
	if err != nil {
		return err
	}
	files, err := preflightPayloadFiles(ctx, p, targets, &intent)
	if err != nil {
		return err
	}
	if err = b.verifyPayloadEvidence(p); err != nil {
		return err
	}
	directories := map[string]bool{}
	remaining := 0
	for _, target := range targets {
		before := files[target.Name]
		if before == nil {
			continue
		}
		if target.Name == "source.tar.gz" && !removeRaw || target.Name != "source.tar.gz" && !removeCompiled {
			remaining++
			continue
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		path, err := packagePath(p.Job.Receipt.Package, target.Name)
		if err != nil {
			return err
		}
		current, err := os.Lstat(path)
		if err != nil || !current.Mode().IsRegular() || !os.SameFile(before, current) || before.Size() != current.Size() || !before.ModTime().Equal(current.ModTime()) {
			return errors.New("build payload changed after retention preflight; preserve it")
		}
		if err = os.Remove(path); err != nil {
			return err
		}
		directories[filepath.Dir(path)] = true
	}
	for dir := range directories {
		if err = syncRetentionDirectory(dir); err != nil {
			return err
		}
	}
	if remaining == 0 && !intent.Complete {
		intent.Complete = true
		return b.savePayloadIntent(intent)
	}
	return nil
}
