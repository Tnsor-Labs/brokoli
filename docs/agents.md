# Agent identities

An **agent** is an account for an AI agent (or any automation that acts on
its own). It is what you give an agent instead of your own credentials or
the server's static API key:

- it has a **role**, chosen and visible, by default the built-in `agent`
  role: see pipelines, runs and lineage, and run pipelines;
- it has an **owner**, the person who answers for it, and it never has
  more permissions than its owner has, now: demote the owner and the agent
  narrows with the same token; remove the owner and it stops;
- it has **tokens of its own** (`bka_...`), each with a name and an expiry,
  revocable one by one, showing when it was last used;
- it **never reads secrets** and never manages people or roles, whatever
  its role says;
- what it does is **attributed to it**: runs it starts say "agent
  triage-bot, for Ada", and audit entries record the agent, its owner and,
  when its token says so, the person it acted for.

An agent has no password and cannot sign in to the UI.

This is phase 2 of [ADR-046](adr/046-agents-mcp-server-and-agent-identities.md).
Its main use is connecting an agent to Brokoli over MCP
([mcp.md](mcp.md)); an agent token works for every API request the agent's
permissions allow.

## Quick start

An administrator (someone who may manage users) creates the agent; its
owner, or the administrator, issues its token:

```bash
brokoli login --server https://brokoli.example.com

brokoli agents create triage-bot --description "Investigates failed runs"
# Created agent triage-bot (role agent, owned by ada).

brokoli agents token create triage-bot --name "ada's laptop" --days 30 --on-behalf
# Token "ada's laptop" for triage-bot, valid until 2026-11-19. It is shown only now:
# bka_3f9c...
```

Give the token to the agent, for example in an MCP client's configuration:

```json
{
  "mcpServers": {
    "brokoli": {
      "command": "brokoli",
      "args": ["mcp", "--server", "https://brokoli.example.com"],
      "env": { "BROKOLI_TOKEN": "bka_3f9c..." }
    }
  }
}
```

`brokoli mcp` then says `serving https://brokoli.example.com as agent
triage-bot, on behalf of ada`, and the agent is told the same.

## Roles and permissions

| Role | What an agent with it may do |
|---|---|
| `agent` (default) | `pipelines.view`, `runs.view`, `pipelines.run` |
| `viewer`, `operator`, `editor`, a custom role | that role's permissions, as for a person |
| `admin`, `superadmin` | not allowed for an agent |

Whatever the role, an agent's effective permissions are:

```
the role's permissions
  ∩ its owner's permissions (read on every request)
  − variables.view_secrets, settings.manage_users, settings.manage_roles,
    workspaces.manage_members, workspaces.manage_tokens
```

`GET /api/auth/me/permissions` with the agent's token shows the result.

Agents never manage agents: an agent's token is refused on every
`/api/agents` route.

## Tokens

- **Shown once.** The token is in the answer that issues it and nowhere
  else; Brokoli stores only its SHA-256. Lists show its first characters
  (`bka_3f9c1a`) to recognize it.
- **Expiry.** 90 days by default, at most 365.
- **Revocation.** One token at a time (`brokoli agents token revoke`), or
  all of them by disabling the agent.
- **Last use.** Recorded at most once a minute, shown per token and per
  agent, so an unused token is easy to spot and revoke.
- **On behalf of.** A token issued by the agent's owner with
  `--on-behalf` (`"on_behalf_of_owner": true`) makes everything the agent
  does with it say it acted for the owner. Only the owner can issue such a
  token.

An agent token is refused (401 `invalid, expired or revoked agent token`)
when it is unknown, expired or revoked, when the agent is disabled, or
when its owner no longer has an account.

## Who manages agents

| | Administrator (`settings.manage_users`) | The agent's owner | Anyone else |
|---|---|---|---|
| Create an agent, choose its owner | yes | no | no |
| Change its role or description | yes | no | no |
| See it, its tokens | every agent | their own agents | no (404) |
| Issue and revoke tokens | yes | yes | no |
| Issue an "on behalf of" token | only for their own agents | yes | no |
| Disable it | yes | yes | no |

Disabling keeps the agent, so what it did stays attributed to it.

### Names

An agent's name is 2 to 48 lowercase letters, digits and dashes, starting
with a letter, and it is unique among agents, disabled ones included (a
disabled agent keeps its name, so what it did stays unambiguous). On a
plain server it is also unique among people's usernames, so an agent
never reads as a person. A distribution with organizations scopes names
to the organization (`AgentHooks.Scope`): two organizations can each have
a `triage-bot`, and neither learns the other has one.

The name is the agent's display name; its stored username is an internal
handle (`agent-<id>`) that nobody signs in with. Agents made before names
were scoped keep their name as their username, and keep working as they
are. An agent refused as it is created (a plan's limit, say) is not kept,
and its name is free again.

## The CLI

```
brokoli agents list
brokoli agents create <name> [--role agent] [--description ...] [--owner <user id>]
brokoli agents disable <agent>
brokoli agents token create <agent> --name <where it is used> [--days 90] [--on-behalf]
brokoli agents token list <agent>
brokoli agents token revoke <agent> <token id>
```

`<agent>` is the agent's name or id. The CLI uses the credentials from
`brokoli login` (or `--server` and `--api-key`).

## The API

```
GET    /api/agents                          the agents you own; every agent for settings.manage_users
POST   /api/agents                          {"name", "role"?, "description"?, "owner_id"?}
GET    /api/agents/{id}
PATCH  /api/agents/{id}                     {"role"?, "description"?}
DELETE /api/agents/{id}                     disable it and revoke its tokens
GET    /api/agents/{id}/tokens
POST   /api/agents/{id}/tokens              {"name", "expires_in_days"?, "on_behalf_of_owner"?}
DELETE /api/agents/{id}/tokens/{tokenId}
```

An agent:

```json
{
  "id": "59e96ad3f5545ca5b88e69f6337fd27d",
  "name": "triage-bot",
  "description": "Investigates failed runs",
  "role": "agent",
  "owner_id": "5ae024002ed15970ba6aa578e438b29c",
  "owner_name": "ada",
  "disabled": false,
  "created_at": "2026-10-20T07:14:10Z",
  "last_used_at": "2026-10-20T07:20:00Z"
}
```

Issuing a token answers `{"token": "bka_...", "record": {...}, "note":
"This is the only time the token is shown..."}`.

Names are 2 to 48 lowercase letters, digits and dashes, starting with a
letter, and share the namespace of people's usernames.

## With the static API key

A server started with `--api-key` authenticates every `/api` request with
that one key, as an administrator; agent tokens, like people's sessions,
are not accepted in that mode. Do not give an agent the static key: over
MCP it would be offered everything, and `brokoli mcp` and the MCP server
warn when it is used.
