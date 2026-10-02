package execution

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/config"
)

func TestPiBrowserExtensionUsesPinnedIsolatedLoopbackServer(t *testing.T) {
	channel, err := createPiBrowserExtension()
	if err != nil {
		t.Fatalf("create Pi browser extension: %v", err)
	}
	defer channel.Close()
	content, err := os.ReadFile(channel.path)
	if err != nil {
		t.Fatalf("read Pi browser extension: %v", err)
	}
	source := string(content)
	if !strings.Contains(source, "const browserCwd = ") || !strings.Contains(source, "NPM_CONFIG_CACHE") || !strings.Contains(source, "cwd: browserCwd") {
		t.Fatalf("Pi browser extension omitted private package runtime: %s", source)
	}
	for _, required := range []string{
		`chrome-devtools-mcp@1.7.0`, `--headless`, `--isolated`, `--slim`,
		`--allowed-url-pattern=http://localhost:*/*`, `--allowed-url-pattern=http://127.0.0.1:*/*`,
		`--host-resolver-rules=MAP * ~NOTFOUND, EXCLUDE localhost, EXCLUDE 127.0.0.1`,
		`--use-mock-keychain`, `--no-usage-statistics`,
		`url.hostname !== "localhost" && url.hostname !== "127.0.0.1"`,
		`findPackageJSON("@earendil-works/pi-mcp"`, `createMcpExtension({`, `new StdioTransport(`,
		`const requestTimeoutMs = 30000`, `timeout: requestTimeoutMs / 1000`, `closeTimeoutMs: 2000`,
		`getMcpServers: () => []`, `registerCommand: () => {}`, `sections: {}`,
		`transport.close()`, `autoEnableCodemode: false`,
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("Pi browser extension omitted %q:\n%s", required, source)
		}
	}
	for _, forbidden := range []string{`createInterface`, `spawn(`, `registerMcpServer`, `McpClient`, `mcp.json`} {
		if strings.Contains(source, forbidden) {
			t.Fatalf("Pi browser extension unexpectedly contains %q", forbidden)
		}
	}
	if err := channel.Verify(); err != nil {
		t.Fatalf("verify Pi browser extension: %v", err)
	}
}

func TestPiBrowserExtensionAddsOnlyExplicitBrowserTools(t *testing.T) {
	base := []string{"--no-extensions", "--tools", "read,grep,find,ls,bash,cortexium_runner_result"}
	args, err := addPiBrowserExtension(base, "/tmp/browser.ts")
	if err != nil {
		t.Fatalf("add Pi browser extension: %v", err)
	}
	wantTools := "read,grep,find,ls,bash,cortexium_runner_result," + strings.Join(piBrowserToolNames, ",")
	if !containsArgPair(args, "--tools", wantTools) || !containsArgPair(args, "--extension", "/tmp/browser.ts") {
		t.Fatalf("Pi browser args = %#v", args)
	}
	if !piInvocationAllowsBrowser(args) {
		t.Fatalf("Pi browser-capable invocation was not detected: %#v", args)
	}
	if piInvocationAllowsBrowser([]string{"--tools", "read,grep,find,ls"}) {
		t.Fatal("read-only Pi invocation unexpectedly received browser tools")
	}
}

func TestPiBrowserExtensionRequiresExplicitToolAllowlist(t *testing.T) {
	if _, err := addPiBrowserExtension([]string{"--no-tools"}, "/tmp/browser.ts"); err == nil {
		t.Fatal("Pi browser extension accepted an invocation without an explicit tool allowlist")
	}
}

func TestPiBrowserExtensionCanJoinExplicitInheritedConfiguration(t *testing.T) {
	args, err := addPiBrowserExtensionForConfig([]string{"--no-session"}, "/tmp/browser.ts", config.HarnessConfigModeInherit)
	if err != nil {
		t.Fatalf("add Pi browser to inherited config: %v", err)
	}
	if !containsArgPair(args, "--extension", "/tmp/browser.ts") || contains(args, "--tools") || !piInvocationAllowsBrowser(args, true) {
		t.Fatalf("inherited Pi browser args = %#v", args)
	}
}

// No model, browser, package download, or operator MCP catalog is used. Pi's
// installed loader and MCP client talk only to our deterministic stdio fixture.
func TestInstalledPiBrowserNativeMCP(t *testing.T) {
	packageDir, path, marker := installedPiBrowserFixture(t, false)
	check := exec.CommandContext(t.Context(), "node", "testdata/pi-browser-check.mjs", packageDir, path, marker)
	check.Env = append(os.Environ(), "PI_OFFLINE=1")
	if output, err := check.CombinedOutput(); err != nil {
		t.Fatalf("native Pi MCP check: %v\n%s", err, output)
	}
}

