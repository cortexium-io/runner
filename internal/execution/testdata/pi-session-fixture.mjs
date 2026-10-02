import assert from "node:assert/strict";
import { join, dirname, basename } from "node:path";
import { pathToFileURL } from "node:url";
import { watch } from "node:fs";
import { access, mkdir, mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";

// Exercise Pi's actual tool pipeline with deterministic assistant messages.
// No provider, model download, browser, or operator credentials are involved.
export async function openSession(packageDir, extensionPaths, tools, factories = [], cwd) {
  const sdk = await import(pathToFileURL(join(packageDir, "dist/index.js")).href);
  const { createAssistantMessageEventStream } = await import(pathToFileURL(join(packageDir,
    "node_modules/@earendil-works/pi-ai/dist/utils/event-stream.js")).href);
  const root = await mkdtemp(join(tmpdir(), "runner-pi-session-"));
  const agentDir = join(root, "agent");
  await mkdir(agentDir);
  process.env.PI_CODING_AGENT_DIR = agentDir;
  cwd ??= root;
  const modelRuntime = await sdk.ModelRuntime.create({ authPath: join(agentDir, "auth.json"), modelsPath: null, refreshOnCreate: false });
  modelRuntime.registerProvider("fixture", { api: "openai-completions", apiKey: "fixture", baseUrl: "http://127.0.0.1:1/v1",
    models: [{ id: "fixture", name: "Fixture", reasoning: false, input: ["text"], contextWindow: 32768,
      maxTokens: 4096, cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 } }] });
  let context;
  const settingsManager = sdk.SettingsManager.inMemory({ defaultTools: ["read"], retry: { enabled: false }, compaction: { enabled: false } });
  const resourceLoader = new sdk.DefaultResourceLoader({ cwd, agentDir, settingsManager,
    noExtensions: factories.length === 0, noSkills: true, noPromptTemplates: true, noThemes: true, noContextFiles: true,
    additionalExtensionPaths: extensionPaths, extensionFactories: [...factories,
      pi => pi.on("session_start", (_event, ctx) => { context = ctx; })],
  });
  await resourceLoader.reload();
  const { session, extensionsResult } = await sdk.createAgentSession({ cwd, agentDir, modelRuntime,
    model: modelRuntime.getModels("fixture").find(model => model.type === "llm"), settingsManager, resourceLoader,
    sessionManager: sdk.SessionManager.inMemory(cwd), tools });
  assert.deepEqual(extensionsResult.errors, []);
  await session.bindExtensions({ mode: "print" });
  let sequence = 0;
  async function call(name, args = {}, signal) {
    let issued = false;
    const id = "fixture-" + ++sequence;
    session.agent.streamFunction = () => {
      const stream = createAssistantMessageEventStream();
      const content = issued ? [{ type: "text", text: "done" }]
        : [{ type: "toolCall", id, name, arguments: args }];
      issued = true;
      const message = { role: "assistant", provider: "fixture", api: "openai-completions", model: "fixture",
        timestamp: Date.now(), stopReason: content[0].type === "toolCall" ? "toolUse" : "stop", content,
        usage: { input: 1, output: 1, cacheRead: 0, cacheWrite: 0, totalTokens: 2,
          cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, total: 0 } } };
      stream.push({ type: "done", reason: message.stopReason, message });
      stream.end(message);
      return stream;
    };
    const abort = () => { void session.abort(); };
    signal?.addEventListener("abort", abort, { once: true });
    try {
      await session.prompt("Execute the fixture call");
      return session.messages.find(message => message.role === "toolResult" && message.toolCallId === id);
    } finally { signal?.removeEventListener("abort", abort); }
  }
  async function ready() {
    const event = { systemPromptOptions: { sections: {} } };
    for (const extension of extensionsResult.extensions)
      for (const handler of extension.handlers.get("before_agent_start") ?? []) await handler(event, context);
    return event.systemPromptOptions.sections;
  }
  async function close() {
    for (const extension of extensionsResult.extensions)
      for (const handler of extension.handlers.get("session_shutdown") ?? []) await handler({}, context);
    session.dispose();
    await rm(root, { recursive: true, force: true });
  }
  return { session, call, ready, close, root, agentDir };
}

export async function waitForMarker(marker) {
  let watcher;
  let timer;
  try {
    await new Promise((resolve, reject) => {
      timer = setTimeout(() => reject(new Error("Fixture marker was not published: " + marker)), 5000);
      watcher = watch(dirname(marker), (_event, file) => { if (file === basename(marker)) resolve(); });
      access(marker).then(resolve).catch(() => {});
    });
  } finally { watcher?.close(); clearTimeout(timer); }
}
