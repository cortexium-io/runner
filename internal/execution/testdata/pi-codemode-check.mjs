import assert from "node:assert/strict";
import { join } from "node:path";
import { writeFile, readFile } from "node:fs/promises";
import { openSession } from "./pi-session-fixture.mjs";

const [packageDir, codePath, browserPath, marker, resultPath, provenance, finalizePath] = process.argv.slice(2);
const tools = ["read", "codemode", "cortexium_runner_result", "cortexium_runner_finalize",
  "runner_browser_navigate", "runner_browser_evaluate", "runner_browser_screenshot"];
const s = await openSession(packageDir, [codePath, browserPath, resultPath, finalizePath], tools);
try {
  await s.ready();
  await writeFile(join(s.root, "left.json"), JSON.stringify({ value: 3, noise: "do not forward" }));
  await writeFile(join(s.root, "right.json"), JSON.stringify({ value: 4, noise: "do not forward" }));
  const batch = await s.call("codemode", { code: `
    if (typeof models !== "undefined") throw new Error("Model calls enabled");
    const forbidden = ["write", "edit", "bash", "cortexium_runner_result", "cortexium_runner_finalize"];
    if (ALL_TOOLS.some(tool => forbidden.includes(tool.name) || tool.name.endsWith("delete_files")))
      throw new Error("Forbidden tool exposed");
    const values = await Promise.all([tools.read({path:"left.json"}), tools.read({path:"right.json"})]);
    text({sum: values.map(value => JSON.parse(value).value).reduce((a,b) => a+b,0)});
  ` });
  assert.equal(batch.isError, false, JSON.stringify(batch));
  assert.match(JSON.stringify(batch.content), /sum.*7/);
  assert.doesNotMatch(JSON.stringify(batch.content), /do not forward/);
  assert.deepEqual(batch.details.calls.map(call => call.name), ["read", "read"]);
  for (const name of ["write", "cortexium_runner_result", "cortexium_runner_finalize"]) {
    const denied = await s.call("codemode", { code: `await tools.${name}({});` });
    assert.equal(denied.isError, true, "Script called " + name);
  }
  const deniedURL = await s.call("codemode", { code: 'await tools.runner_browser_navigate({url:"http://example.com"});' });
  assert.equal(deniedURL.isError, true);
  assert.match(JSON.stringify(deniedURL.content), /restricted/);
  const browser = await s.call("codemode", { code: 'const result = await tools.runner_browser_navigate({url:"http://localhost:8123/"}); text(JSON.parse(result.content[0].text).args);' });
  assert.equal(browser.isError, false, JSON.stringify(browser));
  assert.match(JSON.stringify(browser.content), /localhost:8123/);
  // Both completion tools remain directly declared and callable. Result
  // submission keeps the trusted nonce and attributable outer tool call.
  const finalized = await s.call("cortexium_runner_finalize", {});
  assert.equal(finalized.isError, false, JSON.stringify(finalized));
  const result = await s.call("cortexium_runner_result", { outcome: "succeeded" });
  assert.equal(result.isError, false, JSON.stringify(result));
  assert.equal(result.details.provenance, provenance);
  assert.deepEqual(result.details.arguments, { outcome: "succeeded" });
  const pid = Number(await readFile(marker, "utf8"));
  await s.close();
  assert.throws(() => process.kill(pid, 0), { code: "ESRCH" });
} finally { await s.close(); }

// With no explicit CLI allowlist (inherit mode), enable only Codemode while
// retaining the operator's selected tools. Its optional model calls stay off.
const inherited = await openSession(packageDir, [codePath], undefined);
try {
  assert.deepEqual(inherited.session.getActiveToolNames().sort(), ["codemode", "read"]);
} finally { await inherited.close(); }
