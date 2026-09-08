#!/usr/bin/env node
// Reference brokoli.task-runtime/v1 harness for the `node` runtime class
// (ADR-033 sections 3 and 7) -- the second required reference adapter
// alongside pkg/taskharness/pyharness, and the proof that this protocol
// is genuinely language-neutral rather than "whatever Python happens to
// do."
//
// Mirrors harness.py's behavior frame for frame: read exactly one
// 'start' frame from stdin, emit 'ready', load the invocation descriptor
// it points to (this adapter's own convention -- see invocation.go's
// Invocation type), import the named module, call the named symbol, and
// write a task-result-v1 candidate manifest to result_path before
// emitting 'completed'.
//
// Two deliberate differences from harness.py, both forced by the
// language rather than chosen:
//
//   - Kwargs are passed as ONE object argument (`fn(kwargs)`), since
//     JavaScript has no keyword arguments. Python's `func(**kwargs)` has
//     no faithful JS equivalent; an options object is the idiomatic one.
//   - The result is awaited, so an `async` task function works. Python's
//     reference harness calls synchronously.
//
// Failure taxonomy (ADR-033 section 14), identical to harness.py: a
// throw from the task itself is 'user_code'; anything wrong with what
// the worker handed this harness is 'contract_violation'. Categories
// only a trusted worker may originate (runtime_protocol, platform,
// resource_exhausted, lease_lost) are never emitted here.
//
// Known limitation, identical to harness.py and named rather than
// hidden: this harness does not read a mid-task 'cancel' frame. The
// worker's SIGTERM/SIGKILL escalation after the cancellation grace
// period (pkg/taskharness.Run) covers it fully; only the cooperative
// shutdown path is unimplemented.
//
// Resource ceilings: unlike harness.py, which self-applies RLIMIT_AS,
// the memory ceiling is applied by the parent as V8's
// --max-old-space-size flag (see invocation.go's Command, mirroring
// pkg/codeexec/worker.go's own TypeScript convention). CPU, file size
// and open files are enforced externally via pkg/proctree before this
// process starts, exactly as they are for Python.

import crypto from "node:crypto";
import fs from "node:fs";
import path from "node:path";
import process from "node:process";
import readline from "node:readline";
import { pathToFileURL } from "node:url";

const PROTOCOL = "brokoli.task-runtime/v1";
const ADAPTER = "brokoli-node-taskharness";
const ADAPTER_VERSION = "0.1.0";

function emit(frame) {
  process.stdout.write(JSON.stringify(frame) + "\n");
}

function fail(category, code, message, retryable = false) {
  emit({ type: "failed", failure: { category, code, message, retryable } });
}

async function readStartFrame() {
  const rl = readline.createInterface({ input: process.stdin });
  let line = null;
  for await (const l of rl) {
    line = l;
    break;
  }
  rl.close();
  if (line === null) {
    // No frame to report a failure into (ready was never sent) -- the
    // worker's own "exited before any terminal frame" detection is where
    // this belongs, not something this harness narrates.
    process.exit(1);
  }
  let start;
  try {
    start = JSON.parse(line);
  } catch (err) {
    fail("contract_violation", "malformed_start", `start frame is not valid JSON: ${err.message}`);
    process.exit(1);
  }
  if (start.type !== "start" || start.protocol !== PROTOCOL) {
    fail("contract_violation", "unexpected_start", "first frame was not a valid start frame");
    process.exit(1);
  }
  return start;
}

function loadInvocation(invocationPath) {
  const inv = JSON.parse(fs.readFileSync(invocationPath, "utf8"));
  for (const key of ["module", "symbol", "interface_digest"]) {
    if (!(key in inv)) {
      throw new Error(`invocation descriptor is missing required key '${key}'`);
    }
  }
  return inv;
}

// resolveModuleFile mirrors Python's "module name resolved against a
// search path" convention, which Node has no direct equivalent for: it
// tries each root for the candidate filenames a bundled task module can
// plausibly use, in a fixed order, and names every path it tried when
// none exists (rather than surfacing Node's own resolver error, which
// would name only the last attempt).
function resolveModuleFile(roots, moduleName) {
  const tried = [];
  for (const root of roots) {
    for (const candidate of [
      `${moduleName}.mjs`,
      `${moduleName}.js`,
      path.join(moduleName, "index.mjs"),
      path.join(moduleName, "index.js"),
    ]) {
      const full = path.join(root, candidate);
      tried.push(full);
      if (fs.existsSync(full)) return full;
    }
  }
  throw new Error(`cannot resolve module '${moduleName}'; tried: ${tried.join(", ")}`);
}

const DATASET_FILENAME = "result.ndjson";

