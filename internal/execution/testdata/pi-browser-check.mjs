import assert from "node:assert/strict";
import { dirname, join } from "node:path";
import { pathToFileURL } from "node:url";
import { mkdtemp, readdir, rmdir, stat } from "node:fs/promises";
import { tmpdir } from "node:os";

const [packageDir, extensionPath, marker] = process.argv.slice(2);
const { discoverAndLoadExtensions } = await import(pathToFileURL(join(packageDir, "dist/index.js")).href);
const neutral = await mkdtemp(join(tmpdir(), "runner-pi-browser-check-"));
try {
  const loaded = await discoverAndLoadExtensions([extensionPath], neutral, neutral);
  assert.deepEqual(loaded.errors, []);
  assert.equal(loaded.extensions.length, 1);
  const extension = loaded.extensions[0];
  assert.deepEqual([...extension.tools.keys()], ["runner_browser_navigate", "runner_browser_evaluate", "runner_browser_screenshot"]);
  await assert.rejects(stat(marker), { code: "ENOENT" });
  const call = (name, params = {}, signal) => extension.tools.get("runner_browser_" + name).definition.execute("check", params, signal);
  const shutdown = async () => {
    for (const handler of extension.handlers.get("session_shutdown") ?? []) await handler({}, {});
  };
  try {
    for (const url of ["invalid", "https://localhost", "http://example.com", "file:///tmp/page"]) {
      await assert.rejects(call("navigate", { url }), /invalid|restricted/);
    }
    const aborted = AbortSignal.abort();
    await assert.rejects(call("screenshot", {}, aborted), { name: "AbortError" });
    await assert.rejects(stat(marker), { code: "ENOENT" });
    const result = await call("navigate", { url: "http://127.0.0.1:8123/" });
    assert.deepEqual(result.details, { server: "runner_browser", tool: "navigate" });
    const forwarded = JSON.parse(result.content[0].text);
    assert.deepEqual(forwarded.args, { url: "http://127.0.0.1:8123/" });
    assert.equal(forwarded.name, "navigate");
    // Package cache is shared between private invocation runtimes, never with
    // the assignment or the operator's ambient npm configuration.
    assert.equal(forwarded.cache, join(dirname(forwarded.cwd), "npm-cache"));
    await assert.rejects(call("evaluate", { script: "fail" }), /fixture tool error/);
    const controller = new AbortController();
    const pending = call("evaluate", { script: "hang" }, controller.signal);
    const rejection = assert.rejects(pending, /abort|cancel/i);
    // The screenshot response is a protocol barrier: the server processed the
    // hanging call first, so this proves cancellation of an in-flight request.
    const screenshot = await call("screenshot");
    const state = JSON.parse(screenshot.content[0].text);
    assert.equal(state.hanging, true);
    assert.equal(screenshot.content[1].type, "image");
    controller.abort();
    await rejection;
    assert.equal(JSON.parse((await call("screenshot")).content[0].text).hanging, false);
    await assert.rejects(call("evaluate", { script: "hang" }), /timeout|timed out/i);
    await assert.rejects(call("evaluate", { script: "exit" }), /fixture server exited/);
    await shutdown();
    assert.throws(() => process.kill(state.pid, 0), { code: "ESRCH" });
    await assert.rejects(call("screenshot"), /session is closed/);
  } finally {
    await shutdown();
  }
  // Loading and execution must not populate a project MCP catalog or session.
  assert.deepEqual(await readdir(neutral), []);
} finally {
  await rmdir(neutral);
}
