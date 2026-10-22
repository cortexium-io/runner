package execution

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner/config"
	"github.com/cortexium-io/runner/internal/runner/core"
)

type auditRoundTrip func(*http.Request) (*http.Response, error)

func (f auditRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func auditTemp(t *testing.T) string {
	t.Helper()
	path := t.TempDir()
	if base := os.Getenv("RUNNER_NPM_AUDIT_EVIDENCE"); base != "" {
		var err error
		path, err = os.MkdirTemp(base, "audit-")
		if err != nil {
			t.Fatal(err)
		}
	}
	path, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(path, 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

func npmFixture(t *testing.T) string {
	t.Helper()
	root := auditTemp(t)
	manifest := map[string]any{"name": "private-project-do-not-send", "version": "9.0.0", "dependencies": map[string]string{"parent": "^1.0.0"}, "devDependencies": map[string]string{"dev-leaf": "1.0.0"}, "optionalDependencies": map[string]string{"optional-leaf": "1.0.0"}, "peerDependencies": map[string]string{"peer-leaf": "1.0.0"}, "scripts": map[string]string{"install": "touch /must-not-run"}}
	packages := map[string]any{"": manifest}
	for _, name := range []string{"parent", "leaf", "dev-leaf", "optional-leaf", "peer-leaf"} {
		p := map[string]any{"version": "1.0.0", "resolved": "https://registry.npmjs.org/" + name + "/-/" + name + "-1.0.0.tgz", "integrity": "sha512-" + base64.StdEncoding.EncodeToString(make([]byte, 64))}
		if name == "parent" {
			p["dependencies"] = map[string]string{"leaf": "^1.0.0"}
		}
		if name == "dev-leaf" {
			p["dev"] = true
		}
		if name == "optional-leaf" {
			p["optional"] = true
		}
		if name == "peer-leaf" {
			p["peer"] = true
		}
		packages["node_modules/"+name] = p
	}
	for name, value := range map[string]any{"package.json": manifest, "package-lock.json": map[string]any{"lockfileVersion": 3, "packages": packages}} {
		data, _ := json.Marshal(value)
		if err := os.WriteFile(filepath.Join(root, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestNPMAuditInputsAndControlledTransport(t *testing.T) {
	root := npmFixture(t)
	if err := os.WriteFile(filepath.Join(root, ".npmrc"), []byte("registry=http://127.0.0.1:9\n_authToken=private-canary"), 0600); err != nil {
		t.Fatal(err)
	}
	input, err := InspectNPMAudit(root, ".")
	if err != nil {
		t.Fatal(err)
	}
	if len(input.Packages) != 5 || bytes.Contains(input.Manifest, []byte("private-project")) || bytes.Contains(input.Manifest, []byte("scripts")) || bytes.Contains(input.Lock, []byte("must-not-run")) {
		t.Fatal("non-graph input leaked", input)
	}
	for _, path := range []string{"package.json", "package-lock.json"} {
		original, _ := os.ReadFile(filepath.Join(root, path))
		changed := bytes.ReplaceAll(original, []byte("^1.0.0"), []byte("file:../outside"))
		if err = os.WriteFile(filepath.Join(root, path), changed, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err = InspectNPMAudit(root, "."); err == nil {
			t.Fatal("unsupported dependency accepted")
		}
		if err = os.WriteFile(filepath.Join(root, path), original, 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name, response string
		status         int
		want           bool
	}{{"clean", `{}`, 200, true}, {"outage", `{}`, 503, false}, {"malformed", `[]`, 200, false}, {"truncated advisory", `{"leaf":[{}]}`, 200, false}, {"oversized", strings.Repeat(" ", auditResponseLimit+1), 200, false}} {
		t.Run(tc.name, func(t *testing.T) {
			tr := auditTransport{packages: input.Packages, client: &http.Client{Transport: auditRoundTrip(func(r *http.Request) (*http.Response, error) {
				if r.URL.String() != config.NPMRegistry+"/-/npm/v1/security/advisories/bulk" || r.Method != "POST" || r.Header.Get("Authorization") != "" {
					t.Fatal("unapproved request", r)
				}
				return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(tc.response))}, nil
			})}}
			_, err := tr.request(t.Context(), "bulk", json.RawMessage(`{"leaf":["1.0.0"]}`))
			if (err == nil) != tc.want {
				t.Fatal(err)
			}
			if _, err = tr.request(t.Context(), "quick", json.RawMessage(`{}`)); err == nil {
				t.Fatal("Quick Audit fallback accepted")
			}
		})
	}
	for _, address := range []string{"127.0.0.1", "10.1.1.1", "169.254.169.254", "100.64.0.1", "::1", "fc00::1", "::ffff:127.0.0.1", "2001:db8::1"} {
		if publicAuditIP(netip.MustParseAddr(address)) {
			t.Fatal("private destination accepted", address)
		}
	}
	if !publicAuditIP(netip.MustParseAddr("104.16.25.34")) {
		t.Fatal("public registry address rejected")
	}
	client := auditHTTPClient()
	defer client.CloseIdleConnections()
	if client.Transport.(*http.Transport).Proxy != nil {
		t.Fatal("ambient proxy inherited")
	}
	for _, location := range []string{"http://127.0.0.1/", "https://169.254.169.254/", "https://registry.npmjs.org/other"} {
		req, _ := http.NewRequest("GET", location, nil)
		if client.CheckRedirect(req, nil) == nil {
			t.Fatal("redirect permitted", location)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "http://127.0.0.1:9/", 302) }))
	defer server.Close()
	if _, err := client.Get(server.URL); err == nil {
		t.Fatal("host network access permitted")
	}
}

func registryFixture(packages map[string][]string, vulnerable bool) *auditTransport {
	return &auditTransport{packages: packages, client: &http.Client{Transport: auditRoundTrip(func(r *http.Request) (*http.Response, error) {
		var result any
		if r.Method == "POST" {
			var payload map[string][]string
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				return nil, err
			}
			advisories := map[string]any{}
			if vulnerable {
				for _, name := range []string{"leaf", "dev-leaf", "optional-leaf", "peer-leaf"} {
					if payload[name] != nil {
						advisories[name] = []any{map[string]any{"id": 123, "name": name, "severity": "high", "title": "Fixture vulnerability", "url": "https://example.invalid/advisory", "vulnerable_versions": "<2.0.0", "cwe": []string{}, "cvss": map[string]any{"score": 8.0, "vectorString": ""}}}
					}
				}
			}
			result = advisories
		} else {
			name := strings.TrimPrefix(r.URL.Path, "/")
			versions := map[string]any{"1.0.0": map[string]any{"name": name, "version": "1.0.0"}, "2.0.0": map[string]any{"name": name, "version": "2.0.0"}}
			if name == "parent" {
				versions["1.0.0"].(map[string]any)["dependencies"] = map[string]string{"leaf": "^1.0.0"}
				versions["2.0.0"].(map[string]any)["dependencies"] = map[string]string{"leaf": "^2.0.0"}
			}
			result = map[string]any{"name": name, "versions": versions, "dist-tags": map[string]string{"latest": "2.0.0"}}
		}
		data, _ := json.Marshal(result)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(data))}, nil
	})}}
}

func TestLiveNPMAuditEvaluatorAndCancellation(t *testing.T) {
	if os.Getenv("RUNNER_LIVE_NPM_AUDIT") != "1" {
		t.Skip("set RUNNER_LIVE_NPM_AUDIT=1 for isolated Docker evaluator qualification")
	}
	image := "sha256:7852dc5a7d72f5fe458a597d690f6c42fbc8e9e26ddfa95f8e6ef552558d9bbf"
	if value := os.Getenv("RUNNER_NPM_AUDIT_IMAGE"); value != "" {
		image = value
	}
	input, err := InspectNPMAudit(npmFixture(t), ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, level, status string
		vulnerable          bool
		omit                []string
		count               int
	}{{"clean", "high", "passed", false, nil, 0}, {"transitive and meta", "high", "failed", true, nil, 5}, {"omit dev", "high", "failed", true, []string{"dev"}, 4}, {"omit optional", "high", "failed", true, []string{"optional"}, 4}, {"omit peer", "high", "failed", true, []string{"peer"}, 4}, {"omit all", "high", "failed", true, []string{"dev", "optional", "peer"}, 2}, {"threshold", "critical", "passed", true, nil, 5}} {
		t.Run(tc.name, func(t *testing.T) {
			check := config.QualityCheck{ID: "audit", Directory: ".", Timeout: "1m", Image: image, NPMAudit: &config.NPMAuditPolicy{Level: tc.level, Omit: tc.omit}}
			a := NPMAuditor{transport: func(p map[string][]string) *auditTransport { return registryFixture(p, tc.vulnerable) }}
			r := a.Audit(t.Context(), input, check, "source", filepath.Join(auditTemp(t), "audit.log"), func(core.Process) error { return nil })
			if r.Status != tc.status || !r.Stopped || r.Audit == nil || r.Audit.Receipt.Findings["total"] != tc.count {
				t.Fatalf("%+v", r)
			}
			data, _ := os.ReadFile(r.Audit.Receipt.Report)
			if tc.count > 0 && (!bytes.Contains(data, []byte(`"parent"`)) || !bytes.Contains(data, []byte(`"via":["leaf"]`))) {
				t.Fatalf("meta vulnerability missing: %s", data)
			}
			if err := VerifyAuditReceipt(r.Audit.Receipt); err != nil {
				t.Fatal(err)
			}
			// Compare against npm's own Arborist.audit and npm-audit-report path,
			// including the CLI's severity exit code, using the same registry fixture.
			oracle := npmOracle(t, input, check, tc.vulnerable)
			var actual any
			if json.Unmarshal(data, &actual) != nil || core.Digest(actual) != core.Digest(oracle.Report) || oracle.Exit != r.ExitCode {
				t.Fatal("pinned npm conformance differs", string(data), oracle)
			}
		})
	}
	t.Run("cancel settles evaluator and query", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		entered, settled := make(chan struct{}), make(chan struct{})
		a := NPMAuditor{transport: func(p map[string][]string) *auditTransport {
			return &auditTransport{packages: p, client: &http.Client{Transport: auditRoundTrip(func(r *http.Request) (*http.Response, error) {
				close(entered)
				<-r.Context().Done()
				close(settled)
				return nil, r.Context().Err()
			})}}
		}}
		var owner core.Process
		done := make(chan core.CheckObservation, 1)
		go func() {
			done <- a.Audit(ctx, input, config.QualityCheck{ID: "audit", Directory: ".", Timeout: "1m", Image: image, NPMAudit: &config.NPMAuditPolicy{Level: "high"}}, "source", filepath.Join(auditTemp(t), "audit.log"), func(p core.Process) error { owner = p; return nil })
		}()
		select {
		case <-entered:
		case <-time.After(20 * time.Second):
			t.Fatal("audit never queried")
		}
		cancel()
		select {
		case r := <-done:
			if !r.Stopped || r.Status != "unavailable" || r.Audit == nil || r.Audit.Receipt.Status != "unavailable" || !Gone(owner) {
				t.Fatal("ownership released early", r, owner)
			}
		case <-time.After(15 * time.Second):
			t.Fatal("audit cancellation did not settle")
		}
		select {
		case <-settled:
		default:
			t.Fatal("network request remains owned")
		}
	})
	// This synthetic public graph is explicitly scoped qualification, never a
	// project audit. Record the registry's observed result instead of assuming it.
	t.Run("public registry", func(t *testing.T) {
		root := npmFixture(t)
		transport := &auditTransport{client: auditHTTPClient(), packages: map[string][]string{"lodash": {"4.17.20"}}, bulk: true}
		defer transport.client.CloseIdleConnections()
		metadata, err := transport.request(t.Context(), "package", json.RawMessage(`"lodash"`))
		if err != nil {
			t.Fatal(err)
		}
		var packument struct {
			Versions map[string]struct{ Dist struct{ Integrity string } }
		}
		if err = json.Unmarshal(metadata, &packument); err != nil {
			t.Fatal(err)
		}
		manifest := `{"dependencies":{"lodash":"4.17.20"}}`
		lock := `{"lockfileVersion":3,"packages":{"":{"dependencies":{"lodash":"4.17.20"}},"node_modules/lodash":{"version":"4.17.20","resolved":"https://registry.npmjs.org/lodash/-/lodash-4.17.20.tgz","integrity":"` + packument.Versions["4.17.20"].Dist.Integrity + `"}}}`
		if err = os.WriteFile(filepath.Join(root, "package.json"), []byte(manifest), 0600); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(root, "package-lock.json"), []byte(lock), 0600); err != nil {
			t.Fatal(err)
		}
		input, err := InspectNPMAudit(root, ".")
		if err != nil {
			t.Fatal(err)
		}
		r := (NPMAuditor{}).Audit(t.Context(), input, config.QualityCheck{ID: "public-audit", Directory: ".", Timeout: "1m", Image: image, NPMAudit: &config.NPMAuditPolicy{Level: "high"}}, "synthetic-public-graph", filepath.Join(auditTemp(t), "audit.log"), func(core.Process) error { return nil })
		if !r.Stopped || r.Status == "unavailable" || r.Audit == nil {
			t.Fatalf("live audit: %+v", r)
		}
		data, _ := json.MarshalIndent(r, "", "  ")
		if err := os.WriteFile(filepath.Join(filepath.Dir(r.Evidence), "observation.json"), data, 0600); err != nil {
			t.Fatal(err)
		}
		t.Logf("observed %s, npm %s, queried %s, findings %v, evidence %s", r.Status, r.Audit.Receipt.Tool, r.Audit.Receipt.QueriedAt, r.Audit.Receipt.Findings, r.Evidence)
	})
}

