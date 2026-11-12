package release

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/guidesmedia"
)

// No Jack source, product build, browser, image or model is used. Exact approved
// bytes traverse the existing archive/reproduction/Go-embed/FileServer primitives
// in a new disposable synthetic repository, with real HTTP Range responses.
func TestGuidesExactMediaPackagePropagation(t *testing.T) {
	if os.Getenv("RUNNER_GUIDE_MEDIA_PACKAGE") != "1" {
		t.Skip("owned exact-media synthetic package qualification")
	}
	root := t.TempDir()
	source := filepath.Join(root, "source")
	os.Mkdir(source, 0700)
	m, err := guidesmedia.Approved()
	if err != nil {
		t.Fatal(err)
	}
	sources, compiled := map[string]string{}, map[string]string{}
	put := func(base, name string, data []byte) {
		t.Helper()
		file := filepath.Join(base, name)
		if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, g := range m.Guides {
		for _, f := range g.Files {
			data, e := guidesmedia.ReadApproved(t.Context(), f)
			if e != nil {
				t.Fatal(e)
			}
			name := guidesmedia.Target(g, f)
			put(source, name, data)
			sources[name] = f.SHA256
			static := "frontend_dist/" + strings.TrimPrefix(name, "frontend/public/")
			compiled[static] = f.SHA256
			put(filepath.Join(root, "output"), static, data)
			put(source, "internal/app/http/"+static, data)
		}
	}
	if err := guidesmedia.ValidatePropagation(sources, compiled); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(map[string]string){func(x map[string]string) { delete(x, "frontend_dist/assets/learning/guides/sudoku/video.mp4") }, func(x map[string]string) {
		x["frontend_dist/assets/learning/guides/sudoku/video.mp4"] = strings.Repeat("0", 64)
	}, func(x map[string]string) {
		x["frontend_dist/assets/learning/guides/extra.mp4"] = strings.Repeat("0", 64)
	}} {
		copy := map[string]string{}
		for k, v := range compiled {
			copy[k] = v
		}
		mutate(copy)
		if guidesmedia.ValidatePropagation(sources, copy) == nil {
			t.Fatal("compiled omission/drift/extra accepted")
		}
	}
	files := map[string]string{"go.mod": "module fixture.invalid/guides\n\ngo 1.26\n", "main.go": `package main
import("embed";"fmt";"io/fs";"net";"net/http")
//go:embed internal/app/http/frontend_dist
var assets embed.FS
func main(){root,e:=fs.Sub(assets,"internal/app/http/frontend_dist");if e!=nil{panic(e)};l,e:=net.Listen("tcp","127.0.0.1:0");if e!=nil{panic(e)};fmt.Println(l.Addr());if e=http.Serve(l,http.FileServer(http.FS(root)));e!=nil{panic(e)}}
`}
	for name, data := range files {
		put(source, name, []byte(data))
		sources[name] = Digest([]byte(data))
	}
	// The generated compiled subtree is not part of the original source inventory.
	git := func(args ...string) []byte {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), "git", args...)
		cmd.Dir = source
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
		out, e := cmd.CombinedOutput()
		if e != nil {
			t.Fatal(string(out), e)
		}
		return out
	}
	git("init", "--quiet", "--template=")
	paths := []string{"add", "--"}
	for name := range sources {
		paths = append(paths, name)
	}
	git(paths...)
	git("-c", "user.name=Runner fixture", "-c", "user.email=runner@localhost", "-c", "commit.gpgSign=false", "commit", "--quiet", "-m", "synthetic exact-media package")
	tree := git("ls-tree", "-r", "-z", "HEAD")
	if err := verifyPackageSource(source, tree, sources); err != nil {
		t.Fatal(err)
	}
	missing := map[string]string{}
	for k, v := range sources {
		missing[k] = v
	}
	delete(missing, guidesmedia.Target(m.Guides[0], m.Guides[0].Files[0]))
	if verifyPackageSource(source, tree, missing) == nil {
		t.Fatal("full source inventory omission accepted")
	}
	var sourceArchive bytes.Buffer
	gz := gzip.NewWriter(&sourceArchive)
	tw := tar.NewWriter(gz)
	for name := range sources {
		data, e := os.ReadFile(filepath.Join(source, name))
		if e != nil {
			t.Fatal(e)
		}
		if e = tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0600, Size: int64(len(data))}); e != nil {
			t.Fatal(e)
		}
		if _, e = tw.Write(data); e != nil {
			t.Fatal(e)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(root, "source.tar.gz")
	os.WriteFile(archive, sourceArchive.Bytes(), 0600)
	reproduction := filepath.Join(root, "reproduce")
	if err := extractSource(archive, reproduction, sources); err != nil {
		t.Fatal(err)
	}
	for name, sha := range compiled {
		if err := copyBoundFile(filepath.Join(root, "output", name), filepath.Join(reproduction, "internal/app/http", name), sha); err != nil {
			t.Fatal(err)
		}
	}
	build := func(dir, out string) {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-trimpath", "-buildvcs=false", "-ldflags=-s -w -buildid=", "-o", out, ".")
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GOTOOLCHAIN=local", "GOENV=off", "GOWORK=off", "GOPROXY=off", "GOSUMDB=off", "CGO_ENABLED=0")
		data, e := cmd.CombinedOutput()
		if e != nil {
			t.Fatal(e, string(data))
		}
	}
	first, second := filepath.Join(root, "server"), filepath.Join(root, "reproduced-server")
	build(source, first)
	build(reproduction, second)
	hash, e := fileHash(first)
	other, e2 := fileHash(second)
	if e != nil || e2 != nil || hash != other {
		t.Fatal("independent embed executable drift", hash, other, e, e2)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cmd := exec.CommandContext(ctx, second)
	stdout, e := cmd.StdoutPipe()
	if e != nil {
		t.Fatal(e)
	}
	if e = cmd.Start(); e != nil {
		t.Fatal(e)
	}
	defer func() { cancel(); _ = cmd.Wait() }()
	address, e := bufio.NewReader(stdout).ReadString('\n')
	if e != nil {
		t.Fatal(e)
	}
	address = strings.TrimSpace(address)
	if !strings.HasPrefix(address, "127.0.0.1:") {
		t.Fatal("fixture listener escaped loopback")
	}
	client := &http.Client{
		Timeout:       5 * time.Second,
		Transport:     &http.Transport{Proxy: nil},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	defer client.CloseIdleConnections()
	ranges := 0
	for _, g := range m.Guides {
		for _, f := range g.Files {
			url := "http://" + address + "/" + strings.TrimPrefix(guidesmedia.Target(g, f), "frontend/public/")
			res, e := client.Get(url)
			if e != nil {
				t.Fatal(e)
			}
			body, e := io.ReadAll(io.LimitReader(res.Body, f.Bytes+1))
			res.Body.Close()
			if e != nil || res.StatusCode != 200 || guidesmedia.Digest(body) != f.SHA256 || !strings.HasPrefix(res.Header.Get("Content-Type"), f.MIMEType) {
				t.Fatal("compiled static identity/type lost", url, res.Status, e)
			}
			if f.Role == "video" {
				req, _ := http.NewRequestWithContext(t.Context(), "GET", url, nil)
				req.Header.Set("Range", "bytes=1024-2047")
				r, e := client.Do(req)
				if e != nil {
					t.Fatal(e)
				}
				part, e := io.ReadAll(io.LimitReader(r.Body, 1025))
				r.Body.Close()
				if e != nil || r.StatusCode != 206 || r.Header.Get("Content-Range") != fmt.Sprintf("bytes 1024-2047/%d", f.Bytes) || !bytes.Equal(part, body[1024:2048]) {
					t.Fatal("Range media byte custody lost", r.Status, e)
				}
				ranges++
			}
		}
	}
	// Apply the existing reproduction pruning rule before the unchanged64MiB
	// qualification transport: whole source remains in the authenticated archive.
	for name := range sources {
		if !qualificationSource(name) {
			if err := os.Remove(filepath.Join(reproduction, name)); err != nil {
				t.Fatal(err)
			}
		}
	}
	transport, e := packTree(reproduction)
	if e != nil {
		t.Fatal(e)
	}
	if len(transport) > 64<<20 {
		t.Fatal("qualification archive limit expanded")
	}
	reader, e := gzip.NewReader(bytes.NewReader(transport))
	if e != nil {
		t.Fatal(e)
	}
	rawTransport, e := io.ReadAll(io.LimitReader(reader, (64<<20)+1))
	reader.Close()
	if e != nil || len(rawTransport) > 64<<20 {
		t.Fatal("unexpected qualification expanded size", e)
	}
	for _, g := range m.Guides {
		for _, f := range g.Files {
			name := "internal/app/http/frontend_dist/" + strings.TrimPrefix(guidesmedia.Target(g, f), "frontend/public/")
			actual, e := tarFileHash(rawTransport, name)
			if e != nil || actual != f.SHA256 {
				t.Fatal("qualification transport lost media", name, e)
			}
		}
	}
	var logical int64
	filepath.Walk(root, func(_ string, info os.FileInfo, e error) error {
		if e != nil {
			return e
		}
		if info.Mode().IsRegular() {
			logical += info.Size()
		}
		return nil
	})
	receipt := map[string]any{
		"fixture":                      "disposable synthetic source/archive/Go embed/FileServer; no product build/image/browser/native proof",
		"manifest_sha256":              guidesmedia.ManifestSHA256,
		"media_files":                  36,
		"raw_bytes":                    guidesmedia.AssetBytes,
		"source_archive_bytes":         sourceArchive.Len(),
		"qualification_archive_bytes":  len(transport),
		"qualification_archive_cap":    64 << 20,
		"reproduced_executable_sha256": hash,
		"static_gets":                  36,
		"actual_range_206":             ranges,
		"measured_fixture_logical_peak_lower_bound_bytes": logical,
		"retained_intermediates":                          0,
		"native_device_peak":                              "not measured: no device admitted",
	}
	data, _ := json.MarshalIndent(receipt, "", "  ")
	t.Log(string(data))
	if file := os.Getenv("RUNNER_GUIDE_PACKAGE_RECEIPT"); file != "" {
		if err := os.WriteFile(file, append(data, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
}
