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
