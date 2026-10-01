# Bash operator

A `bash` node runs a shell command on the worker that executes the run, then
passes its input dataset through unchanged. Use it for the steps a pipeline
needs around its data, not on it: calling a CLI, triggering a build,
refreshing a cache, moving files the file nodes do not handle.

- [Trust model](#trust-model)
- [Configuration](#configuration)
- [Environment](#environment)
- [Variables and run parameters](#variables-and-run-parameters)
- [Working directory](#working-directory)
- [Output and logs](#output-and-logs)
- [Failure, timeouts and cancellation](#failure-timeouts-and-cancellation)
- [Resource limits](#resource-limits)
- [Data flow and lineage](#data-flow-and-lineage)
- [Examples](#examples)

## Trust model

**This is trusted-worker execution, not a sandbox.** The command runs as the
worker's own operating-system user. It can read and write any file that user
can, run any installed program, and reach any network address the host can.
The working directory and the filtered environment are conveniences. They are
not a boundary: a command can `cd /`, read `/proc`, or open a socket.

In that respect a bash node is no different from a code node, which can do
the same from Python or TypeScript. Only give pipeline-editing rights to people
you would give a shell on the worker. On a server that runs pipelines for
people who should not have that, disable the node type:
`BROKOLI_DISABLED_NODE_TYPES=bash` (see
[Code execution policy](code-execution-policy.md)).

## Configuration

```json
{
  "type": "bash",
  "name": "Refresh the export",
  "config": {
    "command": "./scripts/refresh.sh --since \"$SINCE\"",
    "working_dir": "/data/exports",
    "env": {"SINCE": "2026-09-01"},
    "timeout": 600,
    "max_retries": 2,
    "retry_delay": 30
  }
}
```

| Key | Required | Meaning |
| --- | --- | --- |
| `command` | yes | Passed to `bash -o pipefail -c`. Multi-line scripts work. A pipeline fails if any stage of it fails. |
| `working_dir` | no | Where the command starts. It must be an existing directory inside the worker's data directories (`BROKOLI_DATA_DIRS`). Without it, the command gets a private temporary directory. |
| `env` | no | Extra environment variables, as an object of string values. Names must be valid shell variable names. |
| `timeout` | no | Seconds before the node is stopped. Defaults to 30 minutes, as for every node. |
| `max_retries`, `retry_delay` | no | The shared retry settings. Each attempt runs the whole command again, so make it safe to repeat. |
| `max_cpu_seconds` | no | A CPU-time ceiling for this node. See [Resource limits](#resource-limits). |

`bash` must be installed on the worker; a worker without it fails the node
with a message saying so. Process-group handling is a Unix feature and
resource limits are applied on Linux; elsewhere the command runs without
them.

## Environment

The command does not inherit the worker's environment. It gets:

- the same allowlist code nodes get: `PATH`, `HOME`, `LANG`, `LC_*`, `TZ`,
  `TMPDIR`, plus any names the operator adds with `BROKOLI_CODE_PASS_ENV`;
- `BROKOLI_RUN_ID` and `BROKOLI_NODE_ID`;
- the node's `env` entries, which win over anything above.

So credentials the worker process holds, such as a database URL or a cloud
key in its environment, are not visible to the command unless they are passed
explicitly. Anything written to stdout or stderr goes to the run log, so do
not print secrets.

## Variables and run parameters

`${param.*}`, `${var.*}`, `${interval.*}`, `${run.*}`, `${secret.*}` and
`${env.*}` references are substituted in the node's `env` values and
`working_dir`, **never in `command`**. A substituted value in the command
would become shell source: a run parameter of `x; rm -rf ~` would run. Put
the value in `env` and quote the variable in the command:

```json
{
  "command": "./scripts/export.sh --customer \"$CUSTOMER\" --day \"$DAY\"",
  "env": {"CUSTOMER": "${param.customer}", "DAY": "${interval.start|date:YYYY-MM-DD}"}
}
```

A command containing one of these references fails validation with a
message saying so. The shell's own expansions, such as `${HOME}` or
`${name:-default}`, are untouched.

## Working directory

With `working_dir`, the command starts in that directory. It is checked
against the data directories when the node runs, on the worker, because the
server that validates the pipeline may not have the same ones.

Without it, each attempt gets a new private directory under the system
temporary directory. It is removed when the attempt ends, whether it
succeeded or not. Write anything that must outlive the node somewhere else,
inside the data directories.

## Output and logs

Each line the command writes to stdout becomes a run-log entry prefixed
`[bash]`, and each stderr line one prefixed `[bash stderr]`. The output is
bounded so a noisy command cannot fill the metadata store:

- A line over 16 KiB is cut there and marked `[line truncated]`.
- At most 10,000 lines are logged per attempt. The rest are still read, so
  the command is never blocked, but not stored. A final warning says how many
  were dropped.

When the command fails, the error also carries the last 20 lines of stderr,
so the reason is visible without opening the log.

## Failure, timeouts and cancellation

- **A non-zero exit fails the node**, with the exit status and the stderr
  tail. `pipefail` is on, so `false | true` fails too.
- **Timeouts and cancellation stop the whole command.** The command runs in
  its own process group. When the node times out or the run is cancelled,
  the group gets SIGTERM, then SIGKILL after five seconds. Programs the
  command started are stopped along with it, so nothing keeps writing after
  the node is reported failed, or alongside a retry.
- **Background processes do not outlive the node.** Anything the command
  leaves running, with `&` or `nohup`, is stopped when bash exits. A
  background process that keeps stdout or stderr open delays the node until
  it exits, for at most five seconds after bash does. Redirect a background
  job's output if the node should not wait for it.
- A bash node is never resumed from a checkpoint. A resumed run executes it
  again, like `dbt` and `notify`.

## Resource limits

The command runs under the code-node ceilings the operator configures:

| Setting | Limit |
| --- | --- |
| `BROKOLI_CODE_CPU_SECONDS`, or the node's `max_cpu_seconds` (clamped to `BROKOLI_CODE_MAX_CPU_SECONDS`) | CPU time. Exceeding it fails the node with a message naming the CPU limit. |
| `BROKOLI_CODE_FILE_SIZE_MB` (default 4096) | Largest file the command can write. |
| `BROKOLI_CODE_OPEN_FILES` (default 256) | Open file descriptors. |
| `BROKOLI_CODE_MEMORY_MB`, or the node's `max_memory_mb` (clamped to `BROKOLI_CODE_MAX_MEMORY_MB`) | Memory. See [Memory](#memory). |

The limits apply to bash and are inherited by what it starts.

### Memory

Code nodes limit memory from inside their interpreter. A shell command has
no interpreter, so a bash node's memory limit is applied from outside, in
this order:

1. **A cgroup, when the worker can create one.** The command and everything
   it starts run in a cgroup v2 leaf with `memory.max` set to the limit and
   swap off. This counts memory actually used, so a JVM, Node or Python the
   command starts behaves normally until it really uses more than the limit.
   When it does, the kernel kills the whole tree, and the node fails with
   `bash command exceeded the memory limit (N MiB)`. The cgroup is removed
   when the attempt ends, and anything still in it is killed, including a
   process that left the process group with `setsid`. The node log says
   `memory limit N MiB, enforced by a cgroup`.
2. **Address space, only when the node asked for a limit itself.** Without a
   cgroup, a node that sets `max_memory_mb` gets `RLIMIT_AS`. That limits
   address space, not memory used. Programs that reserve much more than they
   use, a JVM or Node above all, can fail under it at sizes they would
   otherwise run in. The node log warns about this. A failure that reads like
   an allocation failure is reported as
   `bash command exceeded the memory limit (N MiB, enforced as address space)`.
3. **Otherwise, not enforced, and said so.** The server default
   (`BROKOLI_CODE_MEMORY_MB`) is never turned into `RLIMIT_AS`: it would break
   every JVM and Node a bash command starts. Without a cgroup, the node log
   carries `memory limit N MiB is NOT enforced for this bash command`, with
   the reason.

**Giving the worker a cgroup.** Moving a process into a cgroup needs write
access up to the common ancestor of its old and new cgroup, so the worker
needs a cgroup v2 subtree delegated to it. `BROKOLI_BASH_CGROUP` names it,
absolute or relative to `/sys/fs/cgroup`. The worker enables the memory
controller for its children and creates one child per bash attempt. Common
setups:

- **systemd service:** `Delegate=yes` on the worker's unit. Then set
  `BROKOLI_BASH_CGROUP` to a sub-directory of the unit's cgroup, created
  empty, because a cgroup holding processes cannot also give its children
  controllers. Or leave it unset and run the worker process itself in a
  leaf child of the unit.
- **Container with its own writable cgroup namespace:** leave
  `BROKOLI_BASH_CGROUP` unset. The worker is at its namespace's root, where
  enabling the controller is allowed. Most container runtimes mount
  `/sys/fs/cgroup` read-only, and then this falls back as above.

On an ordinary host, the worker's own cgroup is a leaf holding processes.
The kernel refuses to give such a cgroup's children a controller (`EBUSY`),
so without `BROKOLI_BASH_CGROUP` it falls back.

## Data flow and lineage

The node's output is its input, unchanged. It sits after another node, as a
side effect between two steps or at the end of a branch; a pipeline still
needs a source node. Column lineage passes through. The command cannot read or change the dataset; use a code
node for that.

## Examples

Trigger a downstream build and stop if it fails:

```json
{"command": "curl -fsS -X POST \"$HOOK_URL\"", "env": {"HOOK_URL": "${var.build_hook}"}}
```

Compress yesterday's export, inside a data directory:

```json
{
  "command": "set -eu\nfile=orders-$(date -d yesterday +%F).csv\ngzip -f \"$file\"",
  "working_dir": "/data/exports"
}
```
