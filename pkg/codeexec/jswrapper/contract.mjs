import fs from "node:fs";
import vm from "node:vm";
import { format } from "node:util";

export class ContractError extends Error {
  constructor(kind, message, options = {}) {
    super(message, options);
    this.kind = kind;
  }
}

function rowsFromNDJSON(path) {
  try {
    return fs.readFileSync(path, "utf8").split(/\r?\n/).filter((line) => line.trim()).map((line) => JSON.parse(line));
  } catch (error) {
    throw new ContractError("internal", `read staged input: ${error.message}`, { cause: error });
  }
}

async function* streamNDJSON(path) {
  let pending = "";
  let stream;
  try {
    stream = fs.createReadStream(path, { encoding: "utf8" });
    for await (const chunk of stream) {
      pending += chunk;
      let newline;
      while ((newline = pending.indexOf("\n")) >= 0) {
        const line = pending.slice(0, newline).replace(/\r$/, "");
        pending = pending.slice(newline + 1);
        if (line.trim()) yield JSON.parse(line);
      }
    }
    if (pending.trim()) yield JSON.parse(pending);
  } catch (error) {
    throw new ContractError("internal", `stream staged input: ${error.message}`, { cause: error });
  } finally {
    stream?.destroy();
  }
}

function userError(error) {
  if (error instanceof ContractError) return error;
  const message = error?.message || String(error) || error?.constructor?.name || "script failed";
  const allocation = error?.code === "ERR_WORKER_OUT_OF_MEMORY"
    || (error instanceof RangeError && /heap|allocation|out of memory/i.test(message));
  return new ContractError(allocation ? "resource_limit" : "user_exception", message, { cause: error });
}

function serializeRow(row) {
  try {
    const encoded = JSON.stringify(row);
    if (encoded === undefined) throw new TypeError("row is not JSON serializable");
    return encoded;
  } catch (error) {
    throw new ContractError("user_exception", `serialize output row: ${error.message}`, { cause: error });
  }
}

function rowIterator(value) {
  if (value?.[Symbol.asyncIterator]) return value[Symbol.asyncIterator]();
  if (value?.[Symbol.iterator]) return value[Symbol.iterator]();
  throw new ContractError("user_exception", "output_data.rows must be an array, Iterable, or AsyncIterable");
}

