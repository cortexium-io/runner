import { createInterface } from "node:readline";
import { writeFileSync, renameSync } from "node:fs";

const [marker, mode] = process.argv.slice(2);

let initialized = false;
let hangingRequest;
const input = createInterface({ input: process.stdin });
input.on("line", (line) => {
  const message = JSON.parse(line);
  const reply = (result) => process.stdout.write(JSON.stringify({ jsonrpc: "2.0", id: message.id, result }) + "\n");
  if (message.method === "initialize") {
    // Publish a complete marker only after initialization is in flight.
    writeFileSync(marker + ".tmp", String(process.pid));
    renameSync(marker + ".tmp", marker);
    if (mode === "hold-init") return;
    reply({ protocolVersion: "2025-11-25", capabilities: { tools: {} }, serverInfo: { name: "fixture", version: "1" } });
  } else if (message.method === "notifications/initialized") {
    initialized = true;
  } else if (message.method === "notifications/cancelled") {
    if (message.params.requestId === hangingRequest) hangingRequest = undefined;
  } else if (message.method === "tools/call") {
    if (!initialized) throw new Error("Tool called before initialization completed");
    const { name, arguments: args } = message.params;
    if (name === "evaluate" && args.script === "hang") {
      hangingRequest = message.id;
    } else if (name === "evaluate" && args.script === "exit") {
      process.stderr.write("fixture server exited\n", () => process.exit(1));
    } else if (name === "evaluate" && args.script === "fail") {
      reply({ isError: true, content: [{ type: "text", text: "fixture tool error" }] });
    } else if (name === "screenshot") {
      reply({ content: [
        { type: "text", text: JSON.stringify({ pid: process.pid, hanging: hangingRequest !== undefined }) },
        { type: "image", mimeType: "image/png", data: "aW1hZ2U=" },
      ] });
    } else {
      reply({ content: [{ type: "text", text: JSON.stringify({ name, args, cwd: process.cwd(), cache: process.env.NPM_CONFIG_CACHE }) }] });
    }
  } else {
    throw new Error("Unexpected MCP method: " + message.method);
  }
});
