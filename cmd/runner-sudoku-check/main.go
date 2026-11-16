// runner-sudoku-check collects protected, candidate-bound Sudoku preview proof.
// It is a fixed Runner command, not a product worker or a release approval.
package main

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

//go:embed browser.mjs
var browserScript []byte

//go:embed geometry.mjs
var geometryScript []byte

//go:embed browser-contract.mjs
var browserContractScript []byte

const evidenceLimit int64 = 16 << 20

type options struct {
	mode, acceptedCheck, acceptedCheckSHA256 string
	acceptedHandoff                          string
	handoff, priorCheck                      string
	source, scratch, node, goBinary, modules string
	playwright, chromium, webkit, request    string
	base, preview, previewRoot               string
}

type fileProof struct {
	Name       string `json:"name"`
	SHA256     string `json:"sha256"`
	Bytes      int64  `json:"bytes"`
	GitBlob    string `json:"git_blob_sha1,omitempty"`
	Executable bool   `json:"executable,omitempty"`
}

type report struct {
	Schema           int               `json:"schema"`
	Status           string            `json:"status"`
	Scope            string            `json:"scope"`
	Base             string            `json:"base"`
	SourceSHA256     string            `json:"source_sha256"`
	SourceMethod     string            `json:"source_digest_method"`
	BuildInputSHA256 string            `json:"build_input_sha256,omitempty"`
	SourceAfter      string            `json:"source_after_sha256,omitempty"`
	SourceFiles      []fileProof       `json:"source_files,omitempty"`
	RequestSHA256    string            `json:"request_sha256,omitempty"`
	Checker          map[string]string `json:"checker"`
	Runtime          map[string]string `json:"runtime"`
	Build            []commandProof    `json:"build,omitempty"`
	Frontend         *frontendEvidence `json:"frontend,omitempty"`
	Browser          map[string]any    `json:"browser,omitempty"`
	Preview          *previewProof     `json:"preview,omitempty"`
	Failure          string            `json:"failure,omitempty"`
	Cleanup          cleanupProof      `json:"cleanup"`
	Limitations      []string          `json:"limitations"`
}

type commandProof struct {
	Name       string `json:"name"`
	DurationMS int64  `json:"duration_ms"`
	Diagnostic string `json:"diagnostic,omitempty"`
	Passed     bool   `json:"passed"`
}

type cleanupProof struct {
	BrowserClosed   bool `json:"browser_closed"`
	ServerStopped   bool `json:"server_stopped"`
	DatabaseRemoved bool `json:"database_removed"`
	ScratchRemoved  bool `json:"build_scratch_removed"`
}

type previewProof struct {
	Directory      string `json:"directory"`
	ServerPath     string `json:"server_path"`
	ServerSHA256   string `json:"server_sha256"`
	ManifestSHA256 string `json:"manifest_sha256"`
	Running        bool   `json:"running"`
}

