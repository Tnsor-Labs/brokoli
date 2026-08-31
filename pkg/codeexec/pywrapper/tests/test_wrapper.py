"""Contract tests for the code-node wrapper, from the Python side.

These mirror the Go engine's pinned behaviors (engine/codenode_test.go,
engine/lazyrows_mutation_test.go, engine/stream_exec_test.go) so a
wrapper edit fails here — as Python, with Python tracebacks — before it
fails as a byte-diff inside a Go test. Run with pytest from this
directory's parent (CI does).
"""

import json
import os
import subprocess
import sys
import tempfile
from pathlib import Path

WRAPPER = str(Path(__file__).resolve().parent.parent / "wrapper.py")


def run_wrapper(script, *, stdin=None, env=None, tmp=None):
    """Execute the wrapper with `script` as the user script."""
    tmp = tmp or tempfile.mkdtemp(prefix="brk-wrap-test-")
    script_path = os.path.join(tmp, "user_script.py")
    with open(script_path, "w") as f:
        f.write(script)
    full_env = {
        "PATH": os.environ.get("PATH", ""),
        "BROKED_SCRIPT": script_path,
        "BROKED_CONFIG": "{}",
        "BROKED_PARAMS": "{}",
    }
    if env:
        full_env.update(env)
    return subprocess.run(
        [sys.executable, WRAPPER],
        input=stdin if stdin is not None else "{}",
        capture_output=True,
        text=True,
        env=full_env,
    )


def stdin_payload(rows, columns=None):
    return json.dumps({
        "columns": columns or (list(rows[0].keys()) if rows else []),
        "rows": rows,
        "config": {},
        "params": {},
    })


def write_ndjson(tmp, rows):
    path = os.path.join(tmp, "in.ndjson")
    with open(path, "w") as f:
        for row in rows:
            f.write(json.dumps(row) + "\n")
    return path


# ── stdin (small data) mode ────────────────────────────────────────────

def test_stdin_passthrough():
    r = run_wrapper("", stdin=stdin_payload([{"a": 1}, {"a": 2}]))
    assert r.returncode == 0, r.stderr
    out = json.loads(r.stdout)
    assert out == {"columns": ["a"], "rows": [{"a": 1}, {"a": 2}]}


def test_stdin_transform_sees_wrapper_globals():
    script = 'output_data = {"columns": columns, "rows": [{"a": r["a"] * 2} for r in rows]}'
    r = run_wrapper(script, stdin=stdin_payload([{"a": 21}]))
    assert r.returncode == 0, r.stderr
    assert json.loads(r.stdout)["rows"] == [{"a": 42}]


def test_config_and_params_arrive_via_env():
    script = 'output_data = {"columns": ["v"], "rows": [{"v": config["k"] + params["p"]}]}'
    r = run_wrapper(
        script,
        stdin=stdin_payload([]),
        env={"BROKED_CONFIG": '{"k": "con"}', "BROKED_PARAMS": '{"p": "fig"}'},
    )
    assert r.returncode == 0, r.stderr
    assert json.loads(r.stdout)["rows"] == [{"v": "config"}]


def test_user_traceback_names_the_user_script():
    r = run_wrapper("raise ValueError('boom')", stdin=stdin_payload([]))
    assert r.returncode != 0
    assert "boom" in r.stderr
    # Contract v2: user errors point at the user script, not the wrapper.
    assert "<code-node>" in r.stderr


# ── NDJSON (file) mode: lazy rows ──────────────────────────────────────

def test_lazy_rows_reiterate_and_len_materializes():
    tmp = tempfile.mkdtemp(prefix="brk-wrap-test-")
    inp = write_ndjson(tmp, [{"a": i} for i in range(5)])
    out = os.path.join(tmp, "out.ndjson")
    script = """
first = [r["a"] for r in rows]
second = [r["a"] for r in rows]
assert first == second == [0, 1, 2, 3, 4], (first, second)
assert len(rows) == 5
output_data = {"columns": columns, "rows": rows}
"""
    r = run_wrapper(script, tmp=tmp, env={
        "BROKED_INPUT_NDJSON": inp,
        "BROKED_OUTPUT_NDJSON": out,
        "BROKED_INPUT_COLUMNS": '["a"]',
    })
    assert r.returncode == 0, r.stderr
    written = [json.loads(l) for l in open(out) if l.strip()]
    assert written == [{"a": i} for i in range(5)]