// readInputRows reads the NDJSON rows the trusted worker staged for this
// attempt. Written by the worker, not the task, so this is a cooperating
// file rather than untrusted input -- a malformed line here is a bug in
// the worker, reported as contract_violation because the harness cannot
// tell the task anything useful about it.
function readInputRows(inputPath) {
  const rows = [];
  let lineNo = 0;
  for (const line of fs.readFileSync(inputPath, "utf8").split("\n")) {
    lineNo++;
    const trimmed = line.trim();
    if (!trimmed) continue;
    const unsafe = unsafeIntegerLiteral(trimmed);
    if (unsafe !== null) {
      // Thrown, not failed directly: the caller already wraps this in the
      // contract_violation/invalid_input handler that also exits. Calling
      // fail() here would emit a terminal frame and then keep going,
      // emitting "completed" after it -- a protocol violation the
      // worker correctly rejects.
      throw new Error(
        `input row ${lineNo} contains the integer ${unsafe}, which JavaScript cannot represent ` +
          `exactly (|value| > Number.MAX_SAFE_INTEGER). JSON.parse would silently return a ` +
          `different number, so this task is refused rather than run against altered data. Use a ` +
          `string for 64-bit identifiers, or run this task on a runtime with exact 64-bit ` +
          `integers (python, jvm).`,
      );
    }
    rows.push(JSON.parse(trimmed));
  }
  return rows;
}

// unsafeIntegerLiteral finds an integer literal the JS number type
// cannot hold exactly, WITHOUT parsing -- parsing is what loses it.
//
// brokoli#479 fixed exactly this class of defect on the Go side: a
// decoder that turned every JSON number into a float64 silently altered
// 9007199254740993 to ...992. JavaScript has no wider number, so the
// harness cannot fix the value the way Go could; refusing is the honest
// remaining option, and it beats handing a task an id that is quietly
// off by one.
//
// The check that looks obvious does not work: `v === 9007199254740993`
// is TRUE after the value has been altered, because the literal in the
// comparison rounds identically. Only the raw text knows.
//
// Strings are skipped so digits inside them never match, escapes
// included. A literal with a fraction or exponent is left alone: it was
// never a claim to an exact integer.
function unsafeIntegerLiteral(line) {
  let i = 0;
  const n = line.length;
  while (i < n) {
    const c = line[i];
    if (c === '"') {
      i++;
      while (i < n) {
        if (line[i] === "\\") { i += 2; continue; }
        if (line[i] === '"') { i++; break; }
        i++;
      }
      continue;
    }
    if (c === "-" || (c >= "0" && c <= "9")) {
      const start = i;
      if (line[i] === "-") i++;
      while (i < n && line[i] >= "0" && line[i] <= "9") i++;
      if (line[i] === "." || line[i] === "e" || line[i] === "E") {
        while (i < n && /[0-9.eE+-]/.test(line[i])) i++;
        continue;
      }
      const lit = line.slice(start, i);
      if (lit !== "-" && !Number.isSafeInteger(Number(lit))) return lit;
      continue;
    }
    i++;
  }
  return null;
}

// writeDatasetOutput serializes rows to NDJSON in stagingDir and
// describes them by reference.
//
// The declared interface, not the value's runtime shape, is what says a
// port is a dataset (see the invocation descriptor's output_kind), so a
// task that declared one and returned something that is not an iterable
// of row objects is a contract violation with a precise message rather
// than a confusing serialization error. Async iterables are accepted
// too -- a Node task streaming rows is idiomatic, and refusing it would
// make the adapter worse than the language it wraps.
//
// Size and checksum are computed from the bytes actually written, in
// the same pass, so the worker's own verification (ADR-033 section 7
// rule 6) compares against what is really on disk.
async function writeDatasetOutput(stagingDir, rows) {
  if (rows === null || typeof rows !== "object" || (!rows[Symbol.iterator] && !rows[Symbol.asyncIterator])) {
    throw new TypeError(
      `task declares a dataset output but returned ${rows === null ? "null" : typeof rows}; expected an iterable of row objects`,
    );
  }
  const full = path.join(stagingDir, DATASET_FILENAME);
  const hash = crypto.createHash("sha256");
  const handle = fs.openSync(full, "w");
  let size = 0;
  let i = 0;
  try {
    for await (const row of rows) {
      if (row === null || typeof row !== "object" || Array.isArray(row)) {
        throw new TypeError(
          `task declares a dataset output but row ${i} is ${row === null ? "null" : typeof row}; every row must be an object`,
        );
      }
      const line = Buffer.from(JSON.stringify(row) + "\n", "utf8");
      fs.writeSync(handle, line);
      hash.update(line);
      size += line.length;
      i++;
    }
  } finally {
    fs.closeSync(handle);
  }
  return {
    kind: "dataset",
    path: DATASET_FILENAME,
    codec: "ndjson/v1",
    size_bytes: size,
    checksum: "sha256:" + hash.digest("hex"),
  };
}

const ARTIFACT_FILENAME = "result.bin";

