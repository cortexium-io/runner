// Package workspace adapts the existing config-free Git and filesystem boundary
// to local task identities, including exact-object draft publication.
package workspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/cortexium-io/runner/internal/runner/config"
	"github.com/cortexium-io/runner/internal/runner/core"
	"github.com/cortexium-io/runner/internal/securefs"
	"github.com/cortexium-io/runner/internal/subprocess"
	legacy "github.com/cortexium-io/runner/internal/workspace"
)

type Git struct{ Settings config.Settings }

func profile(ctx context.Context, dir string) (subprocess.PrivilegedGitProfile, error) {
	resolve := func(arg string) (string, error) {
		cmd := exec.CommandContext(ctx, "git", "-c", "core.fsmonitor=false", "-c", "core.hooksPath=/dev/null", "rev-parse", arg)
		cmd.Dir = dir
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_OPTIONAL_LOCKS=0"}
		b, e := cmd.Output()
		if e != nil {
			return "", e
		}
		p := strings.TrimSpace(string(b))
		if !filepath.IsAbs(p) {
			p = filepath.Join(dir, p)
		}
		return filepath.Clean(p), nil
	}
	root, e := resolve("--show-toplevel")
	if e != nil {
		return subprocess.PrivilegedGitProfile{}, e
	}
	if root != dir {
		return subprocess.PrivilegedGitProfile{}, errors.New("repository path must name its checkout root")
	}
	gd, e := resolve("--absolute-git-dir")
	if e != nil {
		return subprocess.PrivilegedGitProfile{}, e
	}
	common, e := resolve("--git-common-dir")
	if e != nil {
		return subprocess.PrivilegedGitProfile{}, e
	}
	for _, p := range []string{dir, gd, common} {
		if e = config.NoSymlinks(p); e != nil {
			return subprocess.PrivilegedGitProfile{}, e
		}
	}
	return subprocess.NewPrivilegedGitProfile(dir, gd, common, filepath.Join(gd, "index"), filepath.Join(common, "objects"))
}
func scalar(ctx context.Context, p subprocess.PrivilegedGitProfile, args ...string) (string, error) {
	r, e := subprocess.RunPrivilegedGit(ctx, nil, p, args, 30*time.Second)
	if e != nil {
		return "", fmt.Errorf("git %s: %w", args[0], e)
	}
	return strings.TrimSpace(r.Stdout), nil
}
func Observe(ctx context.Context, c config.Config) (identity, base string, err error) {
	p, e := profile(ctx, c.Repository.Path)
	if e != nil {
		return "", "", e
	}
	info, e := os.Stat(p.CommonDirectory)
	if e != nil {
		return "", "", e
	}
	stat := info.Sys().(*syscall.Stat_t)
	identity = core.Digest(struct {
		Root, Common  string
		Device, Inode uint64
	}{c.Repository.Path, p.CommonDirectory, uint64(stat.Dev), stat.Ino})
	base, e = scalar(ctx, p, "rev-parse", "--verify", c.Repository.BaseRef+"^{commit}")
	return identity, base, e
}
func (g Git) validate(ctx context.Context) error {
	id, _, e := Observe(ctx, g.Settings.Config)
	if e != nil {
		return e
	}
	if id != g.Settings.RepositoryID {
		return errors.New("repository identity changed")
	}
	return nil
}
func (g Git) request(t core.Task) legacy.Request {
	key := fmt.Sprintf("%s-v%d", t.ID, t.Current().Number)
	return legacy.Request{WorkingDir: g.Settings.Config.Repository.Path, WorktreeRoot: g.Settings.Config.WorktreeDir, WorkID: key, ItemID: t.ID, DelegatedContentDigest: t.Current().Digest, Repository: g.Settings.RepositoryID, BranchName: "runner-next/" + key, BaseRef: "refs/runner-next/" + key}
}
func (g Git) Prepare(ctx context.Context, t core.Task) (legacy.Metadata, error) {
	if e := g.validate(ctx); e != nil {
		return legacy.Metadata{}, e
	}
	if t.Approval == nil {
		return legacy.Metadata{}, errors.New("workspace requires approval")
	}
	r := g.request(t)
	p, e := profile(ctx, r.WorkingDir)
	if e != nil {
		return legacy.Metadata{}, e
	}
	existing, readErr := scalar(ctx, p, "rev-parse", "--verify", r.BaseRef)
	if readErr == nil && existing != t.Approval.Base {
		return legacy.Metadata{}, errors.New("pinned base ref changed")
	}
	if readErr != nil {
		if _, e = scalar(ctx, p, "update-ref", r.BaseRef, t.Approval.Base, strings.Repeat("0", len(t.Approval.Base))); e != nil {
			return legacy.Metadata{}, e
		}
	}
	m, e := legacy.NewGitProvider(nil).Prepare(ctx, r)
	if e == nil && m.BaseRevision != t.Approval.Base {
		return m, errors.New("workspace base differs from approval")
	}
	return m, e
}
func (g Git) Inspect(ctx context.Context, t core.Task) (legacy.Metadata, error) {
	if e := g.validate(ctx); e != nil {
		return legacy.Metadata{}, e
	}
	provider := legacy.NewGitProvider(nil)
	r := g.request(t)
	identity, e := provider.ValidateRetainedIdentity(ctx, r)
	if e != nil {
		return legacy.Metadata{}, e
	}
	if t.Approval == nil || identity.BaseRevision != t.Approval.Base {
		return legacy.Metadata{}, errors.New("workspace approval binding changed")
	}
	p, e := profile(ctx, r.WorkingDir)
	if e != nil {
		return legacy.Metadata{}, e
	}
	base, e := scalar(ctx, p, "rev-parse", "--verify", r.BaseRef)
	if e != nil || base != t.Approval.Base {
		return legacy.Metadata{}, errors.New("pinned base ref changed")
	}
	return provider.InspectRetainedReview(ctx, r)
}
func Snapshot(ctx context.Context, path string) (legacy.Snapshot, error) {
	return legacy.CaptureCheckoutSnapshotStateWithLimits(ctx, nil, path, 30*time.Second, legacy.DefaultSnapshotLimits())
}
func (g Git) Binding(ctx context.Context, t core.Task) (digest, head string, err error) {
	m, e := g.Inspect(ctx, t)
	if e != nil {
		return "", "", e
	}
	d, e := Digest(m.WorktreePath)
	if e != nil {
		return "", "", e
	}
	s, e := Snapshot(ctx, m.WorktreePath)
	return d, s.Head, e
}
func (g Git) Construct(ctx context.Context, t core.Task, a core.Attempt) (core.Candidate, error) {
	m, e := g.Inspect(ctx, t)
	if e != nil {
		return core.Candidate{}, e
	}
	if e = g.checkCandidateQuality(m.WorktreePath); e != nil {
		return core.Candidate{}, e
	}
	digest, e := Digest(m.WorktreePath)
	if e != nil {
		return core.Candidate{}, e
	}
	if digest != a.SourceDigest {
		return core.Candidate{}, errors.New("saved implementation source changed")
	}
	// Staging and Runner's idempotent commit may already have happened. Source
	// bytes plus the private task/base identity remain stable across both steps.
	candidate, e := legacy.NewGitProvider(nil).ConstructCandidate(ctx, m, "Implement Runner task "+t.ID)
	if e != nil {
		return core.Candidate{}, e
	}
	after, e := Digest(m.WorktreePath)
	if e != nil || after != digest {
		return core.Candidate{}, errors.New("source changed during candidate construction")
	}
	return core.Candidate{Commit: candidate.CommitOID, Tree: candidate.TreeOID, Fingerprint: digest, Workspace: m.WorktreePath}, nil
}
func (g Git) ValidateCandidate(ctx context.Context, t core.Task) error {
	if t.Candidate == nil {
		return errors.New("candidate is missing")
	}
	m, e := g.Inspect(ctx, t)
	if e != nil {
		return e
	}
	if m.WorktreePath != t.Candidate.Workspace {
		return errors.New("candidate workspace changed")
	}
	if e = g.checkCandidateQuality(m.WorktreePath); e != nil {
		return e
	}
	d, e := Digest(m.WorktreePath)
	if e != nil {
		return e
	}
	s, e := Snapshot(ctx, m.WorktreePath)
	if e != nil {
		return e
	}
	if d != t.Candidate.Fingerprint || s.Head != t.Candidate.Commit || s.Tree != t.Candidate.Tree || !s.Clean {
		return errors.New("candidate bytes or Git identity changed")
	}
	return nil
}
func (g Git) Review(ctx context.Context, t core.Task) (legacy.ReviewWorkspace, error) {
	if e := g.ValidateCandidate(ctx, t); e != nil {
		return legacy.ReviewWorkspace{}, e
	}
	m, e := g.Inspect(ctx, t)
	if e != nil {
		return legacy.ReviewWorkspace{}, e
	}
	return legacy.NewGitProvider(nil).PrepareReviewWorkspace(ctx, m, legacy.Candidate{CommitOID: t.Candidate.Commit, TreeOID: t.Candidate.Tree})
}
func (g Git) ReadRoots(ctx context.Context, dir string) ([]string, error) {
	p, e := profile(ctx, dir)
	if e != nil {
		return nil, e
	}
	return []string{dir, p.GitDirectory, p.CommonDirectory}, nil
}