func main() {
	var o options
	flag.StringVar(&o.mode, "mode", "check", "check or admit-followup")
	flag.StringVar(&o.acceptedCheck, "accepted-check", "", "exact accepted protected checker archive for follow-up source admission")
	flag.StringVar(&o.acceptedCheckSHA256, "accepted-check-sha256", "", "accepted archive SHA256 bound by the owner follow-up request")
	flag.StringVar(&o.acceptedHandoff, "accepted-handoff", "", "accepted source handoff receipt for the same workspace")
	flag.StringVar(&o.source, "source", "", "read-only canonical project workspace")
	flag.StringVar(&o.scratch, "scratch", "", "Runner-owned writable scratch")
	flag.StringVar(&o.node, "node", "", "configured Node executable")
	flag.StringVar(&o.goBinary, "go", "", "configured compatible Go executable")
	flag.StringVar(&o.modules, "modules", "", "immutable installed frontend node_modules with its original sibling lockfile")
	flag.StringVar(&o.playwright, "playwright", "", "configured Playwright index.mjs")
	flag.StringVar(&o.chromium, "chromium", "", "configured Chrome for Testing executable")
	flag.StringVar(&o.webkit, "webkit", "", "configured Playwright WebKit executable")
	flag.StringVar(&o.request, "request", "", "authoritative task agreement/goal JSON")
	flag.StringVar(&o.base, "base", "", "authenticated original Git base, 40 hex characters")
	flag.StringVar(&o.preview, "preview", "", "optional exact Runner-owned retained preview directory")
	flag.StringVar(&o.previewRoot, "preview-root", "", "optional retained preview root partitioned by exact request SHA256")
	flag.StringVar(&o.handoff, "handoff", "", "typed protected source handoff artifact")
	flag.StringVar(&o.priorCheck, "prior-check", "", "typed authenticated prior frontend check artifact")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	if err := run(ctx, o); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, o options) (err error) {
	if o.mode == "admit-followup" {
		return admitFollowup(ctx, o)
	}
	if err = o.validate(); err != nil {
		return err
	}
	p := report{
		Schema: 1, Status: "blocked", Base: o.base,
		SourceMethod: "sha256 of canonical sorted file-proof JSON (name,sha256,bytes,git_blob_sha1,executable) for tracked and ordinary untracked candidate files; only .runner-source-handoff.json excluded",
		Scope:        "Sudoku agreed layouts: functional journeys and measured geometry, plus three affected shared-shell neighbor smokes; screenshots require independent visual review",
		Checker:      map[string]string{"browser_sha256": digest(browserScript), "geometry_sha256": digest(geometryScript), "browser_contract_sha256": digest(browserContractScript)},
		Runtime:      map[string]string{},
		Limitations: []string{
			"Headless Chrome for Testing and Playwright WebKit use emulated viewport/input profiles; this is not native Safari or physical device proof.",
			"Geometry/function assertions do not approve branding, hierarchy, spacing, icon quality or legibility; an independent reviewer must inspect retained full viewport captures.",
			"No merge, production, restoration, legacy native-gate waiver or benchmark qualification is performed.",
			"The retained preview is a stopped native executable, not a background development server or a release package.",
			"WebKit exposes trusted taps but no public trusted touch hold/cancel API; its hold/move coverage uses a mouse pointer, while Chromium uses CDP touch.",
			"Neighbor evidence is three small Chromium desktop shared-shell smokes, not a full Suguru/Nonogram/Shikaku suite or renewed engine-generation qualification.",
		},
	}
	p.Cleanup.BrowserClosed = true // No browser has been started yet.
	if executable, e := os.Executable(); e == nil {
		p.Checker["binary_sha256"], _ = hashFile(executable, 128<<20)
	}
	work, err := os.MkdirTemp(o.scratch, "sudoku-check-")
	if err != nil {
		return err
	}
	evidence := filepath.Join(work, "evidence")
	if err = os.Mkdir(evidence, 0700); err != nil {
		_ = os.RemoveAll(work)
		return err
	}
	var server *ownedServer
	var serverPath string
	defer func() {
		if server != nil {
			p.Cleanup.ServerStopped = server.close() == nil
		} else {
			p.Cleanup.ServerStopped = true
		}
		if e := os.RemoveAll(filepath.Join(work, "database")); e != nil {
			p.Failure = clip(p.Failure+"; database cleanup: "+e.Error(), 3000)
		} else {
			p.Cleanup.DatabaseRemoved = true
		}
		if p.SourceSHA256 != "" {
			_, after, e := candidateInventory(ctx, o.source)
			p.SourceAfter = after
			if e != nil || after != p.SourceSHA256 {
				p.Status = "blocked"
				p.Failure = clip(p.Failure+"; canonical source changed during protected check", 3000)
			}
		}
		if p.Status == "passed" && p.Cleanup.BrowserClosed && p.Cleanup.ServerStopped && p.Cleanup.DatabaseRemoved && o.preview != "" && serverPath != "" {
			preview, e := retainPreview(o.preview, serverPath, p.SourceSHA256, o.base, p.RequestSHA256, p.Status)
			p.Preview = preview
			if e != nil {
				p.Status = "blocked"
				p.Failure = clip(p.Failure+"; preview retention: "+e.Error(), 3000)
			}
		}
		// Delete regenerable source/build/database before collecting the one archive.
		for _, name := range []string{"source", "server", "browser.mjs", "geometry.mjs", "browser-contract.mjs", "browser-options.json"} {
			if e := os.RemoveAll(filepath.Join(work, name)); e != nil {
				p.Failure = clip(p.Failure+"; scratch cleanup: "+e.Error(), 3000)
			}
		}
		entries, e := os.ReadDir(work)
		p.Cleanup.ScratchRemoved = e == nil && len(entries) == 1 && entries[0].Name() == "evidence"
		if !p.Cleanup.BrowserClosed || !p.Cleanup.ServerStopped || !p.Cleanup.DatabaseRemoved || !p.Cleanup.ScratchRemoved {
			p.Status = "blocked"
		}
		if err != nil {
			p.Failure = clip(err.Error(), 3000)
		}
		if e = writeJSON(filepath.Join(evidence, "report.json"), p); e == nil {
			e = writeSummary(evidence, p)
		}
		if e == nil {
			e = archiveEvidence(evidence, filepath.Join(o.scratch, "evidence.tar.gz"), evidenceLimit)
		}
		if e != nil {
			err = errors.Join(err, e)
			return
		}
		if e = os.RemoveAll(work); e != nil {
			err = errors.Join(err, e)
			return
		}
		summary := fmt.Sprintf("Sudoku evidence status=%s source=%s; canonical report/evidence retained; inspect coverage and gaps before independent visual acceptance", p.Status, p.SourceSHA256)
		if p.Failure != "" {
			summary += "; " + clip(p.Failure, 250)
		}
		// A completed collection is ready even when product assertions failed.
		// The lead reads report.status and decides repair/rejection.
		e = writeJSON(filepath.Join(o.scratch, "domain.json"), map[string]string{"outcome": "ready", "reason": clip(summary, 480)})
		if e == nil {
			fmt.Println(clip(summary, 480))
			err = nil
		} else {
			err = e
		}
	}()
	request, err := readBounded(o.request, 128<<10)
	if err != nil {
		return err
	}
	p.RequestSHA256 = digest(request)
	o.preview = o.previewDirectory(p.RequestSHA256)
	p.SourceFiles, p.SourceSHA256, err = candidateInventory(ctx, o.source)
	if err != nil {
		return err
	}
	if err = validateModules(o.source, o.modules, p.Runtime); err != nil {
		return err
	}
	for key, spec := range map[string]struct {
		binary string
		args   []string
	}{
		"node": {o.node, []string{"--version"}}, "go": {o.goBinary, []string{"version"}},
	} {
		out, e := execute(ctx, "", nil, spec.binary, spec.args...)
		if e != nil {
			return fmt.Errorf("configured %s prerequisite: %w", key, e)
		}
		p.Runtime[key] = clip(string(out), 200)
		p.Runtime[key+"_binary_sha256"], e = hashFile(spec.binary, 256<<20)
		if e != nil {
			return e
		}
	}
	p.Runtime["playwright_module"] = o.playwright
	p.Runtime["playwright_module_sha256"], err = hashFile(o.playwright, 2<<20)
	if err != nil {
		return err
	}
	p.Runtime["chromium_launcher_sha256"], err = hashFile(o.chromium, 128<<20)
	if err != nil {
		return err
	}
	p.Runtime["webkit_launcher_sha256"], err = hashFile(o.webkit, 128<<20)
	if err != nil {
		return err
	}
	p.Frontend, err = frontendGate(ctx, o, work, evidence)
	if err != nil {
		if p.Frontend != nil && p.Frontend.Mode == "fresh" && p.Frontend.Status == "failed" {
			p.Status = "failed"
		}
		return err
	}
	source := filepath.Join(work, "source")
	buildFiles, buildHash, err := inventory(o.source)
	if err != nil {
		return err
	}
	p.BuildInputSHA256 = buildHash
	if err = stageSource(o.source, source, buildFiles); err != nil {
		return err
	}
	if err = stageModules(o.modules, filepath.Join(source, "frontend", "node_modules")); err != nil {
		return err
	}
	p.Runtime["frontend_modules_staging"] = "owned directory; installed package links; private tooling caches"
	serverPath = filepath.Join(work, "server")
	p.Build, err = build(ctx, o, source, serverPath, p.SourceSHA256)
	if err != nil {
		if ctx.Err() == nil {
			p.Status = "failed"
		}
		return err
	}
	if err = os.Mkdir(filepath.Join(work, "database"), 0700); err != nil {
		return err
	}
	server, err = startServer(ctx, serverPath, filepath.Join(work, "database"))
	if err != nil {
		return err
	}
	for name, data := range map[string][]byte{"browser.mjs": browserScript, "geometry.mjs": geometryScript, "browser-contract.mjs": browserContractScript} {
		if err = os.WriteFile(filepath.Join(work, name), data, 0400); err != nil {
			return err
		}
	}
	settings := map[string]string{"origin": server.origin, "evidence": evidence, "playwright": o.playwright, "chromium": o.chromium, "webkit": o.webkit}
	if err = writeJSON(filepath.Join(work, "browser-options.json"), settings); err != nil {
		return err
	}
	p.Cleanup.BrowserClosed = false
	_, browserErr := execute(ctx, work, nil, o.node, filepath.Join(work, "browser.mjs"), filepath.Join(work, "browser-options.json"))
	data, err := readBounded(filepath.Join(evidence, "browser.json"), 2<<20)
	if err != nil {
		return fmt.Errorf("browser evidence completion is unestablished: %w; %v", err, browserErr)
	}
	return recordBrowserReport(&p, data, browserErr)
}

