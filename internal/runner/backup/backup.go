// Package backup owns offline, same-host installation snapshots. Task authority
// stays in SQLite; the external guard only detects possible history after backup.
package backup

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/cortexium-io/runner/internal/runner/config"
	"github.com/cortexium-io/runner/internal/runner/core"
	"github.com/cortexium-io/runner/internal/runner/execution"
	"github.com/cortexium-io/runner/internal/runner/store"
	"github.com/cortexium-io/runner/internal/runner/workspace"
	"github.com/cortexium-io/runner/internal/securefs"
)

type Manifest struct {
	Format      int       `json:"format"`
	StoreFormat int       `json:"store_format"`
	Snapshot    string    `json:"snapshot"`
	CreatedAt   time.Time `json:"created_at"`
	Binary      string    `json:"binary_sha256"`
	Host        string    `json:"host"`
	User        int       `json:"uid"`
	Guard       string    `json:"guard_identity"`
	Config      string    `json:"config"`
	State       string    `json:"state"`
	Roots       []string  `json:"roots"`
	Entries     []Entry   `json:"entries"`
}
type envelope struct {
	Manifest Manifest `json:"manifest"`
	Digest   string   `json:"sha256"`
}
type Report struct {
	Snapshot       string   `json:"snapshot"`
	Destinations   []string `json:"destinations"`
	InspectionOnly bool     `json:"inspection_only"`
	Applied        bool     `json:"applied"`
	Blockers       []string `json:"blockers,omitempty"`
	Next           string   `json:"next"`
}

const recovery = "Inspection only: reconcile retained and remote evidence, then initialize fresh authority at distinct paths and authorize new work. This snapshot cannot resume."

func binaryDigest(ctx context.Context) (string, error) {
	path, err := os.Executable()
	if err != nil {
		return "", err
	}
	return copyFile(ctx, path, "", 0)
}
func sortEntries(entries []Entry) {
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Root != entries[j].Root {
			return entries[i].Root < entries[j].Root
		}
		return entries[i].Name < entries[j].Name
	})
}
func mapped(m Manifest, bundle, path string) (string, error) {
	for i, root := range m.Roots {
		if config.Within(path, root) {
			rel, _ := filepath.Rel(root, path)
			return filepath.Join(dataPath(bundle, i), rel), nil
		}
	}
	return "", fmt.Errorf("referenced path is not in backup: %s", path)
}
func roots(paths []string) ([]string, error) {
	sort.Strings(paths)
	var out []string
	for _, p := range paths {
		if !cleanAbsolute(p) {
			return nil, fmt.Errorf("invalid backup source: %s", p)
		}
		if err := config.NoSymlinks(p); err != nil {
			return nil, err
		}
		if _, err := os.Lstat(p); err != nil {
			return nil, fmt.Errorf("missing backup source %s: %w", p, err)
		}
		contained := false
		for _, q := range out {
			if config.Within(p, q) {
				contained = true
				break
			}
		}
		if !contained {
			out = append(out, p)
		}
	}
	return out, nil
}