// Manifest excludes Git administration but includes ignored files. Integrity
// must cover bytes a reviewer can observe, not just a convenient Git diff.
func Manifest(root string) (map[string]string, error) {
	out := map[string]string{}
	var total int64
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, e := filepath.Rel(root, path)
		if e != nil {
			return e
		}
		if rel == "." {
			return nil
		}
		if rel == ".git" {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if len(out) > 100000 {
			return errors.New("workspace has too many entries")
		}
		if d.IsDir() {
			out[rel+"/"] = "directory"
			return nil
		}
		info, e := d.Info()
		if e != nil {
			return e
		}
		if info.Mode()&os.ModeSymlink != 0 {
			target, e := os.Readlink(path)
			if e != nil {
				return e
			}
			if !config.Within(filepath.Clean(filepath.Join(filepath.Dir(path), target)), root) || filepath.IsAbs(target) {
				return errors.New("source symlink escapes workspace")
			}
			out[rel] = "link:" + target
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("unsupported source entry: %s", rel)
		}
		b, mode, _, e := securefs.ReadFile(path, 64<<20)
		if e != nil {
			return e
		}
		total += int64(len(b))
		if total > 1<<30 {
			return errors.New("workspace exceeds snapshot limit")
		}
		hash := sha256.Sum256(b)
		out[rel] = fmt.Sprintf("%o:%s", mode.Perm(), hex.EncodeToString(hash[:]))
		return nil
	})
	return out, err
}
func Digest(root string) (string, error) {
	m, e := Manifest(root)
	if e != nil {
		return "", e
	}
	return core.Digest(m), nil
}

