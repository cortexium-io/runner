import assert from "node:assert/strict";
import { basename, dirname, join } from "node:path";
import { pathToFileURL } from "node:url";
import { watch } from "node:fs";
import { mkdtemp, readFile, rmdir } from "node:fs/promises";
import { tmpdir } from "node:os";

const [packageDir, extensionPath, marker] = process.argv.slice(2);
const { discoverAndLoadExtensions } = await import(pathToFileURL(join(packageDir, "dist/index.js")).href);
const neutral = await mkdtemp(join(tmpdir(), "runner-pi-startup-check-"));
let extension;
let watcher;
try {
  const loaded = await discoverAndLoadExtensions([extensionPath], neutral, neutral);
  assert.deepEqual(loaded.errors, []);
  extension = loaded.extensions[0];
  const controller = new AbortController();
  let observed;
  const started = new Promise((resolve) => { observed = resolve; });
  watcher = watch(dirname(marker), (_, file) => {
    if (file === basename(marker)) observed();
  });
  const pending = extension.tools.get("runner_browser_screenshot").definition.execute("check", {}, controller.signal);
  const rejected = assert.rejects(pending, /abort|cancel|closed/i);
  // The fixture publishes this marker while withholding initialize's reply.
  await started;
  const pid = Number(await readFile(marker, "utf8"));
  controller.abort();
  await rejected;
  assert.throws(() => process.kill(pid, 0), { code: "ESRCH" });
} finally {
  watcher?.close();
  for (const handler of extension?.handlers.get("session_shutdown") ?? []) await handler({}, {});
  await rmdir(neutral);
}