// references includes retained execution inputs and check output, not installed
// tools or credentials. All recorded artifacts must exist; none are guessed.
func references(v store.Image) []string {
	var paths []string
	add := func(p string) {
		if p != "" {
			paths = append(paths, p)
		}
	}
	checks := func(cs []core.CheckObservation) {
		for _, c := range cs {
			// A prerequisite/launch failure can record a proposed output path
			// without ever producing a file. Completed checks bind actual bytes.
			if c.OutputDigest != "" {
				add(c.Evidence)
				add(c.Directory)
			}
		}
	}
	for _, a := range v.Attempts {
		if a.Process != nil || a.Result != nil {
			add(a.EvidenceDir)
		}
		add(a.ExecutionDir)
		for _, p := range a.ReadRoots {
			add(p)
		}
		checks(a.Checks)
		if a.Candidate != nil {
			add(a.Candidate.Workspace)
		}
	}
	for _, q := range v.Qualities {
		checks(q.Baseline)
	}
	for _, q := range v.Reports {
		checks(q.Quality.Baseline)
	}
	for _, t := range v.Tasks {
		if t.Candidate != nil {
			add(t.Candidate.Workspace)
		}
	}
	for _, p := range v.Publications {
		add(p.Intent.Candidate.Workspace)
	}
	return paths
}
func quiescent(ctx context.Context, v store.Image) error {
	if !v.Drained {
		return errors.New("drain before backup")
	}
	running, err := execution.ObserveCoordinator(filepath.Join(v.Settings.Config.StateDir, "coordinator.lock"))
	if err != nil {
		return err
	}
	if running {
		return errors.New("coordinator still owns installation")
	}
	for _, a := range v.Attempts {
		if a.Occupying {
			return errors.New("resolve occupying attempts before backup")
		}
		if a.Process != nil && !execution.Gone(*a.Process) {
			return fmt.Errorf("attempt %s process ownership is unresolved", a.ID)
		}
	}
	for _, p := range v.Publications {
		if p.Occupying {
			return errors.New("resolve occupying publications before backup")
		}
	}
	for _, t := range v.Tasks {
		for _, w := range t.RefWrites {
			if w.Pending() {
				return errors.New("settle local ref writes before backup")
			}
		}
	}
	// Baseline commands record their launch separately, including a crash before
	// their report was committed. Never assume an orphaned process has stopped.
	path := filepath.Join(v.Settings.Config.StateDir, "quality")
	err = filepath.WalkDir(path, func(path string, d os.DirEntry, walkErr error) error {
		if os.IsNotExist(walkErr) && path == filepath.Join(v.Settings.Config.StateDir, "quality") {
			return nil
		}
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), "-process.json") {
			return nil
		}
		b, _, _, err := securefs.ReadFile(path, 4096)
		if err != nil {
			return err
		}
		var p core.Process
		if err = config.Decode(b, &p); err != nil {
			return err
		}
		if !execution.Gone(p) {
			return fmt.Errorf("baseline process ownership is unresolved: %s", path)
		}
		return nil
	})
	return err
}