// Copy is a disposable source tree, with no shared Git administration. The
// original source manifest is checked after verification. New generated files
// are permitted, while changed/deleted source or new source-like files fail.
func Copy(source, parent string) (string, CopyState, error) {
	before, e := Manifest(source)
	if e != nil {
		return "", CopyState{}, e
	}
	dir, e := os.MkdirTemp(parent, "copy-")
	if e != nil {
		return "", CopyState{}, e
	}
	dir, e = filepath.EvalSymlinks(dir)
	if e != nil {
		return "", CopyState{}, e
	}
	for name := range before {
		if strings.HasSuffix(name, "/") {
			if e = os.MkdirAll(filepath.Join(dir, name), 0700); e != nil {
				return dir, CopyState{}, e
			}
		}
	}
	for name, digest := range before {
		if strings.HasSuffix(name, "/") {
			continue
		}
		dest := filepath.Join(dir, name)
		if e = os.MkdirAll(filepath.Dir(dest), 0700); e != nil {
			return dir, CopyState{}, e
		}
		if strings.HasPrefix(digest, "link:") {
			e = os.Symlink(strings.TrimPrefix(digest, "link:"), dest)
		} else {
			var b []byte
			var mode os.FileMode
			b, mode, _, e = securefs.ReadFile(filepath.Join(source, name), 64<<20)
			if e == nil {
				e = securefs.WriteFileExclusive(dest, b, mode.Perm())
			}
		}
		if e != nil {
			return dir, CopyState{}, e
		}
	}
	after, e := Manifest(dir)
	if e != nil {
		return dir, CopyState{}, e
	}
	if core.Digest(before) != core.Digest(after) {
		return dir, CopyState{}, errors.New("source changed while copying")
	}
	state, e := PrepareCopy(context.Background(), dir)
	return dir, state, e
}
func VerifyCopy(dir string, before CopyState) error {
	if err := verifyCopyGit(dir, before); err != nil {
		return err
	}
	after, e := Manifest(dir)
	if e != nil {
		return e
	}
	for name, digest := range before.Source {
		if after[name] != digest {
			return fmt.Errorf("verification changed source: %s", name)
		}
	}
	for name := range after {
		if _, ok := before.Source[name]; ok || strings.HasSuffix(name, "/") || strings.HasPrefix(name, ".runner-output/") {
			continue
		}
		base := filepath.Base(name)
		ext := filepath.Ext(name)
		switch ext {
		case ".go", ".js", ".ts", ".tsx", ".jsx", ".py", ".rs", ".c", ".h", ".java", ".json", ".yaml", ".yml", ".toml", ".mod", ".sum", ".lock":
			return fmt.Errorf("verification added source/manifest: %s", name)
		}
		if base == "Makefile" || strings.Contains(base, "lock") {
			return fmt.Errorf("verification added source/manifest: %s", name)
		}
	}
	return nil
}

