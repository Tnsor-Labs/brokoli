"""The warm code-node worker (ADR-029).

One long-lived interpreter serving many executions over a private Unix
socket. Each `exec` frame runs the SAME wrapper.py that the legacy
spawn path runs — compiled once, executed per request in a fresh module
namespace — so the execution contract is one implementation, tested by
one pytest suite, regardless of lifecycle. What the pool amortizes is
what actually costs ~100ms+: interpreter boot and library imports
(sys.modules persists across executions; the wrapper's pyarrow/pandas
imports hit the cache from the second execution on).

Per-request state rides os.environ (BROKED_*) exactly as the wrapper
expects; the worker is strictly single-execution-at-a-time, so this is
safe. stdout/stderr are captured per execution and forwarded as typed
log frames; the wrapper's inline result (small datasets, zero-emit) is
recovered from the LAST line of captured stdout — user prints land
BEFORE the wrapper's final output print, so unlike the v1 stdin
transport a print() cannot corrupt the result.

Resource limits arrive via the environment at spawn and are applied by
the preamble below before anything else, so they bound the interpreter
itself, not just user code.
"""

import argparse
import io
import json
import os
import socket
import sys
import traceback

# Apply resource ceilings first (same env contract as wrapper.py's own
# preamble; harmless to run twice since setrlimit to the same value is
# idempotent).
try:
    import resource as _resource

    def _rlimit(env_name, res, scale):
        v = int(os.environ.get(env_name, "0") or 0)
        if v > 0:
            try:
                _resource.setrlimit(res, (v * scale, v * scale))
            except (ValueError, OSError):
                pass

    _rlimit("BROKED_LIMIT_MEMORY_MB", _resource.RLIMIT_AS, 1024 * 1024)
    _rlimit("BROKED_LIMIT_CPU_SECONDS", _resource.RLIMIT_CPU, 1)
    _rlimit("BROKED_LIMIT_FILE_SIZE_MB", _resource.RLIMIT_FSIZE, 1024 * 1024)
    _rlimit("BROKED_LIMIT_OPEN_FILES", _resource.RLIMIT_NOFILE, 1)
except ImportError:
    pass

import protocol  # noqa: E402  (sys.path[0] is this file's directory)
from version import WRAPPER_VERSION  # noqa: E402

_MUTATION_MESSAGE = (
    "a row was modified after the loop that read it had already moved on. "
    "Rows stream from disk one at a time, so an edit has to be made while the row is the "
    "one being handled. Edit it inside the loop body, or collect what you need into a new "
    "list and output that instead. This is refused rather than ignored because applying it "
    "would mean guessing where the row belongs.")

_WRAPPER_PATH = os.path.join(os.path.dirname(os.path.abspath(__file__)), "wrapper.py")
_ENV_KEYS = (
    "BROKED_SCRIPT",
    "BROKED_CONFIG",
    "BROKED_PARAMS",
    "BROKED_INPUT_NDJSON",
    "BROKED_OUTPUT_NDJSON",
    "BROKED_INPUT_COLUMNS",
    "BROKED_OUTPUT_COLUMNS",
    "BROKED_INPUT_CSV",
    "BROKED_OUTPUT_CSV",
)


_PROGRESS_RE = None

def _forward_captured(sock_file, exec_id, captured, level, skip_last_json=False):
    """Send captured output lines as log frames — except the wrapper's
    #PROGRESS: markers, which become typed progress frames (they were
    emitted and DISCARDED for the whole life of the legacy transport).
    Returns the trailing JSON payload when skip_last_json found one
    (the inline result)."""
    global _PROGRESS_RE
    if _PROGRESS_RE is None:
        import re
        _PROGRESS_RE = re.compile(r"^#PROGRESS:(\d+)\s*(.*)$")
    raw_lines = [l for l in captured.getvalue().splitlines() if l.strip()]
    lines = []
    for line in raw_lines:
        m = _PROGRESS_RE.match(line)
        if m:
            protocol.write_frame(sock_file, protocol.FRAME_PROGRESS,
                                 {"exec_id": exec_id, "percent": int(m.group(1)),
                                  "message": m.group(2)})
        else:
            lines.append(line)
    inline = None
    if skip_last_json and lines:
        try:
            candidate = json.loads(lines[-1])
            if isinstance(candidate, dict) and "columns" in candidate and "rows" in candidate:
                inline = candidate
                lines = lines[:-1]
        except ValueError:
            pass
    for line in lines:
        protocol.write_frame(sock_file, protocol.FRAME_LOG,
                             {"exec_id": exec_id, "level": level, "message": line})
    return inline


