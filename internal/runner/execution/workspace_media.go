package execution

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/cortexium-io/runner/internal/guidesmedia"
	"github.com/cortexium-io/runner/internal/runner"
)

const guideAssetRoot = "frontend/public/assets/learning/guides"

func (w *Workspaces) mediaIndex(ctx context.Context, binding *guidesmedia.Binding) (guidesmedia.Index, error) {
	var index guidesmedia.Index
	if binding == nil || binding.ValidateShape() != nil {
		return index, errors.New("source has no valid guide whole-set authority")
	}
	if err := ctx.Err(); err != nil {
		return index, err
	}
	data, err := w.disk.Read("artifacts/"+binding.IndexID, 32<<10)
	if err != nil || guidesmedia.Digest(data) != binding.IndexSHA256 {
		return index, errors.New("guide source index changed or is unavailable")
	}
	return binding.Validate(data)
}

func (w *Workspaces) mediaBlob(ctx context.Context, a guidesmedia.Asset) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	data, err := w.disk.Read("artifacts/"+a.ArtifactID, w.disk.Policy.MaxArtifact)
	if err != nil || int64(len(data)) != a.Bytes || guidesmedia.Digest(data) != a.SHA256 {
		return nil, errors.New("immutable guide source blob changed or is unavailable")
	}
	return data, nil
}

// Reconstruct only the declared whole set into the caller's owned index/object
// namespace. Code and asset authority remain separate from the original base.
func (w *Workspaces) mediaIndexOverlay(ctx context.Context, r workspaceRecord, dir string, env []string) error {
	index, err := w.mediaIndex(ctx, r.GuidesMedia)
	if err != nil {
		return err
	}
	var entries bytes.Buffer
	for _, asset := range index.Assets {
		data, err := w.mediaBlob(ctx, asset)
		if err != nil {
			return err
		}
		id, err := w.mediaGitObject(ctx, r, dir, env, data)
		if err != nil {
			return err
		}
		fmt.Fprintf(&entries, "100644 %s\t%s%c", id, asset.Path, 0)
	}
	_, err = git(ctx, dir, env, entries.Bytes(), 4096, "update-index", "-z", "--index-info")
	return err
}

func (w *Workspaces) mediaCode(r workspaceRecord, patch []byte, limit runner.Bytes) ([]byte, string, error) {
	if r.GuidesMedia == nil {
		return patch, "", nil
	}
	if len(patch) == 0 {
		return nil, "", nil // Initial input: only the declared immutable overlay.
	}
	p, err := guidesmedia.DecodePatch(patch, r.Base, *r.GuidesMedia, int(limit))
	if err != nil {
		return nil, "", err
	}
	return []byte(p.Code), p.Tree, nil
}

func mediaDiffPaths() []string {
	paths := []string{"--", "."}
	m, _ := guidesmedia.Approved()
	for _, g := range m.Guides {
		for _, f := range g.Files {
			paths = append(paths, ":(exclude)"+guidesmedia.Target(g, f))
		}
	}
	return paths
}

func (w *Workspaces) mediaSourcePatch(ctx context.Context, r workspaceRecord, env []string, tree string, cached bool, limit runner.Bytes) ([]byte, error) {
	args := []string{"diff"}
	if cached {
		args = append(args, "--cached")
	}
	args = append(args, "--binary", "--full-index", "--no-ext-diff", "--no-textconv", "--no-renames", r.Base)
	if !cached {
		args = append(args, tree)
	}
	if r.GuidesMedia == nil {
		return git(ctx, r.Repository, env, nil, int(limit), append(args, "--")...)
	}
	if err := w.validateMediaTree(ctx, r, env, tree); err != nil {
		return nil, err
	}
	code, err := git(ctx, r.Repository, env, nil, int(limit), append(args, mediaDiffPaths()...)...)
	if err != nil {
		return nil, err
	}
	return guidesmedia.EncodePatch(r.Base, tree, *r.GuidesMedia, code, int(limit))
}

func (w *Workspaces) validateMediaCodePaths(ctx context.Context, r workspaceRecord, env []string) error {
	paths, err := git(ctx, r.Repository, env, nil, 1<<20, "diff", "--cached", "--name-only", "--no-renames", "-z", r.Base, "--")
	if err != nil {
		return err
	}
	for _, name := range bytes.Split(paths, []byte{0}) {
		if len(name) > 0 && !guidesmedia.CodePathAllowed(string(name)) {
			return errors.New("guide code patch changes an immutable asset or non-media source path")
		}
	}
	return nil
}

