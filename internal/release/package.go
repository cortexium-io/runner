package release

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type packageManifest struct {
	Revision  string            `json:"baseRevision"`
	Clean     bool              `json:"cleanReviewedCheckout"`
	Identity  string            `json:"identity"`
	Artifacts map[string]string `json:"artifacts"`
	Sources   map[string]string `json:"sourceFiles"`
}

func fileHash(file string) (string, error) {
	f, err := os.Open(file)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, (4<<30)+1))
	if err != nil {
		return "", err
	}
	if n > 4<<30 {
		return "", errors.New("delivery file exceeds its 4GiB bound")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (b *Live) sandbox(ctx context.Context, j *Job, writePaths []string, args ...string) ([]byte, error) {
	c := b.Config.Delivery
	var config strings.Builder
	config.WriteString("default_permissions=\"runner-build\"\n[permissions.runner-build.filesystem]\n\":minimal\"=\"read\"\n")
	for _, path := range c.ReadPaths {
		fmt.Fprintf(&config, "%s=\"read\"\n", strconv.Quote(path))
	}
	for _, path := range writePaths {
		fmt.Fprintf(&config, "%s=\"write\"\n", strconv.Quote(path))
	}
	// The builder may generate assets in its checkout, but cannot replace the
	// Git inventory used by the privileged collector to bind reviewed source.
	metadata := filepath.Join(writePaths[0], "source", ".git")
	if _, err := os.Stat(metadata); err == nil {
		fmt.Fprintf(&config, "%s=\"read\"\n", strconv.Quote(metadata))
	}
	fmt.Fprintln(&config, "[permissions.runner-build.network]\nenabled=true")
	control := filepath.Join(filepath.Dir(j.Worker), "sandbox-"+Digest([]byte(config.String()))[:12])
	if err := os.MkdirAll(control, 0700); err != nil {
		return nil, err
	}
	if err := writeExclusive(filepath.Join(control, "config.toml"), []byte(config.String())); err != nil {
		return nil, err
	}
	home := filepath.Join(writePaths[0], "home")
	if err := os.MkdirAll(home, 0700); err != nil {
		return nil, err
	}
	tmp := filepath.Join(home, "tmp")
	if err := os.MkdirAll(tmp, 0700); err != nil {
		return nil, err
	}
	argv := []string{"sandbox", "-P", "runner-build", "-C", writePaths[0], "--", "/usr/bin/env", "-i",
		"PATH=" + strings.Join([]string{
			filepath.Dir(c.Go),
			filepath.Dir(c.Node),
			filepath.Dir(c.Python),
			filepath.Dir(c.Git),
			"/usr/bin",
			"/bin",
			"/usr/local/bin",
		}, ":"),
		"TMPDIR=" + tmp, "TMP=" + tmp, "TEMP=" + tmp,
		"HOME=" + home, "GOMODCACHE=" + c.ModuleCache, "GOCACHE=" + filepath.Join(home, "go-build"), "GOPROXY=off", "GOTOOLCHAIN=local", "GOWORK=off", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "npm_config_cache=" + filepath.Join(home, "npm"), "NODE_OPTIONS=--use-system-ca", "NODE_EXTRA_CA_CERTS=" + c.TrustBundle, "OPENSSL_CONF=/opt/homebrew/etc/openssl@3/openssl.cnf"}
	argv = append(argv, args...)
	// Only the sandbox launcher inherits CODEX_HOME. The actual build receives
	// the explicit env -i above, with no account authentication or private HOME.
	return b.commandWithEnvironment(ctx, nil, []string{"PATH=/usr/bin:/bin", "HOME=" + home, "CODEX_HOME=" + control}, c.Codex, argv...)
}

func (b *Live) packageCandidate(ctx context.Context, j *Job) error {
	c := b.Config.Delivery
	work := filepath.Join(filepath.Dir(j.Worker), "work")
	source := filepath.Join(work, "source")
	if err := os.MkdirAll(work, 0700); err != nil {
		return err
	}
	git := func(args ...string) ([]byte, error) {
		return b.command(ctx, nil, c.Git, append([]string{
			"--no-replace-objects",
			"-c",
			"core.hooksPath=/dev/null",
			"-c",
			"core.fsmonitor=false",
			"-c",
			"credential.helper=!" + quote(b.Config.GH) + " auth git-credential",
			"-C",
			source,
		}, args...)...)
	}
	if _, err := b.command(ctx, nil, c.Git, "init", "--quiet", source); err != nil {
		return err
	}
	if _, err := git("remote", "add", "origin", "https://github.com/"+b.Config.Repository+".git"); err != nil {
		return err
	}
	if _, err := git("fetch", "--quiet", "--depth=1", "origin", j.Receipt.Head); err != nil {
		return err
	}
	if _, err := git("checkout", "--quiet", "--detach", "FETCH_HEAD"); err != nil {
		return err
	}
	actual, err := git("rev-parse", "HEAD")
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(actual)) != j.Receipt.Head {
		return errors.New("package checkout differs from reviewed candidate")
	}
	output := j.Receipt.Package
	if _, err = b.sandbox(ctx, j, []string{work, output}, c.Python, "-B", filepath.Join(source, "scripts/production/build.py"), output); err != nil {
		return packageFailure(output, err)
	}
	data, err := readPackageFile(output, "manifest.json", 8<<20)
	if err != nil {
		return err
	}
	var manifest packageManifest
	if json.Unmarshal(data, &manifest) != nil || manifest.Revision != j.Receipt.Head || !manifest.Clean || !strings.HasPrefix(manifest.Identity, "source-sha256:") {
		return errors.New("clean package identity differs")
	}
	if _, err = b.sandbox(ctx, j, []string{work, output}, "/usr/bin/env", "GENNY_ENV=production", "GAMES_PUBLIC_ORIGIN=https://rehearsal.example.test", "GAMES_TRUSTED_PROXIES=127.0.0.1/32", c.Python, "-B", filepath.Join(source, "scripts/production/preflight.py"), output, "--manifest-sha256", Digest(data)); err != nil {
		return &sourceFailure{reason: "candidate package preflight failed; inspect its bounded package diagnostics"}
	}
	data, err = readPackageFile(output, "manifest.json", 8<<20)
	if err != nil {
		return err
	}
	if json.Unmarshal(data, &manifest) != nil || manifest.Revision != j.Receipt.Head || !manifest.Clean {
		return errors.New("package changed during its preflight")
	}
	tree, err := git("ls-tree", "-r", "-z", j.Receipt.Head)
	if err != nil {
		return err
	}
	if err = verifyPackageSource(source, tree, manifest.Sources); err != nil {
		return err
	}
	for path, hash := range manifest.Artifacts {
		file, err := packagePath(output, path)
		if err != nil {
			return err
		}
		actual, err := fileHash(file)
		if err != nil {
			return err
		}
		if actual != hash {
			return errors.New("package artifact hash differs: " + path)
		}
	}
	if manifest.Artifacts["context/Dockerfile"] != manifest.Sources["deploy/Dockerfile"] || manifest.Artifacts["context/.dockerignore"] != manifest.Sources["deploy/.dockerignore"] {
		return errors.New("image context instructions differ from reviewed source")
	}
	for _, name := range []string{"context/server", "context/Dockerfile", "context/.dockerignore"} {
		hash, err := fileHash(filepath.Join(output, name))
		if err != nil {
			return err
		}
		if hash != manifest.Artifacts[name] {
			return errors.New("packaged artifact differs: " + name)
		}
	}
	// Reproduce from the builder's frozen source and its bound compiled assets,
	// inside the same credential-free sandbox, rather than trusting a receipt.
	if err = os.RemoveAll(source); err != nil {
		return err
	}
	reproduce := filepath.Join(work, "reproduce")
	if err = extractSource(filepath.Join(output, "source.tar.gz"), reproduce, manifest.Sources); err != nil {
		return err
	}
	for name, hash := range manifest.Artifacts {
		if !strings.HasPrefix(name, "frontend_dist/") {
			continue
		}
		if err = copyBoundFile(filepath.Join(output, name), filepath.Join(reproduce, "internal/app/http", name), hash); err != nil {
			return err
		}
	}
	binary := filepath.Join(work, "reproduced-server")
	args := []string{
		"/usr/bin/env",
		"CGO_ENABLED=0",
		"GOOS=linux",
		"GOARCH=amd64",
		"GOAMD64=v1",
		"GOEXPERIMENT=",
		c.Go,
		"-C",
		reproduce,
		"build",
		"-trimpath",
		"-buildvcs=false",
		"-ldflags",
		"-s -w -buildid= -X main.buildIdentity=" + manifest.Identity,
		"-o",
		binary,
		"./cmd/server",
	}
	if _, err = b.sandbox(ctx, j, []string{work, output}, args...); err != nil {
		return err
	}
	hash, err := fileHash(binary)
	if err != nil {
		return err
	}
	if hash != manifest.Artifacts["context/server"] {
		return errors.New("independent executable reproduction differs")
	}
	if err = writeJSON(filepath.Join(output, "reproduction.json"), map[string]any{"status": "passed", "commit": j.Receipt.Head, "serverSHA256": hash, "identity": manifest.Identity}); err != nil {
		return err
	}
	j.Receipt.ServerSHA = hash
	// Qualification needs the protocol's bound source subset. Keep the complete
	// frozen archive, rather than three expanded copies of historical evidence.
	for name := range manifest.Sources {
		if !qualificationSource(name) {
			if err = os.Remove(filepath.Join(reproduce, name)); err != nil {
				return err
			}
		}
	}
	return done(j, "Clean candidate packaged and executable independently reproduced; no production changes")
}