// writeArtifactOutput writes opaque bytes to stagingDir and describes
// them by reference. A task declaring an artifact output returns the
// bytes themselves -- a Buffer/TypedArray, or a string encoded UTF-8.
// Anything else is a contract violation named precisely, since
// "expected bytes, got object" is the only form of that error an author
// can act on.
function writeArtifactOutput(stagingDir, payload, mediaType) {
  let buf;
  if (typeof payload === "string") {
    buf = Buffer.from(payload, "utf8");
  } else if (Buffer.isBuffer(payload)) {
    buf = payload;
  } else if (ArrayBuffer.isView(payload) || payload instanceof ArrayBuffer) {
    buf = Buffer.from(payload.buffer ?? payload);
  } else {
    throw new TypeError(
      `task declares an artifact output but returned ${payload === null ? "null" : typeof payload}; expected bytes or a string`,
    );
  }
  fs.writeFileSync(path.join(stagingDir, ARTIFACT_FILENAME), buf);
  return {
    kind: "artifact",
    path: ARTIFACT_FILENAME,
    // The manifest has no media_type field, so an artifact states its
    // media type in codec -- see the engine's artifactMediaType.
    codec: mediaType || "application/octet-stream",
    size_bytes: buf.length,
    checksum: "sha256:" + crypto.createHash("sha256").update(buf).digest("hex"),
  };
}

// writeCollectionOutput describes separately addressable items, each by
// its own contract. A task declaring a collection returns a Map, a plain
// object, or an iterable of [key, value] pairs. The key is what makes an
// item separately addressable (ADR-032 section 6), so it is required
// rather than derived from position -- positional identity is exactly
// what a key exists to replace.
//
// Buffer/TypedArray items become artifacts, each staged as its own file
// with its own checksum; everything else is an inline scalar.
function writeCollectionOutput(stagingDir, items, mediaType) {
  let pairs;
  if (items instanceof Map) {
    pairs = [...items.entries()];
  } else if (Array.isArray(items)) {
    pairs = items;
  } else if (items && typeof items === "object") {
    pairs = Object.entries(items);
  } else {
    throw new TypeError(
      `task declares a collection output but returned ${items === null ? "null" : typeof items}; expected a Map, object, or iterable of [key, value] pairs`,
    );
  }

  const out = pairs.map(([key, value], i) => {
    if (key === undefined) {
      throw new TypeError(`task collection item ${i} has no key; a collection's items must be separately addressable`);
    }
    if (Buffer.isBuffer(value) || ArrayBuffer.isView(value)) {
      const buf = Buffer.isBuffer(value) ? value : Buffer.from(value.buffer ?? value);
      const name = `item-${i}.bin`;
      fs.writeFileSync(path.join(stagingDir, name), buf);
      return {
        kind: "artifact",
        path: name,
        codec: mediaType || "application/octet-stream",
        size_bytes: buf.length,
        checksum: "sha256:" + crypto.createHash("sha256").update(buf).digest("hex"),
        item_key: key,
      };
    }
    return { kind: "scalar", value, item_key: key };
  });
  return { kind: "collection", items: out };
}

async function main() {
  const start = await readStartFrame();
  emit({
    type: "ready",
    protocol: PROTOCOL,
    adapter: ADAPTER,
    adapter_version: ADAPTER_VERSION,
    capabilities: [],
  });

  let inv;
  let modulePath;
  try {
    inv = loadInvocation(start.invocation_path);
    modulePath = resolveModuleFile(inv.module_roots ?? [], inv.module);
  } catch (err) {
    fail("contract_violation", "invalid_invocation", err.message);
    process.exit(1);
  }

  const args = { ...(inv.kwargs ?? {}) };
  if (inv.input_path) {
    try {
      args.input = readInputRows(inv.input_path);
    } catch (err) {
      fail("contract_violation", "invalid_input", err.message);
      process.exit(1);
    }
  }

  let result;
  try {
    const mod = await import(pathToFileURL(modulePath).href);
    // `mod.default?.[symbol]` covers a CommonJS bundle, whose exports
    // arrive under default rather than as namespace members.
    const fn = mod[inv.symbol] ?? mod.default?.[inv.symbol];
    if (typeof fn !== "function") {
      fail(
        "contract_violation",
        "symbol_not_callable",
        `module '${inv.module}' has no callable export named '${inv.symbol}'`,
      );
      process.exit(1);
    }
    result = await fn(args);
  } catch (err) {
    fail("user_code", "task_raised", err && err.stack ? err.stack : String(err));
    process.exit(1);
  }

  try {
    fs.mkdirSync(start.output_staging_dir, { recursive: true });
    let port;
    if (inv.output_kind === "dataset") {
      port = await writeDatasetOutput(start.output_staging_dir, result);
    } else if (inv.output_kind === "artifact") {
      port = writeArtifactOutput(start.output_staging_dir, result, inv.output_media_type);
    } else if (inv.output_kind === "collection") {
      port = writeCollectionOutput(start.output_staging_dir, result, inv.output_media_type);
    } else {
      port = { kind: "scalar", value: result === undefined ? null : result };
    }
    fs.writeFileSync(
      start.result_path,
      JSON.stringify({
        contract: "brokoli.task-result/v1",
        interface_digest: inv.interface_digest,
        outputs: { result: port },
      }),
      "utf8",
    );
  } catch (err) {
    fail("contract_violation", "result_write_failed", err.message);
    process.exit(1);
  }

  emit({ type: "completed" });
}

await main();