func TestInstalledPiBrowserStartupCancellation(t *testing.T) {
	packageDir, path, marker := installedPiBrowserFixture(t, true)
	check := exec.CommandContext(t.Context(), "node", "testdata/pi-browser-startup-check.mjs", packageDir, path, marker)
	check.Env = append(os.Environ(), "PI_OFFLINE=1")
	if output, err := check.CombinedOutput(); err != nil {
		t.Fatalf("native Pi MCP startup cancellation: %v\n%s", err, output)
	}
}

func TestLivePiBrowser(t *testing.T) {
	if os.Getenv("CORTEXIUM_RUNNER_TEST_PI_LIVE_BROWSER") != "1" {
		t.Skip("set CORTEXIUM_RUNNER_TEST_PI_LIVE_BROWSER=1 to launch the pinned server and headless Chrome")
	}
	packageDir := installedPiPackageDir(t)
	channel, err := createPiBrowserExtension()
	if err != nil {
		t.Fatal(err)
	}
	defer channel.Close()
	check := exec.CommandContext(t.Context(), "node", "testdata/pi-browser-live-check.mjs", packageDir, channel.path)
	check.Env = append(os.Environ(), "PI_OFFLINE=1")
	if output, err := check.CombinedOutput(); err != nil {
		t.Fatalf("live Pi browser check: %v\n%s", err, output)
	}
}

func installedPiBrowserFixture(t *testing.T, holdInitialization bool) (packageDir, path, marker string) {
	t.Helper()
	packageDir = installedPiPackageDir(t)
	channel, err := createPiBrowserExtension()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = channel.Close() })
	source, err := os.ReadFile(channel.path)
	if err != nil {
		t.Fatal(err)
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := filepath.Abs("testdata/pi-browser-server.mjs")
	if err != nil {
		t.Fatal(err)
	}
	command, args := runnerBrowserCommand()
	encodedArgs, _ := json.Marshal(args)
	marker = filepath.Join(t.TempDir(), "server-started")
	serverArgs := []string{fixture, marker}
	if holdInitialization {
		serverArgs = append(serverArgs, "hold-init")
	}
	fixtureArgs, _ := json.Marshal(serverArgs)
	// Only replace the server executable and shorten its request deadline. All
	// registration, restrictions, forwarding, cancellation, and shutdown are real.
	source = []byte(strings.NewReplacer(
		"const browserCommand = "+strconv.Quote(command), "const browserCommand = "+strconv.Quote(node),
		"const browserArgs = "+string(encodedArgs), "const browserArgs = "+string(fixtureArgs),
		"const requestTimeoutMs = 30000", "const requestTimeoutMs = 2000",
	).Replace(string(source)))
	path = filepath.Join(t.TempDir(), "browser-extension.ts")
	if err := os.WriteFile(path, source, 0o600); err != nil {
		t.Fatal(err)
	}
	return packageDir, path, marker
}

func installedPiPackageDir(t *testing.T) string {
	t.Helper()
	if os.Getenv("CORTEXIUM_RUNNER_TEST_PI_EXTENSION_LOAD") != "1" {
		t.Skip("set CORTEXIUM_RUNNER_TEST_PI_EXTENSION_LOAD=1 for the local native MCP check")
	}
	pi, err := exec.LookPath("pi")
	if err != nil {
		t.Fatal(err)
	}
	pi, err = filepath.EvalSymlinks(pi)
	if err != nil {
		t.Fatal(err)
	}
	packageDir := filepath.Dir(pi)
	for {
		content, _ := os.ReadFile(filepath.Join(packageDir, "package.json"))
		var pkg struct{ Name string }
		if json.Unmarshal(content, &pkg) == nil && pkg.Name == "@earendil-works/pi-coding-agent" {
			return packageDir
		}
		parent := filepath.Dir(packageDir)
		if parent == packageDir {
			t.Fatal("cannot find installed Pi package")
		}
		packageDir = parent
	}
}

func TestInstalledPiResultExtensions(t *testing.T) {
	packageDir := installedPiPackageDir(t)
	schema := []byte(`{"type":"object","properties":{"outcome":{"type":"string"}},"required":["outcome"],"additionalProperties":false}`)
	structured, err := createPiStructuredResultExtension(schema, "require")
	if err != nil {
		t.Fatal(err)
	}
	defer structured.Close()
	native, err := createPiNativeStructuredResultExtension(schema, "medium", false, true)
	if err != nil {
		t.Fatal(err)
	}
	defer native.Close()
	direct, err := createPiDirectNativeStructuredResultExtension(schema, true)
	if err != nil {
		t.Fatal(err)
	}
	defer direct.Close()
	check := exec.CommandContext(t.Context(), "node", "testdata/pi-result-check.mjs", packageDir, structured.path, native.path, direct.path)
	check.Env = append(os.Environ(), "PI_OFFLINE=1", "PI_EXPERIMENTAL=1")
	if output, err := check.CombinedOutput(); err != nil {
		t.Fatalf("installed Pi result-extension check: %v\n%s", err, output)
	}
}
