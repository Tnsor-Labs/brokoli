# ADR-036: JVM task bundles and the JVM authoring SDK

**Status:** proposed
**Date:** 2026-09-08

## Context

ADR-033 section 3 defines a closed vocabulary of runtime classes and
marks one of them unfinished:

| Runtime class | Initial status |
|---|---|
| `python` | required reference adapter |
| `node` | required reference adapter |
| `jvm` | **deferred until adapter exists** |

Two reference adapters (`pkg/taskharness/pyharness`,
`pkg/taskharness/nodeharness`) now exist and prove the protocol is
language-neutral: `engine/task.go`'s `prepareTaskHarness` is the only
language-aware code in the engine, and everything after it — the JSONL
exchange, resource ceilings, result reading, contract validation — is
identical for both. Adding a third runtime class is therefore a question
about *that adapter*, not about the engine.

The JVM is the largest ecosystem Brokoli cannot currently author tasks
in, and it is the one where the existing data tooling (Spark, Flink,
Kafka clients, JDBC drivers) already lives. But it differs from Python
and Node in one way that drives this entire decision: **it is compiled.**
Both existing adapters ship harness *source* embedded in the Go binary
(`//go:embed harness.py`, `//go:embed harness.mjs`) and hand it to an
interpreter that the host already has. Java has no equivalent by
default.

### What was measured

Java 11's JEP 330 lets `java Harness.java` run a source file directly,
which would preserve the existing pattern exactly — embed source, launch
it, no build step anywhere. It works, and it is too slow. Measured on
the development host (OpenJDK 24, warm page cache, 3 runs each):

| Launch mode | Per-invocation |
|---|---|
| `java Harness.java` (JEP 330 source launch) | **1.07 – 1.16 s** |
| `java -cp . Harness` (precompiled class) | 0.07 – 0.08 s |
| `java -jar harness.jar` | 0.08 – 0.09 s |
| `node harness.mjs` (existing adapter) | 0.05 – 0.07 s |
| `python3 harness.py` (existing adapter) | 0.05 – 0.07 s |
| `javac` (one-time compile) | 1.63 s |

ADR-035 gives task nodes a fresh child per attempt with no warm pool, so
source-launch would add roughly **one second of pure overhead to every
task attempt** — a 15x penalty against the same harness precompiled, and
20x against the Node and Python adapters it must sit alongside.
Precompiled, the JVM is within ~30 ms of both existing adapters, which
is not a meaningful difference at attempt granularity.

That measurement is the whole reason this ADR exists as a decision
rather than a straightforward third copy of the existing pattern.

### The constraint that does carry over

ADR-026 established that **runtimes are resolved, never provisioned**:
Brokoli does not download an interpreter, records what it resolved, and
refuses an unsupported or absent one *by name at validation* rather than
mid-run. That applies unchanged here. Brokoli must never download a JDK.

## Decision

Add the `jvm` runtime class with a **compile-once, cache, and reuse**
adapter:

1. `pkg/taskharness/jvmharness` embeds the harness as `.java` **source**,
   consistent with both existing adapters and with a plain `go build`
   producing a complete binary.
2. On first use for a given harness version *on a given host*, the worker
   compiles that source once with the resolved `javac` into a
   version-keyed cache directory. Every subsequent attempt launches the
   compiled class directly and pays 0.07 s, not 1.1 s.
3. The cache key is the harness source digest plus the resolved JDK's
   reported version. A JDK upgrade or a harness bump produces a new key
   rather than reusing a stale artifact; nothing is invalidated in place.