func (w *Workspaces) validateMediaTree(ctx context.Context, r workspaceRecord, env []string, tree string) error {
	if r.GuidesMedia == nil {
		return nil
	}
	index, err := w.mediaIndex(ctx, r.GuidesMedia)
	if err != nil {
		return err
	}
	wanted := map[string]guidesmedia.Asset{}
	for _, a := range index.Assets {
		wanted[a.Path] = a
		info, err := git(ctx, r.Repository, env, nil, 4096, "ls-tree", tree, "--", a.Path)
		if err != nil || !strings.HasPrefix(string(info), "100644 blob ") || !strings.HasSuffix(strings.TrimSuffix(string(info), "\n"), "\t"+a.Path) {
			return errors.New("guide tree asset type/path differs")
		}
		data, err := git(ctx, r.Repository, env, nil, int(a.Bytes)+1, "cat-file", "blob", tree+":"+a.Path)
		if err != nil || int64(len(data)) != a.Bytes || guidesmedia.Digest(data) != a.SHA256 {
			return errors.New("guide full candidate tree changed an immutable asset")
		}
	}
	files, err := git(ctx, r.Repository, env, nil, 32<<10, "ls-tree", "--name-only", "-r", "-z", tree, "--", guideAssetRoot)
	if err != nil {
		return err
	}
	count := 0
	for _, file := range bytes.Split(files, []byte{0}) {
		if len(file) != 0 {
			if _, ok := wanted[string(file)]; !ok {
				return errors.New("guide full tree contains unlisted media")
			}
			count++
		}
	}
	if count != guidesmedia.AssetCount {
		return errors.New("guide full tree lacks the complete immutable media set")
	}
	changed, err := git(ctx, r.Repository, env, nil, 1<<20, "diff-tree", "--no-commit-id", "--name-only", "--no-renames", "-r", "-z", r.Base, tree, "--")
	if err != nil {
		return err
	}
	for _, file := range bytes.Split(changed, []byte{0}) {
		if len(file) == 0 {
			continue
		}
		if _, ok := wanted[string(file)]; !ok && !guidesmedia.CodePathAllowed(string(file)) {
			return errors.New("guide candidate changes puzzle/controller/entry/save/privacy or other non-media source")
		}
	}
	return nil
}

func (w *Workspaces) mediaCheckout(ctx context.Context, r workspaceRecord, allowCreate bool) error {
	if r.GuidesMedia == nil {
		return nil
	}
	index, err := w.mediaIndex(ctx, r.GuidesMedia)
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(r.WorkDir)
	if err != nil {
		return err
	}
	defer root.Close()
	for _, asset := range index.Assets {
		if err := ctx.Err(); err != nil {
			return err
		}
		data, err := w.mediaBlob(ctx, asset)
		if err != nil {
			return err
		}
		if err := func() error {
			current := root
			parts := strings.Split(asset.Path, "/")
			for _, part := range parts[:len(parts)-1] {
				info, err := current.Lstat(part)
				if errors.Is(err, os.ErrNotExist) && allowCreate {
					if err := current.Mkdir(part, 0755); err != nil {
						return err
					}
					info, err = current.Lstat(part)
				}
				if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
					return errors.New("guide checkout parent changed or is missing")
				}
				next, err := current.OpenRoot(part)
				if err != nil {
					return err
				}
				defer next.Close()
				opened, err := next.Stat(".")
				if err != nil || !os.SameFile(info, opened) {
					return errors.New("guide checkout parent changed while opening")
				}
				current = next
			}
			name := parts[len(parts)-1]
			if info, err := current.Lstat(name); err == nil {
				if !info.Mode().IsRegular() || info.Mode().Perm()&0111 != 0 || info.Size() != asset.Bytes {
					return errors.New("guide checkout asset type/mode/size differs")
				}
				data, mode, err := captureSourceFile(r.WorkDir, asset.Path, w.disk.Policy.MaxArtifact)
				if err != nil || mode != "100644" || guidesmedia.Digest(data) != asset.SHA256 {
					return errors.New("guide checkout asset changed; never overwrite it")
				}
				return nil
			} else if !errors.Is(err, os.ErrNotExist) || !allowCreate {
				return errors.New("guide checkout asset missing or unavailable; no repair of admitted source")
			}
			f, err := current.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
			if err != nil {
				return err
			}
			if _, err = f.Write(data); err == nil {
				err = f.Sync()
			}
			return errors.Join(err, f.Close())
		}(); err != nil {
			return err
		}
	}
	return nil
}

func mediaSourceMatches(r workspaceRecord, source *runner.SourceResult) bool {
	if r.GuidesMedia == nil {
		return source == nil || source.MediaIndexID == "" && source.MediaIndexSHA256 == ""
	}
	return source != nil && source.MediaIndexID == r.GuidesMedia.IndexID && source.MediaIndexSHA256 == r.GuidesMedia.IndexSHA256
}

func mediaSourceFields(r workspaceRecord, source *runner.SourceResult) {
	if r.GuidesMedia != nil {
		source.MediaIndexID = r.GuidesMedia.IndexID
		source.MediaIndexSHA256 = r.GuidesMedia.IndexSHA256
	}
}

func sameMediaBinding(a, b *guidesmedia.Binding) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}

func (w *Workspaces) applyMediaIndexPatch(ctx context.Context, r workspaceRecord, env []string, patch []byte, limit runner.Bytes) error {
	code, tree, err := w.mediaCode(r, patch, limit)
	if err != nil {
		return err
	}
	if len(code) > 0 {
		if _, err = git(ctx, r.Repository, env, code, 4096, "apply", "--cached", "--binary"); err != nil {
			return err
		}
	}
	if err := w.validateMediaCodePaths(ctx, r, env); err != nil {
		return err
	}
	if err := w.mediaIndexOverlay(ctx, r, r.Repository, env); err != nil {
		return err
	}
	actual, err := git(ctx, r.Repository, env, nil, 256, "write-tree")
	if err != nil {
		return err
	}
	if tree != "" && tree != strings.TrimSpace(string(actual)) {
		return errors.New("code plus complete immutable asset set does not reconstruct the full candidate tree")
	}
	return w.validateMediaTree(ctx, r, env, strings.TrimSpace(string(actual)))
}