func Create(ctx context.Context, configPath, destination string) (Report, error) {
	var out Report
	var err error
	configPath, err = securefs.AbsolutePath(configPath)
	if err != nil {
		return out, err
	}
	destination, err = securefs.AbsolutePath(destination)
	if err != nil {
		return out, err
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return out, err
	}
	g, err := store.LockGuard(cfg.StateDir, true, false)
	if err != nil {
		return out, err
	}
	defer g.Close()
	v, err := store.ReadImage(ctx, cfg.StateDir)
	if err != nil {
		return out, err
	}
	if !config.SameRepository(v.Settings, config.Settings{Config: cfg, RepositoryID: v.Settings.RepositoryID, Restoration: v.Settings.Restoration}) {
		return out, errors.New("configuration names different authority")
	}
	if err = quiescent(ctx, v); err != nil {
		return out, err
	}
	if err = (workspace.Git{Settings: v.Settings}).ValidateRetained(ctx, v.Tasks, v.Qualities); err != nil {
		return out, err
	}
	source, err := workspace.BackupRoots(ctx, cfg)
	if err != nil {
		return out, err
	}
	source = append(source, configPath, cfg.StateDir)
	source = append(source, references(v)...)
	m := Manifest{Format: 1, StoreFormat: store.Format, CreatedAt: time.Now().UTC(), Config: configPath, State: cfg.StateDir, User: os.Geteuid()}
	m.Roots, err = roots(source)
	if err != nil {
		return out, err
	}
	for _, root := range m.Roots {
		if config.Within(destination, root) || config.Within(root, destination) || config.Within(store.GuardPath(cfg.StateDir), root) {
			return out, errors.New("backup destination and guard must be outside all source roots")
		}
	}
	m.Binary, err = binaryDigest(ctx)
	if err != nil {
		return out, err
	}
	m.Host, err = os.Hostname()
	if err != nil {
		return out, err
	}
	m.Guard, err = g.Identity()
	if err != nil {
		return out, err
	}
	token := make([]byte, 32)
	if _, err = rand.Read(token); err != nil {
		return out, err
	}
	m.Snapshot = hex.EncodeToString(token)
	if err = config.NoSymlinks(destination); err != nil {
		return out, err
	}
	if err = os.Mkdir(destination, 0700); err != nil {
		return out, err
	}
	if err = os.Mkdir(filepath.Join(destination, "data"), 0700); err != nil {
		return out, err
	}
	for i, root := range m.Roots {
		es, err := scan(ctx, root, dataPath(destination, i), i, root == cfg.StateDir)
		if err != nil {
			return out, err
		}
		m.Entries = append(m.Entries, es...)
	}
	state, err := mapped(m, destination, cfg.StateDir)
	if err != nil {
		return out, err
	}
	if _, err = g.Snapshot(ctx, cfg.StateDir, filepath.Join(state, "runner.db")); err != nil {
		return out, err
	}
	// Compare the source a second time. The maintenance lock excludes Runner
	// writers; an operator must also keep external Git/filesystem tools quiescent.
	var again []Entry
	for i, root := range m.Roots {
		es, err := scan(ctx, root, "", i, root == cfg.StateDir)
		if err != nil {
			return out, err
		}
		again = append(again, es...)
	}
	if !reflect.DeepEqual(m.Entries, again) {
		return out, errors.New("source changed during backup; incomplete bundle retained")
	}
	for i, root := range m.Roots {
		if root == cfg.StateDir {
			es, err := scan(ctx, filepath.Join(state, "runner.db"), "", i, false)
			if err != nil {
				return out, err
			}
			es[0].Name = "runner.db"
			m.Entries = append(m.Entries, es...)
		}
	}
	sortEntries(m.Entries)
	if err = validateEntries(m); err != nil {
		return out, err
	}
	if err = syncDir(state); err != nil {
		return out, err
	}
	if err = syncDir(filepath.Join(destination, "data")); err != nil {
		return out, err
	}
	b, err := json.MarshalIndent(envelope{Manifest: m, Digest: core.Digest(m)}, "", "  ")
	if err != nil {
		return out, err
	}
	if len(b) > 64<<20 {
		return out, errors.New("backup manifest exceeds the supported 64 MiB limit")
	}
	if err = g.Seal(m.Snapshot); err != nil {
		return out, err
	}
	// The manifest is the completion marker. A partial write fails its digest.
	if err = securefs.WriteFileExclusive(filepath.Join(destination, "manifest.json"), b, 0600); err != nil {
		return out, err
	}
	if err = syncDir(destination); err != nil {
		return out, err
	}
	if err = syncDir(filepath.Dir(destination)); err != nil {
		return out, err
	}
	out = report(m, v, true, false)
	out.Next = "Backup complete. Preserve this directory and the exact Runner binary; keep the original state guard in place. Restore previews with --from DIRECTORY."
	return out, nil
}

