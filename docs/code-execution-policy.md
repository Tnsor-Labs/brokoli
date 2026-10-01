# Code execution policy

Some nodes run code that pipeline authors write: `code` nodes run Python or
TypeScript, `bash` nodes run shell commands, and `task` nodes run packaged
task bundles. They run on the worker with the worker's own access. This page
covers what an operator can do about that, and the rules that keep values
from outside the pipeline from becoming code.

- [Who can do what](#who-can-do-what)
- [Disabling node types](#disabling-node-types)
- [Run parameters never become code](#run-parameters-never-become-code)
- [Limits that apply to author code](#limits-that-apply-to-author-code)

## Who can do what

| Can | Permission | Effect |
| --- | --- | --- |
| Write code that runs on the worker | `pipelines.create` / `pipelines.edit` | Anything the worker's user can do. |
| Start a run and choose its parameters | `pipelines.run` (the built-in operator role has it without `pipelines.edit`) | Choose values, not code (see below). |
| Start a run through a webhook | holding the pipeline's webhook token | Declared (typed) parameters only, which are never substituted as `${param.*}`. |
| Change a workspace variable | `variables.edit` | Values substituted into node configs, including scripts (see below). |

Editing a pipeline that contains a code, bash or task node is the same as
having a shell on the worker. Grant it accordingly, or disable those node
types.

## Disabling node types

```sh
BROKOLI_DISABLED_NODE_TYPES=bash,code,task
```

A comma-separated list of node types this process refuses. Case and spaces
are ignored.

- **At validation.** A pipeline with a disabled node fails validation with
  `bash nodes are disabled on this deployment (BROKOLI_DISABLED_NODE_TYPES)`.
  It can't be saved or run, and the editor shows the message on the node.
- **Where it would run.** The worker refuses the node again before executing
  it, with the same message. Set the variable on every process that executes
  runs, the server and each worker. A worker without it relies on the
  server's validation alone.
- **Advertised.** `GET /api/capabilities` lists the refused types under
  `disabled_node_types`, so a client can leave them out rather than find out
  at validation.

Plugin node types can be disabled by name too. A name that is neither a
built-in type nor installed disables nothing, and the server logs a warning
naming it so a typo doesn't pass silently.

Each process reads the setting from its environment at startup, so a change
takes effect when the process restarts.

## Run parameters never become code

Brokoli substitutes `${...}` references in a node's settings before the node
runs. A run parameter (`${param.*}`) is chosen by whoever starts the run, and
`pipelines.run` does not imply `pipelines.edit`. Substituting a parameter into
code would let someone who may only run a pipeline change what it executes.
So:

- **`bash` command:** no reference is substituted, `${param.*}` or otherwise.
  Put values in the node's `env` and use `"$NAME"` in the command. See
  [Bash operator](bash-operator.md#variables-and-run-parameters).
- **`code` script:** `${param.*}` is not substituted. The script reads
  parameters from `params`: `params["name"]` in Python, `params.name` in
  TypeScript. Other references (`${var.*}`, `${run.*}`, `${interval.*}`)
  are still substituted. They come from people who can already edit the
  workspace.

In both cases a script or command that contains the reference fails
validation, with a message saying what to use instead. The reference is
never run as written by accident.

**SQL is different today ([#774](https://github.com/Tnsor-Labs/brokoli/issues/774)).** `${param.*}` is substituted into `source_db`
queries and other SQL, and that is documented behaviour. A value is
therefore spliced into SQL text. Treat pipeline parameters used in SQL as
trusted, or validate them in a declared parameter type, until SQL gets bound
parameters.

## Limits that apply to author code

Code and bash nodes run under the same ceilings, set by the operator:

| Setting | Limit |
| --- | --- |
| `BROKOLI_CODE_CPU_SECONDS` (node `max_cpu_seconds`, clamped to `BROKOLI_CODE_MAX_CPU_SECONDS`) | CPU time |
| `BROKOLI_CODE_MEMORY_MB` (node `max_memory_mb`, clamped to `BROKOLI_CODE_MAX_MEMORY_MB`) | Memory. Code nodes enforce it in the interpreter. Bash nodes enforce it with a cgroup when `BROKOLI_BASH_CGROUP` gives the worker one, otherwise as address space only when the node set `max_memory_mb` itself (see [Bash operator](bash-operator.md#memory)) |
| `BROKOLI_CODE_FILE_SIZE_MB` (default 4096) | Largest file written |
| `BROKOLI_CODE_OPEN_FILES` (default 256) | Open files |
| `BROKOLI_CODE_PASS_ENV` | Host environment variables visible to author code, beyond `PATH`, `HOME`, `LANG`, `LC_*`, `TZ` and `TMPDIR` |

These are applied on Linux. They bound resource use, not access: they are
not a sandbox.
