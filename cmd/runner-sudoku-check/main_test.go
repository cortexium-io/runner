package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func fixtureFile(t *testing.T, root, name, body string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}

func fixtureSource(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, name := range []string{"cmd/server/main.go", "internal/example.go", "frontend/src/main.ts", "go.mod", "go.sum"} {
		fixtureFile(t, root, name, "fixture\n")
	}
	return root
}

func TestStageSourceExcludesRegenerableCopiesAndAuthenticatesInputs(t *testing.T) {
	source := fixtureSource(t)
	for _, name := range []string{"frontend/node_modules/large", "internal/app/http/frontend_dist/stale", "docs/evidence/old/movie", "frontend/.genny/static-pages/stale", ".git/objects/private"} {
		fixtureFile(t, source, name, "excluded")
	}
	fixtureFile(t, source, "docs/reference.json", "{\"fixture\":true}")
	fixtureFile(t, source, "frontend/src/import.ts", "import x from '../../docs/reference.json'\n")
	files, identity, err := inventory(source)
	if err != nil {
		t.Fatal(err)
	}
	if identity == "" || len(files) != 7 {
		t.Fatalf("unexpected minimal inventory: %s %+v", identity, files)
	}
	staging := filepath.Join(t.TempDir(), "build")
	if err = stageSource(source, staging, files); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"frontend/node_modules", "internal/app/http/frontend_dist", "docs/evidence", ".git"} {
		if _, err = os.Stat(filepath.Join(staging, path)); !os.IsNotExist(err) {
			t.Fatalf("copied excluded input %s: %v", path, err)
		}
	}
	fixtureFile(t, source, "internal/example.go", "changed")
	if err = stageSource(source, filepath.Join(t.TempDir(), "changed"), files); err == nil {
		t.Fatal("changed candidate bytes were staged under the old identity")
	}
}

func TestStageModulesOwnsToolCachesAndPreservesSharedDependencies(t *testing.T) {
	modules, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	shared := map[string]string{
		"vite/package.json":      "installed package",
		"@scope/plugin/index.js": "installed scoped package",
		".bin/vite":              "installed executable",
		".package-lock.json":     "installed lock",
		".vite/prior":            "shared cache",
		".vite-temp/prior":       "shared config cache",
		".cache/prior":           "another shared cache",
	}
	for name, body := range shared {
		fixtureFile(t, modules, name, body)
	}
	staging := filepath.Join(t.TempDir(), "node_modules")
	if err := stageModules(modules, staging); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(staging); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		t.Fatal("staged node_modules is not an owned real directory", err)
	}
	for _, name := range []string{"vite", "@scope", ".bin", ".package-lock.json"} {
		link, err := os.Readlink(filepath.Join(staging, name))
		if err != nil || link != filepath.Join(modules, name) {
			t.Fatalf("installed dependency entry was copied or lost: %s %s %v", name, link, err)
		}
	}
	for _, name := range []string{".vite", ".vite-temp", ".cache"} {
		if _, err := os.Lstat(filepath.Join(staging, name)); !os.IsNotExist(err) {
			t.Fatalf("shared cache entered the build wrapper: %s %v", name, err)
		}
		fixtureFile(t, staging, name+"/owned", "private regenerable cache")
		if _, err := os.Stat(filepath.Join(modules, name, "owned")); !os.IsNotExist(err) {
			t.Fatalf("build cache escaped into installed dependencies: %s %v", name, err)
		}
	}
	if err := stageModules(modules, staging); err == nil {
		t.Fatal("existing staging directory was replaced")
	}
	if err := os.RemoveAll(staging); err != nil {
		t.Fatal(err)
	}
	for name, body := range shared {
		data, err := os.ReadFile(filepath.Join(modules, name))
		if err != nil || string(data) != body {
			t.Fatalf("owned cache cleanup changed shared dependency bytes: %s %v", name, err)
		}
	}
}