func inspect(ctx context.Context, bundle, configPath string) (Manifest, store.Image, error) {
	var m Manifest
	var v store.Image
	if err := securefs.ValidatePrivateDir(bundle); err != nil {
		return m, v, err
	}
	b, _, _, err := securefs.ReadFile(filepath.Join(bundle, "manifest.json"), 64<<20)
	if err != nil {
		return m, v, err
	}
	var e envelope
	if err = config.Decode(b, &e); err != nil {
		return m, v, err
	}
	m = e.Manifest
	if m.Format != 1 || m.StoreFormat != store.Format || e.Digest != core.Digest(m) || len(m.Snapshot) != 64 {
		return m, v, errors.New("incomplete, corrupt or incompatible backup manifest")
	}
	host, err := os.Hostname()
	if err != nil {
		return m, v, err
	}
	binary, err := binaryDigest(ctx)
	if err != nil {
		return m, v, err
	}
	if m.Binary != binary || m.Host != host || m.User != os.Geteuid() || m.Config != configPath {
		return m, v, errors.New("restore requires the same binary, host, user and original configuration path")
	}
	if err = validateEntries(m); err != nil {
		return m, v, err
	}
	var actual []Entry
	for i, root := range m.Roots {
		if config.Within(bundle, root) || config.Within(root, bundle) || config.Within(store.GuardPath(m.State), root) {
			return m, v, errors.New("backup roots overlap bundle or guard")
		}
		if err = config.NoSymlinks(dataPath(bundle, i)); err != nil {
			return m, v, err
		}
		es, err := scan(ctx, dataPath(bundle, i), "", i, false)
		if err != nil {
			return m, v, err
		}
		actual = append(actual, es...)
	}
	sortEntries(actual)
	if !reflect.DeepEqual(m.Entries, actual) {
		return m, v, errors.New("backup contents do not match manifest")
	}
	items, err := os.ReadDir(filepath.Join(bundle, "data"))
	if err != nil {
		return m, v, err
	}
	if len(items) != len(m.Roots) {
		return m, v, errors.New("unexpected backup roots")
	}
	state, err := mapped(m, bundle, m.State)
	if err != nil {
		return m, v, err
	}
	v, err = store.ReadImage(ctx, state)
	if err != nil {
		return m, v, err
	}
	cfgPath, err := mapped(m, bundle, m.Config)
	if err != nil {
		return m, v, err
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return m, v, err
	}
	if cfg.StateDir != m.State || !v.Drained || !config.SameRepository(v.Settings, config.Settings{Config: cfg, RepositoryID: v.Settings.RepositoryID, Restoration: v.Settings.Restoration}) {
		return m, v, errors.New("backup configuration or authority does not match manifest")
	}
	required := append([]string{cfg.Repository.Path, cfg.WorktreeDir}, references(v)...)
	for _, p := range required {
		dest, err := mapped(m, bundle, p)
		if err != nil {
			return m, v, err
		}
		if _, err = os.Lstat(dest); err != nil {
			return m, v, err
		}
	}
	for _, a := range v.Attempts {
		if a.Occupying {
			return m, v, errors.New("backup contains occupied attempt")
		}
	}
	for _, p := range v.Publications {
		if p.Occupying {
			return m, v, errors.New("backup contains occupied publication")
		}
	}
	for _, t := range v.Tasks {
		for _, w := range t.RefWrites {
			if w.Pending() {
				return m, v, errors.New("backup contains unsettled local ref write")
			}
		}
	}
	return m, v, nil
}
func report(m Manifest, v store.Image, fresh, applied bool) Report {
	held := !fresh || v.Settings.Restoration != nil && v.Settings.Restoration.InspectionOnly
	out := Report{Snapshot: m.Snapshot, Destinations: m.Roots, InspectionOnly: held, Applied: applied, Next: recovery}
	if !held {
		out.Next = "Restore stays drained. After applying, inspect status and retained work; run explicitly when ready."
	}
	return out
}
func freshness(g *store.Guard, m Manifest) (bool, error) {
	id, err := g.Identity()
	if err != nil {
		return false, err
	}
	token, err := g.Token()
	return id == m.Guard && token == m.Snapshot, err
}
func absent(roots []string) error {
	for _, root := range roots {
		if err := config.NoSymlinks(root); err != nil {
			return err
		}
		if _, err := os.Lstat(root); !os.IsNotExist(err) {
			return fmt.Errorf("restore destination must be absent: %s", root)
		}
		// Parents must be deliberately supplied by the operator. Do not create a
		// path hierarchy that could hide a missing mount or misplaced destination.
		i, err := os.Stat(filepath.Dir(root))
		if err != nil {
			return err
		}
		if !i.IsDir() {
			return errors.New("restore parent is not a directory")
		}
	}
	return nil
}

