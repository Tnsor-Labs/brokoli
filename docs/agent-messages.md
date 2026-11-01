# Messages from agents

An agent can send a **message** to a person: what it found, a decision it
needs, something urgent. The message lands in the person's inbox in
Brokoli, shown with the agent's avatar and name, with links to the runs,
pipelines and change requests it is about.

This is [ADR-048](adr/048-talking-with-agents.md), phases 1 and 2: agents
send; people read, reply, mark read and mute; a reply reaches the agent
through its triggers. Scheduled triggers and reminders come later. Agent
identities are [agents.md](agents.md); what tells an agent to start work is
[agent-triggers.md](agent-triggers.md); the MCP tools are [mcp.md](mcp.md).

## Sending

With MCP, the `send_message` tool:

```json
{
  "to": "owner",
  "level": "attention",
  "topic": "orders_daily keeps failing",
  "text": "It failed four nights running. The fix I proposed on Monday is still waiting.",
  "links": [
    {"kind": "run", "id": "01a1959b-..."},
    {"kind": "change_request", "id": "440b3094..."}
  ]
}
```

Or the same body to `POST /api/agents/me/messages` with the agent's token.

| Field | |
|---|---|
| `to` | `"owner"` (the agent's owner), a person's username or id in the agent's workspace, or `"workspace"` (every person in it) |
| `text` | plain text with simple Markdown (paragraphs, emphasis, lists, code). At most 8 KB; longer text is cut and says so |
| `level` | `info` (default), `attention` or `urgent`, below |
| `topic` | optional, one line of at most 120 characters, to keep matters apart in the conversation |
| `links` | optional, at most 10: `pipeline`, `run` or `change_request`, each by id |

The answer names each message made:

```json
{"sent": [{"to": "eve", "conversation_id": "...", "message_id": "msg_..."}], "level": "attention"}
```

It adds `"redacted": true` when something that looked like a credential
(a password in a URL, a bearer token) was replaced before the message was
stored, and `"skipped": N` when a workspace message left out people who may
not see what it links to.

### Who may send

An agent needs **`messages.send`**. The built-in `agent`, `operator` and
`editor` roles have it; `viewer` does not, so a read-only agent stays
read-only. As for everything an agent does, it holds the permission only
if its owner holds it too, and disabling the agent or demoting its owner
stops it at once. A person cannot send through this route; people reply in
a conversation (phase 2).

### Who may receive

- A message goes only to people in the agent's workspace (and, where there
  are organizations, its organization).
- Every reader must be able to see everything the message links to, as
  their own sign-in would. A person named in `to` who may not is refused,
  with the reason; a workspace message leaves such people out.
- The agent must be able to see every link too. A link to something it may
  not see is answered as if it did not exist.
- Links are references: Brokoli resolves each one and shows its name (a
  pipeline's name, a change request's title). An agent cannot write a link's
  label or point a link outside Brokoli. A URL typed in the text is shown as
  text.

## Levels

| Level | Means | Shown |
|---|---|---|
| `info` | worth knowing; read when convenient | in the inbox |
| `attention` | needs a decision or an action, not now | in the inbox, and counted on the bell |
| `urgent` | needs someone now | as `attention`; later phases also send it to the workspace's outbound channel |

`urgent` goes only to the agent's owner, or to a person who has written to
the agent. A message to the whole workspace is `info` only.

## Limits

Per agent, counted by Brokoli:

| | |
|---|---|
| to one person | 20 an hour |
| in all | 60 sends an hour (a workspace message is one send) |
| `urgent` | 3 a day |
| to the whole workspace | 5 a day |

Over a limit, the answer is `429` with `Retry-After` and which limit it was.

## Reading, for people

```
GET  /api/messages?unread=true&limit=50          your messages from agents, newest first, and needs_you
GET  /api/messages/conversations/{id}            one conversation, oldest first
POST /api/messages/read                          {"message_id": "..."}, or {} for all
POST /api/messages/conversations/{id}/mute       {"muted": true} or {"muted": false}
```

Each message in the inbox carries the agent's name and avatar, its level,
and `shown_level`: the level the inbox gives it. `needs_you` is how many
unread `attention` and `urgent` messages are not muted, which is what the
bell counts.

A conversation is between one agent and one person; nobody else can read
it, and only that person can mark it read or mute it.

**Muting** an agent's conversation keeps its messages arriving, quietly:
while it is muted, every message in it, including unread ones from before
the mute, is shown at `info` whatever level it was sent at, and none counts
on the bell. Unmuting brings back the ones from before; those that arrived
while muted stay quiet. Its owner sees how many of its messages arrived
muted:

```
GET /api/agents/{id}/messages/summary            {"days": 7, "sent": 12, "muted": 4}
```

That is for the agent's owner, or whoever manages users.

## Replying

A person writes in their conversation with an agent:

```
POST /api/messages/conversations/{id}/messages   {"text": "Yes, and keep the column nullable."}
POST /api/agents/{id}/conversation               its owner opens (or finds) their conversation with an agent
```

Only the conversation's person writes in it. A conversation exists once the
agent has written to someone, or once the agent's owner (or whoever manages
users) opens one from the agent's page, so nobody starts a conversation with
an agent they do not own. A person's message is redacted like an agent's
(it may be posted to the agent's URL), cut at 8 KB, and limited to 30 an
hour in one conversation. Nothing reaches a disabled agent (`409`).

The answer says how many of the agent's triggers were told (`told`). With
none, it adds a note: the agent is not told at once, and reads the message
when it next checks.

### How the agent hears it

Every trigger of the agent that subscribes to **`message.received`**
([agent-triggers.md](agent-triggers.md)) is told, by webhook or in its feed,
whatever pipeline the trigger watches and without a cooldown:

```json
{
  "type": "message.received",
  "id": "dlv_...",
  "agent": "triage-bot",
  "message": {"conversation_id": "...", "message_id": "msg_...", "from": "ada", "text": "Yes, and keep the column nullable."},
  "next": {"suggested_tool": "read_messages", "mcp": "https://.../api/mcp"}
}
```

An agent with no such trigger still reads its messages; it just learns of
them when it checks.

### How the agent reads it

With MCP, **`read_messages`**: without arguments, the unread messages people
wrote to the agent, oldest first, each with `from_name` and its
conversation, which it then marks read; with `conversation_id`, that
conversation's whole thread. The agent answers with `send_message` to the
person's username, which lands in the same conversation. Having written to
the agent, that person may also be sent `urgent` messages.

```
GET  /api/agents/me/messages?unread=true         what people wrote to the agent (unread=false for all)
POST /api/agents/me/messages/read                {"message_ids": [...]}, or {} for all
GET  /api/agents/me/conversations/{id}           one of its conversations
```

These answer the calling agent only. The agent's view of a conversation does
not say whether the person muted it.

## Kept

Messages are kept for 180 days, then deleted, and a conversation left
empty goes with them. The audit log records each send (the agent, who it
went to, the level and the links) but not its text, and each mute.
