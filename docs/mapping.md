# Mapping a node over a collection

A `code` node with a `map` block runs once per item of an upstream node's
output, instead of once over the whole output. Each item runs as its own
instance, with its own status, and the node's output is every item's
output combined. This is the first part of
[ADR-050](adr/050-dynamic-pipelines.md) (issue #967).

- [When to map](#when-to-map)
- [The `map` block](#the-map-block)
- [What each item sees](#what-each-item-sees)
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
| `max_items` | no | The most items the node may fan out into. More items fail the node. Default 1000. |
| `max_parallel` | no | How many items run at once. Default 4. See [How many items run at once](#how-many-items-run-at-once). |
| `on_item_failure` | no | `fail` (default), `continue` or `skip`. See [When an item fails](#when-an-item-fails). |

Any other setting inside `map` is refused when the pipeline is saved, so a
misspelled setting is reported instead of ignored.

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
at the same time, 4 when the block does not say. Code items share that
process's code workers and CPU with every other node of the run, so a
larger number helps items that wait on the network more than items that
compute.

When items are dispatched to remote workers, up to `max_parallel` of them
are in flight at once, and never more than 16 for one node, whatever
`max_parallel` says.

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

Only `code` nodes can map in this version. A `map` block on any other node
type is refused when the pipeline is saved, and again when the node runs,
so a pipeline stored before the check cannot run that node once over its
whole input as if the block were not there:

```text
node Orders (orders): 'map' is not supported on source_db nodes in this version; only code nodes can map
```

Mapping other node types, groups of nodes and calls to other pipelines are
the next phases of [ADR-050](adr/050-dynamic-pipelines.md), tracked in
issue #972.
