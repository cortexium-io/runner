package execution

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/securefs"
)

const piBrowserExtensionName = "browser-extension.ts"

var piBrowserToolNames = []string{
	"runner_browser_navigate",
	"runner_browser_evaluate",
	"runner_browser_screenshot",
}

type piBrowserChannel struct {
	artifacts *securefs.ArtifactSet
	path      string
	runtime   string
}

func (c *piBrowserChannel) Close() error {
	return errors.Join(c.artifacts.Close(), os.RemoveAll(c.runtime))
}

func (c *piBrowserChannel) Verify() error {
	return c.artifacts.VerifyImmutable(piBrowserExtensionName)
}

func createPiBrowserExtension() (*piBrowserChannel, error) {
	command, args := runnerBrowserCommand()
	runtimeDir, err := newTrustedToolDir()
	if err != nil {
		return nil, fmt.Errorf("create Pi browser runtime: %w", err)
	}
	encodedArgs, err := json.Marshal(args)
	if err != nil {
		_ = os.RemoveAll(runtimeDir)
		return nil, fmt.Errorf("encode Pi browser command: %w", err)
	}
	encodedEnvironment, err := json.Marshal(runnerBrowserEnvironment(runtimeDir))
	if err != nil {
		_ = os.RemoveAll(runtimeDir)
		return nil, fmt.Errorf("encode Pi browser environment: %w", err)
	}
	source := `import { findPackageJSON } from "node:module";
import { dirname, join } from "node:path";
import { pathToFileURL } from "node:url";
import { getPackageDir } from "@earendil-works/pi-coding-agent";
import { Type } from "typebox";

// Resolve Pi's own dependency, not a package from the assignment or operator catalog.
let mcpPackage;
try {
  mcpPackage = findPackageJSON("@earendil-works/pi-mcp", pathToFileURL(join(getPackageDir(), "package.json")));
} catch (error) {
  throw new Error("Runner browser requires the Pi 0.99.1+ Node package with its bundled MCP client.", { cause: error });
}
const { McpClient, StdioTransport } = await import(pathToFileURL(join(dirname(mcpPackage), "dist/index.js")).href);

const browserCommand = ` + strconv.Quote(command) + `;
const browserArgs = ` + string(encodedArgs) + `;
const browserCwd = ` + strconv.Quote(runtimeDir) + `;
const browserEnv = ` + string(encodedEnvironment) + `;
const requestTimeoutMs = 30000;

export default function (pi) {
  let client;
  let transport;
  let startup;
  let closing;
  let closed = false;

  function closeClient() {
    if (client && !closing) closing = client.close();
    return closing;
  }

  function browserError(error) {
    const detail = transport?.stderr.trim();
    return detail ? new Error((error instanceof Error ? error.message : String(error)) + ": " + detail, { cause: error }) : error;
  }

  async function ensureStarted() {
    if (closed) throw new Error("Runner browser session is closed.");
    if (startup) return startup;
    closing = undefined;
    client = new McpClient({ name: "cortexium-runner-pi", version: "1", requestTimeoutMs });
    transport = new StdioTransport({
      command: browserCommand,
      args: browserArgs,
      cwd: browserCwd,
      env: browserEnv,
      maxStderrBytes: 4000,
      closeTimeoutMs: 2000,
    });
    startup = client.connect(transport);
    try {
      await startup;
    } catch (error) {
      await closeClient();
      startup = undefined;
      throw browserError(error);
    }
  }

  async function call(name, args, signal) {
    signal?.throwIfAborted();
    // Cancellation also closes an in-flight initialization; callTool handles
    // cancellation after initialization without discarding a healthy connection.
    const abortStartup = () => { void closeClient(); };
    signal?.addEventListener("abort", abortStartup, { once: true });
    try {
      await ensureStarted();
    } finally {
      signal?.removeEventListener("abort", abortStartup);
      if (signal?.aborted) await closing;
    }
    signal?.throwIfAborted();
    let result;
    try {
      result = await client.callTool(name, args, { signal, timeoutMs: requestTimeoutMs });
    } catch (error) {
      throw browserError(error);
    }
    if (result?.isError) {
      const detail = Array.isArray(result.content)
        ? result.content.filter((item) => item?.type === "text").map((item) => item.text).join("\n")
        : "";
      throw new Error(detail || "Runner browser tool failed: " + name);
    }
    return {
      content: Array.isArray(result?.content)
        ? result.content
        : [{ type: "text", text: "Runner browser completed " + name + "." }],
      details: { server: "runner_browser", tool: name },
    };
  }

  pi.registerTool({
    name: "runner_browser_navigate",
    label: "Runner browser: navigate",
    description: "Navigate the isolated Runner browser to a loopback HTTP page.",
    parameters: Type.Object({ url: Type.String() }, { additionalProperties: false }),
    async execute(_toolCallId, params, signal) {
      let url;
      try {
        url = new URL(params.url);
      } catch {
        throw new Error("Runner browser URL is invalid.");
      }
      if (url.protocol !== "http:" || (url.hostname !== "localhost" && url.hostname !== "127.0.0.1")) {
        throw new Error("Runner browser navigation is restricted to http://localhost and http://127.0.0.1.");
      }
      return call("navigate", { url: url.href }, signal);
    },
  });

  pi.registerTool({
    name: "runner_browser_evaluate",
    label: "Runner browser: evaluate",
    description: "Evaluate JavaScript in the current isolated Runner browser page.",
    parameters: Type.Object({ script: Type.String() }, { additionalProperties: false }),
    async execute(_toolCallId, params, signal) {
      return call("evaluate", { script: params.script }, signal);
    },
  });

  pi.registerTool({
    name: "runner_browser_screenshot",
    label: "Runner browser: screenshot",
    description: "Capture the current isolated Runner browser page.",
    parameters: Type.Object({}, { additionalProperties: false }),
    async execute(_toolCallId, _params, signal) {
      return call("screenshot", {}, signal);
    },
  });

  pi.on("session_shutdown", async () => {
    closed = true;
    await closeClient();
  });
}
`
	artifacts, err := securefs.NewArtifactSet("cortexium-runner-pi-browser", []securefs.ArtifactFile{{
		Name: piBrowserExtensionName, Content: []byte(source),
	}})
	if err != nil {
		_ = os.RemoveAll(runtimeDir)
		return nil, fmt.Errorf("create Pi browser extension: %w", err)
	}
	return &piBrowserChannel{artifacts: artifacts, path: artifacts.Path(piBrowserExtensionName), runtime: runtimeDir}, nil
}

