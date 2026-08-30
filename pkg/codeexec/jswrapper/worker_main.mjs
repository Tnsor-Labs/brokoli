import net from "node:net";
import process from "node:process";
import { executeContract, ContractError } from "./contract.mjs";
import {
  EOFError,
  FRAME_ERROR,
  FRAME_EXEC,
  FRAME_HELLO,
  FRAME_INIT,
  FRAME_LOG,
  FRAME_PROGRESS,
  FRAME_RESULT,
  FRAME_SHUTDOWN,
  PROTOCOL_VERSION,
  ProtocolError,
  readFrame,
  writeFrame,
} from "./protocol.mjs";
import { JS_WRAPPER_VERSION } from "./version.mjs";

function argumentsFor(argv) {
  const socketIndex = argv.indexOf("--socket");
  if (socketIndex < 0 || !argv[socketIndex + 1]) throw new Error("--socket is required");
  return { socket: argv[socketIndex + 1], oneShot: argv.includes("--one-shot") };
}

async function main() {
  const args = argumentsFor(process.argv.slice(2));
  const socket = net.createConnection(args.socket);
  await new Promise((resolve, reject) => {
    socket.once("connect", resolve);
    socket.once("error", reject);
  });
  await writeFrame(socket, FRAME_HELLO, {
    protocol_version: PROTOCOL_VERSION,
    wrapper_version: JS_WRAPPER_VERSION,
    language: "typescript",
    pid: process.pid,
  });

  let served = 0;
  for (;;) {
    const { frameType, payload } = await readFrame(socket);
    if (frameType === FRAME_SHUTDOWN) break;
    if (frameType === FRAME_INIT) continue;
    if (frameType !== FRAME_EXEC) throw new ProtocolError(`unexpected frame 0x${frameType.toString(16)}`);

    let writes = Promise.resolve();
    let logBytes = 0;
    let logsTruncated = false;
    const queue = (frameType, frame) => { writes = writes.then(() => writeFrame(socket, frameType, frame)); };
    const queueLog = (level, message) => {
      const bounded = Buffer.byteLength(message) > 64 * 1024 ? `${message.slice(0, 64 * 1024)}…` : message;
      const size = Buffer.byteLength(bounded);
      if (logBytes + size > 1024 * 1024) {
        if (!logsTruncated) queue(FRAME_LOG, { exec_id: payload.exec_id || "", level: "warning", message: "worker log output truncated after 1 MiB" });
        logsTruncated = true;
        return;
      }
      logBytes += size;
      queue(FRAME_LOG, { exec_id: payload.exec_id || "", level, message: bounded });
    };
    try {
      const output = await executeContract(payload, {
        log: queueLog,
        progress: (percent, message) => queue(FRAME_PROGRESS, { exec_id: payload.exec_id || "", percent, message }),
      });
      await writes;
      await writeFrame(socket, FRAME_RESULT, { exec_id: payload.exec_id || "", output });
    } catch (error) {
      await writes;
      const failure = error instanceof ContractError ? error : new ContractError("internal", error?.message || String(error), { cause: error });
      await writeFrame(socket, FRAME_ERROR, {
        exec_id: payload.exec_id || "",
        kind: failure.kind,
        message: failure.message,
        traceback: failure.cause?.stack || failure.stack || "",
      });
    }
    served++;
    if (args.oneShot && served >= 1) break;
  }
  socket.destroy();
}

main().catch((error) => {
  if (!(error instanceof EOFError)) process.stderr.write(`${error.stack || error}\n`);
  process.exitCode = error instanceof EOFError ? 0 : 1;
});