// This optional installed-tool proof loads only a tiny synthetic Vite config.
// It never builds or imports a product and grants writes only to owned scratch.
func TestInstalledViteConfigUsesOwnedModuleCache(t *testing.T) {
	modules := os.Getenv("RUNNER_SUDOKU_MODULES_PREFLIGHT")
	if modules == "" {
		t.Skip("set RUNNER_SUDOKU_MODULES_PREFLIGHT for the installed Vite synthetic config proof")
	}
	if runtime.GOOS != "darwin" {
		t.Skip("installed tool proof uses the qualified macOS seatbelt sandbox")
	}
	node := os.Getenv("RUNNER_SUDOKU_NODE_PREFLIGHT")
	if node == "" || !filepath.IsAbs(node) || !filepath.IsAbs(modules) {
		t.Fatal("installed proof requires absolute configured Node and modules paths")
	}
	work, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := stageModules(modules, filepath.Join(work, "node_modules")); err != nil {
		t.Fatal(err)
	}
	fixtureFile(t, work, "vite.config.mts", `import {defineConfig} from 'vite';
export default defineConfig({define:{__RUNNER_FIXTURE__:'"owned-cache"'}});
`)
	fixtureFile(t, work, "probe.mjs", `import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import {loadConfigFromFile} from 'vite';
const loaded=await loadConfigFromFile({command:'build',mode:'production'},'vite.config.mts',process.cwd());
assert.equal(loaded?.config.define.__RUNNER_FIXTURE__,'"owned-cache"');
assert.equal((await fs.lstat('node_modules/.vite-temp')).isDirectory(),true);
assert.equal((await fs.lstat('node_modules/.vite-temp')).isSymbolicLink(),false);
console.log('synthetic Vite config loaded with owned temporary cache');
`)
	policy := "(version 1)\n(allow default)\n(deny file-write* (require-not (subpath " + strconv.Quote(work) + ")))\n"
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/usr/bin/sandbox-exec", "-p", policy, node, filepath.Join(work, "probe.mjs"))
	cmd.Dir, cmd.WaitDelay = work, 2*time.Second
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("synthetic installed Vite config failed: %v %s", err, output)
	}
	t.Log(strings.TrimSpace(string(output)))
}

