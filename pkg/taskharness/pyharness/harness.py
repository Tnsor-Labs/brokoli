#!/usr/bin/env python3
"""Reference brokoli.task-runtime/v1 harness (ADR-033 section 7).

Reads exactly one 'start' frame from stdin, loads the invocation
descriptor it points to (this harness's own convention for what lives
at invocation_path -- the wire schema only promises it is a string; see
invocation.go's Invocation type for the exact shape both sides agree
on), imports the named module and calls the named symbol with the given
keyword arguments, and writes a task-result-v1 candidate manifest to
result_path before emitting 'completed'.

Failure taxonomy (ADR-033 section 14): an exception raised by the task
itself is reported as 'user_code' -- the only category a harness may
originate for that. Anything wrong with what the worker handed this
harness (a malformed start frame, an invocation descriptor missing a
required key) is reported as 'contract_violation'. Categories a harness
must never claim for itself (runtime_protocol, platform,
resource_exhausted, lease_lost) are the trusted worker's alone to
assign, and this harness never emits them.

Known Phase 2a limitation, not a silent gap: this reference harness does
not read for a mid-task 'cancel' frame -- the task call is a single
synchronous Python call with no natural interruption point without
threads or signal-based interruption, which is real future work. A
worker's forceful termination fallback (pkg/taskharness.Run's
SIGTERM/SIGKILL escalation after the cancellation grace period) already
covers this harness fully; only the cooperative, clean-shutdown path is
unimplemented here.

Resource ceilings (phase 2b, mirrors pkg/codeexec/pywrapper/wrapper.py's
own preamble verbatim): applied first, before importing the task module,
so its own imports count under the cap. RLIMIT_AS is address space, not
RSS. engine.Runner.runTask sets the same BROKED_LIMIT_* env vars a code
node's wrapper reads; CPU/file-size/open-files are additionally enforced
externally via pkg/proctree before this process even starts, so this
block's real job is memory -- the one ceiling that must be self-applied
from inside the process pkg/taskharness.Options.Rlimits has no field for.
"""
import importlib
import json
import os
import sys
import traceback

try:
    import resource as _resource

    def _brokoli_rlimit(env_name, res, scale):
        _v = int(os.environ.get(env_name, "0") or 0)
        if _v > 0:
            try:
                _resource.setrlimit(res, (_v * scale, _v * scale))
            except (ValueError, OSError):
                pass

    _brokoli_rlimit("BROKED_LIMIT_MEMORY_MB", _resource.RLIMIT_AS, 1024 * 1024)
    _brokoli_rlimit("BROKED_LIMIT_CPU_SECONDS", _resource.RLIMIT_CPU, 1)
    _brokoli_rlimit("BROKED_LIMIT_FILE_SIZE_MB", _resource.RLIMIT_FSIZE, 1024 * 1024)
    _brokoli_rlimit("BROKED_LIMIT_OPEN_FILES", _resource.RLIMIT_NOFILE, 1)
except ImportError:
    pass

PROTOCOL = "brokoli.task-runtime/v1"
ADAPTER = "brokoli-python-taskharness"
ADAPTER_VERSION = "0.1.0"


def emit(frame):
    sys.stdout.write(json.dumps(frame) + "\n")
    sys.stdout.flush()


def fail(category, code, message, retryable=False):
    emit({
        "type": "failed",
        "failure": {
            "category": category,
            "code": code,
            "message": message,
            "retryable": retryable,
        },
    })


def load_start_frame():
    line = sys.stdin.readline()
    if not line:
        # No frame to report a failure into (we haven't even sent ready
        # yet) -- the worker's own protocol-failure detection ("exiting
        # before any terminal frame") is the right place for this to
        # surface, not something this harness narrates.
        sys.exit(1)
    try:
        start = json.loads(line)
    except json.JSONDecodeError as exc:
        fail("contract_violation", "malformed_start", "start frame is not valid JSON: %s" % exc)
        sys.exit(1)
    if start.get("type") != "start" or start.get("protocol") != PROTOCOL:
        fail("contract_violation", "unexpected_start", "first frame was not a valid start frame")
        sys.exit(1)
    return start


def load_invocation(invocation_path):
    with open(invocation_path, "r", encoding="utf-8") as f:
        inv = json.load(f)
    for key in ("module", "symbol", "interface_digest"):
        if key not in inv:
            raise ValueError("invocation descriptor is missing required key %r" % key)
    return inv


def main():
    start = load_start_frame()
    emit({
        "type": "ready",
        "protocol": PROTOCOL,
        "adapter": ADAPTER,
        "adapter_version": ADAPTER_VERSION,
        "capabilities": [],
    })

    try:
        inv = load_invocation(start["invocation_path"])
    except Exception as exc:
        fail("contract_violation", "invalid_invocation", str(exc))
        sys.exit(1)

    for p in inv.get("sys_path", []):
        if p not in sys.path:
            sys.path.insert(0, p)

    try:
        module = importlib.import_module(inv["module"])
        func = getattr(module, inv["symbol"])
        result = func(**inv.get("kwargs", {}))
    except Exception:
        fail("user_code", "task_raised", traceback.format_exc())
        sys.exit(1)

    try:
        os.makedirs(start["output_staging_dir"], exist_ok=True)
        candidate = {
            "contract": "brokoli.task-result/v1",
            "interface_digest": inv["interface_digest"],
            "outputs": {
                "result": {"kind": "scalar", "value": result},
            },
        }
        with open(start["result_path"], "w", encoding="utf-8") as f:
            json.dump(candidate, f)
    except Exception as exc:
        fail("contract_violation", "result_write_failed", str(exc))
        sys.exit(1)

    emit({"type": "completed"})


if __name__ == "__main__":
    main()
