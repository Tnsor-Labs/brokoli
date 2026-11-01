# ADR-048: Talking with agents: messages, conversations and reminders

**Status:** proposed
**Date:** 2026-11-01

## Context

ADR-046 made agents users of the runtime, and ADR-047 gave them a reason
to start work: a trigger delivers a run event (a failure, a blocked run, a
recovery) to the agent, by a signed webhook or a feed it reads. An agent
can then investigate over MCP and propose a change request.

Everything flows one way. Brokoli can tell an agent something; an agent
cannot tell a person anything, except indirectly: a change request it
proposes, or a comment on an issue (`comment_on_issue`). That leaves out
most of what people want from an agent that watches their data:

- **Something important happened.** "orders_daily has failed four nights
  running and the fix I proposed on Monday is still waiting." Today the
  agent's only outlet is its own log, which nobody reads (the reference
  triage agent says so in its README: a report "is only logged").
- **A question back.** "The fix needs a new column in the warehouse. Do
  you want me to propose that too?" There is no way to ask, or to answer.
- **Proactive work on a schedule.** "Every weekday at 8:00, look at last
  night's runs and tell me only if something matters." Triggers start an
  agent on events, never on time.
- **Reminders.** "Remind ada on Monday to review this change request."

Three facts shape the design:

1. **Brokoli does not run the agents.** They run elsewhere (the reference
   triage agent, Claude Code on a laptop, a hosted agent) and connect over
   MCP with their own token. So a conversation is Brokoli carrying
   messages both ways and keeping the record; an agent answers whenever it
   is running. A chat that expects a reply within a second is the wrong
   model.
2. **The delivery machinery exists.** ADR-047's deliveries are durable,
   retried, signed, logged, checked against what the agent may see, and
   readable from a feed by an agent without a URL. A message for an agent
   is one more event to deliver.
3. **People already have an inbox.** The alert inbox (`/api/alerts`)
   stores alerts per organization with severity, links to a pipeline or a
   run, and read state per person. What it lacks is an addressee and a
   reply.

The risk is the same as with any software that can message people: spam,
alarm fatigue, and impersonation. An agent that can write to a person can
also write something misleading to that person. The design has to make an
agent's message unmistakably an agent's, bounded in volume, and impossible
to aim outside the agent's own workspace.

## Decision

Agents and people exchange **messages** in **conversations** that Brokoli
stores. An agent sends with an MCP tool; a person reads and replies in the
app; a person's message reaches the agent the way a run event does,
through its triggers or its feed. Triggers can also fire **on a schedule**,
and an agent can leave a **reminder** that Brokoli delivers at a set time.

### 1. Messages and conversations

A **conversation** is between one agent and one person, in one workspace.
Each pair has at most one open conversation, so "talking to triage-bot" is
always the same thread, and a message can start a new **topic** inside it
(a subject line) when the agent wants to keep matters apart.

A **message** has:

- the conversation it belongs to, and who wrote it: the agent or the person;
- its **text**: plain text with a small Markdown subset (paragraphs,
  emphasis, lists, inline code, code blocks). No HTML, no images;
- a **level**: `info`, `attention` or `urgent` (section 4);
- **links**, each one of: a pipeline, a run, a change request, an issue.
  Links are references, resolved and rendered by the app, never URLs the
  agent writes; free-text URLs in the body are shown as text, not links;
- an optional **topic**;
- when it was written, and per reader when it was read.

Messages are kept for 180 days, then deleted with their conversation's
empty topics. The audit log records that a message was sent (who, to
whom, level, links), not its text.

### 2. An agent sends: `send_message`

A new MCP tool and its REST twin:

```
send_message(to, text, level?, topic?, links?)       MCP
POST /api/agents/me/messages                          REST, agent token
```

`to` is one of:

- `"owner"`: the agent's owner;
- a person's username or id, who must be a member of the agent's
  workspace and able to see every linked object;
- `"workspace"`: every member of the workspace who can see every linked
  object. Allowed only at `info` level; workspace announcements are not
  for alarms.