func TestInventoryRejectsSourceSymlinksAndEscapingFixtures(t *testing.T) {
	source := fixtureSource(t)
	if err := os.Symlink("/etc/passwd", filepath.Join(source, "internal/link.go")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := inventory(source); err == nil {
		t.Fatal("source symlink admitted")
	}
	if err := os.Remove(filepath.Join(source, "internal/link.go")); err != nil {
		t.Fatal(err)
	}
	fixtureFile(t, source, "frontend/src/import.ts", "import x from '../../../../outside.json'\n")
	if _, _, err := inventory(source); err == nil {
		t.Fatal("escaping JSON fixture admitted")
	}
}

func TestArchiveRejectsUnownedFilesAndPreservesOnlyBoundedCanonicalEvidence(t *testing.T) {
	dir := t.TempDir()
	fixtureFile(t, dir, "report.json", "{\"status\":\"failed\"}")
	target := filepath.Join(t.TempDir(), "evidence.tar.gz")
	if err := archiveEvidence(dir, target, evidenceLimit); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(target)
	if err != nil {
		t.Fatal(err)
	}
	z, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	r := tar.NewReader(z)
	header, err := r.Next()
	if err != nil || header.Name != "report.json" {
		t.Fatalf("canonical report missing: %+v %v", header, err)
	}
	if _, err = io.Copy(io.Discard, r); err != nil {
		t.Fatal(err)
	}
	if _, err = r.Next(); err != io.EOF {
		t.Fatalf("unexpected copied evidence: %v", err)
	}
	z.Close()
	f.Close()
	fixtureFile(t, dir, "raw-transcript.log", "unrelated")
	other := filepath.Join(t.TempDir(), "rejected.tar.gz")
	if err = archiveEvidence(dir, other, evidenceLimit); err == nil {
		t.Fatal("raw/unrelated file was archived")
	}
	if _, err = os.Stat(other); !os.IsNotExist(err) {
		t.Fatal("partial rejected archive remains")
	}
	if err = os.Remove(filepath.Join(dir, "raw-transcript.log")); err != nil {
		t.Fatal(err)
	}
	if err = archiveEvidence(dir, other, 1); err == nil {
		t.Fatal("compressed archive bound ignored")
	}
	if _, err = os.Stat(other); !os.IsNotExist(err) {
		t.Fatal("oversized archive remains")
	}
}

func TestPreviewRefusesUnrelatedAndChangedPriorResources(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "server")
	if err := os.WriteFile(binary, []byte("stopped fixture executable"), 0600); err != nil {
		t.Fatal(err)
	}
	preview := filepath.Join(t.TempDir(), "preview")
	proof, err := retainPreview(preview, binary, "source", "base", "request", "failed")
	if err != nil || proof.Running || proof.ServerSHA256 == "" {
		t.Fatalf("preview proof: %+v %v", proof, err)
	}
	if _, err = retainPreview(preview, binary, "source", "base", "request", "passed"); err != nil {
		t.Fatal(err)
	}
	if _, err = retainPreview(preview, binary, "other-source", "base", "another-goal", "passed"); err == nil {
		t.Fatal("preview from another goal was replaced")
	}
	fixtureFile(t, preview, "owner-recording", "preserve")
	if _, err = retainPreview(preview, binary, "source", "base", "request", "passed"); err == nil {
		t.Fatal("unrelated preview content was replaced")
	}
	if err = os.Remove(filepath.Join(preview, "owner-recording")); err != nil {
		t.Fatal(err)
	}
	fixtureFile(t, preview, filepath.Base(proof.ServerPath), "changed independently")
	if _, err = retainPreview(preview, binary, "source", "base", "request", "passed"); err == nil {
		t.Fatal("changed previous preview was replaced")
	}
}

func TestPreviewInterruptedPromotionKeepsPreviousPointerAndRecovers(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "server")
	if err := os.WriteFile(binary, []byte("old executable"), 0600); err != nil {
		t.Fatal(err)
	}
	preview := filepath.Join(t.TempDir(), "preview")
	old, err := retainPreview(preview, binary, "source-old", "base", "request", "passed")
	if err != nil {
		t.Fatal(err)
	}
	newBytes := []byte("new executable")
	fixtureFile(t, preview, "server-"+digest(newBytes), string(newBytes))
	fixtureFile(t, preview, ".manifest-next", "unfinished pointer")
	// Exact filesystem state after new executable preparation and before the
	// pointer switch: the old preview remains independently hash-verifiable.
	if hash, err := hashFile(old.ServerPath, 128<<20); err != nil || hash != old.ServerSHA256 {
		t.Fatal("old preview lost before pointer commit")
	}
	if err = os.WriteFile(binary, newBytes, 0600); err != nil {
		t.Fatal(err)
	}
	current, err := retainPreview(preview, binary, "source-new", "base", "request", "passed")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(old.ServerPath); !os.IsNotExist(err) {
		t.Fatal("obsolete preview binary was not collected")
	}
	if hash, err := hashFile(current.ServerPath, 128<<20); err != nil || hash != current.ServerSHA256 {
		t.Fatal("recovered preview differs")
	}
	entries, err := os.ReadDir(preview)
	if err != nil || len(entries) != 4 {
		t.Fatalf("preview intermediates leaked: %+v %v", entries, err)
	}
}

