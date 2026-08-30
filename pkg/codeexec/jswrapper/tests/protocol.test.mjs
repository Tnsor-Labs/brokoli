import assert from "node:assert/strict";
import { PassThrough } from "node:stream";
import test from "node:test";
import {
  EOFError,
  FRAME_EXEC,
  MAX_FRAME_PAYLOAD,
  PROTOCOL_VERSION,
  ProtocolError,
  readFrame,
  writeFrame,
} from "../protocol.mjs";

function rawFrame(payload, type = FRAME_EXEC, version = PROTOCOL_VERSION) {
  const body = Buffer.from(JSON.stringify(payload));
  const frame = Buffer.alloc(6 + body.length);
  frame.writeUInt32BE(body.length, 0);
  frame[4] = version;
  frame[5] = type;
  body.copy(frame, 6);
  return frame;
}

test("round trips unicode and multiple buffered frames", async () => {
  const stream = new PassThrough();
  await writeFrame(stream, FRAME_EXEC, { value: "héllo" });
  stream.write(Buffer.concat([rawFrame({ n: 2 }), rawFrame({ n: 3 })]));
  assert.deepEqual(await readFrame(stream), { frameType: FRAME_EXEC, payload: { value: "héllo" } });
  assert.deepEqual((await readFrame(stream)).payload, { n: 2 });
  assert.deepEqual((await readFrame(stream)).payload, { n: 3 });
});

test("handles fragmented headers and bodies", async () => {
  const stream = new PassThrough();
  const frame = rawFrame({ fragmented: true });
  const reading = readFrame(stream);
  for (const byte of frame) stream.write(Buffer.from([byte]));
  assert.deepEqual((await reading).payload, { fragmented: true });
});

test("refuses unsupported versions and oversized declarations", async () => {
  const wrong = new PassThrough();
  wrong.end(rawFrame({}, FRAME_EXEC, 9));
  await assert.rejects(readFrame(wrong), ProtocolError);
  const oversized = Buffer.alloc(6);
  oversized.writeUInt32BE(MAX_FRAME_PAYLOAD + 1, 0);
  oversized[4] = PROTOCOL_VERSION;
  oversized[5] = FRAME_EXEC;
  const stream = new PassThrough();
  stream.end(oversized);
  await assert.rejects(readFrame(stream), /exceeds/);
});

test("reports malformed JSON and partial EOF", async () => {
  const malformed = Buffer.alloc(7);
  malformed.writeUInt32BE(1, 0);
  malformed[4] = PROTOCOL_VERSION;
  malformed[5] = FRAME_EXEC;
  malformed[6] = 0xff;
  const bad = new PassThrough();
  bad.end(malformed);
  await assert.rejects(readFrame(bad), ProtocolError);
  const partial = new PassThrough();
  partial.end(rawFrame({ value: 1 }).subarray(0, 8));
  await assert.rejects(readFrame(partial), EOFError);
});