def test_live_row_mutation_persists_and_journals_are_cleaned():
    tmp = tempfile.mkdtemp(prefix="brk-wrap-test-")
    inp = write_ndjson(tmp, [{"name": "ada"}, {"name": "grace"}])
    out = os.path.join(tmp, "out.ndjson")
    script = """
for r in rows:
    r["name"] = r["name"].upper()
output_data = {"columns": columns, "rows": rows}
"""
    r = run_wrapper(script, tmp=tmp, env={
        "BROKED_INPUT_NDJSON": inp,
        "BROKED_OUTPUT_NDJSON": out,
        "BROKED_INPUT_COLUMNS": '["name"]',
        "TMPDIR": tmp,
    })
    assert r.returncode == 0, r.stderr
    written = [json.loads(l)["name"] for l in open(out) if l.strip()]
    assert written == ["ADA", "GRACE"]
    leftovers = [p for p in os.listdir(tmp) if p.startswith("brokoli-rows-")]
    assert leftovers == [], f"journals leaked: {leftovers}"


def test_mutation_after_the_pass_refuses_with_exit_3():
    tmp = tempfile.mkdtemp(prefix="brk-wrap-test-")
    inp = write_ndjson(tmp, [{"a": 1}, {"a": 2}])
    out = os.path.join(tmp, "out.ndjson")
    script = """
kept = []
for r in rows:
    kept.append(r)
kept[0]["a"] = 99
"""
    r = run_wrapper(script, tmp=tmp, env={
        "BROKED_INPUT_NDJSON": inp,
        "BROKED_OUTPUT_NDJSON": out,
        "BROKED_INPUT_COLUMNS": '["a"]',
    })
    assert r.returncode == 3, (r.returncode, r.stderr)
    assert "BROKOLI_ERROR" in r.stderr


# ── emit mode ──────────────────────────────────────────────────────────

def test_emit_streams_rows_to_the_output_file():
    tmp = tempfile.mkdtemp(prefix="brk-wrap-test-")
    inp = write_ndjson(tmp, [{"a": i} for i in range(4)])
    out = os.path.join(tmp, "out.ndjson")
    script = """
for r in rows:
    if r["a"] % 2 == 0:
        emit({"a": r["a"]})
"""
    r = run_wrapper(script, tmp=tmp, env={
        "BROKED_INPUT_NDJSON": inp,
        "BROKED_OUTPUT_NDJSON": out,
        "BROKED_INPUT_COLUMNS": '["a"]',
    })
    assert r.returncode == 0, r.stderr
    written = [json.loads(l)["a"] for l in open(out) if l.strip()]
    assert written == [0, 2]


def test_zero_emit_returns_inline_and_writes_no_file():
    # The pinned trap: a filter that matches nothing MUST NOT fall back
    # to passthrough. begin_emit declares emit mode explicitly.
    tmp = tempfile.mkdtemp(prefix="brk-wrap-test-")
    inp = write_ndjson(tmp, [{"a": 1}])
    out = os.path.join(tmp, "out.ndjson")
    script = """
begin_emit(["a"])
for r in rows:
    if r["a"] > 100:
        emit(r)
"""
    r = run_wrapper(script, tmp=tmp, env={
        "BROKED_INPUT_NDJSON": inp,
        "BROKED_OUTPUT_NDJSON": out,
        "BROKED_INPUT_COLUMNS": '["a"]',
    })
    assert r.returncode == 0, r.stderr
    assert not os.path.exists(out) or os.path.getsize(out) == 0
    assert json.loads(r.stdout) == {"columns": ["a"], "rows": []}