func (o options) validate() error {
	if o.mode != "" && o.mode != "check" {
		return errors.New("--mode must be check or admit-followup")
	}
	for name, path := range map[string]string{"source": o.source, "scratch": o.scratch, "node": o.node, "go": o.goBinary, "modules": o.modules, "playwright": o.playwright, "chromium": o.chromium, "webkit": o.webkit, "request": o.request, "handoff": o.handoff, "prior-check": o.priorCheck} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return fmt.Errorf("--%s requires one absolute canonical path", name)
		}
		if _, err := os.Stat(path); err != nil {
			return fmt.Errorf("--%s: %w", name, err)
		}
	}
	if len(o.base) != 40 || !isHex(o.base) {
		return errors.New("--base requires the authenticated original 40-character Git base")
	}
	if o.preview != "" && o.previewRoot != "" {
		return errors.New("select exact --preview or --preview-root, not both")
	}
	for _, preview := range []string{o.preview, o.previewRoot} {
		if preview != "" && (!filepath.IsAbs(preview) || filepath.Clean(preview) != preview || preview == "/" || nested(o.source, preview) || nested(preview, o.source) || nested(o.scratch, preview) || nested(preview, o.scratch)) {
			return errors.New("preview path must be an exact separate owned directory")
		}
	}
	if nested(o.source, o.scratch) || nested(o.scratch, o.source) {
		return errors.New("scratch and source must be separate")
	}
	return nil
}