// Restore previews by default. Applying never replaces an existing path. Each
// root is staged on its destination filesystem, with held authority installed last.
func Restore(ctx context.Context, bundle, configPath string, apply bool) (Report, error) {
	return restore(ctx, bundle, configPath, apply, nil)
}
func restore(ctx context.Context, bundle, configPath string, apply bool, beforeInstall func(string) error) (out Report, err error) {
	bundle, err = securefs.AbsolutePath(bundle)
	if err != nil {
		return out, err
	}
	configPath, err = securefs.AbsolutePath(configPath)
	if err != nil {
		return out, err
	}
	m, v, err := inspect(ctx, bundle, configPath)
	if err != nil {
		return out, err
	}
	out = report(m, v, false, false)
	destinationErr := absent(m.Roots)
	if destinationErr != nil && apply {
		return out, destinationErr
	}
	if destinationErr != nil {
		out.Blockers = []string{destinationErr.Error()}
	}
	g, err := store.LockGuard(m.State, true, apply)
	if !apply && os.IsNotExist(err) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	defer g.Close()
	fresh, err := freshness(g, m)
	if err != nil {
		return out, err
	}
	out = report(m, v, fresh, false)
	if destinationErr != nil {
		out.Blockers = []string{destinationErr.Error()}
	}
	if !apply {
		return out, nil
	}
	// Invalidate before changing any destination. A crash during install makes
	// subsequent recovery inspection-only even if some copies look complete.
	if err = g.Invalidate(); err != nil {
		return out, err
	}
	staged := make([]string, len(m.Roots))
	for i, root := range m.Roots {
		dir, err := os.MkdirTemp(filepath.Dir(root), ".runner-restore-")
		if err != nil {
			return out, err
		}
		defer os.RemoveAll(dir)
		staged[i] = filepath.Join(dir, "data")
		es, err := scan(ctx, dataPath(bundle, i), staged[i], i, false)
		if err != nil {
			return out, err
		}
		var expected []Entry
		for _, e := range m.Entries {
			if e.Root == i {
				expected = append(expected, e)
			}
		}
		sortEntries(es)
		if !reflect.DeepEqual(es, expected) {
			return out, errors.New("backup changed while staging restore")
		}
		if root == m.State {
			if err = g.BindRestored(ctx, staged[i], config.Restoration{Snapshot: m.Snapshot, InspectionOnly: true}); err != nil {
				return out, err
			}
		}
	}
	order := make([]int, 0, len(m.Roots))
	stateIndex := -1
	for i, root := range m.Roots {
		if root == m.State {
			stateIndex = i
		} else {
			order = append(order, i)
		}
	}
	if stateIndex < 0 {
		return out, errors.New("state must be a separate backup root")
	}
	order = append(order, stateIndex)
	for _, i := range order {
		if err = ctx.Err(); err != nil {
			return out, err
		}
		if beforeInstall != nil {
			if err = beforeInstall(m.Roots[i]); err != nil {
				return out, err
			}
		}
		if err = absent([]string{m.Roots[i]}); err != nil {
			return out, err
		}
		if err = install(staged[i], m.Roots[i]); err != nil {
			return out, err
		}
		if err = syncDir(filepath.Dir(m.Roots[i])); err != nil {
			return out, err
		}
	}
	// Validate actual Git registrations at their original paths before enabling
	// even a drained authority. Logical IDs are preserved across physical copies.
	binding, _, err := workspace.Observe(ctx, v.Settings.Config)
	if err != nil {
		return out, err
	}
	r := config.Restoration{Snapshot: m.Snapshot, RepositoryBinding: binding, InspectionOnly: out.InspectionOnly}
	v.Settings.Restoration = &r
	if _, err = workspace.BackupRoots(ctx, v.Settings.Config); err != nil {
		return out, err
	}
	if err = (workspace.Git{Settings: v.Settings}).ValidateRetained(ctx, v.Tasks, v.Qualities); err != nil {
		return out, err
	}
	if err = g.BindRestored(ctx, m.State, r); err != nil {
		return out, err
	}
	out.Applied = true
	return out, nil
}