export async function executeContract(message, hooks = {}) {
  const input = message.input || {};
  const output = message.output || {};
  const inline = input.mode !== "ndjson";
  let materialized = input.mode === "inline" ? (input.rows || []) : input.mode === "none" ? [] : null;
  let rowsAccessed = false;
  let outputData;
  let emitMode = false;
  let emitColumns = null;
  let emittedRows = 0;
  let emittedInline = [];
  let outputFD = null;
  const timers = new Set();
  const startedAt = Date.now();
  const timeoutMs = Math.max(0, Number(message.timeout_ms) || 0);

  const remaining = () => timeoutMs > 0 ? Math.max(0, timeoutMs - (Date.now() - startedAt)) : 0;
  const awaitWithinDeadline = async (value) => {
    if (!timeoutMs) return await value;
    const left = remaining();
    if (!left) throw new ContractError("resource_limit", `script timed out after ${timeoutMs}ms`);
    let timer;
    try {
      return await Promise.race([
        value,
        new Promise((_, reject) => {
          timer = setTimeout(() => reject(new ContractError("resource_limit", `script timed out after ${timeoutMs}ms`)), left);
        }),
      ]);
    } finally {
      if (timer) clearTimeout(timer);
    }
  };

  const loadRows = () => {
    rowsAccessed = true;
    if (materialized === null) materialized = rowsFromNDJSON(input.path);
    return JSON.stringify(materialized);
  };
  const makePull = () => {
    const iterator = input.mode === "ndjson"
      ? streamNDJSON(input.path)[Symbol.asyncIterator]()
      : (async function* () { for (const row of input.rows || []) yield row; })();
    return async () => {
      const next = await iterator.next();
      return next.done ? null : serializeRow(next.value);
    };
  };
  const beginEmit = (declared) => {
    emitMode = true;
    if (declared !== undefined) {
      if (!Array.isArray(declared)) throw new ContractError("user_exception", "begin_emit columns must be an array");
      emitColumns = [...declared];
    }
  };
  const emit = (row) => {
    beginEmit();
    if (emitColumns === null && emittedRows === 0 && row && typeof row === "object") emitColumns = Object.keys(row);
    if (inline) emittedInline.push(JSON.parse(serializeRow(row)));
    else {
      try {
        if (outputFD === null) outputFD = fs.openSync(output.path, "w", 0o600);
        fs.writeSync(outputFD, `${serializeRow(row)}\n`);
      } catch (error) {
        if (error instanceof ContractError) throw error;
        throw new ContractError("internal", `write staged output: ${error.message}`, { cause: error });
      }
    }
    emittedRows++;
  };
  const sleep = (value) => {
    const ms = Number(value);
    if (!Number.isFinite(ms)) return Promise.reject(new TypeError("sleep milliseconds must be finite"));
    return new Promise((resolve) => {
      const timer = setTimeout(() => { timers.delete(timer); resolve(); }, Math.max(0, ms));
      timers.add(timer);
    });
  };
  const log = (level, values) => {
    for (const line of format(...values).split(/\r?\n/)) {
      if (line) hooks.log?.(level, line);
    }
  };

  const sandbox = {};
  const context = vm.createContext(sandbox, { codeGeneration: { strings: false, wasm: false } });
  const parseInContext = vm.runInContext("JSON.parse", context);
  const syncBridge = vm.runInContext("callback => (...args) => callback(...args)", context);
  const asyncBridge = vm.runInContext("callback => async (...args) => await callback(...args)", context);
  const rowsGetter = vm.runInContext("load => { let rows; return () => rows ??= JSON.parse(load()); }", context)(loadRows);
  const rowsStream = vm.runInContext(`makePull => () => (async function* () {
    const pull = makePull();
    for (;;) {
      const encoded = await pull();
      if (encoded === null) return;
      yield JSON.parse(encoded);
    }
  })()`, context)(makePull);
  const columns = parseInContext(JSON.stringify(Array.isArray(input.columns) ? input.columns : []));
  const defaultOutput = vm.runInContext(`(columns, getRows) => {
    let assigned = false;
    let assignedRows;
    const value = { columns };
    Object.defineProperty(value, "rows", {
      enumerable: true,
      get: () => assigned ? assignedRows : getRows(),
      set: rows => { assigned = true; assignedRows = rows; },
    });
    return value;
  }`, context)(columns, rowsGetter);
  outputData = defaultOutput;

  Object.assign(sandbox, {
    columns,
    config: parseInContext(JSON.stringify(message.config || {})),
    params: parseInContext(JSON.stringify(message.params || {})),
    rowsStream,
    emit: syncBridge(emit),
    begin_emit: syncBridge(beginEmit),
    sleep: asyncBridge(sleep),
    console: vm.runInContext("(log, warn, error) => ({ log, warn, error })", context)(
      syncBridge((...values) => log("info", values)),
      syncBridge((...values) => log("warning", values)),
      syncBridge((...values) => log("error", values)),
    ),
    process: undefined,
    require: undefined,
    module: undefined,
    Buffer: undefined,
    setTimeout: undefined,
    setInterval: undefined,
    Atomics: undefined,
    SharedArrayBuffer: undefined,
  });
  Object.defineProperty(sandbox, "rows", { enumerable: true, get: rowsGetter });
  Object.defineProperty(sandbox, "output_data", {
    enumerable: true,
    get: vm.runInContext("callback => () => callback()", context)(() => outputData),
    set: vm.runInContext("callback => value => callback(value)", context)((value) => { outputData = value; }),
  });

  try {
    const script = new vm.Script(`(async () => {\n${message.script || ""}\n})()`, { filename: "<code-node>", lineOffset: -1 });
    const execution = script.runInContext(context, timeoutMs > 0 ? { timeout: Math.max(1, remaining()) } : {});
    await awaitWithinDeadline(execution);

    if (emitMode) {
      const resolvedColumns = emitColumns ?? [];
      if (inline || emittedRows === 0) {
        if (!inline && output.path) fs.rmSync(output.path, { force: true });
        return { mode: "inline", columns: resolvedColumns, rows: emittedInline, rows_written: emittedRows };
      }
      return { mode: "ndjson", path: output.path, columns: resolvedColumns, rows_written: emittedRows };
    }

    if (!outputData || typeof outputData !== "object") {
      throw new ContractError("user_exception", "output_data must be an object with columns and rows");
    }
    const resolvedColumns = Array.isArray(outputData.columns) ? [...outputData.columns] : [];
    if (!inline && outputData === defaultOutput && !rowsAccessed) {
      try {
        fs.copyFileSync(input.path, output.path);
        let rowsWritten = 0;
        for await (const _row of streamNDJSON(input.path)) rowsWritten++;
        return { mode: "ndjson", path: output.path, columns: resolvedColumns, rows_written: rowsWritten };
      } catch (error) {
        if (error instanceof ContractError) throw error;
        throw new ContractError("internal", `copy staged output: ${error.message}`, { cause: error });
      }
    }

    const iterator = rowIterator(outputData.rows);
    if (inline) {
      const rows = [];
      for (;;) {
        const next = await awaitWithinDeadline(iterator.next());
        if (next.done) break;
        rows.push(JSON.parse(serializeRow(next.value)));
      }
      return { mode: "inline", columns: resolvedColumns, rows, rows_written: rows.length };
    }

    let fd;
    let rowsWritten = 0;
    try {
      fd = fs.openSync(output.path, "w", 0o600);
      for (;;) {
        const next = await awaitWithinDeadline(iterator.next());
        if (next.done) break;
        fs.writeSync(fd, `${serializeRow(next.value)}\n`);
        rowsWritten++;
      }
    } catch (error) {
      if (error instanceof ContractError) throw error;
      throw userError(error);
    } finally {
      if (fd !== undefined) fs.closeSync(fd);
    }
    hooks.progress?.(95, `Wrote ${rowsWritten} rows via NDJSON`);
    return { mode: "ndjson", path: output.path, columns: resolvedColumns, rows_written: rowsWritten };
  } catch (error) {
    const classified = userError(error);
    if (!classified.stack && error?.stack) classified.stack = error.stack;
    throw classified;
  } finally {
    for (const timer of timers) clearTimeout(timer);
    if (outputFD !== null) fs.closeSync(outputFD);
  }
}