def _run_exec(sock_file, msg, wrapper_code, tmpdir):
    exec_id = msg.get("exec_id", "")
    script_path = os.path.join(tmpdir, f"user_script_{os.getpid()}.py")
    with open(script_path, "w") as f:
        f.write(msg.get("script", ""))

    inp = msg.get("input") or {}
    out = msg.get("output") or {}
    input_path = inp.get("path", "")
    stdin_payload = "{}"
    inline = inp.get("mode") == "inline"
    if inline:
        # Inline input keeps the v1 stdin contract EXACTLY: rows arrive
        # as a plain list (mutable anywhere, no lazy-rows strictness)
        # and the result returns inline. Only ndjson-mode input gets the
        # file data plane and its streaming semantics — pinned by
        # TestCodeNode_FilterAndTransform, which post-mutates collected
        # rows the way stdin-mode scripts always could.
        input_path = ""
        stdin_payload = json.dumps({
            "columns": inp.get("columns") or [],
            "rows": inp.get("rows") or [],
            "config": msg.get("config") or {},
            "params": msg.get("params") or {},
        }, ensure_ascii=False)

    for key in _ENV_KEYS:
        os.environ.pop(key, None)
    os.environ["BROKED_SCRIPT"] = script_path
    os.environ["BROKED_CONFIG"] = json.dumps(msg.get("config") or {}, ensure_ascii=False)
    os.environ["BROKED_PARAMS"] = json.dumps(msg.get("params") or {}, ensure_ascii=False)
    output_path = ""
    if input_path:
        os.environ["BROKED_INPUT_NDJSON"] = input_path
        columns = inp.get("columns") or []
        if columns:
            os.environ["BROKED_INPUT_COLUMNS"] = json.dumps(columns)
    if not inline:
        output_path = out.get("path") or os.path.join(tmpdir, f"out_{os.getpid()}.ndjson")
        os.environ["BROKED_OUTPUT_NDJSON"] = output_path

    ns = {"__name__": "__main__", "__file__": _WRAPPER_PATH, "__builtins__": __builtins__}
    real_stdin, real_stdout, real_stderr = sys.stdin, sys.stdout, sys.stderr
    captured_out, captured_err = io.StringIO(), io.StringIO()
    sys.stdin = io.StringIO(stdin_payload)
    sys.stdout, sys.stderr = captured_out, captured_err
    failure = None
    try:
        exec(wrapper_code, ns)
    except SystemExit as e:
        code = e.code if isinstance(e.code, int) else 1
        if code == 3:
            failure = {"kind": "mutation_after_pass",
                       "message": captured_err.getvalue().strip() or "mutation after the pass was refused"}
        elif code != 0:
            failure = {"kind": "user_exception", "message": f"script exited with status {code}"}
    except MemoryError:
        failure = {"kind": "resource_limit",
                   "message": "script exceeded the memory limit",
                   "traceback": traceback.format_exc()}
    except BaseException as e:  # noqa: BLE001 — every failure becomes a typed frame
        if type(e).__name__ == "_MutationAfterPass":
            failure = {"kind": "mutation_after_pass", "message": _MUTATION_MESSAGE}
        else:
            failure = {"kind": "user_exception", "message": str(e) or type(e).__name__,
                       "traceback": traceback.format_exc()}
    finally:
        sys.stdin, sys.stdout, sys.stderr = real_stdin, real_stdout, real_stderr
        # The wrapper registers its journal cleanup with atexit, which a
        # long-lived worker never reaches: run and unregister it now so
        # journals from a mutating script don't outlive the execution.
        drop = ns.get("_drop_journals")
        if callable(drop):
            try:
                drop()
            except Exception:
                pass
            try:
                import atexit
                atexit.unregister(drop)
            except Exception:
                pass
        try:
            os.remove(script_path)
        except OSError:
            pass

    inline_result = _forward_captured(sock_file, exec_id, captured_out, "info",
                                      skip_last_json=failure is None)
    _forward_captured(sock_file, exec_id, captured_err, "warning")

    if failure is not None:
        failure["exec_id"] = exec_id
        protocol.write_frame(sock_file, protocol.FRAME_ERROR, failure)
        return

    if output_path and os.path.exists(output_path) and os.path.getsize(output_path) > 0 and inline_result is None:
        rows_written = 0
        columns = []
        with open(output_path) as f:
            for line in f:
                if line.strip():
                    if rows_written == 0:
                        columns = list(json.loads(line).keys())
                    rows_written += 1
        # The script's declared column order beats first-row key order:
        # _out_cols is what the wrapper resolved for output_data mode,
        # _emit_state["cols"] what emit mode fixed. This replaces the v1
        # sidecar file — the order travels in the result frame.
        declared = ns.get("_out_cols")
        if not declared and isinstance(ns.get("_emit_state"), dict):
            declared = ns["_emit_state"].get("cols")
        result = {"mode": "ndjson", "path": output_path,
                  "columns": list(declared) if declared else columns,
                  "rows_written": rows_written}
    else:
        payload = inline_result or {"columns": [], "rows": []}
        result = {"mode": "inline", "columns": payload.get("columns") or [],
                  "rows": payload.get("rows") or [],
                  "rows_written": len(payload.get("rows") or [])}
    protocol.write_frame(sock_file, protocol.FRAME_RESULT,
                         {"exec_id": exec_id, "output": result})


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--socket", required=True)
    parser.add_argument("--one-shot", action="store_true",
                        help="serve exactly one exec then exit (overflow workers)")
    args = parser.parse_args()

    with open(_WRAPPER_PATH) as f:
        wrapper_code = compile(f.read(), _WRAPPER_PATH, "exec")

    conn = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    conn.connect(args.socket)
    sock_file = conn.makefile("rwb")

    import platform
    protocol.write_frame(sock_file, protocol.FRAME_HELLO, {
        "protocol_version": protocol.PROTOCOL_VERSION,
        "wrapper_version": WRAPPER_VERSION,
        "language": "python",
        "python_version": platform.python_version(),
        "pid": os.getpid(),
    })

    import tempfile
    tmpdir = tempfile.mkdtemp(prefix="brokoli-worker-")

    served = 0
    try:
        while True:
            frame_type, msg = protocol.read_frame(sock_file)
            if frame_type == protocol.FRAME_SHUTDOWN:
                return
            if frame_type == protocol.FRAME_INIT:
                for module_name in msg.get("warmup") or []:
                    try:
                        __import__(module_name)
                    except Exception:
                        pass
                continue
            if frame_type == protocol.FRAME_EXEC:
                _run_exec(sock_file, msg, wrapper_code, tmpdir)
                served += 1
                if args.one_shot and served >= 1:
                    return
                continue
            raise protocol.ProtocolError(f"unexpected frame {frame_type:#x}")
    except EOFError:
        return
    finally:
        import shutil
        shutil.rmtree(tmpdir, ignore_errors=True)


if __name__ == "__main__":
    main()
