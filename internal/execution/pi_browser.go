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
	source := `import { randomBytes } from "node:crypto";
import { findPackageJSON } from "node:module";
import { dirname, join } from "node:path";
import { pathToFileURL } from "node:url";
import { createMcpExtension, getPackageDir } from "@earendil-works/pi-coding-agent";

// Resolve Pi's transport from its installation, never from the assignment.
const mcpPackage = findPackageJSON("@earendil-works/pi-mcp", pathToFileURL(join(getPackageDir(), "package.json")));
const { StdioTransport } = await import(pathToFileURL(join(dirname(mcpPackage), "dist/index.js")).href);
const browserCommand = ` + strconv.Quote(command) + `;
const browserArgs = ` + string(encodedArgs) + `;
const browserCwd = ` + strconv.Quote(runtimeDir) + `;
const browserEnv = ` + string(encodedEnvironment) + `;
const requestTimeoutMs = 30000;

export default function (pi) {
  const serverName = "runner_browser_" + randomBytes(8).toString("hex");
  const names = new Map(["navigate", "evaluate", "screenshot"].map(tool =>
    ["mcp__" + serverName + "__" + tool, "runner_browser_" + tool]));
  const transports = new Set();
  let closed = false;
  // A private factory must not replace Pi's /mcp manager or consume its catalog.
  // Operator-managed servers remain owned by the built-in extension in inherit mode.
  const browserAPI = {
    ...pi,
    getMcpServers: () => [],
    registerCommand: () => {},
    on(event, handler) {
      if (event === "before_agent_start") {
        pi.on(event, (input, ctx) => handler({ ...input,
          systemPromptOptions: { ...input.systemPromptOptions, sections: {} } }, ctx));
      } else pi.on(event, handler);
    },
    registerTool(definition) {
      const name = names.get(definition.name);
      if (!name) return;
      pi.registerTool({ ...definition, name,
        async execute(...args) {
          const result = await definition.execute(...args);
          return { ...result, details: { ...result.details, server: "runner_browser" } };
        },
      });
    },
  };
  createMcpExtension({
    loadConfig: () => ({ servers: [{ name: serverName, scope: "extension", source: browserCwd,
      config: { command: browserCommand, args: browserArgs, cwd: browserCwd, env: browserEnv,
        timeout: requestTimeoutMs / 1000, exposure: "hidden",
        toolExposure: { navigate: "direct", evaluate: "direct", screenshot: "direct" } },
    }], errors: [], autoEnableCodemode: false }),
    startupWaitMs: ` + strconv.Itoa(runnerBrowserStartupTimeoutSeconds*1000) + `,
    logPath: join(browserCwd, "mcp.log"),
    createTransport() {
      if (closed) throw new Error("Runner browser session is closed.");
      const transport = new StdioTransport({ command: browserCommand, args: browserArgs,
        cwd: browserCwd, env: browserEnv, maxStderrBytes: 4000, closeTimeoutMs: 2000 });
      transports.add(transport);
      return transport;
    },
  })(browserAPI);

  pi.on("tool_call", event => {
    if (event.toolName !== "runner_browser_navigate") return;
    let url;
    try { url = new URL(event.input.url); }
    catch { return { block: true, reason: "Runner browser URL is invalid." }; }
    if (url.protocol !== "http:" || (url.hostname !== "localhost" && url.hostname !== "127.0.0.1")) {
      return { block: true, reason: "Runner browser navigation is restricted to http://localhost and http://127.0.0.1." };
    }
  });
  // Pi 1.0's MCP manager owns initialized clients only. Close transports too,
  // so shutdown while initialize/tools/list is pending cannot leave a child alive.
  pi.on("session_shutdown", async () => {
    closed = true;
    await Promise.all([...transports].map(transport => transport.close()));
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
