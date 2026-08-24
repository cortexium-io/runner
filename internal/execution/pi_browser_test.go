package execution

import (
	"os"
	"os/exec"
	"strings"
	"testing"
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
	for _, required := range []string{
		`chrome-devtools-mcp@1.7.0`, `--headless`, `--isolated`, `--slim`,
		`--allowed-url-pattern=http://localhost:*/*`, `--allowed-url-pattern=http://127.0.0.1:*/*`,
		`--host-resolver-rules=MAP * ~NOTFOUND, EXCLUDE localhost, EXCLUDE 127.0.0.1`,
		`--use-mock-keychain`, `--no-usage-statistics`,
		`url.hostname !== "localhost" && url.hostname !== "127.0.0.1"`,
		`method: "notifications/initialized"`, `child.stdin.end()`, `child.kill("SIGTERM")`,
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("Pi browser extension omitted %q:\n%s", required, source)
		}
	}
	for _, tool := range piBrowserToolNames {
		if strings.Count(source, `name: "`+tool+`"`) != 1 {
			t.Fatalf("Pi browser extension must register %q exactly once", tool)
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

func TestInstalledPiLoadsBrowserExtension(t *testing.T) {
	if os.Getenv("CORTEXIUM_RUNNER_TEST_PI_EXTENSION_LOAD") != "1" {
		t.Skip("set CORTEXIUM_RUNNER_TEST_PI_EXTENSION_LOAD=1 for the local Pi extension-load smoke")
	}
	channel, err := createPiBrowserExtension()
	if err != nil {
		t.Fatalf("create Pi browser extension: %v", err)
	}
	defer channel.Close()
	command := exec.Command("pi", "--no-extensions", "--no-skills", "--no-prompt-templates", "--no-themes", "--no-context-files", "--no-approve", "--extension", channel.path, "--list-models")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("installed Pi could not load Runner browser extension: %v\n%s", err, output)
	}
}