func piInvocationAllowsBrowser(args []string, ambientToolsAllowed ...bool) bool {
	if len(ambientToolsAllowed) > 0 && ambientToolsAllowed[0] {
		return true
	}
	for index := 0; index+1 < len(args); index++ {
		if args[index] != "--tools" {
			continue
		}
		for _, tool := range strings.Split(args[index+1], ",") {
			if strings.TrimSpace(tool) == "bash" {
				return true
			}
		}
	}
	return false
}

func addPiBrowserExtension(args []string, path string) ([]string, error) {
	return addPiBrowserExtensionForConfig(args, path, config.HarnessConfigModeIsolated)
}

func addPiBrowserExtensionForConfig(args []string, path, harnessConfigMode string) ([]string, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("Pi browser invocation requires an extension path")
	}
	result := append([]string(nil), args...)
	added := false
	for index := 0; index+1 < len(result); index++ {
		if result[index] != "--tools" {
			continue
		}
		for _, name := range piBrowserToolNames {
			if !containsCSVValue(result[index+1], name) {
				result[index+1] += "," + name
			}
		}
		added = true
		break
	}
	if !added {
		if inheritsHarnessConfiguration(harnessConfigMode) {
			return append(result, "--extension", path), nil
		}
		return nil, errors.New("Pi browser invocation requires an explicit tool allowlist")
	}
	return append(result, "--extension", path), nil
}

func containsCSVValue(value, expected string) bool {
	for _, candidate := range strings.Split(value, ",") {
		if strings.TrimSpace(candidate) == expected {
			return true
		}
	}
	return false
}