func verifyPackageSource(root string, tree []byte, declared map[string]string) error {
	count := 0
	for _, row := range strings.Split(string(tree), "\x00") {
		if row == "" {
			continue
		}
		header, name, ok := strings.Cut(row, "\t")
		parts := strings.Fields(header)
		if !ok || len(parts) != 3 || parts[1] != "blob" || filepath.IsAbs(name) || filepath.Clean(name) != name || strings.HasPrefix(name, "../") {
			return errors.New("unsupported reviewed source tree entry")
		}
		if strings.HasPrefix(name, "tmp/") || strings.HasPrefix(name, "internal/app/http/frontend_dist/") {
			continue
		}
		if parts[0] != "100644" && parts[0] != "100755" {
			return errors.New("unsupported reviewed source file mode")
		}
		file, err := packagePath(root, name)
		if err != nil {
			return err
		}
		f, err := os.Open(file)
		if err != nil {
			return err
		}
		info, err := f.Stat()
		if err != nil {
			f.Close()
			return err
		}
		h := sha1.New()
		strong := sha256.New()
		fmt.Fprintf(h, "blob %d%c", info.Size(), 0)
		n, copyErr := io.Copy(io.MultiWriter(h, strong), io.LimitReader(f, info.Size()+1))
		closeErr := f.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		if n != info.Size() || hex.EncodeToString(h.Sum(nil)) != parts[2] || hex.EncodeToString(strong.Sum(nil)) != declared[name] {
			return errors.New("package source differs from the reviewed Git tree: " + name)
		}
		count++
	}
	if count != len(declared) {
		return errors.New("package source inventory differs from reviewed Git tree")
	}
	return nil
}

