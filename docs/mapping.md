# Mapping a node over a collection

A `code`, `task`, `source_db` or `source_api` node with a `map` block runs
once per item of an upstream node's output, instead of once. Each item runs as its own
instance, with its own status, and the node's output is every item's
output combined. This is the first part of
[ADR-050](adr/050-dynamic-pipelines.md) (issue #967).

For a `code` or `task` node each item is its input; for a `source_db` node
each item's values are bound into the query, and for a `source_api` node
into the request.

- [When to map](#when-to-map)
- [The `map` block](#the-map-block)
- [What each item sees](#what-each-item-sees)
- [Mapping a `task` node](#mapping-a-task-node)
- [Mapping a `source_db` node](#mapping-a-source_db-node)
- [Mapping a `source_api` node](#mapping-a-source_api-node)
- [Keys](#keys)
- [How many items run at once](#how-many-items-run-at-once)
- [When an item fails](#when-an-item-fails)
- [Limits](#limits)
- [Retries](#retries)
- [The `expansion` block](#the-expansion-block)
- [What cannot map yet](#what-cannot-map-yet)

## When to map

Map when the same work repeats for each entry of a list that is only known
when the pipeline runs: one call per customer, one parse per file that
landed today, one request per page of IDs. Each item's success or failure
is recorded on its own, items run in parallel, and a retry of the node
reruns only the items that did not succeed.

When the work is a transformation of the whole dataset at once, do not map:
an ordinary `code` node sees every row in one run, which is much cheaper
than one run per row.

## The `map` block

```json
{
  "id": "fetch_orders",
  "type": "code",
  "name": "Fetch orders per customer",
  "config": {
    "script": "...",
    "map": {
      "over": "customers",
      "key": "customer_id",
      "max_items": 5000,
      "max_parallel": 8,
      "on_item_failure": "continue"
    }
  }
}
```

| Setting | Required | Meaning |
| --- | --- | --- |
| `over` | yes | The upstream node whose output rows are the items, by node ID. There must be an edge from it to this node. To zip several collections item by item, give an object of name to node ID instead: `{"file": "files", "schema": "schemas"}`. Every named collection must then have the same number of rows, and each item is that index's row from each of them, merged. |
| `key` | no | The item column whose value identifies the item. See [Keys](#keys). Without it, items are identified by position. |
| `max_items` | no | The most items the node may fan out into. More items fail the node. Default 1000, or what the server sets; at most 100000, or the server's ceiling. See [Limits](#limits). |
| `max_parallel` | no | How many items run at once. Default 4, or what the server sets; at most 64, or the server's ceiling. See [How many items run at once](#how-many-items-run-at-once). |
| `on_item_failure` | no | `fail` (default), `continue` or `skip`. See [When an item fails](#when-an-item-fails). |

Any other setting inside `map` is refused when the pipeline is saved, so a
misspelled setting is reported instead of ignored.

A server that can map lists `node-map` in `supported_execution_features`
of `GET /api/capabilities`, and the node types it can map in
`map_node_types`. A server from before mapping ignores a `map` block and
runs the node once over its whole input, so a client should not send one
to a server that does not list `node-map`; the SDKs refuse to deploy it.

## What each item sees

The script runs once per item. In each run, `rows` holds exactly one row,
the item, and `columns` its column names. Everything else is as in an
ordinary `code` node: the node's other config, run parameters through the
code node's usual mechanism, its `timeout` per item, and `output_data` for
what the item returns.

```python
# Inside a mapped code node: rows[0] is this item.
customer = rows[0]["customer_id"]
orders = fetch_orders(customer)
output_data = {"columns": ["customer_id", "order_id", "total"], "rows": orders}
```

The node's output is every item's `output_data` combined in item order, the
same way a `union` node combines datasets. An empty upstream output is not
an error: the node runs no items and outputs an empty dataset.

## Mapping a `task` node

A `task` node with a `map` block runs its bundle once per item. The task
receives the item as its input: a dataset of exactly that one row,
checked against the task's declared input like any other input, so a row
the contract refuses fails that item before the task sees it. The node's
output is every item's result combined.

```json
{
  "id": "score",
  "type": "task",
  "config": {
    "task_bundle": {"digest": "sha256:...", "format": "brokoli.task-bundle/v2"},
    "map": {"over": "customers", "key": "customer_id", "max_parallel": 8}
  },
  "interface": {
    "contract": "brokoli.task-interface/v1",
    "inputs": {"input": {"value": {"kind": "dataset"}}},
    "outputs": {"result": {"value": {"kind": "dataset"}}}
  }
}
```

With remote workers each item is its own job, under its own instance key
and attempt, so items run on as many workers as are free, up to
`max_parallel` (see [How many items run at once](#how-many-items-run-at-once)).
Without them, items run on the server.

Two shapes are refused when the pipeline is saved:

- A task that declares no input port. Each item reaches the task only as
  its input, so such a task would run the same way for every item.
- A task that declares an artifact or collection output. Those outputs are
  written under a grant that belongs to the node's attempt, not to one
  item's, so in this version a mapped task outputs datasets only.

## Mapping a `source_db` node

A `source_db` node with a `map` block runs its query once per item, with
`${item.column}` replaced by that item's value for the column. The edge
from the node named in `over` is the one incoming edge a source node may
have.

```json
{
  "id": "orders",
  "type": "source_db",
  "config": {
    "conn_id": "shop",
    "query": "SELECT id, total FROM orders WHERE customer_id = ${item.customer_id} AND region = '${item.region}'",
    "map": {"over": "customers", "key": "customer_id", "max_parallel": 4}
  }
}
```

Item values are **bound, never written into the SQL**, by the same rules as
run parameters:

- `'${item.x}'`, the whole of a string literal, is bound as text.
- `${item.x}` on its own is bound as a number: a whole number as an
  integer, a decimal as its exact text. A value that is not a number is
  refused, naming the reference.
- A reference anywhere else is refused: inside a longer literal
  (`'%${item.x}%'`) when the pipeline is saved, and as a table or column
  name (`"${item.x}"`, `` `${item.x}` ``) when the query runs.

Item values come from data, and a value written into the SQL would let
whatever wrote that data change the query. So a table or column name cannot
come from an item. A pipeline that needs one read per table should be one
pipeline per table, generated from the list of tables (ADR-050's
generators, a later phase).

When an item does not have the column a reference names, or has it as null,
that item fails naming the reference, instead of running its query with an
empty value. An `${item...}` reference in a node without a `map` block is
refused when the pipeline is saved.

A mapped `source_db` runs its items on the server or worker running the
node; they are not dispatched to remote workers one by one. A connection
that reads through a native ADBC driver refuses `${item...}` references,
as it refuses run parameters. A dry run maps only the first 3 items.

## Mapping a `source_api` node

A `source_api` node with a `map` block makes its request once per item,
with `${item.column}` replaced by that item's value for the column. With
pagination, each item's fetch paginates on its own. The edge from the node
named in `over` is the one incoming edge a source node may have.

```json
{
  "id": "orders",
  "type": "source_api",
  "config": {
    "url": "https://api.example.com/customers/${item.customer_id}/orders?since=${item.since}",
    "method": "POST",
    "headers": {"X-Tenant": "${item.tenant}"},
    "body": {"customer": "${item.customer_id}", "note": "orders for ${item.name}"},
    "records": "results",
    "map": {"over": "customers", "key": "customer_id", "on_item_failure": "continue"}
  }
}
```

Item values come from data, so each is encoded for the place it lands, and
may only land where it cannot change where the request goes:

| Where | How the value is written |
| --- | --- |
| The url's path | Percent-encoded as part of one path segment, so a `/`, `?` or `#` in the value stays inside it. A value that makes a segment `.` or `..` fails its item. |
| The url's query | Percent-encoded as a query component, so a `&` or `=` in the value cannot add a parameter. |
| A `params` value | As text; the request encodes it. |
| A `headers` value | As text. A value with a line break or NUL fails its item. |
| A JSON `body` | A string that is exactly one reference, such as `"${item.customer_id}"`, takes the item's value with its type: a number stays a number. A reference inside a longer string becomes text. |

Anywhere else a reference is refused, when the pipeline is saved and again
when it runs:

- the url's scheme, host, port or fragment, and a url without a scheme
  and host: an item cannot choose which server the request goes to;
- a header or parameter name, and a field name in a JSON body;
- a `body` given as text, whose format the engine cannot know, so it
  cannot encode a value for it. Give the body as a JSON object or array;
- any other setting, such as pagination or authentication.

As in a `source_db` query, a reference to a column the item does not have,
or has as null, fails that item naming the reference, and an `${item...}`
reference in a node without a `map` block is refused.

Three settings cannot be combined with `map`:

- `execution.checkpoint_every`: a checkpoint belongs to the node, and every
  item would overwrite it.
- `"response": "artifact"`: the items' responses would have to be combined,
  and artifacts cannot be.
- `execution.requests_per_second` with more than one item at a time: it
  paces one item's requests, so with four items at once the API would see
  up to four times the declared rate. Set `"max_parallel": 1`, and the pause
  also separates one item's last request from the next item's first, so the
  rate holds across items.

A mapped `source_api` runs its items on the server or worker running the
node, never as remote pagination instances. A dry run maps only the first 3
items.

## Keys

Every item has an instance key, recorded with its status and used to
recognise it on a retry.

- With `key`, the key is `key:<value>` from that column, for example
  `key:42` or `key:orders.csv`. Every item must have a value there, values
  must be unique, and a key may be at most 256 characters. A missing, null,
  duplicate or longer value fails the node before any item runs, naming the
  items.
- Without `key`, the key is the item's position, `idx:0`, `idx:1` and so on.
  A position identifies the item only as long as the upstream output keeps
  its order, so set `key` when the upstream can reorder or gain rows.

## How many items run at once

On the server or worker that runs the node, up to `max_parallel` items run
at the same time, 4 when the block does not say (the server's default, see
[Limits](#limits)). Code items share that
process's code workers and CPU with every other node of the run, so a
larger number helps items that wait on the network more than items that
compute.

When items are dispatched to remote workers, up to `max_parallel` of them
are in flight at once, and never more than the server's remote bound (16
unless the operator changes it) for one node. A higher `max_parallel` is
lowered to that bound for remote items, not refused, and the node's log says
so: the bound protects the job queue, and the same pipeline may run with
more at once on a server without remote workers.

## When an item fails

`on_item_failure` decides what one item's failure does to the node:

| Value | What happens |
| --- | --- |
| `fail` | No new item starts after the first failure. Items already running finish, and the node fails with the first failure's error. This is the default. |
| `continue` | Every item runs. If any failed, the node then fails, naming how many failed and their keys (the first ten). |
| `skip` | Every item runs. Failed items are left out of the output, and the node succeeds with a warning in its log naming them. If no item succeeded, the node fails: skipping every item would turn a broken node into an empty success. |

In every case each item's status, including its error, is recorded on its
own instance.

## Limits

A collection with more items than `max_items` fails the node before any item
runs:

```text
node Fetch orders (fetch_orders): expansion would create 6200 instances, exceeding the configured limit of 5000 (map.max_items) ...
```

The collection is never truncated to fit.

### Server-wide limits

The operator of a Brokoli server sets the defaults a block gets when it
declares no limit, and the ceilings on what it may declare, with these
environment variables on the server and on every worker:

| Variable | Default | Meaning |
| --- | --- | --- |
| `BROKOLI_MAP_DEFAULT_MAX_ITEMS` | 1000 | The item limit of a `map` block without `max_items`, or an `expansion` block without `max_instances`. |
| `BROKOLI_MAP_MAX_ITEMS` | 100000 | The most items a block may declare in `max_items` or `max_instances`. |
| `BROKOLI_MAP_DEFAULT_MAX_PARALLEL` | 4 | How many items of a `map` block without `max_parallel` run at once on one server or worker. |
| `BROKOLI_MAP_MAX_PARALLEL` | 64 | The most a block may declare in `max_parallel`. |
| `BROKOLI_MAP_MAX_REMOTE_PARALLEL` | 16 | The most items of one node in flight on remote workers at once. A higher `max_parallel` is lowered to it for remote items. |

A block that declares more than a ceiling is refused when the pipeline is
saved, and again when the node runs, since the ceiling may have been lowered
since the pipeline was saved:

```text
node Fetch orders (fetch_orders): 'map.max_items' is 200000, above this server's ceiling of 100000 items (BROKOLI_MAP_MAX_ITEMS)
```

A collection over the server's default names the default and how far the
block may raise it:

```text
node Fetch orders (fetch_orders): expansion would create 1200 instances, exceeding the configured limit of 1000 (this server's default, BROKOLI_MAP_DEFAULT_MAX_ITEMS; set map.max_items to raise it, up to 100000) ...
```

A variable that is not a whole number of at least 1 is ignored, and a
default above its ceiling is lowered to the ceiling. Either way, the log of
every mapped node carries a warning naming the variable, so a mistyped
value does not pass unnoticed. Set the same values on the server and its
workers: a pipeline is checked against the server's limits when it is saved,
and its nodes against the limits of whatever runs them.

## Retries

When a mapped node is retried (its `retries` setting), items that succeeded
on an earlier attempt of the same run are not run again: their output is
reused, and only the items that failed or never started run. This works
within one run on one server; a run recovered after the server restarted
runs every item again.

## The `expansion` block

The Python SDK's `.expand()` compiles to an `expansion` block, which Brokoli
still runs, unchanged:

```json
"expansion": {"over": {"file": "files"}, "max_instances": 100}
```

It behaves like `map` with `on_item_failure: fail`, position keys and items
run one at a time on the local server (remote workers still run several at
once). A node may have `map` or `expansion`, not both. To get parallel local
items, keys or another failure policy, write `map` instead.

## What cannot map yet

`code`, `task`, `source_db` and `source_api` nodes can map in this version.
A `map` block on any other node type is refused when the pipeline is saved,
and again when the node runs, so a pipeline stored before the check cannot
run that node once over its whole input as if the block were not there:

```text
node Out (out): 'map' is not supported on sink_file nodes in this version; code, task, source_db and source_api nodes can map
```

Mapping sinks, groups of nodes and calls to other pipelines are the next
phases of [ADR-050](adr/050-dynamic-pipelines.md), tracked in issue #972.
