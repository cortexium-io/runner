import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { openSession, waitForMarker } from "./pi-session-fixture.mjs";

const [packageDir, extensionPath, marker] = process.argv.slice(2);
const s = await openSession(packageDir, [extensionPath], ["runner_browser_screenshot"]);
try {
  // This fixture withholds initialize. Shutdown must kill its child before
  // returning, without waiting for the request deadline to expire.
  await waitForMarker(marker);
  const pid = Number(await readFile(marker, "utf8"));
  await s.close();
  assert.throws(() => process.kill(pid, 0), { code: "ESRCH" });
} finally { await s.close(); }
