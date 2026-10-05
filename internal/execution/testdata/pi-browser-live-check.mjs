import assert from "node:assert/strict";
import { relative, isAbsolute } from "node:path";
import { createServer } from "node:http";
import { readFile, unlink } from "node:fs/promises";
import { openSession } from "./pi-session-fixture.mjs";

const [packageDir, extensionPath, artifactDir] = process.argv.slice(2);
const server = createServer((_request, response) => {
  response.setHeader("Content-Type", "text/html");
  response.end("<html><head><title>Runner Pi browser</title></head><body><h1>Local proof</h1></body></html>");
});
await new Promise(resolve => server.listen(0, "127.0.0.1", resolve));
const s = await openSession(packageDir, [extensionPath],
  ["runner_browser_navigate", "runner_browser_evaluate", "runner_browser_screenshot"]);
try {
  await s.ready();
  const navigate = await s.call("runner_browser_navigate", { url: "http://127.0.0.1:" + server.address().port + "/" });
  assert.equal(navigate.isError, false, JSON.stringify(navigate));
  const evaluated = await s.call("runner_browser_evaluate", { script: "document.title" });
  assert.equal(evaluated.isError, false, JSON.stringify(evaluated));
  assert.match(JSON.stringify(evaluated.content), /Runner Pi browser/);
  const screenshot = await s.call("runner_browser_screenshot", {});
  assert.equal(screenshot.isError, false, JSON.stringify(screenshot));
  const path = screenshot.content.find(block => block.type === "text").text.trim();
  const artifactRelative = relative(artifactDir, path);
  assert.ok(!artifactRelative.startsWith("..") && !isAbsolute(artifactRelative), "screenshot escaped worker scratch: " + path);
  const png = await readFile(path);
  assert.equal(png.subarray(0, 8).toString("hex"), "89504e470d0a1a0a");
  assert.ok(png.length > 100);
  await unlink(path);
} finally { await s.close(); await new Promise(resolve => server.close(resolve)); }