// BaselineCopy materializes only committed base content through Git's archive
// format, then uses the same checked copy boundary as focused verification.
func BaselineCopy(ctx context.Context, c config.Config, base string) (string, error) {
	p, e := profile(ctx, c.Repository.Path)
	if e != nil {
		return "", e
	}
	parent, e := os.MkdirTemp("", "runner-baseline-")
	if e != nil {
		return "", e
	}
	parent, e = filepath.EvalSymlinks(parent)
	if e != nil {
		return "", e
	}
	// Archive reads only committed objects, leaving the original checkout and
	// index untouched and avoiding hooks and checkout filters.
	result, e := subprocess.RunPrivilegedGit(ctx, nil, p, []string{"archive", "--format=tar", base}, 30*time.Second)
	if e != nil {
		return parent, e
	}
	if e = extractArchive(parent, []byte(result.Stdout)); e != nil {
		return parent, e
	}
	return parent, nil
}

// ValidateConstruction recognizes only the single commit described by the
// durable construction intent, not arbitrary history added after completion.
func (g Git) ValidateConstruction(ctx context.Context, t core.Task, a core.Attempt) error {
	m, e := g.Inspect(ctx, t)
	if e != nil {
		return e
	}
	p, e := profile(ctx, m.WorktreePath)
	if e != nil {
		return e
	}
	parent, e := scalar(ctx, p, "show", "-s", "--format=%P", "HEAD")
	if e != nil {
		return e
	}
	message, e := scalar(ctx, p, "show", "-s", "--format=%B", "HEAD")
	if e != nil {
		return e
	}
	if parent != a.SourceHead || message != "Implement Runner task "+t.ID {
		return errors.New("HEAD does not match the saved construction intent")
	}
	return nil
}

func (g Git) CheckQuality(ctx context.Context, base string) error {
	p, e := profile(ctx, g.Settings.Config.Repository.Path)
	if e != nil {
		return e
	}
	for name, expected := range g.Settings.Quality.Sources {
		r, e := subprocess.RunPrivilegedGit(ctx, nil, p, []string{"show", base + ":" + name}, 30*time.Second)
		if e != nil {
			return fmt.Errorf("quality source %s unavailable at approved base: %w", name, e)
		}
		sum := sha256.Sum256([]byte(r.Stdout))
		if hex.EncodeToString(sum[:]) != expected {
			return fmt.Errorf("quality source %s changed; apply a quality revision and explicitly adopt it, or use the pinned base", name)
		}
	}
	return nil
}
func (g Git) checkCandidateQuality(root string) error {
	for name, expected := range g.Settings.Quality.Sources {
		b, _, _, e := securefs.ReadFile(filepath.Join(root, name), 1<<20)
		if e != nil {
			return e
		}
		sum := sha256.Sum256(b)
		if hex.EncodeToString(sum[:]) != expected {
			return fmt.Errorf("candidate changed pinned quality source %s", name)
		}
	}
	return nil
}

// CopyState pins source, Git controls and logical inventory independently of
// incidental Git index stat-cache refreshes performed by verification commands.
type CopyState struct {
	Source map[string]string
	Git    map[string]string
	Index  string
}

func PrepareCopy(ctx context.Context, dir string) (CopyState, error) {
	source, e := Manifest(dir)
	if e != nil {
		return CopyState{}, e
	}
	paths := []string{}
	for name := range source {
		if !strings.HasSuffix(name, "/") {
			paths = append(paths, name)
		}
	}
	index, e := legacy.PrepareVerificationIndex(ctx, dir, paths)
	if e != nil {
		return CopyState{}, e
	}
	controls, e := Manifest(filepath.Join(dir, ".git"))
	delete(controls, "index")
	return CopyState{Source: source, Git: controls, Index: index}, e
}
func verifyCopyGit(dir string, before CopyState) error {
	controls, e := Manifest(filepath.Join(dir, ".git"))
	if e != nil {
		return e
	}
	delete(controls, "index")
	if core.Digest(controls) != core.Digest(before.Git) {
		return errors.New("verification changed Git controls")
	}
	index, e := legacy.ReadVerificationIndex(context.Background(), dir)
	if e != nil {
		return e
	}
	if index != before.Index {
		return errors.New("verification changed source inventory")
	}
	return nil
}

func ResolveQuality(ctx context.Context, c config.Config, base string, spec config.QualitySpec) (config.Quality, error) {
	q := config.Quality{Definition: spec.Definition, Checks: spec.Checks, Base: base, Sources: map[string]string{}, Documents: map[string]string{}}
	p, err := profile(ctx, c.Repository.Path)
	if err != nil {
		return q, err
	}
	for _, name := range spec.Sources {
		r, err := subprocess.RunPrivilegedGit(ctx, nil, p, []string{"show", base + ":" + name}, 30*time.Second)
		if err != nil {
			return q, err
		}
		digest := sha256.Sum256([]byte(r.Stdout))
		q.Sources[name] = hex.EncodeToString(digest[:])
		q.Documents[name] = r.Stdout
	}
	return q, nil
}