func TestFailedSetupRetainsReportAndRemovesOwnedStaging(t *testing.T) {
	source := fixtureSource(t)
	modules := filepath.Join(t.TempDir(), "node_modules")
	packages := map[string]map[string]string{}
	for _, name := range []string{"vite", "vue", "vue-tsc", "typescript", "@vitejs/plugin-vue", "@tailwindcss/vite"} {
		packages["node_modules/"+name] = map[string]string{"version": "1.0.0"}
		fixtureFile(t, modules, name+"/package.json", "{\"version\":\"1.0.0\"}")
	}
	lock, _ := json.Marshal(map[string]any{"packages": packages})
	fixtureFile(t, source, "frontend/package-lock.json", string(lock))
	fixtureFile(t, filepath.Dir(modules), "package-lock.json", string(lock))
	fixtureFile(t, modules, ".package-lock.json", string(lock))
	scratch := t.TempDir()
	request := filepath.Join(t.TempDir(), "request.json")
	if err := os.WriteFile(request, []byte("{\"request\":\"fixture only\"}"), 0600); err != nil {
		t.Fatal(err)
	}
	// This uninitialized synthetic source must fail setup, before product work.
	// Any accidental runtime launch also fails through /usr/bin/false.
	o := options{source: source, scratch: scratch, node: "/usr/bin/false", goBinary: "/usr/bin/false", modules: modules, playwright: request, chromium: request, webkit: request, request: request, base: strings.Repeat("a", 40)}
	o.handoff, o.priorCheck = request, request
	if err := run(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(scratch)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("owned build staging leaked: %+v", entries)
	}
	f, err := os.Open(filepath.Join(scratch, "evidence.tar.gz"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	z, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer z.Close()
	r := tar.NewReader(z)
	header, err := r.Next()
	if err != nil || header.Name != "report.json" {
		t.Fatalf("missing failure report: %v", err)
	}
	var p report
	if err = json.NewDecoder(r).Decode(&p); err != nil {
		t.Fatal(err)
	}
	if p.Status != "blocked" || !p.Cleanup.DatabaseRemoved || !p.Cleanup.ScratchRemoved || !p.Cleanup.ServerStopped || p.Failure == "" {
		t.Fatalf("failure/cleanup facts lost: %+v", p)
	}
	var domain map[string]string
	data, err := os.ReadFile(filepath.Join(scratch, "domain.json"))
	if err != nil {
		t.Fatal(err)
	}
	if json.Unmarshal(data, &domain) != nil || domain["outcome"] != "ready" {
		t.Fatal("failed product/prerequisite evidence was dropped instead of returned for assessment")
	}
}

func TestGeometryContractFixtures(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node is required for the pure geometry contract fixtures")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, "--test", "../../testdata/sudoku-check/geometry.test.mjs", "../../testdata/sudoku-check/browser-contract.test.mjs")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("geometry contract fixture: %v\n%s", err, out)
	}
}

func TestSummaryReferencesFullProofWithoutRepeatingSourceInventory(t *testing.T) {
	dir := t.TempDir()
	p := report{Schema: 1, Status: "passed", SourceSHA256: "candidate", SourceAfter: "candidate", SourceFiles: []fileProof{{Name: "private/full-source-inventory.go", SHA256: "hash"}}, Browser: map[string]any{"file": "browser.json", "geometry": 146, "journeys": 20}}
	if err := writeJSON(filepath.Join(dir, "report.json"), p); err != nil {
		t.Fatal(err)
	}
	if err := writeSummary(dir, p); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "summary.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "full-source-inventory") || strings.Contains(string(data), "source_files") {
		t.Fatal("summary repeats full source catalog")
	}
	var summary struct {
		Report struct {
			File   string
			SHA256 string
		}
		Source string `json:"source_sha256"`
	}
	if json.Unmarshal(data, &summary) != nil || summary.Source != "candidate" || summary.Report.File != "report.json" {
		t.Fatal("summary lost canonical proof references")
	}
	hash, err := hashFile(filepath.Join(dir, "report.json"), 2<<20)
	if err != nil || hash != summary.Report.SHA256 {
		t.Fatal("summary does not bind the full report")
	}
}
