# ADR-037: Authoring experience — move preflight to where the mistake is made

**Status:** proposed
**Date:** 2026-09-08

## Context

Brokoli pipelines are authored in code, in an editor, and validated
somewhere else entirely: at `deploy`, minutes later, against a server the
author may not have in front of them. Everything between writing the
mistake and learning about it is wasted, and some mistakes are not caught
even then.

The case for improving this is not general developer-experience
enthusiasm. It is a specific, measured defect class that this codebase
keeps producing.

### The defect class

Over one working session, seven separate instances of the same shape were
found and fixed — *authored code that deploys cleanly and then fails, or
worse, silently does the wrong thing*:

| Gap | What the author saw |
|---|---|
| `task` nodes ungated (brokoli-sdk#95, ts#13) | deploy accepted; server's validator hard-refused it |
| named ports ungated (same) | ports silently dropped, edge routed as if unported |
| `deferrable-waits` ungated (ts#14) | deploy accepted; `no runtime handler` mid-run |
| `code-typescript` ungated (brokoli-sdk#96) | deploy accepted; failed at run time with no Node |
| declared parameters never delivered (#487, #490) | **green run, wrong answer** |
| 64-bit ids corrupted in the Node harness (#492, #494, #496) | **green run, wrong answer** |
| int64 corrupted in the engine's result decode (#496) | **green run, wrong answer** |

Three of those produced a *correct-looking result that was wrong*. That
is the worst outcome the system can produce, and none of them were
visible to a passing test suite.

### What already exists to build on

The SDKs are not short of machinery here — they are short of *placement*.

- Both SDKs compute `required_execution_features()` from a compiled
  pipeline and compare it against the server's advertised
  `supported_execution_features`, refusing a deploy that cannot work.
  This is real, tested, and symmetric across both SDKs.
- Both SDKs already compile a pipeline to IR (`to_json()` /
  `toJSON()`), which is a complete graph description — nodes, edges,
  conditions, interfaces — available without contacting a server.
- The server advertises its capabilities at `GET /api/capabilities`.

So the information needed to tell an author "this will not work, and
here is why" exists at authoring time. It is simply consulted at deploy
time.

### What does not exist

- The **TypeScript SDK has never been released**: no tags, no changelog,
  no publish workflow, no npm org or package name. Three merged fixes
  currently have nowhere to go.
- There is no editor integration of any kind.

## Decision

Build editor integration, but scope it around **moving existing
validation earlier** rather than around visualisation or deployment, and
sequence it behind the SDK release story.

Three candidate capabilities, in the order their value justifies:

**1. Preflight as you type (the reason to do this at all).** Surface
`required_execution_features` against the configured server's advertised
capabilities, inline, as a diagnostic:

> `@task(threshold: float)` declares a pipeline parameter, which requires
> `task-parameters-v1`. Your server (v0.11.5) does not advertise it: the
> value you submit would be silently ignored.

This is the same machinery, moved from where the mistake is *caught* to
where it is *made*. It needs no new backend work and no new contract.

**2. The pipeline graph, rendered from the IR.** Both SDKs already emit
a full graph; drawing it on save is a rendering problem, not a
correctness one. High payoff, genuinely low cost, and it makes the
"nodes being built through the code" legible.

**3. Deployment.** Deliberately last and deliberately thin. The CLI
already deploys; an extension wrapping it adds a button, not a
capability.

### Prerequisite: the SDK release story comes first

An extension is a client of the SDKs. Publishing an extension for an SDK
that cannot be installed inverts the dependency — so the TypeScript
SDK's release infrastructure (npm org, package name, publish workflow)
is a hard prerequisite, not a parallel track.

### The JVM is served differently, on purpose

JVM development happens predominantly in IntelliJ, not VS Code, and the
JVM authoring path is a CLI command (`brokoli bundle jvm`) precisely so
it composes with Gradle and Maven. JVM developers are therefore served
by **build-tool integration**, not by this extension. Treating "editor
integration" as one undifferentiated investment would spend VS Code
effort on an audience that is not there.

## Consequences

### Positive

- The defect class above is caught in the editor, where it costs seconds,
  rather than at deploy or — for the three silent ones — never.
- Capability negotiation stops being invisible plumbing and becomes the
  thing the author actually sees, which is also the best documentation it
  could have.
- The graph view makes an IR-compiling SDK's output inspectable without
  deploying anything.

### Negative

- **A new product surface with its own release cadence.** Marketplace
  publishing, its own versioning, and a compatibility matrix against two
  SDKs *and* the server. That matrix is the real cost: today's
  capability gates already have to reason about three moving versions,
  and an extension adds a fourth.
- It creates pressure to keep SDK internals stable that were not
  previously public surface — `required_execution_features` becomes
  something an extension depends on.
- Editor integration is easy to keep extending and hard to keep scoped.
  The scope above is narrow on purpose.

### Deferred

- IntelliJ / build-tool integration for the JVM path.
- Anything that requires a language server rather than an extension.
- Live run monitoring in the editor: the UI already does that, and
  duplicating it would be the "add a button, not a capability" mistake
  applied to observability.

## Alternatives considered

- **Do nothing; rely on deploy-time preflight.** This is the status quo
  that produced seven instances of the defect class in one session. The
  gates work; they simply fire late, and three of the seven were cases
  where firing late meant not firing at all.
- **Lead with the graph view.** More immediately impressive and much
  less useful: a picture of a pipeline does not tell an author their
  parameters will be ignored.
- **Lead with deployment integration.** Rejected as the thinnest of the
  three — the CLI already deploys, so this is a convenience wrapper
  competing for the same effort as a correctness improvement.
- **A language server instead of an extension.** Correct long-term shape
  if diagnostics grow beyond capability checks, and disproportionate for
  the scope above. Revisit if item 1 succeeds and demand grows.
- **One integration covering VS Code and IntelliJ.** Rejected: the two
  audiences want different things (editor diagnostics vs build-tool
  tasks), and pretending otherwise would serve neither well.

## Follow-ups

1. **TypeScript SDK release infrastructure** — npm org, package name,
   publish workflow. Hard prerequisite; three merged fixes are already
   blocked on it.
2. **Extract capability preflight into a stable, documented SDK entry
   point** in both SDKs, so an extension consumes a supported API rather
   than an internal function.
3. **Extension MVP: preflight diagnostics only.** One capability, done
   well, against a configured server.
4. **Graph view from compiled IR**, once (3) has shown the extension is
   worth maintaining.
5. **Build-tool integration for the JVM path** (Gradle/Maven task
   wrapping `brokoli bundle jvm`), tracked separately from this ADR.