func (o options) previewDirectory(requestSHA string) string {
	if o.previewRoot != "" {
		return filepath.Join(o.previewRoot, requestSHA)
	}
	return o.preview
}

func digest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func isHex(value string) bool   { _, err := hex.DecodeString(value); return err == nil }
func clip(value string, limit int) string {
	if len(value) > limit {
		return value[:limit]
	}
	return value
}
func nested(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func readBounded(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > limit {
		return nil, errors.New("input is not a bounded regular file")
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if int64(len(data)) > limit {
		return nil, errors.New("input exceeds byte bound")
	}
	return data, err
}

func hashFile(path string, limit int64) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > limit {
		return "", errors.New("hash input is not a bounded regular file")
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, limit+1))
	if err != nil || n > limit {
		return "", errors.New("hash input exceeds bound or is unreadable")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func writeJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	if len(data) > 2<<20 {
		return errors.New("evidence JSON exceeds 2MiB")
	}
	return os.WriteFile(path, append(data, '\n'), 0600)
}

type tailBuffer struct {
	mu   sync.Mutex
	data []byte
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	const limit = 16 << 10
	if len(p) >= limit {
		b.data = append(b.data[:0], p[len(p)-limit:]...)
	} else {
		if len(b.data)+len(p) > limit {
			keep := limit - len(p)
			copy(b.data, b.data[len(b.data)-keep:])
			b.data = b.data[:keep]
		}
		b.data = append(b.data, p...)
	}
	return len(p), nil
}
func (b *tailBuffer) bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.data...)
}

func environment(extra map[string]string) []string {
	values := map[string]string{}
	for _, entry := range os.Environ() {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			values[key] = value
		}
	}
	for key, value := range extra {
		values[key] = value
	}
	var result []string
	for key, value := range values {
		result = append(result, key+"="+value)
	}
	return result
}