func copyBoundFile(source, destination, hash string) error {
	data, err := readFile(source, 256<<20)
	if err != nil {
		return err
	}
	if Digest(data) != hash {
		return errors.New("bound artifact bytes differ")
	}
	if err = os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
		return err
	}
	// This destination is a fresh reproduction tree owned by this attempt.
	return os.WriteFile(destination, data, 0600)
}

func extractSource(archive, directory string, expected map[string]string) error {
	if err := os.Mkdir(directory, 0700); err != nil {
		return err
	}
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	t := tar.NewReader(gz)
	seen := map[string]bool{}
	var total int64
	for {
		h, err := t.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if h.Typeflag != tar.TypeReg || filepath.Clean(h.Name) != h.Name || filepath.IsAbs(h.Name) || strings.HasPrefix(h.Name, "../") || expected[h.Name] == "" || seen[h.Name] || h.Size < 0 || h.Size > 1<<30 {
			return errors.New("unsafe or unexpected frozen source entry")
		}
		total += h.Size
		if total > 4<<30 {
			return errors.New("frozen source exceeds the 4GiB extraction reserve")
		}
		path := filepath.Join(directory, h.Name)
		if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return err
		}
		out, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, os.FileMode(h.Mode)&0777)
		if err != nil {
			return err
		}
		hash := sha256.New()
		n, copyErr := io.Copy(io.MultiWriter(out, hash), io.LimitReader(t, h.Size+1))
		closeErr := out.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		if n != h.Size || hex.EncodeToString(hash.Sum(nil)) != expected[h.Name] {
			return errors.New("frozen source hash differs")
		}
		seen[h.Name] = true
	}
	if len(seen) != len(expected) {
		return errors.New("frozen source inventory is incomplete")
	}
	return nil
}
