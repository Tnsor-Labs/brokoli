/**
 * Cross-language integration test: @sodp/client (TypeScript) ↔ Go SODP server.
 *
 * This script is executed by the Go test TestCrossLanguage_SodpClient.
 * The Go test passes the server URL as the first CLI argument and injects
 * events on a fixed schedule (see crosslang_test.go).
 *
 * Exit 0 = pass, exit 1 = fail (stderr has the reason).
 */
import { SodpClient, applyOps } from "@sodp/client";

const url = process.argv[2];
if (!url) {
  console.error("usage: node sodp_client_test.mjs ws://host:port/api/ws");
  process.exit(1);
}

const errors = [];
function assert(cond, msg) {
  if (!cond) {
    errors.push(msg);
    console.error("FAIL:", msg);
  }
}

try {
  // =========================================================================
  // Sanity: applyOps is re-exported from package root (P3 fix in 0.2.0)
  // =========================================================================
  assert(typeof applyOps === "function", "applyOps should be re-exported from @sodp/client");
  // RFC 6901 array append should now work
  const appended = applyOps([1, 2, 3], [{ op: "ADD", path: "/-", value: 4 }]);
  assert(JSON.stringify(appended) === "[1,2,3,4]", `applyOps /- should append, got ${JSON.stringify(appended)}`);
  // Unknown op type should throw, not silently no-op
  let threw = false;
  try { applyOps({ x: 1 }, [{ op: "update", path: "/x", value: 2 }]); }
  catch (e) { threw = true; }
  assert(threw, "applyOps should throw on unknown op type");

  const client = new SodpClient(url, {
    reconnect: false,
    onConnect: () => console.log("connected"),
    onDisconnect: () => console.log("disconnected"),
  });

  await client.ready;
  console.log("ready");

  // =========================================================================
  // Test 1: Watch _events — STATE_INIT with null (key doesn't exist yet)
  // =========================================================================
  let rawCallbackCount = 0;
  let lastValue = undefined;
  let lastMeta = undefined;
  let initSourceCount = 0;
  let deltaSourceCount = 0;

  const unsub = client.watch("_events", (value, meta) => {
    rawCallbackCount++;
    lastValue = value;
    lastMeta = meta;
    if (meta.source === "init") initSourceCount++;
    if (meta.source === "delta") deltaSourceCount++;
    console.log(`callback #${rawCallbackCount}: source=${meta.source} value=${JSON.stringify(value)?.slice(0, 80)}`);
  });

  await sleep(200);
  assert(rawCallbackCount === 1, `expected 1 callback (STATE_INIT), got ${rawCallbackCount}`);
  assert(lastValue === null, `STATE_INIT value should be null, got: ${JSON.stringify(lastValue)}`);
  assert(lastMeta.source === "init", `first callback source should be "init", got: ${lastMeta.source}`);

  // =========================================================================
  // Test 2: Server injects 3 events → client receives 3 DELTAs
  // The server now sends single-element ADD ops at /- (O(1) wire cost).
  // applyOps reconstructs the full array client-side.
  //
  // Wait window covers the 1500/2000/2500ms injection schedule with margin.
  // =========================================================================
  await sleep(3000);

  assert(rawCallbackCount === 4, `expected 4 total callbacks (1 init + 3 deltas), got ${rawCallbackCount}`);
  assert(initSourceCount === 1, `expected 1 init callback, got ${initSourceCount}`);
  assert(deltaSourceCount === 3, `expected 3 delta callbacks, got ${deltaSourceCount}`);
  assert(Array.isArray(lastValue), `value should be array, got ${typeof lastValue}`);
  assert(lastValue?.length === 3, `should have 3 events, got ${lastValue?.length}`);

  const types = lastValue.map(e => e.type);
  assert(types[0] === "run.started", `event 0 type: ${types[0]}`);
  assert(types[1] === "node.completed", `event 1 type: ${types[1]}`);
  assert(types[2] === "run.completed", `event 2 type: ${types[2]}`);
  assert(lastValue[0].run_id === "cross-lang-1", `run_id: ${lastValue[0].run_id}`);

  // =========================================================================
  // Test 3: ws.ts baseline pattern using meta.source === "init"
  // =========================================================================
  let baseline = 0;
  const forwarded = [];

  client.watch("_events2", (events, meta) => {
    if (meta.source === "init") {
      baseline = events?.length ?? 0;
      console.log(`init: baseline set to ${baseline}`);
      return;
    }
    if (!events) { baseline = 0; return; }
    if (events.length > baseline) {
      const newEvents = events.slice(baseline);
      baseline = events.length;
      for (const ev of newEvents) forwarded.push(ev);
      console.log(`delta: forwarded ${newEvents.length}, total: ${forwarded.length}`);
    } else {
      baseline = events.length;
    }
  });

  // Go test injects 2 events into _events2 at ~2700ms and ~3000ms (relative
  // to the Go goroutine start). Wait long enough for both to land.
  await sleep(2000);
  assert(forwarded.length === 2, `should have forwarded 2 events, got ${forwarded.length}`);
  assert(forwarded[0]?.type === "test.event.1", `first forwarded: ${forwarded[0]?.type}`);
  assert(forwarded[1]?.type === "test.event.2", `second forwarded: ${forwarded[1]?.type}`);

  // =========================================================================
  // Test 4: meta.version monotonically increases on deltas
  // =========================================================================
  assert(lastMeta.version > 0, `version should be > 0, got ${lastMeta.version}`);

  // =========================================================================
  // Test 5: CALL state.set + watch round-trip
  // =========================================================================
  const result = await client.set("test.roundtrip", { hello: "from-js", n: 42 });
  assert(result !== undefined, "CALL result should not be undefined");
  console.log("CALL result:", JSON.stringify(result));

  let watchedValue = undefined;
  client.watch("test.roundtrip", (value) => { watchedValue = value; });
  await sleep(200);

  assert(watchedValue !== null && watchedValue !== undefined, `should have value`);
  assert(watchedValue?.hello === "from-js", `hello: ${watchedValue?.hello}`);
  assert(watchedValue?.n === 42, `n: ${watchedValue?.n}`);

  unsub();
  client.close();

  if (errors.length > 0) {
    console.error(`\n${errors.length} assertion(s) failed`);
    process.exit(1);
  }

  console.log("\nAll cross-language assertions passed");
  process.exit(0);

} catch (err) {
  console.error("FATAL:", err);
  process.exit(1);
}

function sleep(ms) {
  return new Promise(resolve => setTimeout(resolve, ms));
}
