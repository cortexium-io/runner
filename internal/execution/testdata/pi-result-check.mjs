import assert from "node:assert/strict";
import { join } from "node:path";
import { pathToFileURL } from "node:url";
import { mkdtemp, rmdir } from "node:fs/promises";
import { tmpdir } from "node:os";

const [packageDir, structured, native, direct] = process.argv.slice(2);
const { discoverAndLoadExtensions } = await import(pathToFileURL(join(packageDir, "dist/index.js")).href);
const neutral = await mkdtemp(join(tmpdir(), "runner-pi-result-check-"));
async function load(path) {
  const loaded = await discoverAndLoadExtensions([path], neutral, neutral);
  assert.deepEqual(loaded.errors, []);
  assert.equal(loaded.extensions.length, 1);
  return loaded.extensions[0];
}
try {
  const structuredExtension = await load(structured);
  const resultTool = structuredExtension.tools.get("cortexium_runner_result").definition;
  assert.deepEqual(resultTool.constrainedSampling, { type: "json_schema", strict: "require" });
  const result = await resultTool.execute("result", { outcome: "succeeded" });
  assert.deepEqual(result.details.arguments, { outcome: "succeeded" });
  assert.match(result.details.provenance, /^[a-f0-9]{64}$/);
  let aborted = false;
  for (const handler of structuredExtension.handlers.get("tool_execution_end")) {
    await handler({ toolName: "cortexium_runner_result" }, { abort: () => { aborted = true; } });
  }
  assert.equal(aborted, true);

  const nativeExtension = await load(native);
  const finalized = await nativeExtension.tools.get("cortexium_runner_finalize").definition.execute("finalize", {});
  assert.match(finalized.details.provenance, /^[a-f0-9]{64}$/);
  const payload = { tools: [{ name: "bash" }], tool_choice: "auto", messages: [] };
  const nativeResult = await nativeExtension.handlers.get("before_provider_request")[0]({ payload });
  assert.deepEqual(nativeResult.tools, []);
  assert.equal(nativeResult.response_format.type, "json_schema");
  assert.equal(nativeResult.response_format.json_schema.strict, true);

  const directExtension = await load(direct);
  assert.equal(directExtension.tools.size, 0);
  const directResult = await directExtension.handlers.get("before_provider_request")[0]({ payload });
  assert.deepEqual(directResult.tools, []);
  assert.equal(directResult.response_format.type, "json_schema");
} finally {
  await rmdir(neutral);
}
