export const PROTOCOL_VERSION = 1;
export const MAX_FRAME_PAYLOAD = 128 * 1024 * 1024;

export const FRAME_INIT = 0x01;
export const FRAME_EXEC = 0x02;
export const FRAME_SHUTDOWN = 0x03;
export const FRAME_HELLO = 0x10;
export const FRAME_LOG = 0x11;
export const FRAME_PROGRESS = 0x12;
export const FRAME_RESULT = 0x13;
export const FRAME_ERROR = 0x14;

export class ProtocolError extends Error {}

const readers = new WeakMap();

export async function writeFrame(stream, frameType, payload) {
  const body = Buffer.from(JSON.stringify(payload), "utf8");
  if (body.length > MAX_FRAME_PAYLOAD) {
    throw new ProtocolError(`frame 0x${frameType.toString(16)} payload ${body.length} exceeds ${MAX_FRAME_PAYLOAD}`);
  }
  const frame = Buffer.allocUnsafe(6 + body.length);
  frame.writeUInt32BE(body.length, 0);
  frame[4] = PROTOCOL_VERSION;
  frame[5] = frameType;
  body.copy(frame, 6);
  await new Promise((resolve, reject) => {
    stream.write(frame, (error) => error ? reject(error) : resolve());
  });
}

export async function readFrame(stream) {
  let state = readers.get(stream);
  if (!state) {
    state = { buffer: Buffer.alloc(0), iterator: stream[Symbol.asyncIterator](), ended: false };
    readers.set(stream, state);
  }

  const fill = async (size) => {
    while (state.buffer.length < size) {
      const { value, done } = await state.iterator.next();
      if (done) {
        state.ended = true;
        throw new EOFError(state.buffer.length ? "socket closed mid-frame" : "socket closed");
      }
      state.buffer = Buffer.concat([state.buffer, Buffer.from(value)]);
    }
  };

  await fill(6);
  const size = state.buffer.readUInt32BE(0);
  const version = state.buffer[4];
  const frameType = state.buffer[5];
  if (version !== PROTOCOL_VERSION) throw new ProtocolError(`unsupported protocol version ${version}`);
  if (size > MAX_FRAME_PAYLOAD) throw new ProtocolError(`frame payload ${size} exceeds ${MAX_FRAME_PAYLOAD}`);
  await fill(6 + size);
  const body = state.buffer.subarray(6, 6 + size);
  state.buffer = state.buffer.subarray(6 + size);
  try {
    return { frameType, payload: JSON.parse(body.toString("utf8")) };
  } catch (error) {
    throw new ProtocolError(`invalid frame JSON: ${error.message}`);
  }
}

export class EOFError extends Error {}
