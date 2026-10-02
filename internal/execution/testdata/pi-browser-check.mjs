import assert from "node:assert/strict";
import { dirname, join } from "node:path";
import { pathToFileURL } from "node:url";
import { writeFile, readFile, access, mkdtemp, mkdir, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { openSession, waitForMarker } from "./pi-session-fixture.mjs";

const [packageDir, extensionPath, marker] = process.argv.slice(2);
const names = ["navigate", "evaluate", "screenshot"].map(name => "runner_browser_" + name);
// A real built-in manager and two project catalog entries prove coexistence.
// The private browser must neither replace /mcp nor erase the server prompt.
const project = await mkdtemp(join(tmpdir(), "runner-pi-inherit-"));
const ambientMarker = join(project, "ambient");
await mkdir(join(project, ".pi"));
const sdk = await import(pathToFileURL(join(packageDir, "dist/index.js")).href);
const fixture = join(import.meta.dirname, "pi-browser-server.mjs");
await writeFile(join(project, ".pi", "mcp.json"), JSON.stringify({ autoEnableCodemode: false, mcpServers: {
  ambient: { command: process.execPath, args: [fixture, ambientMarker], exposure: "direct" },
  discoverable: { command: process.execPath, args: [fixture, join(project, "discoverable")], exposure: "codemode" },
} }));

const s = await openSession(packageDir, [extensionPath], names, [], project);
const call = (name, params, signal) => s.call("runner_browser_" + name, params, signal);
try {
  await s.ready();
  await assert.rejects(access(ambientMarker), { code: "ENOENT" });
  assert.deepEqual(s.session.getActiveToolNames().sort(), names.sort());
  const pid = Number(await readFile(marker, "utf8"));
  for (const url of ["invalid", "https://localhost", "http://example.com", "file:///tmp/page"]) {
    const result = await call("navigate", { url });
    assert.equal(result.isError, true);
    assert.match(result.content[0].text, /invalid|restricted/);
  }
  const result = await call("navigate", { url: "http://127.0.0.1:8123/" });
  assert.equal(result.isError, false);
  assert.deepEqual(result.details, { server: "runner_browser", tool: "navigate" });
  const forwarded = JSON.parse(result.content[0].text);
  assert.deepEqual(forwarded.args, { url: "http://127.0.0.1:8123/" });
  assert.equal(forwarded.cache, join(dirname(forwarded.cwd), "npm-cache"));
  const failed = await call("evaluate", { script: "fail" });
  assert.equal(failed.isError, true);
  assert.match(failed.content[0].text, /fixture tool error/);
  const controller = new AbortController();
  const hanging = waitForMarker(marker + ".hang");
  const pending = call("evaluate", { script: "hang" }, controller.signal);
  await hanging;
  controller.abort();
  const cancelled = await pending;
  assert.equal(cancelled.isError, true);
  assert.match(cancelled.content[0].text, /abort|cancel/i);
  const screenshot = await call("screenshot");
  assert.equal(JSON.parse(screenshot.content[0].text).hanging, false);
  assert.equal(screenshot.content[1].type, "image");
  const timedOut = await call("evaluate", { script: "hang" });
  assert.equal(timedOut.isError, true);
  assert.match(timedOut.content[0].text, /timeout|timed out/i);
  const exited = await call("evaluate", { script: "exit" });
  assert.equal(exited.isError, true);
  assert.match(exited.content[0].text, /closed|exit|connection/i);
  await s.close();
  assert.throws(() => process.kill(pid, 0), { code: "ESRCH" });
} finally { await s.close(); }

// A real built-in manager must coexist with Runner’s private browser.
const inherited = await openSession(packageDir, [extensionPath], undefined,
  [{ name: "mcp", factory: sdk.createMcpExtension({ logPath: join(project, "mcp.log") }), builtin: true, replaceable: true }], project);
try {
  const sections = await inherited.ready();
  await access(ambientMarker);
  assert.ok(inherited.session.getActiveToolNames().includes("mcp__ambient__navigate"));
  assert.ok(inherited.session.getActiveToolNames().includes("runner_browser_navigate"));
  assert.match(sections.mcp_servers, /discoverable/);
  assert.equal(inherited.session.extensionRunner.getRegisteredCommands().filter(command => command.name === "mcp").length, 1);
  assert.equal((await inherited.call("mcp__ambient__navigate", { url: "http://operator.example" })).isError, false);
  assert.equal((await inherited.call("runner_browser_navigate", { url: "http://operator.example" })).isError, true);
} finally { await inherited.close(); await rm(project, { recursive: true, force: true }); }
