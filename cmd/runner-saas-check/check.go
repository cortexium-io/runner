package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/cortexium-io/runner/internal/saas"
)

func boundedFile(path string, max int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > max {
		return nil, errors.New("missing or oversized regular input")
	}
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil || int64(len(b)) > max || int64(len(b)) != info.Size() {
		return nil, errors.New("regular input changed or exceeds its bound")
	}
	return b, nil
}

func runtimePin(path, sha string) error {
	b, err := boundedFile(path, 256<<20)
	if err != nil {
		return err
	}
	if saas.Digest(b) != sha {
		return errors.New("configured runtime bytes differ from frozen authority")
	}
	return nil
}

func sourceCopy(from, to string) error {
	var total int64
	return filepath.WalkDir(from, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(from, p)
		if err != nil {
			return err
		}
		if d.IsDir() {
			if rel != "." && (d.Name() == ".git" || d.Name() == ".runner-tmp" || d.Name() == "node_modules" || d.Name() == "dist") {
				return filepath.SkipDir
			}
			return os.Mkdir(filepath.Join(to, rel), 0700)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Size() > 8<<20 {
			return errors.New("source must contain bounded regular files, without links")
		}
		if strings.HasSuffix(d.Name(), ".db") || strings.HasPrefix(d.Name(), ".env") || strings.HasSuffix(d.Name(), ".env") {
			return errors.New("private/runtime state cannot enter local qualification")
		}
		total += info.Size()
		if total > 32<<20 {
			return errors.New("selected app exceeds32MiB fixed source-copy bound")
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(to, rel), b, info.Mode().Perm()&0700|0600)
	})
}

func manifests(root string, a saas.Authority) error {
	for name, want := range map[string]string{"go.mod": a.GoMod, "go.sum": a.GoSum, "frontend/package.json": a.Package, "frontend/package-lock.json": a.Lock} {
		b, err := boundedFile(filepath.Join(root, filepath.FromSlash(name)), 8<<20)
		if err != nil || string(b) != want {
			return fmt.Errorf("selected %s differs from the admitted pinned foundation", name)
		}
	}
	return nil
}

func snapshot(root string) (map[string]string, error) {
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		if d.IsDir() {
			r := filepath.ToSlash(rel)
			if d.Name() == "node_modules" || r == "frontend/.genny/static-pages" || r == "internal/app/http/frontend_dist" {
				return filepath.SkipDir
			}
			return nil
		}
		b, err := boundedFile(p, 8<<20)
		if err == nil {
			out[filepath.ToSlash(rel)] = saas.Digest(b)
		}
		return err
	})
	return out, err
}

func run(ctx context.Context, o options) error {
	if !absolute(o.source, o.scratch, o.authority) {
		return errors.New("absolute configured source/scratch/authority paths required")
	}
	if err := os.MkdirAll(o.scratch, 0700); err != nil {
		return err
	}
	authority, err := boundedFile(o.authority, saas.MaxBundle)
	if err != nil {
		return err
	}
	a, err := saas.DecodeAuthority(authority, o.base)
	if err != nil {
		return err
	}
	if o.reference {
		return os.WriteFile(filepath.Join(o.scratch, "authority.json"), authority, 0600)
	}
	if o.runtimeFile != "" {
		if !filepath.IsAbs(o.runtimeFile) {
			return errors.New("runtime manifest must be an absolute operator-configured file")
		}
		b, err := boundedFile(o.runtimeFile, 64<<10)
		if err != nil {
			return err
		}
		var r struct {
			Go             string `json:"go"`
			Genny          string `json:"genny"`
			Node           string `json:"node"`
			Modules        string `json:"modules"`
			BrowserRuntime string `json:"browser_runtime"`
			Chrome         string `json:"chrome"`
			GoCheck        string `json:"go_check"`
		}
		d := json.NewDecoder(bytes.NewReader(b))
		d.DisallowUnknownFields()
		if err = d.Decode(&r); err != nil {
			return err
		}
		o.goBinary, o.genny, o.node, o.modules, o.browserRuntime, o.chrome, o.goCheck = r.Go, r.Genny, r.Node, r.Modules, r.BrowserRuntime, r.Chrome, r.GoCheck
	}
	if !absolute(o.patch, o.goBinary, o.genny, o.node, o.modules, o.browserRuntime, o.chrome, o.goCheck) {
		return errors.New("all qualification runtime paths must be explicitly configured")
	}
	if !filepath.IsLocal(o.patchID) || strings.ContainsAny(o.patchID, "\\\x00\r\n") || filepath.ToSlash(filepath.Clean(o.patchID)) != o.patchID {
		return errors.New("literal Runner patch artifact identity required")
	}
	for p, h := range map[string]string{o.goBinary: a.GoSHA256, o.goCheck: a.GoCheckSHA256, o.genny: a.GennySHA256, o.node: a.NodeSHA256, o.chrome: a.ChromeSHA256} {
		if err = runtimePin(p, h); err != nil {
			return err
		}
	}
	if saas.Digest(saas.BrowserDriver) != a.DriverSHA256 {
		return errors.New("installed fixed browser driver differs from admission")
	}
	patch, err := boundedFile(o.patch, saas.MaxBundle)
	if err != nil {
		return err
	}
	appSource := filepath.Join(o.source, a.AppDirectory)
	real, err := filepath.EvalSymlinks(appSource)
	if err != nil || real != appSource {
		return errors.New("selected application path is absent or linked")
	}
	if err = manifests(appSource, a); err != nil {
		return err
	}
	// Dependencies were prepared from these lock bytes before admission. Never
	// install packages, run npm scripts or fetch modules during qualification.
	lock, err := boundedFile(filepath.Join(o.modules, "..", "package-lock.json"), 8<<20)
	if err != nil || !bytes.Equal(lock, []byte(a.Lock)) {
		return errors.New("installed Vue dependency lock differs from selected application")
	}
	return qualify(ctx, o, a, authority, patch, appSource)
}