def test_generator_output_streams():
    tmp = tempfile.mkdtemp(prefix="brk-wrap-test-")
    inp = write_ndjson(tmp, [{"a": i} for i in range(3)])
    out = os.path.join(tmp, "out.ndjson")
    script = """
def doubled():
    for r in rows:
        yield {"a": r["a"] * 2}
output_data = {"columns": ["a"], "rows": doubled()}
"""
    r = run_wrapper(script, tmp=tmp, env={
        "BROKED_INPUT_NDJSON": inp,
        "BROKED_OUTPUT_NDJSON": out,
        "BROKED_INPUT_COLUMNS": '["a"]',
    })
    assert r.returncode == 0, r.stderr
    written = [json.loads(l)["a"] for l in open(out) if l.strip()]
    assert written == [0, 2, 4]


# ── resource preamble ──────────────────────────────────────────────────

def test_rlimit_preamble_is_a_noop_without_env():
    r = run_wrapper("", stdin=stdin_payload([{"a": 1}]))
    assert r.returncode == 0, r.stderr


def test_memory_limit_turns_allocation_into_memoryerror():
    script = """
chunks = []
for _ in range(64):
    chunks.append(bytearray(64 * 1024 * 1024))
"""
    r = run_wrapper(script, stdin=stdin_payload([]), env={"BROKED_LIMIT_MEMORY_MB": "256"})
    assert r.returncode != 0
    assert "MemoryError" in r.stderr


# ── task bundles (ADR-031) ─────────────────────────────────────────────
#
# These exercise worker_main._run_exec in-process: the same function the
# pool-driven worker calls per exec frame, with a fake socket capturing
# protocol frames. They pin the bundle-mount contract from the Python
# side; the cross-bundle isolation-by-digest guarantee is the pool's
# sub-key, proved end-to-end in engine/taskbundle_exec_test.go.

import io  # noqa: E402  (after the subprocess-based helpers above)

sys.path.insert(0, str(Path(__file__).resolve().parent.parent))  # wrapper dir: protocol, version, worker_main
import protocol  # noqa: E402
import worker_main  # noqa: E402

WRAPPER_CODE = compile(open(WRAPPER).read(), WRAPPER, "exec")


class FrameCapture:
    """Fake socket whose writes are reassembled into (type, payload) frames."""

    def __init__(self):
        self.buf = b""

    def write(self, data):
        self.buf += data
        return len(data)

    def flush(self):
        return None

    def frames(self):
        buf = io.BytesIO(self.buf)
        out = []
        while True:
            try:
                t, payload = protocol.read_frame(buf)
            except Exception:
                break
            out.append((t, payload))
        return out


def run_bundle_exec(bundle_dir, entry, files, script_override=None):
    capture = FrameCapture()
    tmp = tempfile.mkdtemp(prefix="brk-bundle-test-")
    msg = {
        "exec_id": "t-bundle",
        "bundle": {"dir": bundle_dir, "entry": entry, "files": files},
        "config": {},
        "params": {},
        "input": {"mode": "none"},
        "output": {"mode": "ndjson"},
        "timeout_ms": 30000,
    }
    worker_main._run_exec(capture, msg, WRAPPER_CODE, tmp)
    return capture.frames()


def make_bundle_dir(*, mult_name="helpers", mult):
    """A two-file bundle: a helper module and an entry importing it."""
    root = tempfile.mkdtemp(prefix="brk-bundle-dir-")
    with open(os.path.join(root, "helpers.py"), "w") as f:
        f.write(f"def apply(x):\n    return x * {mult}\n")
    entry = "tasks.py"
    with open(os.path.join(root, entry), "w") as f:
        f.write(
            "from helpers import apply\n"
            "output_data = {'columns': ['v'], 'rows': [{'v': apply(r['v'])} for r in rows]}\n"
        )
    # These in-process tests share one interpreter, so the helper module
    # from an EARLIER bundle stays cached in sys.modules and would shadow
    # this bundle's own helpers.py — the very leak the producer-side pool
    # fences by digest-joined sub-keys, and impossible to simulate across
    # two bundles here. Purge the module names to emulate a fresh worker.
    sys.modules.pop("helpers", None)
    sys.modules.pop("tasks", None)
    return root, entry, ["helpers.py", entry]


