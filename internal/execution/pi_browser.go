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
	source := `import { spawn } from "node:child_process";
import { createInterface } from "node:readline";
import { Type } from "typebox";

const browserCommand = ` + strconv.Quote(command) + `;
const browserArgs = ` + string(encodedArgs) + `;
const browserCwd = ` + strconv.Quote(runtimeDir) + `;
const browserEnv = ` + string(encodedEnvironment) + `;
const requestTimeoutMs = 30000;

export default function (pi) {
  let child;
  let startup;
  let nextId = 1;
  let stderr = "";
  const pending = new Map();

  function stop(error) {
    for (const request of pending.values()) {
      clearTimeout(request.timer);
      request.reject(error);
    }
    pending.clear();
  }

  function send(message) {
    if (!child?.stdin?.writable) throw new Error("Runner browser process is unavailable.");
    child.stdin.write(JSON.stringify(message) + "\n");
  }

  function request(method, params, signal) {
    const id = nextId++;
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => {
        pending.delete(id);
        reject(new Error("Runner browser request timed out: " + method));
      }, requestTimeoutMs);
      const abort = () => {
        clearTimeout(timer);
        pending.delete(id);
        reject(new Error("Runner browser request was cancelled: " + method));
      };
      if (signal?.aborted) return abort();
      signal?.addEventListener("abort", abort, { once: true });
      pending.set(id, {
        timer,
        resolve(value) {
          signal?.removeEventListener("abort", abort);
          resolve(value);
        },
        reject(error) {
          signal?.removeEventListener("abort", abort);
          reject(error);
        },
      });
      try {
        send({ jsonrpc: "2.0", id, method, params });
      } catch (error) {
        clearTimeout(timer);
        pending.delete(id);
        signal?.removeEventListener("abort", abort);
        reject(error);
      }
    });
  }

  async function ensureStarted() {
    if (startup) return startup;
    startup = (async () => {
      child = spawn(browserCommand, browserArgs, {
        shell: false,
        stdio: ["pipe", "pipe", "pipe"],
		cwd: browserCwd,
		env: { ...process.env, ...browserEnv },
      });
      child.stderr.on("data", (chunk) => {
        stderr = (stderr + String(chunk)).slice(-4000);
      });
      createInterface({ input: child.stdout }).on("line", (line) => {
        let message;
        try {
          message = JSON.parse(line);
        } catch {
          stop(new Error("Runner browser returned malformed protocol output."));
          return;
        }
        if (typeof message.id !== "number") return;
        const active = pending.get(message.id);
        if (!active) return;
        clearTimeout(active.timer);
        pending.delete(message.id);
        if (message.error) {
          active.reject(new Error("Runner browser protocol error: " + String(message.error.message || message.error.code)));
        } else {
          active.resolve(message.result);
        }
      });
      child.once("error", (error) => stop(error));
      child.once("exit", (code) => {
        const detail = stderr.trim();
        stop(new Error("Runner browser exited" + (code == null ? "" : " with status " + code) + (detail ? ": " + detail : ".")));
      });
      await request("initialize", {
        protocolVersion: "2025-11-25",
        capabilities: {},
        clientInfo: { name: "cortexium-runner-pi", version: "1" },
      });
      send({ jsonrpc: "2.0", method: "notifications/initialized", params: {} });
    })();
    try {
      await startup;
    } catch (error) {
      startup = undefined;
      throw error;
    }
  }

  async function call(name, args, signal) {
    await ensureStarted();
    const result = await request("tools/call", { name, arguments: args }, signal);
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
    if (!child || child.exitCode !== null) return;
    const closed = new Promise((resolve) => child.once("close", resolve));
    child.stdin.end();
    await Promise.race([closed, new Promise((resolve) => setTimeout(resolve, 2000))]);
    if (child.exitCode === null) child.kill("SIGTERM");
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
