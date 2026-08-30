import assert from "node:assert/strict";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { executeContract } from "../contract.mjs";

function message(script, overrides = {}) {
  return {
    exec_id: "exec-1",
    script,
    config: { multiplier: 2 },
    params: { region: "eu" },
    input: { mode: "inline", columns: ["id"], rows: [{ id: 1 }] },
    output: { mode: "ndjson", path: "" },
    timeout_ms: 1000,
    ...overrides,
  };
}

test("runs inline scripts with the fixed namespace and top-level await", async () => {
  const output = await executeContract(message(`
    await sleep(-1);
    output_data = { columns: ["id", "value", "region"], rows: rows.map(row => ({ id: row.id, value: row.id * config.multiplier, region: params.region })) };
  `));
  assert.deepEqual(output, {
    mode: "inline",
    columns: ["id", "value", "region"],
    rows: [{ id: 1, value: 2, region: "eu" }],
    rows_written: 1,
  });
});

test("honors mutable rows and drains generators", async () => {
  const mutated = await executeContract(message("rows[0].id = 7;"));
  assert.equal(mutated.rows[0].id, 7);
  const assigned = await executeContract(message("output_data.rows = [{ id: 9 }];"));
  assert.deepEqual(assigned.rows, [{ id: 9 }]);
  const generated = await executeContract(message(`
    output_data = { columns: ["id"], rows: (async function* () { yield { id: 2 }; yield { id: 3 }; })() };
  `));
  assert.deepEqual(generated.rows, [{ id: 2 }, { id: 3 }]);
});

test("streams NDJSON input afresh and writes constant-memory output", async (t) => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "brokoli-js-contract-"));
  t.after(() => fs.rmSync(dir, { recursive: true, force: true }));
  const input = path.join(dir, "input.ndjson");
  const outputPath = path.join(dir, "output.ndjson");
  fs.writeFileSync(input, '{"id":1}\n\n{"id":2}\n');
  const output = await executeContract(message(`
    begin_emit(["id"]);
    for await (const row of rowsStream()) emit({ id: row.id * 2 });
  `, {
    input: { mode: "ndjson", columns: ["id"], path: input },
    output: { mode: "ndjson", path: outputPath },
  }));
  assert.deepEqual(output, { mode: "ndjson", path: outputPath, columns: ["id"], rows_written: 2 });
  assert.equal(fs.readFileSync(outputPath, "utf8"), '{"id":2}\n{"id":4}\n');

  const repeated = await executeContract(message(`
    const ids = [];
    for await (const row of rowsStream()) { row.id = 99; ids.push(row.id); }
    for await (const row of rowsStream()) ids.push(row.id);
    output_data = { columns: ["id"], rows: ids.map(id => ({ id })) };
  `, {
    input: { mode: "ndjson", columns: ["id"], path: input },
    output: { mode: "ndjson", path: outputPath },
  }));
  assert.equal(fs.readFileSync(repeated.path, "utf8"), '{"id":99}\n{"id":99}\n{"id":1}\n{"id":2}\n');

  const mutated = await executeContract(message("rows[0].id = 7;", {
    input: { mode: "ndjson", columns: ["id"], path: input },
    output: { mode: "ndjson", path: outputPath },
  }));
  assert.equal(fs.readFileSync(mutated.path, "utf8"), '{"id":7}\n{"id":2}\n');
});

test("begin_emit distinguishes empty output from passthrough", async () => {
  const output = await executeContract(message('begin_emit(["id"]);'));
  assert.deepEqual(output, { mode: "inline", columns: ["id"], rows: [], rows_written: 0 });
  const emitted = await executeContract(message('output_data = { columns: ["wrong"], rows: [{ wrong: true }] }; emit({ id: 2 });'));
  assert.deepEqual(emitted, { mode: "inline", columns: ["id"], rows: [{ id: 2 }], rows_written: 1 });
});

test("captures console levels and exposes only contractual host names", async () => {
  const logs = [];
  await executeContract(message(`
    console.log("hello", 1);
    console.warn("careful");
    console.error("bad");
    if (typeof process !== "undefined" || typeof require !== "undefined" || typeof setTimeout !== "undefined" || typeof Atomics !== "undefined") throw new Error("host capability leaked");
  `), { log: (level, value) => logs.push({ level, value }) });
  assert.deepEqual(logs, [
    { level: "info", value: "hello 1" },
    { level: "warning", value: "careful" },
    { level: "error", value: "bad" },
  ]);
  await assert.rejects(executeContract(message('sleep.constructor("return process")();')), /Code generation from strings disallowed/);
});

test("names user stacks and classifies ordinary RangeError as user code", async () => {
  await assert.rejects(executeContract(message('throw new RangeError("bad index");')), (error) => {
    assert.equal(error.kind, "user_exception");
    assert.match(error.cause.stack, /<code-node>/);
    return true;
  });
});

test("uses a fresh VM context for every execution", async () => {
  await executeContract(message("globalThis.leaked = 1;"));
  await assert.doesNotReject(executeContract(message('if (globalThis.leaked) throw new Error("leaked");')));
});

test("applies the frame deadline while draining output iterables", async () => {
  await assert.rejects(executeContract(message(`
    output_data = { columns: ["id"], rows: (async function* () { await sleep(100); yield { id: 1 }; })() };
  `, { timeout_ms: 20 })), (error) => {
    assert.equal(error.kind, "resource_limit");
    assert.match(error.message, /timed out/);
    return true;
  });
});

test("classifies output iterable failures as user exceptions", async () => {
  await assert.rejects(executeContract(message(`
    output_data = { columns: [], rows: (async function* () { throw new Error("iterator boom"); })() };
  `)), (error) => {
    assert.equal(error.kind, "user_exception");
    assert.match(error.message, /iterator boom/);
    return true;
  });
});
