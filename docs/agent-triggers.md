# Agent triggers

An **agent trigger** delivers run events to an agent, so the agent starts
work without a person starting it. A run fails at night; the trigger tells
the agent; the agent finds out why over MCP and proposes the fix as a
change request; a person applies it in the morning.

A trigger only says what happened. The agent then works through the same
MCP tools and permission checks as when a person starts it, with its own
token: the event never carries a credential, and nothing about what the
agent may do changes. Agents never apply a change.

To see the whole loop work, start from the reference agent in
[examples/agents/triage](../examples/agents/triage/README.md): one Python
file that receives deliveries (or reads the feed), asks a model whether the
cause is in the pipeline's definition, and proposes the fix.

This is [ADR-047](adr/047-agent-triggers.md). Agent identities are
[agents.md](agents.md); the MCP tools are [mcp.md](mcp.md); change requests
are [change-requests.md](change-requests.md).

## What a trigger is

| | |
|---|---|
| **Agent** | the agent identity that is told |
| **Workspace** | the workspace the trigger belongs to; it sees only that workspace's pipelines |
| **Scope** | one pipeline, or every pipeline in the workspace the agent may see |
| **Events** | `run.failed`, `run.blocked`, `run.recovered` (below) |
| **Delivery** | `webhook`: posted to the URL and signed; or `feed`: kept for the agent, which reads it with its own token (below) |
| **URL** | for a webhook, where deliveries are sent: `http` or `https`, allowed by the server's outbound network policy, with no credentials in it. A feed has none |
| **Cooldown** | after delivering an event about a pipeline, how long the trigger stays quiet about the same event for that pipeline (default 30 minutes; 0 to 7 days). It is per event type, so a delivered failure does not hide the recovery after it; test events do not count |
| **Signing secret** | for a webhook, a Standard Webhooks secret (`whsec_...`), shown once, when the trigger is made. A feed has none: nothing it delivers is signed |

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
| `run.recovered` | a run succeeds after the pipeline's previous finished run failed (runs that were blocked or cancelled, or are still going, are passed over). It is how an agent learns that a fix it proposed, once applied, worked. | `list_runs` |

A successful run, a cancelled run and a run still in progress are not
events here.

`GET /api/agents/trigger-events` lists every event type this server
offers, with what each means and the MCP tool an agent should start with.

A distribution can add its own types, for example a deadline it tracks.
In Go, it registers the type while the program starts, then raises it
when it happens:

```go
api.RegisterTriggerEvent(api.TriggerEventType{
	Name:        "pipeline.deadline_missed",
	Description: "A pipeline did not finish by its deadline.",
})

// later, on any replica
api.RaiseTriggerEvent(api.RaisedEvent{
	Type:       "pipeline.deadline_missed",
	PipelineID: p.ID,
	Key:        "2026-11-01",  // or RunID, when it is about a run
	Summary:    "orders_daily had not finished by 06:00.",
})
```

A raised event follows every rule here: it goes to the triggers in the
pipeline's workspace that subscribe to it, each sends an occurrence (its
run, or its key for that pipeline) once, the cooldown holds repeats back,
and the agent's access is checked when it is delivered. Its body is the
one below with a `summary`, and without `run` when it is not about one.

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

## For an agent without a URL: the feed

An agent on a laptop, in a CI job or behind a NAT cannot receive a
webhook. Give its trigger the feed instead, and the agent asks for its
events:

```bash
brokoli agents trigger create triage-bot --feed --events run.failed,run.recovered
```

Over the API, `"delivery": "feed"` and no `url`. A feed trigger has no
signing secret and needs no encryption key on the server.

The agent reads with **its own token** (`bka_...`), not a person's:

```
GET /api/agents/me/events?after=<cursor>&limit=50
Authorization: Bearer bka_...
```

```json
{
  "events": [ { "type": "run.failed", "id": "dlv_...", "pipeline": { ... }, "run": { ... }, "next": { ... } } ],
  "cursor": "MjAyNi0xMC0zMVQwMzoxMjowOS4..."
}
```

- Each event is exactly what a webhook would have posted.
- **Pass back the cursor to acknowledge.** Reading with `after=<cursor>`
  marks every event up to it as delivered and answers what came after.
  An event the agent did not get to, because it stopped or crashed, comes
  back on the next read. Read, handle, then read again with the cursor.