4. A host with a JRE but no `javac`, a JDK below the floor, or no `java`
   at all is **refused by name at validation** (ADR-026's rule), never
   discovered mid-run.

The bundle side is additive to `task-bundle/v2`: a payload with
`"runtime": "jvm"` carries JARs, and its entrypoint names a class and a
static method.

### Payload shape

```json
{
  "runtime": "jvm",
  "entrypoint": "com.example.tasks.Transforms#dailyRollup",
  "classpath": ["lib/task.jar", "lib/deps.jar"],
  "runtime_version": ">=17",
  "adapter": "brokoli-jvm-taskharness"
}
```

- `entrypoint` is `fully.qualified.ClassName#staticMethodName`. The `#`
  separator is unambiguous because neither a class name nor a method name
  may contain it, unlike the `:` and `.` that both appear inside package
  names.
- `classpath` entries are bundle-relative paths, resolved against the
  extracted bundle root and covered by the bundle digest. They are never
  URLs, Maven coordinates, or absolute paths — ADR-033 section 6's rule
  that references are not locations applies here too, and it is also what
  keeps a task from fetching code at run time.
- `runtime_version` is a floor the resolved JDK must satisfy. **Java 17**
  is the floor: it is the oldest release still under broad vendor support
  and the oldest that all current build tooling targets by default.

### The harness contract

The harness is one dependency-free `.java` file implementing the
`brokoli.task-runtime/v1` side of the protocol exactly as `harness.py`
and `harness.mjs` do: read the `start` frame from stdin, emit `ready`,
then `log`/`progress`/`warning`/`metric`, then exactly one terminal
`completed` or `failed`.

Two JVM-specific obligations:

**JSON without dependencies.** The JDK ships no JSON parser, and the
harness must not acquire one — a dependency in the harness would be a
dependency in every JVM task's classpath, with version conflicts against
whatever the task itself uses. The harness therefore hand-rolls a
minimal reader and writer over the small, closed frame vocabulary.

This is a correctness risk with precedent in this codebase, so it is
constrained rather than left to care: **the harness's number handling
must satisfy the same fixtures that caught issue #479**, where
`json.Unmarshal` silently decoded `9007199254740993` as `...992` because
every JSON number became a `float64`. Java is better positioned than
either existing adapter here — `long` is natively 64-bit, where
JavaScript's `Number` is a double and cannot represent the value at all —
but "better positioned" is not "verified", and the conformance fixtures
decide it.

**Entrypoint resolution is explicit, never inferred.** The harness loads
the named class through a `URLClassLoader` over the declared classpath
and invokes the named `public static` method. It does not scan for
annotated methods, does not fall back to a single public method, and does
not consult the thread-context classloader. A missing or ambiguous
entrypoint is a `contract_violation`, which is ADR-033 section 14's
existing category for exactly this.

### Resource ceilings

`-Xmx` carries the attempt's memory ceiling, mirroring how the Node
adapter passes `--max-old-space-size`. This is in *addition* to the
rlimits `pkg/codeexec.Limits` already applies to every task child, not
instead of them: `-Xmx` bounds the Java heap, while the rlimit bounds the
process, and a JVM's non-heap memory (metaspace, thread stacks, direct
buffers, the JIT's own allocations) lives outside `-Xmx` entirely. Only
the rlimit is a true ceiling; `-Xmx` exists so the JVM fails with an
`OutOfMemoryError` it can report through the protocol rather than being
killed by the kernel with nothing to say.

`-XX:TieredStopAtLevel=1` is *not* set by default. It shaved no
measurable time off startup here (0.07–0.08 s either way) and would cap
JIT optimization for long-running tasks, which is the wrong trade for
anything but the shortest.

### The authoring SDK

A separate `brokoli-jvm` repository, published to Maven Central,
following the ordering the Python and TypeScript SDKs already
established: the runtime contract ships first and the authoring
ergonomics follow it.

The SDK's job is the same as its siblings': let a user write an ordinary
function, and produce IR plus a bundle. A task is a static method taking
a typed row list and returning one:

```java
package com.example.tasks;

import dev.brokoli.task.Task;
import dev.brokoli.task.Rows;

public final class Transforms {
    @Task(name = "daily-rollup")
    public static Rows dailyRollup(Rows input, double threshold) {
        return input.filter(row -> row.getDouble("score") >= threshold);
    }
}
```

Java's static types make ADR-032 interface declaration considerably more
direct than in either existing SDK: the parameter and return types *are*
the contract, so the port types and `parameter_bindings` are derived from
the method signature at build time rather than from decorators, type
hints, or inference. `@Task` supplies only what the signature cannot —
the node name, and any non-default validation mode.

The annotation processor runs at compile time and emits the interface
JSON alongside the class, so a signature and its declared contract cannot
drift: they are produced from the same source in the same step.

## Consequences

### Positive

- The JVM adapter costs 0.07 s per attempt rather than 1.1 s — within
  30 ms of the two existing adapters.
- `//go:embed` of source is preserved, so `go build` alone still yields a
  complete binary. No JDK is needed to build Brokoli, and no binary
  artifact is checked in or shipped in a release.
- A third runtime class in a *compiled* language is the real test of
  ADR-033's claim that the engine contains no language-specific
  invocation code. Python and Node are both interpreted and both
  dynamically typed; if `prepareTaskHarness` needs changes beyond a new
  case, the claim was weaker than believed and this is where that shows.
- Static typing makes the ADR-032 contract derivable from the signature,
  which no other SDK can do.

### Negative

- **A JDK is required at run time, not merely a JRE.** This is the real
  cost of the decision, and it is a genuine narrowing: JRE-only container
  images are common. It is refused by name at validation, so it is never
  a mid-run surprise, but it is a deployment requirement that neither
  existing adapter has.
- The first attempt on a fresh host pays the one-time compile (~1.6 s)
  and needs a writable cache directory. Both are new operational
  surfaces.
- A hand-rolled JSON reader is a correctness risk, mitigated by
  conformance fixtures but not eliminated.
- Three adapters means three implementations of the same protocol to keep
  in step. The shared conformance suite (ADR-033 section 19) is what
  keeps that from becoming three dialects.

### Deferred

- **Kotlin and Scala.** They compile to the same bytecode and should work
  through the same adapter unchanged, but "should" is not "verified", and
  neither is in scope until the conformance suite runs against them.
- **Warm JVM reuse.** The 0.07 s startup does not justify the isolation
  cost, and ADR-035 deliberately gives task nodes no warm path. If JVM
  startup ever becomes the bottleneck, AppCDS is the next step and it is
  a pure optimization behind an unchanged protocol.
- **Spark and Flink as tasks.** They need cluster-side resource
  negotiation, which is a scheduling concern rather than a runtime-class
  one.
- **A prebuilt harness JAR.** Reconsider only if JRE-only hosts become a
  requirement; see the alternative below for the trade it makes.

## Alternatives considered

- **JEP 330 source launch on every attempt** — the most elegant option,
  preserving the existing pattern exactly with no cache, no compile step,
  and no `javac` requirement. Rejected on measurement: 1.07–1.16 s per
  attempt against 0.07 s precompiled. Adopting it would have meant the
  JVM adapter was 20x slower to start than the two it sits beside, for
  the sake of a pattern rather than a property.
- **Embed a prebuilt harness JAR** (`//go:embed harness.jar`) — needs
  only a JRE at run time, has zero compile cost, and needs no cache. It
  is genuinely the better *runtime* story. Rejected because it moves a
  JDK into Brokoli's own build: `go build` from a clean checkout would no
  longer produce a working JVM adapter, only a release pipeline would,
  and a compiled artifact would have to be checked in or built by CI. The
  single-binary, buildable-from-source property is worth more than
  JRE-only support today, and this decision is reversible if that
  changes.
- **Require a JSON library on the task classpath** — removes the
  hand-rolled parser risk, but puts a Brokoli-chosen dependency into
  every JVM task's classpath, where it will eventually conflict with the
  task's own. The harness must be invisible to the task's dependency
  graph.
- **`container` runtime class instead of a JVM adapter** — already
  supported, and remains the escape hatch for tasks with heavy system
  dependencies. Rejected as the *default* because ADR-033 already states
  the reason: one Pod per tiny transform is too expensive for the
  single-binary and high-throughput cases, which is precisely where an
  ordinary JVM transform belongs.
- **Annotation-free convention (scan for a single public static
  method)** — fewer concepts for the user, but makes adding a second
  public method to a class a silent behavior change. An explicit
  entrypoint that fails loudly is worth the annotation.

## Follow-ups

Sequenced so each phase is provable on its own, matching how ADR-033's
own phases were staged:

1. **`pkg/taskharness/jvmharness`** — embedded `.java` source, resolve
   `java`/`javac`, the version-keyed compile cache, and `Command()`.
   Refusal by name for a missing JDK, a JRE-only host, and a JDK below
   the floor. Provable standalone with no engine wiring, exactly as
   phase 2a was.
2. **The harness itself** — protocol conformance against
   `docs/schema/task-runtime-v1.json`, including the #479 numeric
   fixtures as a hard gate on the hand-rolled JSON.
3. **`task-bundle/v2` `jvm` payloads** — schema addition, classpath
   resolution under the bundle root, `taskbundlev2.SelectPayload`
   accepting the new runtime class.
4. **Engine wiring** — a `jvm` case in `prepareTaskHarness`, and
   `supportedTaskRuntimes` gaining the class. This phase is where the
   "no language-specific invocation code" claim is tested for real.
5. **Conformance suite across three adapters** — the same fixtures
   against Python, Node, and JVM, which is what keeps one protocol from
   becoming three dialects.
6. **`brokoli-jvm` SDK** — annotation processor, IR emission, bundle
   packaging, Maven Central publication.

Phases 1–5 land in this repository. Phase 6 is a new repository and
should not begin before phase 4 proves the runtime contract, matching the
ordering both existing SDKs followed.
