import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import fs from "node:fs";
import net from "node:net";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";
import {
  FRAME_ERROR,
  FRAME_EXEC,
  FRAME_HELLO,
  FRAME_LOG,
  FRAME_RESULT,
  FRAME_SHUTDOWN,
  readFrame,
  writeFrame,
} from "../protocol.mjs";

const worker = fileURLToPath(new URL("../worker_main.mjs", import.meta.url));

async function startWorker(t, extra = []) {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "brokoli-js-worker-"));
  const socketPath = path.join(dir, "worker.sock");
  const server = net.createServer();
  await new Promise((resolve, reject) => server.listen(socketPath, resolve).once("error", reject));
  const child = spawn(process.execPath, [worker, "--socket", socketPath, ...extra], { stdio: ["ignore", "ignore", "pipe"] });
  const socket = await new Promise((resolve) => server.once("connection", resolve));
  t.after(async () => {
    socket.destroy();
    server.close();
    child.kill("SIGKILL");
    fs.rmSync(dir, { recursive: true, force: true });
  });
  return { child, socket };
}

function execPayload(script) {
  return {
    exec_id: "exec-1",
    script,
    config: {},
    params: {},
    input: { mode: "inline", columns: ["id"], rows: [{ id: 1 }] },
    output: { mode: "ndjson", path: "" },
    timeout_ms: 1000,
  };
}

test("announces identity and returns logs before an inline result", async (t) => {
  const { socket } = await startWorker(t);
  const hello = await readFrame(socket);
  assert.equal(hello.frameType, FRAME_HELLO);
  assert.deepEqual({ ...hello.payload, pid: 0 }, { protocol_version: 1, wrapper_version: 1, language: "typescript", pid: 0 });
  await writeFrame(socket, FRAME_EXEC, execPayload('console.log("safe"); output_data = { columns: ["id"], rows };'));
  const log = await readFrame(socket);
  assert.equal(log.frameType, FRAME_LOG);
  assert.equal(log.payload.message, "safe");
  const result = await readFrame(socket);
  assert.equal(result.frameType, FRAME_RESULT);
  assert.deepEqual(result.payload.output.rows, [{ id: 1 }]);
  await writeFrame(socket, FRAME_SHUTDOWN, {});
});

test("reports user exceptions with code-node stacks", async (t) => {
  const { socket } = await startWorker(t, ["--one-shot"]);
  await readFrame(socket);
  await writeFrame(socket, FRAME_EXEC, execPayload('throw new Error("boom");'));
  const failure = await readFrame(socket);
  assert.equal(failure.frameType, FRAME_ERROR);
  assert.equal(failure.payload.kind, "user_exception");
  assert.match(failure.payload.traceback, /<code-node>/);
});
