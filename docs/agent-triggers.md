# Agent triggers

An **agent trigger** delivers run events to an agent, so the agent starts
work without a person starting it. A run fails at night; the trigger tells
the agent; the agent finds out why over MCP and proposes the fix as a
change request; a person applies it in the morning.

A trigger only says what happened. The agent then works through the same
MCP tools and permission checks as when a person starts it, with its own
token: the event never carries a credential, and nothing about what the
agent may do changes. Agents never apply a change.

This is [ADR-047](adr/047-agent-triggers.md). Agent identities are
[agents.md](agents.md); the MCP tools are [mcp.md](mcp.md); change requests
are [change-requests.md](change-requests.md).

## What a trigger is

| | |
|---|---|
| **Agent** | the agent identity that is told |
| **Workspace** | the workspace the trigger belongs to; it sees only that workspace's pipelines |
| **Scope** | one pipeline, or every pipeline in the workspace the agent may see |
| **Events** | `run.failed`, `run.blocked` (below) |
| **URL** | where deliveries are sent: `http` or `https`, allowed by the server's outbound network policy, with no credentials in it |
| **Cooldown** | after a delivery about a pipeline, how long the trigger stays quiet about that pipeline (default 30 minutes; 0 to 7 days) |
| **Signing secret** | a Standard Webhooks secret (`whsec_...`), shown once, when the trigger is made |

Whoever may manage the agent manages its triggers: the agent's **owner**,
or someone with `settings.manage_users`. An agent's own token cannot
manage triggers, its own included.

A trigger is refused when its agent could not see what it would be told
about: the agent's role, within its owner's permissions, must grant
`pipelines.view` and `runs.view`, and the pipeline must be in the trigger's
workspace and the agent's organization. The same check runs again before
every delivery, so demoting the owner, disabling the agent, or removing
its access stops its triggers without anyone editing them.

## Events

| Event | When | Where the agent starts |
|---|---|---|
| `run.failed` | a run ends failed. A quality check or contract gate that blocks the data ends the run this way too: the event names each failed step and its type, so `quality_check` or `contract_gate` means data was stopped, not that code broke. It also covers a run that startup recovery could not bring back. | `explain_run` |
| `run.blocked` | a run did not start because its dependencies were not satisfied | `get_lineage` |

A successful run, a cancelled run and a run still in progress are not
events here.

## Make one

From the command line, signed in as the agent's owner (`brokoli login`):

```bash
brokoli agents trigger create triage-bot \
  --url https://agents.example.com/brokoli \
  --events run.failed,run.blocked
# Trigger 975e1f20... delivers run.failed and run.blocked about every pipeline to https://agents.example.com/brokoli.
# Its signing secret is shown only now:
# whsec_3f9c...
```

The secret goes to standard output and everything else to standard error,
so `> secret.txt` keeps only the secret. Store it where the receiver reads
its configuration; Brokoli never shows it again.

One pipeline, with a longer cooldown:

```bash
brokoli agents trigger create triage-bot --url https://agents.example.com/brokoli \
  --events run.failed --pipeline <pipeline-id> --cooldown 2h
```

`--workspace <id>` makes it in a workspace other than your default.

Over the API, `POST /api/agents/{agent-id}/triggers`:

```json
{
  "events": ["run.failed"],
  "url": "https://agents.example.com/brokoli",
  "pipeline_id": "",
  "cooldown_seconds": 1800,
  "enabled": true
}
```

The answer is `201` with `trigger` and `secret`. Every later read of the
trigger leaves the secret out.

## Manage them

| CLI | API |
|---|---|
| `brokoli agents trigger list <agent>` | `GET /api/agents/{id}/triggers` |
| `brokoli agents trigger update <agent> <trigger> [--events ...] [--url ...] [--pipeline ...] [--cooldown ...] [--enable or --disable]` | `PATCH /api/agents/{id}/triggers/{trigger}` with the fields to change |
| `brokoli agents trigger delete <agent> <trigger>` | `DELETE /api/agents/{id}/triggers/{trigger}` |
| `brokoli agents trigger rotate-secret <agent> <trigger>` | `POST /api/agents/{id}/triggers/{trigger}/secret` |
| `brokoli agents trigger deliveries <agent> <trigger>` | `GET /api/agents/{id}/triggers/{trigger}/deliveries` |
| `brokoli agents trigger redeliver <agent> <trigger> <delivery>` | `POST /api/agents/{id}/triggers/{trigger}/deliveries/{delivery}/redeliver` |

Every change is recorded in the audit log.

## What a delivery looks like

A `POST` to the trigger's URL, with a JSON body:

```json
{
  "type": "run.failed",
  "id": "dlv_01a1...",
  "occurred_at": "2026-10-31T03:12:09Z",
  "workspace_id": "default",
  "agent": "triage-bot",
  "pipeline": { "id": "...", "name": "orders_daily" },
  "run": {
    "id": "01a1...",
    "status": "failed",
    "failed_nodes": [{ "name": "Fetch orders", "type": "source_api" }],
    "error": "node Fetch orders (fetch) failed: GET https://shop.example.com/v2/orders: 404 Not Found"
  },
  "next": {
    "mcp": "https://brokoli.example.com/api/mcp",
    "suggested_tool": "explain_run",
    "run_url": "https://brokoli.example.com/#/pipelines/.../runs?run=01a1..."
  }
}
```

- **`id`** is the delivery's id, also in the `webhook-id` header. A retry
  or a redelivery sends the same id and the same body; a receiver that
  has already handled an id ignores it.
