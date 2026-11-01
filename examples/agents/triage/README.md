# A reference triage agent

When a pipeline run fails, this agent finds out why. If the cause is in the
pipeline's definition, it drafts the fix and proposes it as a change
request. Otherwise it reports what it found and proposes nothing. A person
reviews every proposal and decides whether to apply it; the agent never
applies anything.

It is one file, [`triage.py`](triage.py), meant to be read, run as it is,
and changed. Talking to Brokoli needs only the Python standard library. The
model can be Claude, any model behind an OpenAI-compatible API (OpenAI,
Gemini, Mistral, OpenRouter, Ollama and many more), or a program of your
own; see [The model](#the-model).

This is the reference agent described in
[ADR-047](../../../docs/adr/047-agent-triggers.md). Triggers themselves are
documented in [docs/agent-triggers.md](../../../docs/agent-triggers.md),
and the MCP tools it calls in [docs/mcp.md](../../../docs/mcp.md).

## What it does

For each `run.failed` event:

1. **Checks whether it already has a proposal waiting.** A pipeline that
   keeps failing for the same reason fails on every run until someone
   applies the fix. If this agent already has an open change request for
   the pipeline that can still be applied, it says so and stops. A stale
   request (the pipeline changed since) or one proposed by someone else
   does not stop it.
2. **Asks Brokoli why the run failed** with `explain_run`: the failing
   nodes, their errors and last log lines, the last successful run, and
   the changes to the pipeline since then. It reads the definition with
   `get_pipeline`.
3. **Asks the model for a decision**: is the cause in the definition? If
   it is, the model answers the whole corrected definition, a title, and
   a Markdown description for the reviewer (why the run failed, what
   changes node by node, what to check). If not, it answers what it found.
4. **Validates the fix** with `validate_pipeline`. If Brokoli rejects it,
   the errors go back to the model once. A fix that still does not
   validate is reported, not proposed.
5. **Proposes it** with `propose_change`. The change request shows the
   diff, the validation, and the description, and the reviewer can test
   it on a few rows before applying it.
6. **Tells its owner** with `send_message` (see
   [agent messages](../../../docs/agent-messages.md)): a proposal as an
   `info` message linking the run and the change request, and a cause
   outside the definition (a missing file, a source that is down) as
   `attention`, with what it found and the run and pipeline linked. The
   owner reads them in the Inbox in Brokoli. A message that cannot be sent
   (an older server, a role without `messages.send`, a limit reached) is
   logged and the triage stands. `--no-messages` turns this off.

Test events (`"test": true`) are logged and not triaged. Other event types
are ignored.

Everything it does in Brokoli is an MCP call with its own agent token, so
it can see and do only what the agent's role allows. Proposing needs
`pipelines.propose` and messaging needs `messages.send`; the built-in
`agent` role has both.

## Set it up

You need a Brokoli server you can reach, and the `brokoli` CLI logged in
as the person who will own the agent.

**1. Create the agent and a token for it.**

```bash
brokoli agents create triage-bot --description "investigates failed runs"
brokoli agents token create triage-bot --name triage --on-behalf > triage.token
```

The token is printed once. Keep it in a secret store, not in the
repository.

**2. Install the model provider and set its key.**

```bash
pip install -r requirements.txt
export ANTHROPIC_API_KEY=...   # or sign in with `ant auth login`
```

That is for the default provider, Claude. For any other, see
[Providers](#providers): for example a model on your own machine with
Ollama needs no key at all:

```bash
export TRIAGE_PROVIDER=openai TRIAGE_BASE_URL=http://localhost:11434/v1 TRIAGE_MODEL=<model>
```

**3. Choose how Brokoli reaches the agent**, then run it. There are two
ways, described below.

### As a service: webhooks

Brokoli posts each failure to the agent as it happens, signed with the
trigger's secret.

```bash
brokoli agents trigger create triage-bot \
  --url https://triage.example.com/ --events run.failed > triage.secret

export BROKOLI_SERVER=https://brokoli.example.com
export BROKOLI_TOKEN="$(cat triage.token)"
export TRIAGE_WEBHOOK_SECRET="$(cat triage.secret)"
python triage.py serve --host 0.0.0.0 --port 8787
```

The agent rejects any delivery whose signature does not verify (HTTP 401).
It answers each delivery at once and triages it afterwards, because Brokoli
waits about 10 seconds for an answer and retries deliveries that get none.
A retried delivery keeps its `webhook-id`, and the agent triages each id
once.

Put it behind HTTPS (a reverse proxy, or your platform's ingress). Brokoli
refuses a trigger URL that resolves to a private or loopback address unless
the server allows it.

To check the wiring without waiting for a failure, send a test event:

```bash
brokoli agents trigger test triage-bot <trigger-id>
```

The agent logs `test event for <pipeline>: received; nothing to do`.

### As a scheduled job: the feed

If nothing can reach the agent (no public URL, a laptop, a CI runner),
Brokoli keeps the events in the agent's feed and the agent reads them with
its token.

```bash
brokoli agents trigger create triage-bot --feed --events run.failed

export BROKOLI_SERVER=https://brokoli.example.com
export BROKOLI_TOKEN="$(cat triage.token)"
python triage.py poll            # read, triage, sleep 30s, repeat
python triage.py poll --once     # read until the feed is empty, then exit
```

`poll --once` suits a scheduled job. For example, in GitHub Actions:

```yaml
on:
  schedule:
    - cron: "*/15 * * * *"
jobs:
  triage:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-python@v5
        with:
          python-version: "3.12"
      - run: pip install -r examples/agents/triage/requirements.txt
      - run: python examples/agents/triage/triage.py poll --once
        env:
          BROKOLI_SERVER: https://brokoli.example.com
          BROKOLI_TOKEN: ${{ secrets.BROKOLI_TRIAGE_TOKEN }}
          ANTHROPIC_API_KEY: ${{ secrets.ANTHROPIC_API_KEY }}
```

The agent reads one event at a time and acknowledges it only after it was
triaged. If the agent stops part way, or cannot reach Brokoli, the event
stays in the feed and is read again next time. Events nobody reads expire
after 7 days.

### One run, by hand

```bash
python triage.py run <run-id> --pipeline <pipeline-id>
```

This triages that run now, whether or not a trigger saw it. It does not
check for a waiting proposal, because it does not know the agent's name.

## Options

| Flag | Environment | Default | What it does |
| --- | --- | --- | --- |
| `--server` | `BROKOLI_SERVER` | `http://localhost:8080` | the Brokoli server |
| | `BROKOLI_TOKEN` | (required) | the agent's token |
| `--provider` | `TRIAGE_PROVIDER` | `anthropic` | `anthropic`, `openai` (any OpenAI-compatible API) or `command` |
| `--model` | `TRIAGE_MODEL` | `claude-opus-5-5` with `anthropic` | the model; required with `openai` |
| `--effort` | `TRIAGE_EFFORT` | `high` with `anthropic` | how hard the model works; see [Providers](#providers) |
| `--base-url` | `TRIAGE_BASE_URL` | OpenAI's | `openai`: where the API is |
| `--api-key-env` | `TRIAGE_API_KEY_ENV` | `OPENAI_API_KEY` | `openai`: the variable that holds the key |
| `--response-format` | `TRIAGE_RESPONSE_FORMAT` | `json_schema` | `openai`: `json_object` for a service without JSON schemas |
| `--model-command` | `TRIAGE_MODEL_COMMAND` | | `command`: the program to run (below) |
| `--dry-run` | | off | decide and validate, but propose nothing and send no message |
| `--no-messages` | | off | only log findings; do not send them to the owner's inbox |
| | `TRIAGE_WEBHOOK_SECRET` | (required for `serve`) | the trigger's signing secret |

During a secret rotation Brokoli signs with the new and the previous secret
for 24 hours, so you have a day to give the agent the new one.

## The model

Whatever the provider, the model gets the same instructions and the same
facts, and must answer the same JSON decision: verdict, finding, title,
description, definition. The agent checks the answer's shape before acting
on it, and Brokoli validates every drafted definition, so a weaker model
costs you proposals, not broken pipelines: an answer of the wrong shape is
logged as no decision, and a fix that does not validate is sent back once
and then reported instead of proposed.

The agent calls Brokoli's MCP tools itself instead of handing the MCP
server to the model. Two reasons:

- the order of steps is fixed in code, so the model cannot skip validation
  or propose twice;
- the Brokoli server and the agent's token never leave your network.

That is also why any provider works: the model only has to answer one
question at a time, with no tools.

### Providers

| Provider | `--provider` | `--base-url` | Key in |
| --- | --- | --- | --- |
| Claude (Anthropic) | `anthropic` (default) | | `ANTHROPIC_API_KEY` |
| OpenAI | `openai` | | `OPENAI_API_KEY` |
| Azure OpenAI | `openai` | `https://<resource>.openai.azure.com/openai/v1/` | your choice, with `--api-key-env` |
| Google Gemini | `openai` | `https://generativelanguage.googleapis.com/v1beta/openai/` | `GEMINI_API_KEY`, with `--api-key-env` |
| Mistral | `openai` | `https://api.mistral.ai/v1` | `MISTRAL_API_KEY`, with `--api-key-env` |
| DeepSeek | `openai` | `https://api.deepseek.com` | `DEEPSEEK_API_KEY`, with `--api-key-env` |
| Groq | `openai` | `https://api.groq.com/openai/v1` | `GROQ_API_KEY`, with `--api-key-env` |
| OpenRouter | `openai` | `https://openrouter.ai/api/v1` | `OPENROUTER_API_KEY`, with `--api-key-env` |
| Ollama | `openai` | `http://localhost:11434/v1` | none |
| vLLM | `openai` | `http://localhost:8000/v1` | none |
| LM Studio | `openai` | `http://localhost:1234/v1` | none |
| anything else | `command` | | up to your program |

`--model` is the provider's own name for the model; look it up in its
console or model list, since names change often. For example, with Gemini:

```bash
export GEMINI_API_KEY=...
python triage.py --provider openai \
  --base-url https://generativelanguage.googleapis.com/v1beta/openai/ \
  --api-key-env GEMINI_API_KEY --model <gemini model> poll --once
```

Install only the SDK you use if you like: `anthropic` for Claude, `openai`
for every `openai` row (it is only the client; nothing is sent to OpenAI
unless `--base-url` is left at its default).

### Claude

The request asks
for adaptive thinking and constrains the answer to a JSON schema (verdict,
finding, title, description, definition), so the agent never has to parse
free text. It also turns on the API's server-side fallback: if the model's
safety classifiers decline the request, another model answers it in the
same call. If the whole chain declines, the agent logs that it has no
decision and proposes nothing. `--effort` is `low`, `medium`, `high` (the
default), `xhigh` or `max`.

### OpenAI-compatible APIs

`--provider openai` sends a chat completions request with the decision's
JSON schema as a strict `response_format`. Most services and local servers
honor it. One that answers that it does not support `json_schema` (some
models on some services only offer JSON mode) can be asked for any JSON
object with `--response-format json_object`: the schema then goes in the
instructions, and the agent's own check of the answer does the rest.

`--effort` is sent as `reasoning_effort`, and only when you set it,
because only reasoning models accept it, with the values the provider
documents. A refusal, an answer cut off at the output limit, or one stopped
by the provider's content filter is logged as no decision.

### Your own program

Give a command with `--model-command` (and `--provider command`, or no
`--provider`). The agent runs it once per decision,

Give a command with `--model-command`. The agent runs it once per decision,
writes this to its stdin:

```json
{
  "system": "the instructions",
  "schema": {"type": "object", "properties": {"verdict": "..."}},
  "messages": [{"role": "user", "content": "the explanation and the definition"}]
}
```

and reads the decision from its stdout:

```json
{
  "verdict": "fix",
  "finding": "The source reads /data/order.csv, which does not exist.",
  "title": "Read orders.csv, not order.csv",
  "description": "## Why\n\n...",
  "definition": "{\"id\": \"...\", \"nodes\": [...], \"edges\": [...]}"
}
```

`definition` is the whole pipeline as JSON text, or `""` when the verdict
is `report`. When a fix does not validate, the command is run again with
two more messages: its own answer (`assistant`) and Brokoli's errors
(`user`). A non-zero exit status means no decision.

## Run the tests

```bash
python -m unittest test_triage.py
```

They need no server and no model. The tests that check the request each
provider's SDK sends run when that SDK from `requirements.txt` is
installed, and are skipped otherwise.

## Make it yours

- **Report somewhere else too.** Reports reach the owner's Inbox. Send
  them to a person other than the owner (`to` is any username in the
  workspace), to chat as well, or comment on the pipeline's issue with
  `comment_on_issue`.
- **Look further before deciding.** Add `get_run_logs`, `get_lineage`, or
  `dry_run_pipeline` (a preview on a few rows that writes nothing) to what
  the model sees.
- **Narrow what it may touch.** Give the agent a role that sees only the
  pipelines it should triage, or a trigger for one pipeline
  (`--pipeline`).
- **Handle recoveries.** Subscribe to `run.recovered` and close or comment
  on the agent's proposals once a pipeline is healthy again.