- `limit` is 50 by default and at most 200.
- Access is checked when the agent reads, as a webhook's is before it is
  sent: an event about a pipeline the agent may no longer see is skipped,
  not answered.
- Unread events are kept 7 days, then marked **expired**.
- Over MCP, the `next_events` tool does the same: call it, handle the
  events, then call it again with the `cursor` it answered.
- Only an agent has a feed; a person's token is refused. A person sees a
  trigger's events in its delivery log, like any other trigger's.

## Manage them

| CLI | API |
|---|---|
| `brokoli agents trigger list <agent>` | `GET /api/agents/{id}/triggers` |
| `brokoli agents trigger update <agent> <trigger> [--events ...] [--url ...] [--pipeline ...] [--cooldown ...] [--enable or --disable]` | `PATCH /api/agents/{id}/triggers/{trigger}` with the fields to change |
| `brokoli agents trigger delete <agent> <trigger>` | `DELETE /api/agents/{id}/triggers/{trigger}` |
| `brokoli agents trigger rotate-secret <agent> <trigger>` | `POST /api/agents/{id}/triggers/{trigger}/secret` |
| `brokoli agents trigger deliveries <agent> <trigger>` | `GET /api/agents/{id}/triggers/{trigger}/deliveries` |
| `brokoli agents trigger redeliver <agent> <trigger> <delivery>` | `POST /api/agents/{id}/triggers/{trigger}/deliveries/{delivery}/redeliver` |
| `brokoli agents trigger test <agent> <trigger>` | `POST /api/agents/{id}/triggers/{trigger}/test` |

Every change is recorded in the audit log.

## See who was told

Two reads answer anyone who may see the pipeline, not only whoever manages
the agent:

- `GET /api/runs/{id}/agents`: the agents told about a run. Each entry
  names the agent, the event, where its delivery stands (`delivered`,
  `pending`, `failed`, `skipped`, `expired`) and, if the agent then
  proposed a change to the pipeline, that change request (`proposal`: its
  id, title and status). The proposal is the agent's first change request
  to the pipeline made after it was told; Brokoli does not ask the agent
  which run it was answering.
- `GET /api/pipelines/{id}/triggers`: the triggers that watch a pipeline,
  those on it alone (`"scope": "pipeline"`) and those on every pipeline in
  its workspace (`"scope": "workspace"`), enabled or not. A webhook's URL
  is included, with `"may_manage": true`, only for whoever may manage the
  agent.

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

With the official library in Python (`pip install standardwebhooks`):

```python
from standardwebhooks.webhooks import Webhook

# Raises standardwebhooks.webhooks.WebhookVerificationError when the
# delivery does not verify; returns the parsed body when it does.
event = Webhook(secret).verify(body, headers)
```

`headers` maps the header names to their values, and `body` is the raw
bytes. During a rotation the library accepts the delivery with either
secret.

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

## Send a test event

To wire up a receiver without waiting for a real failure:

```bash
brokoli agents trigger test triage-bot 975e1f20...
# Queued test delivery dlv_4b2e... about pipeline orders_daily; it is sent within a few seconds.
```

It is a `run.failed` about the trigger's pipeline, or the first pipeline in
its workspace the agent may see, with `"test": true` and a run id starting
`test-`. It is signed, retried and logged like any other delivery, it is
not held back by the cooldown, and it does not count toward it: a real
failure right after a test is still delivered. Over the API, `POST
/api/agents/{id}/triggers/{trigger}/test`.

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
| A new trigger is not sent a failure from just before it was made | A trigger hears about what fails from the moment it is created. Redeliver nothing; to try it, send a test event or fail the pipeline again. |
| No deliveries at all | No replica runs a scheduler (`--mode all` or `--mode scheduler`); or the failures are within the cooldown of one already delivered; or the run event was not recorded (the run's log then has an `append run event` warning). |
| A delivery is `skipped` | Its reason says why; fix that and redeliver it. |
| Signatures do not verify | The body was parsed and re-serialized before verifying, the receiver uses an old secret after a rotation's 24 hours, or its clock is more than five minutes off. |