func npmOracle(t *testing.T, input AuditInput, check config.QualityCheck, vulnerable bool) (result struct {
	Report any
	Exit   int
}) {
	t.Helper()
	dir := auditTemp(t)
	for _, name := range []string{"input", "output", "docker-client"} {
		if err := os.Mkdir(filepath.Join(dir, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	boundary := bytes.Index(npmAuditEvaluator, []byte("\nasync function main()"))
	if boundary < 0 {
		t.Fatal("missing evaluator boundary")
	}
	script := append(append([]byte(nil), npmAuditEvaluator[:boundary]...), []byte(`
async function oracle() {
  const policy = JSON.parse(await fs.readFile('/input/policy.json', 'utf8'))
  const Arborist = req('@npmcli/arborist')
  const arb = new Arborist({ path: '/input', cache: '/tmp/npm-cache', registry: 'https://registry.npmjs.org/', workspacesEnabled: false, ignoreScripts: true, packageLock: true, packageLockOnly: true, audit: true, omit: policy.omit || [] })
  await arb.audit({ fix: false })
  if (arb.auditReport.error) throw arb.auditReport.error
  const cli = req('npm-audit-report')(arb.auditReport, { reporter: 'json', auditLevel: policy.level })
  await fs.writeFile('/output/report.json', JSON.stringify({ Report: JSON.parse(cli.report), Exit: cli.exitCode }))
}
oracle().catch(e => { process.stderr.write(String(e)); process.exitCode = 125 })
`)...)
	policy, _ := json.Marshal(check.NPMAudit)
	for name, data := range map[string][]byte{"package.json": input.Manifest, "package-lock.json": input.Lock, "adapter.cjs": script, "policy.json": policy} {
		if err := os.WriteFile(filepath.Join(dir, "input", name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	protocol := &auditProtocol{ctx: ctx, cancel: cancel, transport: registryFixture(input.Packages, vulnerable), dir: filepath.Join(dir, "output")}
	var log bytes.Buffer
	stopped, err := runAuditEvaluator(ctx, dir, check.Image, protocol, &log, func(core.Process) error { return nil })
	if err != nil || !stopped || protocol.err != nil {
		t.Fatal("npm oracle failed", err, protocol.err, log.String())
	}
	data, err := os.ReadFile(filepath.Join(dir, "output/report.json"))
	if err != nil || json.Unmarshal(data, &result) != nil {
		t.Fatal("npm oracle report missing", err)
	}
	return
}

func TestAuditQueryCancellationAndRequestAllowlist(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	tr := &auditTransport{packages: map[string][]string{"leaf": {"1.0.0"}}, client: &http.Client{Transport: auditRoundTrip(func(r *http.Request) (*http.Response, error) { return nil, errors.New("offline") })}}
	for _, request := range []struct{ kind, data string }{{"package", `"leaf"`}, {"bulk", `{"private":["1.0.0"]}`}, {"bulk", `{"leaf":["2.0.0"]}`}, {"url", `"https://registry.npmjs.org/"`}} {
		tr.bulk = false
		if _, err := tr.request(ctx, request.kind, json.RawMessage(request.data)); err == nil {
			t.Fatal("unapproved request", request)
		}
	}
}
