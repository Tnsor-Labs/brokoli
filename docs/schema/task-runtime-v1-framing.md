# Task runtime protocol v1: wire framing and state machine (ADR-033 section 7)

This is the spec a future duplex-protocol implementation must match — it
is not the implementation itself, exactly like `task-interface-canonicalization.md`'s
own scoping note. `task-runtime-v1.json` validates one *decoded* frame's
shape; the rules below govern the *wire encoding* and *ordering*, which a
JSON Schema over a single frame cannot express.

Not yet implemented anywhere in this repository (rollout phase 0, issue
#439 step 5): no Go/Python/Node code speaks this protocol yet. This does
not replace ADR-029's framed binary protocol (`pkg/codeexec`), which
remains the protocol between the Go engine and a pooled `code`-node
worker (ADR-035 Decision 3) — a worker process speaks at most one of the
two, determined by which node kind it was launched to execute.

## Wire framing

- Duplex JSON Lines: worker control frames on the harness's stdin,
  harness event frames on stdout.
- Each frame is exactly one UTF-8 JSON object terminated by a single LF.
- No literal unescaped newline inside a frame.
- Duplicate keys within one frame are rejected.
- A frame, including its trailing LF, is capped at 1 MiB.
- Empty lines, invalid UTF-8, invalid JSON, an oversized frame, or any
  harness output on stdout *before* `ready` are all protocol failures
  (`runtime_protocol` category, section 14) — not warnings, not
  best-effort recovery.

## stdout ownership

Before loading user modules, a managed harness (Python/Node adapter)
redirects user `stdout`/`stderr` to structured `log` events; the
protocol's own frames travel on a private descriptor the user code never
touches. A direct native/container task that speaks the protocol itself
must reserve stdout for protocol frames and use stderr or explicit `log`
events for anything else — corrupting stdout with unstructured output is
that task's own protocol failure, not something the worker recovers from.

## State machine

```
start -> ready -> (log | progress | warning | metric)* -> completed | failed
```

- Exactly one terminal frame (`completed` or `failed`) is valid per
  attempt.
- Exiting before any terminal frame, sending a frame after a terminal
  frame, or a non-zero process exit *after* `completed` was already read
  are all protocol failures.
- The worker waits for process exit before accepting the terminal state
  as final — a `completed`/`failed` frame is not itself suffient; the
  process must actually end.
- The harness must emit `ready` before a worker-enforced handshake
  deadline; a harness that never reaches `ready` in time is a
  `runtime_protocol` failure, not a hang the worker waits out indefinitely.

## Cancellation

- The worker may send `cancel` only after `ready` has been read.
- The harness must reply `cancel_ack` and exit by `cancel.deadline`.
- A race between a terminal frame and an incoming `cancel` is resolved by
  whichever frame the worker reads first — but the trusted worker still
  applies control-plane fencing and cancellation state independently of
  which frame won the race; a harness "finishing" a moment before
  `cancel` arrived does not let a fenced/cancelled attempt's output
  become authoritative.

## Result collection (section 7 rule 6)

`completed` means only "the harness returned and wrote a candidate result
to `result_path`." Before trusting anything in that file:

1. The worker freezes/terminates the complete sandbox process tree first
   — no further writes from the attempt are possible once collection
   begins.
2. It opens every candidate file named in the result manifest relative to
   a trusted, held staging-directory descriptor, using no-follow/beneath
   semantics — never by re-resolving a path string, which would be a
   symlink/replacement race.
3. Only regular files are accepted; per-file, aggregate-size, and
   file-count limits are enforced during this same pass.
4. Hashing and upload happen from the same held descriptors used to open
   the files — never a second, separate open that could resolve
   differently.
5. Only after all of that does the worker validate declared ports,
   codecs, the interface digest, and ADR-032 contracts against
   `task-result-v1.json`'s shape.

A crash after upload but before the atomic `CommitAttemptResult` (section
7 rule 7 — not described by either schema in this phase-0 slice) leaves
invisible orphan blobs, reclaimed by attempt-retention cleanup, never
partially-visible output.