func execute(ctx context.Context, directory string, env map[string]string, binary string, args ...string) ([]byte, error) {
	cmd := exec.Command(binary, args...)
	cmd.Dir, cmd.Env = directory, environment(env)
	cmd.WaitDelay = 2 * time.Second
	var output tailBuffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return output.bytes(), err
	case <-ctx.Done():
		_ = cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
		return output.bytes(), ctx.Err()
	}
}

type ownedServer struct {
	cmd     *exec.Cmd
	done    chan error
	origin  string
	output  *tailBuffer
	stopped bool
	proxy   *httptest.Server
}

func startServer(ctx context.Context, binary, database string) (*ownedServer, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	address := listener.Addr().String()
	if err = listener.Close(); err != nil {
		return nil, err
	}
	backend, _ := url.Parse("http://" + address)
	proxy := httputil.NewSingleHostReverseProxy(backend)
	var public *url.URL
	proxy.Director = func(r *http.Request) {
		profile, e := strconv.Atoi(r.Header.Get("X-Runner-Sudoku-Profile"))
		if e != nil || profile < 1 || profile > 64 {
			profile = 1
		}
		r.Header.Del("X-Runner-Sudoku-Profile")
		r.URL.Scheme, r.URL.Host, r.Host = "http", address, public.Host
		r.Header.Set("X-Forwarded-Proto", "https")
		r.Header.Set("X-Forwarded-For", "198.18.0."+strconv.Itoa(profile))
	}
	// One local self-signed TLS gateway models distinct isolated client profiles,
	// matching the existing protected browser checker without disabling limits.
	gateway := httptest.NewTLSServer(proxy)
	public, _ = url.Parse(gateway.URL)
	settings := map[string]string{
		"GENNY_ENV": "development", "DB_DRIVER": "sqlite", "DATABASE_URL": filepath.Join(database, "games.db"), "ADDR": address,
		"GAMES_PUBLIC_ORIGIN": gateway.URL, "GAMES_TRUSTED_PROXIES": "127.0.0.1/32", "GENNY_TRUST_PROXY_HEADERS": "0", "GAMES_GOOGLE_ENABLED": "0",
		"GENNY_AUTH_ACCEPT_NOOP_MAILER": "1", "GENNY_AUTH_ACCEPT_LOCAL_RATE_LIMITER": "1", "GOMAXPROCS": "2", "GOMEMLIMIT": "768MiB",
	}
	s := &ownedServer{cmd: exec.Command(binary, "--migrate"), done: make(chan error, 1), origin: gateway.URL, output: &tailBuffer{}, proxy: gateway}
	s.cmd.Env, s.cmd.Dir = environment(settings), database
	s.cmd.WaitDelay = 2 * time.Second
	s.cmd.Stdout, s.cmd.Stderr = s.output, s.output
	if err = s.cmd.Start(); err != nil {
		gateway.Close()
		return nil, err
	}
	go func() { s.done <- s.cmd.Wait() }()
	client := &http.Client{Timeout: time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(30 * time.Second)
	defer deadline.Stop()
	for {
		req, _ := http.NewRequestWithContext(ctx, "GET", "http://"+address+"/api/ready", nil)
		resp, e := client.Do(req)
		if e == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == 200 {
				return s, nil
			}
		}
		select {
		case e = <-s.done:
			s.stopped = true
			return s, fmt.Errorf("owned server exited before readiness: %v; %s", e, clip(string(s.output.bytes()), 1500))
		case <-deadline.C:
			return s, errors.New("owned server readiness deadline exceeded")
		case <-ctx.Done():
			return s, ctx.Err()
		case <-ticker.C:
		}
	}
}

func (s *ownedServer) close() error {
	if s.proxy != nil {
		defer s.proxy.Close()
	}
	if s.stopped {
		return nil
	}
	_ = s.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-s.done:
		s.stopped = true
		return nil
	case <-time.After(5 * time.Second):
		_ = s.cmd.Process.Kill()
		select {
		case <-s.done:
			s.stopped = true
			return nil
		case <-time.After(3 * time.Second):
			return errors.New("owned server settlement is unconfirmed")
		}
	}
}