- **`run.error`** is redacted as every MCP result is (passwords in URLs,
  `Bearer` and `Basic` credentials) and cut at 2,000 characters. It says
  what happened; `explain_run` says why.
- **`next.mcp`** and **`next.run_url`** are present when the server knows
  the address agents reach it at: set `BROKOLI_PUBLIC_URL`
  (`https://brokoli.example.com`).
- There is no credential anywhere in it. The agent calls MCP with the
  token it already holds.

## Verify a delivery

Deliveries are signed per the **Standard Webhooks** specification
(standardwebhooks.com), which has libraries in most languages. Three
headers come with each one:

| Header | |
|---|---|
| `webhook-id` | the delivery id |
| `webhook-timestamp` | when it was signed, in Unix seconds |
| `webhook-signature` | `v1,` and the base64 HMAC-SHA256 of `id.timestamp.body`, keyed with the secret's base64 after `whsec_`; several, separated by spaces, during a rotation |

A receiver accepts a delivery when any signature matches and the timestamp
is within five minutes of its clock, which bounds replay. Compute the
signature over the body **exactly as received**, before parsing it.

Python, standard library only:

```python
import base64, hashlib, hmac, time

def verify(secret: str, headers, body: bytes) -> bool:
    msg_id, ts = headers["webhook-id"], headers["webhook-timestamp"]
    if abs(time.time() - int(ts)) > 300:
        return False
    key = base64.b64decode(secret.removeprefix("whsec_"))
    expected = base64.b64encode(hmac.new(key, f"{msg_id}.{ts}.".encode() + body, hashlib.sha256).digest()).decode()
    return any(
        part.startswith("v1,") and hmac.compare_digest(part[3:], expected)
        for part in headers["webhook-signature"].split()
    )
```

Node, standard library only:

```js
const crypto = require("node:crypto");

function verify(secret, headers, body /* Buffer */) {
  const id = headers["webhook-id"], ts = headers["webhook-timestamp"];
  if (Math.abs(Date.now() / 1000 - Number(ts)) > 300) return false;
  const key = Buffer.from(secret.replace(/^whsec_/, ""), "base64");
  const expected = crypto.createHmac("sha256", key).update(`${id}.${ts}.`).update(body).digest();
  return headers["webhook-signature"].split(" ").some((part) => {
    if (!part.startsWith("v1,")) return false;
    const got = Buffer.from(part.slice(3), "base64");
    return got.length === expected.length && crypto.timingSafeEqual(got, expected);
  });
}
```

Answer `2xx` quickly, and do the agent's work after answering: a delivery
that is not answered within 10 seconds counts as failed and is retried.

## Delivery, retries and the log

- **Where it runs.** The dispatcher runs on the scheduler, and works only
  on the replica that leads, so a server with several replicas sends each
  delivery from one place. It reads the run event log, so runs that
  finished on remote workers are covered like any other.
- **At least once.** A delivery answered with anything but `2xx` within
  10 seconds is retried with growing, jittered waits (from about 30
  seconds up to an hour) for 24 hours, then marked **failed**. A leader
  that changes mid-send can send a delivery twice; the id makes that
  harmless.
- **Skipped**, not sent, when at send time the trigger was removed or
  disabled, the pipeline no longer exists, or the agent may no longer see
  it. The log says which.
- **The log** keeps each delivery's state, attempts, HTTP status, timing
  and the first 512 bytes of the answer, for 30 days.
- **Redeliver** puts a delivery back in the queue, due now, with a fresh
  24 hour window and the same id.

```bash
brokoli agents trigger deliveries triage-bot 975e1f20...
# ID            EVENT       RUN       CREATED           STATUS     ATTEMPTS  LAST  DETAIL
# dlv_8c1d...   run.failed  01a1...   2026-10-31 03:12  delivered  1         200
# dlv_51ab...   run.failed  01a0...   2026-10-30 22:40  pending    2         503   next try 22:44:10 (answered 503)
```

## Rotate the secret

`brokoli agents trigger rotate-secret` returns a new secret. For 24 hours
every delivery carries two signatures, one per secret, so a receiver can
switch to the new one before the old one stops. Rotate when the secret
may have been seen by someone who should not have it.

## Troubleshooting

| What you see | Why |
|---|---|
| `url is not allowed by this server's outbound network policy` | The URL resolves to a private, loopback or link-local address. An operator allows internal receivers with `BROKOLI_OUTBOUND_ALLOW_PRIVATE`, `BROKOLI_OUTBOUND_ALLOW_LOOPBACK` or `BROKOLI_OUTBOUND_ALLOW_CIDRS`. |
| `triggers need the server's encryption key` | The server has no encryption key (`BROKOLI_ENCRYPTION_KEY`, or the key file next to the database), which also protects connection credentials. |
| `agent ... would not be allowed to act on it` | The agent's role, within its owner's, lacks `pipelines.view` or `runs.view`, or the pipeline is outside the agent's organization or workspace. |
| No deliveries at all | No replica runs a scheduler (`--mode all` or `--mode scheduler`); or the failures are within the cooldown of one already delivered; or the run event was not recorded (the run's log then has an `append run event` warning). |
| A delivery is `skipped` | Its reason says why; fix that and redeliver it. |
| Signatures do not verify | The body was parsed and re-serialized before verifying, the receiver uses an old secret after a rotation's 24 hours, or its clock is more than five minutes off. |
