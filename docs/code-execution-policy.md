# Code execution policy

Some nodes run code that pipeline authors write: `code` nodes run Python or
TypeScript, `bash` nodes run shell commands, and `task` nodes run packaged
task bundles. They run on the worker with the worker's own access. This page
covers what an operator can do about that, and the rules that keep values
from outside the pipeline from becoming code.

- [Who can do what](#who-can-do-what)
- [Disabling node types](#disabling-node-types)
- [Run parameters never become code](#run-parameters-never-become-code)
- [Run parameters in SQL](#run-parameters-in-sql)
- [Running author code as its own user](#running-author-code-as-its-own-user)
- [Limits that apply to author code](#limits-that-apply-to-author-code)

## Who can do what

| Can | Permission | Effect |
| --- | --- | --- |
| Write code that runs on the worker | `pipelines.create` / `pipelines.edit` | Anything the worker's user can do, or, with `BROKOLI_USER_CODE_UIDS`, anything its tenant's own user can do (see [below](#running-author-code-as-its-own-user)). |
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
- **SQL a source runs** (`source_db` `query`, `migrate` `source_query`):
  `${param.*}` is bound as a database parameter, never written into the SQL
  text. See [Run parameters in SQL](#run-parameters-in-sql).

For the bash command and the code script, a reference that would not be
substituted fails validation, with a message saying what to use instead,
so it is never run as written by accident.

## Run parameters in SQL

A `${param.*}` reference in a source query becomes the driver's own
placeholder (`$1` on Postgres, `?` on MySQL, SQLite, ClickHouse,
Snowflake, Databricks and BigQuery, `@p1` on SQL Server, `:1` on Oracle),
and its value is passed to the database separately. Whatever the value
contains, it is compared as data, never run. Two forms are accepted:

| Written as | Bound as | Example |
| --- | --- | --- |
| `'${param.x}'`, the whole of a string literal | text | `WHERE region = '${param.region}'` |
| `${param.x}` on its own, outside quotes | a whole number as an integer, a decimal as its exact text | `LIMIT ${param.limit}`, `WHERE amount > ${param.min}` |

A one-letter literal prefix goes with the literal: `N'${param.x}'` on SQL
Server and `E'${param.x}'` on Postgres both bind as text. Where `"..."` is a
string (MySQL, Databricks, BigQuery), `"${param.x}"` binds as text too.

These are refused, with a message saying what to write instead:

- **Inside a longer literal**, such as `LIKE '%${param.x}%'` or
  `'${param.d} 00:00:00'`. Build the value outside the literal, for example
  `LIKE CONCAT('%', '${param.x}', '%')` or `'%' || '${param.x}' || '%'`.
  This is caught when the pipeline is validated.
- **As a name**, such as `FROM ${param.table}` or `"${param.col}"`. A
  database cannot bind a table or column name. Use a fixed name, or a
  workspace variable (`${var.x}`), which editors control.
- **A bare reference whose value is not a number**, such as
  `WHERE id = ${param.id}` run with `id=abc`. Quote it to bind it as text.
  This depends on the value, so it fails the run rather than validation.

References in SQL comments are left as they are. Other references
(`${var.*}`, `${interval.*}`, `${run.*}`) are still written into the SQL
text: they come from editors and from Brokoli itself, not from whoever
starts the run.

A source query that uses a run parameter is not pushed down into a
same-server write: the pushed-down statement could not carry the binding.
The rows go through the engine instead, which is slower for large copies
but gives the same result.

## Running author code as its own user

A server that runs as root (the usual case in a container) runs author code
as root too, unless told otherwise. Root code can read every file on the
worker, replace the server's own binary, and read, trace or signal any
other process, including another tenant's code running at the same moment.
On a server that runs pipelines for more than one organization or
workspace, set:

```sh
BROKOLI_USER_CODE_UIDS=1000000000-1999999999
```

Every child that runs author code then starts as an unprivileged user of
its own tenant instead of as the server's user:

| Node | Runs as the tenant's user |
| --- | --- |
| `code` (Python, TypeScript), pooled, one-shot and streamed | yes |
| `bash` | yes |
| `task` (Python, Node, JVM) | yes |
| `dbt` | yes |
| plugin-backed nodes | no: plugins are installed by the operator and run as the server's user |

### How a tenant's user is chosen

The tenant is the org and workspace the run belongs to. Its user ID is a
hash of that key into the configured range, so the same tenant always gets
the same user, on every worker, without any state to keep. Two tenants get
different users unless their keys collide in the range; with a billion IDs
(the example above), a thousand tenants share one with a probability of
about one in two thousand. Keep the range wide. It must start at 10000 or
above, clear of system and login accounts. Work sent to a remote worker
carries its tenant, so a remote worker separates tenants the same way.

The users do not need to exist in `/etc/passwd`: a process can run as any
numeric user ID. Tools that look the user up by name (`whoami`, `id -un`)
report that it has none, which is expected.

### What it changes for the code

- It cannot read files only root may read, write the server's binary or
  its data directory, or raise the resource limits it was given.
- It cannot read another tenant's files or `/proc` entries, or signal
  another tenant's processes. Code of the same tenant shares a user and can
  see its own other processes, as before.
- It gets a private folder per execution, owned by its user, as `HOME` and
  `TMPDIR`. The folder holds its script and its input and is removed when
  the execution ends. Warm code workers are kept per tenant and per user, so
  a worker that ran one tenant's code never runs another's.
- A bash node's `working_dir` and a dbt project folder are used as they
  are, so they must be open to the tenant's user. When a bash
  `working_dir` is not, the node fails with an error naming the directory
  and the user, rather than starting somewhere else.
- The server writes its own files private (umask 077) while the setting
  is on, so files it leaves in a shared directory such as `/tmp` are not
  readable by any tenant's code.

The server reads back what the code wrote (output files, task results, dbt
artifacts, compiled repositories) without following links out of the
code's folder, and refuses anything that is not a regular file the code
wrote itself. It reads more than the code may, so a link planted in place
of an output would otherwise make it read a root-only file on the code's
behalf.

### What the server needs

The setting only works if the server can start processes as other users
and still manage them. It checks at startup and refuses to start, rather
than run author code as root, when it cannot:

- It runs as root with `CAP_CHOWN`, `CAP_DAC_OVERRIDE`, `CAP_FOWNER`,
  `CAP_KILL`, `CAP_SETUID` and `CAP_SETGID` (all in a container's default
  set), plus `CAP_SYS_RESOURCE`, which containers do not get by default
  and which it needs to set the children's resource limits. In Kubernetes:

  ```yaml
  securityContext:
    allowPrivilegeEscalation: false
    capabilities:
      add: ["SYS_RESOURCE"]
  ```

  `allowPrivilegeEscalation: false` is not required, but recommended: it
  stops author code from gaining privileges through a setuid program in the
  image.
- The host has `fs.protected_hardlinks=1` (the default on current Linux
  distributions). Without it, author code could hard-link a root-only file
  into its own folder.
- It runs on Linux. Elsewhere the setting is refused.

Unset, nothing changes: author code runs as the server's user, and a
server running as root says so in its log at startup.

### What it does not do

It separates users; it is not a sandbox. Author code can still use the
network the worker can reach, read files that are readable by everyone, and
use as much CPU and memory as the limits below allow. Shared data
directories (`BROKOLI_DATA_DIRS`) are read and written by file nodes as the
server's user, and author code reaches them only where their permissions
let any user in.

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