The answer carries the message and its conversation id. Every check is
made against the agent's permissions as they are at the moment it sends
(its role within its owner's current permissions, as ADR-046 and 047 do),
so demoting the owner or disabling the agent silences it at once.

Sending needs a new permission, **`messages.send`**. The built-in `agent`
and `operator` roles hold it; `viewer` does not, so a read-only agent
stays read-only. A distribution maps it into its own roles.

The text is screened as MCP results are (`mcpserver.RedactText`): a
credential in a message is replaced before it is stored, and the agent
is told so in the answer. Text is cut at 8 KB.

### 3. A person replies, and the agent hears it

In the app, each agent has a **conversation view** (on the agent's page,
and in the inbox, section 5). A person writes; Brokoli stores the message
and raises a **`message.received`** event for the agent, through the
event-type registry ADR-047 phase 3 added (`api.RegisterTriggerEvent`,
`api.RaiseTriggerEvent`). So:

- an agent with a webhook trigger subscribed to `message.received` is
  posted the message, signed, retried and logged like a run event;
- an agent that reads its feed gets it there;
- in either case the event carries the conversation id and the message,
  and the agent reads the rest of the thread with **`read_messages`**
  (MCP) or `GET /api/agents/me/conversations/{id}`.

An agent with no trigger for `message.received` can still read its
conversations with `read_messages`; the conversation view says that
messages wait until it does ("triage-bot reads messages when it checks;
it has no trigger that tells it at once").

Who may write to an agent: its owner, whoever manages users, and anyone
the agent has written to (they may reply in that conversation). Nobody
can start a conversation with someone else's agent uninvited: an agent's
existence stays the owner's to share (ADR-046).

### 4. Levels, and keeping the volume down

The level says what the message asks of the reader:

| Level | Means | Where it shows |
|---|---|---|
| `info` | worth knowing; read when convenient | the inbox |
| `attention` | needs a decision or an action, not now | the inbox, and the count on the shell's bell |
| `urgent` | needs someone now | the inbox, the bell, and the outbound channels (section 6) |

Limits per agent, counted by Brokoli and answered with the time until the
next allowed message:

- at most 20 messages an hour to any one person, 60 in all;
- at most 3 `urgent` a day, and only to its owner or to a person who has
  written to it;
- `workspace` messages at most 5 a day.

A person can **mute** an agent: its messages still arrive, quietly, at
`info` whatever their level. The owner sees in the agent's page how many
messages it sent and how many were muted, which is how a noisy agent gets
noticed and fixed.

### 5. The inbox

The shell's bell becomes the **inbox**: alerts (as today) and messages to
the reader, newest first, filterable by kind. A message row shows the
agent's avatar and name, the level, the first line, and its links. Opening
it opens the conversation. Unread `attention` and `urgent` messages count
toward the bell's badge; `info` does not, so the badge means "something
needs you".

An agent's message is always rendered as an agent's: its avatar, its
name, an "agent" label, and its owner's name on hover. It never borrows a
person's look, and its text cannot set a sender name.

### 6. Outside the app

`urgent` messages also go to the workspace's outbound notification
channel when one is configured (the Slack webhook in
`/api/settings/notifications` today), as one line with the agent's name,
the first line of the text, and a link back to the conversation. Nothing
else leaves the app.

A distribution can add channels (a person's email, say) and digests (one
daily summary instead of several messages) through a hook called when a
message is stored, the same way it adds event types and MCP tools. Core
defines the hook and the levels; it does not send email.

### 7. Scheduled triggers

A trigger can fire on a **schedule** instead of on run events: a cron
expression and a time zone, delivered as event **`schedule.tick`** with
the scheduled time. It goes through ADR-047's machinery, so it is durable
and logged, and each scheduled time is one occurrence: a leader that
restarts does not deliver the same 08:00 twice, and a missed tick (the
server was down) is delivered once when it comes back, marked late, not
once per missed minute.

The scheduler that runs pipelines already evaluates cron in time zones;
scheduled triggers use it. A schedule fires at most once a minute, and a
workspace has at most 50 scheduled triggers.

This is what makes an agent proactive without polling: "every weekday at
8:00" wakes it, it reads the night's runs over MCP, and it sends a message
only if something matters.

### 8. Reminders

An agent can leave a reminder for a person:

```
remind(to, at, text, links?)            MCP
POST /api/agents/me/reminders           REST
GET/DELETE /api/agents/me/reminders     its pending reminders
```

At `at`, Brokoli posts it into the conversation as a message from the
agent, at `attention` level, whether or not the agent is running then.
`to` follows section 2's rules (no `workspace`). `at` is between one
minute and one year away; an agent has at most 100 pending reminders.
The person sees pending reminders in the conversation and can cancel
them, as can the agent and its owner.

So "remind me on Monday to review this" said in a conversation becomes
the agent calling `remind`, and the reminder arrives even if the agent is
off by Monday.

## Consequences

### Positive

- Agents can say what they found. The triage agent's "report" stops being
  a log line and becomes a message to the owner, with the run linked.
- A person can steer an agent in words ("not that pipeline, it is being
  retired") without editing its configuration, and the agent can ask
  before acting.
- Proactive agents need no polling loop of their own: scheduled triggers
  wake them, and reminders outlive them.
- One record of what each agent told whom, and when, beside the change
  requests it proposed.
- Messages to agents reuse ADR-047's delivery, so signing, retries, the
  delivery log, the visibility check and the feed work for conversations
  on day one.

### Negative

- A new store (conversations, messages, reminders) and its retention job,
  in SQLite and Postgres.
- Rate limits and levels are policy; the numbers above are a first guess
  and will need tuning from use.
- A reply is only as quick as the agent's checking. An agent without a
  `message.received` trigger may take hours to answer; the UI has to say
  so plainly.
- Agents can now write text that people read and trust. Redaction, the
  link rule and the agent styling reduce the risk of a misleading message;
  they do not remove it. A compromised agent token can send messages
  until it is revoked, bounded by the rate limits.

### Deferred

- Group conversations (several people and one agent, or agents talking to
  each other). One agent, one person keeps permission checks simple.
- Attachments (a CSV of the bad rows). Links to a run's data cover the
  common case.
- Rich actions in a message ("Apply this change" buttons). A link to the
  change request does the same with the existing permission checks.
- Email, push and digests in core. They arrive through the hook in a
  distribution first.
- Agents messaging people outside the workspace.

## Alternatives considered

**Extend the alert inbox instead of a new store.** Alerts are generated by
the system for an organization, with no addressee and no reply. Making
them addressed and threaded would change every existing alert query and
the meaning of "read" for alerts. The inbox UI shows both; the records
stay separate.

**Comments on issues as the conversation.** `comment_on_issue` exists, and
issues have threads. But an issue is about a pipeline and visible to
everyone who can see it; a question from an agent to its owner is not an
issue, and putting it in one either hides it or broadcasts it.

**A chat that waits for the agent's reply.** Brokoli would hold the
person's request open until the agent answers. Agents run elsewhere and
may be off; a stored conversation with asynchronous delivery is honest
about that, and the agent that is running answers within seconds anyway.

**Let agents post to Slack directly.** An agent can already be given a
Slack token by its operator. That bypasses the workspace boundary, the
rate limits and the record, and splits the conversation from the runs it
is about. The outbound channel in section 6 is for `urgent` only, and
links back.

**Agents schedule their own wake-ups with cron on their host.** Many will,
and that keeps working. Scheduled triggers exist for agents without a
host that stays up (a scheduled job reading its feed), and so the
schedule is visible and manageable next to the agent's other triggers.

## Follow-ups

- **Phase 1:** the store (conversations, messages, per-person read
  state, retention); `messages.send`; `send_message` (MCP and REST) with
  levels, links, redaction and rate limits; the inbox in the shell and
  the conversation view on the agent's page (read-only for people);
  mute; the reference triage agent sends its reports as messages.
- **Phase 2:** replies: a person writes in a conversation;
  `message.received` through the event-type registry; `read_messages`
  and the conversation API for agents; the reference agent answers a
  question about its last finding.
- **Phase 3:** scheduled triggers (`schedule.tick`) and reminders.
- **Phase 4:** `urgent` to the workspace's outbound channel; the hook a
  distribution uses for its own channels and digests.
- **Docs:** `docs/agent-messages.md`, and the triage agent's README.