def test_bundle_exec_computes_with_a_relative_import():
    bundle_dir, entry, files = make_bundle_dir(mult_name="helpers", mult=2)
    frames = run_bundle_exec(bundle_dir, entry, files)
    result = [f for t, f in frames if t == protocol.FRAME_RESULT]
    assert result, [f for t, f in frames if t == protocol.FRAME_ERROR]
    assert result[0]["output"]["columns"] == ["v"]
    # Inline input is empty here ("none"); construct a rowed input instead.
    assert result[0]["output"]["rows"] == []


def test_bundle_sees_its_own_helper_module():
    capture = FrameCapture()
    tmp = tempfile.mkdtemp(prefix="brk-bundle-test-")
    bundle_dir, entry, files = make_bundle_dir(mult=3)
    msg = {
        "exec_id": "t-bundle-rows",
        "bundle": {"dir": bundle_dir, "entry": entry, "files": files},
        "config": {},
        "params": {},
        "input": {"mode": "inline", "rows": [{"v": 7}]},
        "output": {"mode": "ndjson"},
        "timeout_ms": 30000,
    }
    worker_main._run_exec(capture, msg, WRAPPER_CODE, tmp)
    frames = capture.frames()
    result = [f for t, f in frames if t == protocol.FRAME_RESULT]
    assert result, [f for t, f in frames if t == protocol.FRAME_ERROR]
    assert result[0]["output"]["rows"] == [{"v": 21}]


def test_bundle_cannot_import_from_the_wrapper_directory():
    # _escaped_probe lives in the wrapper dir, which the mount excludes
    # from sys.path; importing it must resolve against nothing.
    root = tempfile.mkdtemp(prefix="brk-bundle-dir-")
    entry = "tasks.py"
    with open(os.path.join(root, entry), "w") as f:
        f.write("import _escaped_probe\n")
    frames = run_bundle_exec(root, entry, [entry])
    errors = [f for t, f in frames if t == protocol.FRAME_ERROR]
    assert errors, "expected an error frame, got %r" % frames
    assert "No module named '_escaped_probe'" in errors[0]["message"] or "escaped_probe" in errors[0]["traceback"]


def test_bundle_entry_outside_the_manifest_is_refused():
    root, entry, files = make_bundle_dir(mult=2)
    frames = run_bundle_exec(root, "sneaky.py", files)
    errors = [f for t, f in frames if t == protocol.FRAME_ERROR]
    assert errors, "expected an internal refusal, got %r" % frames
    assert errors[0]["kind"] == "internal"
    assert "not in the manifest file list" in errors[0]["message"]


def test_bundle_import_scope_excludes_site_packages():
    wrapper_dir = str(Path(__file__).resolve().parent.parent)
    fake_main = object()
    paths = _scope_with_worker_main(wrapper_dir, "/tmp/bundle-x")
    bundle = paths[0]
    assert bundle == "/tmp/bundle-x"
    for p in paths[1:]:
        low = p.lower().replace("\\", "/")
        assert "site-packages" not in low and "dist-packages" not in low, p
        assert p != wrapper_dir


def _scope_with_worker_main(wrapper_dir, bundle_dir):
    import sys as _sys
    state = [
        bundle_dir,
        "/usr/lib/python3.11",
        "/usr/local/lib/python3.11/site-packages",
        wrapper_dir,
    ]
    saved = _sys.path[:]
    try:
        _sys.path[:] = state
        return worker_main._bundle_import_paths(bundle_dir)
    finally:
        _sys.path[:] = saved
