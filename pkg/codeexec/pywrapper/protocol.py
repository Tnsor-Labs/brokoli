# Framed protocol, Python side (ADR-029). Mirrors pkg/codeexec/
# protocol.go exactly: uint32 BE length | uint8 version | uint8 type |
# JSON payload. Go parses PROTOCOL_VERSION out of this file's bytes at
# init, so the two sides cannot drift.

import json
import struct

PROTOCOL_VERSION = 1
MAX_FRAME_PAYLOAD = 128 * 1024 * 1024

# Host -> worker
FRAME_INIT = 0x01
FRAME_EXEC = 0x02
FRAME_SHUTDOWN = 0x03
# Worker -> host
FRAME_HELLO = 0x10
FRAME_LOG = 0x11
FRAME_PROGRESS = 0x12
FRAME_RESULT = 0x13
FRAME_ERROR = 0x14

_HEADER = struct.Struct(">IBB")


class ProtocolError(Exception):
    pass


def write_frame(sock_file, frame_type, payload):
    body = json.dumps(payload, ensure_ascii=False).encode("utf-8")
    if len(body) > MAX_FRAME_PAYLOAD:
        raise ProtocolError(f"frame {frame_type:#x} payload {len(body)} exceeds {MAX_FRAME_PAYLOAD}")
    sock_file.write(_HEADER.pack(len(body), PROTOCOL_VERSION, frame_type) + body)
    sock_file.flush()


def read_frame(sock_file):
    header = sock_file.read(_HEADER.size)
    if len(header) < _HEADER.size:
        raise EOFError("socket closed")
    size, version, frame_type = _HEADER.unpack(header)
    if version != PROTOCOL_VERSION:
        raise ProtocolError(f"unsupported protocol version {version}")
    if size > MAX_FRAME_PAYLOAD:
        raise ProtocolError(f"frame payload {size} exceeds {MAX_FRAME_PAYLOAD}")
    body = b""
    while len(body) < size:
        chunk = sock_file.read(size - len(body))
        if not chunk:
            raise EOFError("socket closed mid-frame")
        body += chunk
    return frame_type, json.loads(body.decode("utf-8"))
