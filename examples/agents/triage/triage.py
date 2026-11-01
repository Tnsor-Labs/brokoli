#!/usr/bin/env python3
"""A reference triage agent for Brokoli (ADR-047).

When a pipeline run fails, Brokoli tells this agent: by a signed webhook
(`serve`), or through the agent's feed (`poll`). For each failure the agent:

1. asks Brokoli why the run failed (MCP `explain_run`) and reads the
   pipeline's definition (`get_pipeline`);
2. asks a model whether the cause is in the definition, and if it is, for
   the corrected definition, a title, and a description for the reviewer;
3. checks that definition (`validate_pipeline`), and proposes it as a
   change request (`propose_change`) for a person to apply;
4. otherwise reports what it found and proposes nothing.

It never applies a change: a person reviews and applies every proposal.

Everything it does in Brokoli goes through MCP with its own agent token, so
it can see and do exactly what the agent's role allows. Only the Python
standard library is needed to talk to Brokoli. The model can be Claude
(`--provider anthropic`, the default, needs the `anthropic` package), any
service or local server that speaks the OpenAI chat API (`--provider
openai`, needs the `openai` package), or a command of your own. See
README.md for the setup.
"""

from __future__ import annotations

import argparse
import base64
import collections
import hashlib
import hmac
import http.server
import itertools
import json
import os
import queue
import shlex
import subprocess
import sys
import threading
import time
import urllib.error
import urllib.request

# --- Brokoli over MCP


class BrokoliError(Exception):
    pass


class Brokoli:
    """Calls Brokoli's MCP tools at /api/mcp with the agent's token.

    Brokoli's MCP endpoint is stateless and answers in JSON, so each tool
    call is one JSON-RPC request; no session to open or keep.
    """

    def __init__(self, server: str, token: str, timeout: float = 120):
        self.url = server.rstrip("/") + "/api/mcp"
        self.token = token
        self.timeout = timeout
        self._ids = itertools.count(1)

    def call(self, tool: str, **arguments):
        body = json.dumps({
            "jsonrpc": "2.0", "id": next(self._ids), "method": "tools/call",
            "params": {"name": tool, "arguments": arguments},
        }).encode()
        req = urllib.request.Request(self.url, data=body, method="POST", headers={
            "Authorization": "Bearer " + self.token,
            "Content-Type": "application/json",
            "Accept": "application/json, text/event-stream",
        })
        try:
            with urllib.request.urlopen(req, timeout=self.timeout) as res:
                reply = json.load(res)
        except urllib.error.HTTPError as e:
            raise BrokoliError(f"{tool}: HTTP {e.code}: {e.read()[:300].decode(errors='replace')}") from None
        except urllib.error.URLError as e:
            raise BrokoliError(f"{tool}: {e.reason}") from None
        if "error" in reply:
            raise BrokoliError(f"{tool}: {reply['error'].get('message')}")
        result = reply["result"]
        text = "".join(c.get("text", "") for c in result.get("content", []) if c.get("type") == "text")
        if result.get("isError"):
            raise BrokoliError(f"{tool}: {text}")
        if "structuredContent" in result:
            return result["structuredContent"]
        try:
            return json.loads(text)
        except ValueError:
            return text


# --- Standard Webhooks signatures (what Brokoli signs webhooks with)

TOLERANCE = 5 * 60


def verify_webhook(secret: str, headers, body: bytes, now: float | None = None) -> bool:
    """True when the body was signed with secret, recently.

    During a rotation Brokoli signs with the new and the previous secret for
    24 hours; either one verifies, so this agent keeps working when the
    secret is rotated as long as it is given the new one within that time.
    """
    msg_id = headers.get("webhook-id") or ""
    ts = headers.get("webhook-timestamp") or ""
    sigs = headers.get("webhook-signature") or ""
    if not (msg_id and ts.isdigit() and sigs):
        return False
    if abs((now or time.time()) - int(ts)) > TOLERANCE:
        return False
    key = base64.b64decode(secret.removeprefix("whsec_"))
    expected = base64.b64encode(
        hmac.new(key, f"{msg_id}.{ts}.".encode() + body, hashlib.sha256).digest()
    ).decode()
    return any(
        s.startswith("v1,") and hmac.compare_digest(s[3:], expected)
        for s in sigs.split(" ")
    )


# --- The model

SYSTEM = """\
You are a data engineer on call for Brokoli, a data pipeline runtime. A \
pipeline run failed. You are given Brokoli's explanation of the failure \
(the failing nodes, their errors and log lines, the last successful run, \
and the changes to the pipeline since then) and the pipeline's definition \
(its nodes, their settings, and the edges between them).

Decide whether the failure is caused by the pipeline's definition, so that \
changing the definition fixes it: a wrong column or table name, a query \
or code error, a misconfigured node, a step in the wrong order. Many \
failures are not: a source that was down, missing credentials, a full \
disk, a timeout against a slow system, bad data that the definition \
handles correctly by failing. Those need a person, not a new definition.

If the cause is in the definition, answer verdict "fix" with the whole \
corrected definition as JSON text: the definition you were given, with \
only the changes the fix needs. Keep every node id, every other setting \
and every edge as it is. Give a title that says what the change does, in \
one line, and a Markdown description for the person who decides whether \
to apply it: why the run failed, what the change does node by node, what \
to expect in the data, and anything they should check. The diff is shown \
next to the description, so do not repeat it.

Otherwise answer verdict "report", leave definition and title empty, and \
say in description what you found and what a person should look at.

In both cases, finding is one or two plain sentences on the cause. Do not \
guess: if the explanation does not show the cause, say so and report."""

DECISION = {
    "type": "object",
    "properties": {
        "verdict": {"type": "string", "enum": ["fix", "report"]},
        "finding": {"type": "string"},
        "title": {"type": "string"},
        "description": {"type": "string"},
        "definition": {"type": "string"},
    },
    "required": ["verdict", "finding", "title", "description", "definition"],
    "additionalProperties": False,
}


class Refused(Exception):
    pass


class AnthropicModel:
    """The default provider: Claude through the Anthropic API.

    It reads ANTHROPIC_API_KEY (or a profile from `ant auth login`). The
    answer is constrained to DECISION with structured outputs. If the
    model's safety classifiers decline the request, the API's server-side
    fallback serves it with another model in the same call.
    """

    def __init__(self, model: str, effort: str):
        import anthropic  # only this provider needs it

        if int(anthropic.__version__.split(".")[0]) < 2:
            raise SystemExit(f"anthropic {anthropic.__version__} is too old: pip install -r requirements.txt")
        self.client = anthropic.Anthropic()
        self.model = model
        self.effort = effort

    def decide(self, messages: list) -> tuple[dict, list]:
        with self.client.beta.messages.stream(
            model=self.model,
            max_tokens=64000,
            system=SYSTEM,
            messages=messages,
            thinking={"type": "adaptive"},
            output_config={"effort": self.effort, "format": {"type": "json_schema", "schema": DECISION}},
            betas=["server-side-fallback-2026-07-01"],
            fallbacks="default",
        ) as stream:
            response = stream.get_final_message()
        if response.stop_reason == "refusal":
            raise Refused(getattr(response.stop_details, "explanation", None) or "the model declined")
        if response.stop_reason == "max_tokens":
            raise Refused("the answer did not fit in max_tokens")
        text = next(b.text for b in response.content if b.type == "text")
        # The whole content goes back on a retry, thinking blocks included,
        # so the conversation stays append-only.
        return json.loads(text), [{"role": "assistant", "content": response.content}]


class OpenAIModel:
    """Any model behind the OpenAI chat completions API.

    That is OpenAI itself, and every service or server that offers the same
    API: Google Gemini, Mistral, DeepSeek, Groq, OpenRouter, Azure OpenAI,
    and Ollama, vLLM or LM Studio on your own machine. --base-url says
    where. The key is read from the variable --api-key-env names
    (OPENAI_API_KEY by default); a server of your own needs none.

    The answer is constrained to DECISION with a strict JSON schema. A
    service that does not support schemas can be asked for any JSON object
    instead (--response-format json_object): the schema then goes in the
    system prompt, and the answer is checked here.
    """

    def __init__(self, model: str, effort: str | None, base_url: str | None,
                 api_key_env: str, response_format: str):
        import openai  # only this provider needs it

        key = os.environ.get(api_key_env)
        if not key and not (base_url or os.environ.get("OPENAI_BASE_URL")):
            raise SystemExit(f"set {api_key_env} to the provider's API key")
        # A server of your own takes any key, but the SDK will not send none.
        self.client = openai.OpenAI(api_key=key or "none", base_url=base_url, timeout=900)
        self.model = model
        self.effort = effort
        self.system = SYSTEM
        if response_format == "json_schema":
            self.format = {"type": "json_schema",
                           "json_schema": {"name": "decision", "schema": DECISION, "strict": True}}
        else:
            self.format = {"type": "json_object"}
            self.system += ("\n\nAnswer with one JSON object, and nothing else, that matches this JSON Schema:\n"
                            + json.dumps(DECISION))

    def decide(self, messages: list) -> tuple[dict, list]:
        extra = {"reasoning_effort": self.effort} if self.effort else {}
        response = self.client.chat.completions.create(
            model=self.model,
            messages=[{"role": "system", "content": self.system}, *messages],
            response_format=self.format,
            **extra,
        )
        choice = response.choices[0]
        if getattr(choice.message, "refusal", None):
            raise Refused(choice.message.refusal)
        if choice.finish_reason == "length":
            raise Refused("the answer did not fit in the model's output limit")
        if choice.finish_reason == "content_filter":
            raise Refused("the provider's content filter stopped the answer")
        text = choice.message.content or ""
        return checked(text), [{"role": "assistant", "content": text}]


def checked(text: str) -> dict:
    """The decision in text, if it has the shape DECISION asks for.

    Only Anthropic's and strict schema answers are held to the schema by
    the provider; anything else is checked here before it is acted on."""
    try:
        decision = json.loads(text)
    except ValueError as e:
        raise Refused(f"the answer is not JSON: {e}") from None
    if not isinstance(decision, dict):
        raise Refused("the answer is not a JSON object")
    missing = [k for k in DECISION["required"] if not isinstance(decision.get(k), str)]
    if missing:
        raise Refused(f"the answer has no {', '.join(missing)}")
    if decision["verdict"] not in DECISION["properties"]["verdict"]["enum"]:
        raise Refused(f"the answer's verdict is {decision['verdict']!r}")
    return decision


class CommandModel:
    """Any other provider: a command the operator supplies.

    The command gets {"system", "schema", "messages"} as JSON on stdin and
    answers the decision as JSON on stdout. Messages hold text only.
    """

    def __init__(self, command: str):
        self.argv = shlex.split(command)

    def decide(self, messages: list) -> tuple[dict, list]:
        out = subprocess.run(
            self.argv, input=json.dumps({"system": SYSTEM, "schema": DECISION, "messages": messages}),
            capture_output=True, text=True, timeout=900, check=False,
        )
        if out.returncode != 0:
            raise Refused(f"model command exited {out.returncode}: {out.stderr.strip()[:300]}")
        text = out.stdout.strip()
        return checked(text), [{"role": "assistant", "content": text}]


# --- Triage


def log(*parts):
    print(time.strftime("%H:%M:%S"), *parts, flush=True)


class Triage:
    # How many times the model may fix a definition that does not validate.
    attempts = 2

    def __init__(self, brokoli: Brokoli, model, dry_run: bool = False, messages: bool = True):
        self.brokoli = brokoli
        self.model = model
        self.dry_run = dry_run
        self.messages = messages

    def handle(self, event: dict) -> dict:
        """Triage one event; answers what was done, for the log."""
        kind, run = event.get("type"), event.get("run") or {}
        pipeline = (event.get("pipeline") or {}).get("id")
        if event.get("test"):
            log(f"test event for {pipeline}: received; nothing to do")
            return {"outcome": "test"}
        if kind != "run.failed" or not run.get("id") or not pipeline:
            log(f"{kind} for {pipeline}: nothing to triage")
            return {"outcome": "ignored"}

        # A pipeline that keeps failing for the same reason fails on every
        # run until a person applies the fix: one proposal waiting is enough.
        waiting = self.waiting_proposal(pipeline, event.get("agent"))
        if waiting:
            log(f"run {run['id']} of {pipeline} failed; \"{waiting['title']}\" ({waiting['id']}) "
                "is still waiting for a person, so nothing more is proposed")
            return {"outcome": "waiting", "change_request": waiting["id"]}

        log(f"run {run['id']} of {pipeline} failed; asking why")
        explanation = self.brokoli.call("explain_run", run=run["id"])
        current = self.brokoli.call("get_pipeline", pipeline=pipeline)
        messages = [{"role": "user", "content": (
            "Brokoli's explanation of the failed run:\n\n"
            + json.dumps(explanation, indent=2)
            + "\n\nThe pipeline's definition:\n\n"
            + json.dumps(current, indent=2)
        )}]

        for attempt in range(1, self.attempts + 1):
            try:
                decision, reply = self.model.decide(messages)
            except Refused as e:
                log(f"run {run['id']}: no decision from the model: {e}")
                return {"outcome": "no-decision", "reason": str(e)}
            if decision["verdict"] != "fix":
                return self.report(pipeline, run["id"], decision)
            try:
                definition = json.loads(decision["definition"])
            except ValueError as e:
                problems = [f"the definition is not valid JSON: {e}"]
            else:
                checked = self.brokoli.call("validate_pipeline", definition=definition)
                problems = [] if checked.get("valid") else checked.get("errors") or ["not valid"]
            if not problems:
                return self.propose(pipeline, run["id"], decision, definition)
            log(f"run {run['id']}: the drafted fix does not validate (attempt {attempt}): {problems}")
            messages += reply + [{"role": "user", "content": (
                "Brokoli rejected that definition:\n\n" + json.dumps(problems, indent=2)
                + "\n\nCorrect it, or answer report if you cannot."
            )}]
        decision["description"] += "\n\nThe drafted fix did not validate, so nothing was proposed."
        return self.report(pipeline, run["id"], decision)

    def waiting_proposal(self, pipeline: str, agent: str | None) -> dict | None:
        """This agent's open change request for the pipeline, if one can
        still be applied (a stale one cannot: the pipeline changed since)."""
        if not agent:
            return None
        listed = self.brokoli.call("list_change_requests", pipeline=pipeline, status="open")
        for cr in listed.get("change_requests") or []:
            if cr.get("created_by_kind") == "agent" and cr.get("created_by_name") == agent and not cr.get("stale"):
                return cr
        return None

    def propose(self, pipeline: str, run_id: str, decision: dict, definition: dict) -> dict:
        description = decision["description"] + f"\n\n---\nProposed by the triage agent after run `{run_id}` failed."
        if self.dry_run:
            log(f"run {run_id}: would propose \"{decision['title']}\" (dry run)")
            return {"outcome": "would-propose", "title": decision["title"]}
        cr = self.brokoli.call("propose_change", pipeline=pipeline, definition=definition,
                               title=decision["title"], description=description)
        log(f"run {run_id}: proposed \"{decision['title']}\" as change request {cr.get('id')}")
        links = [{"kind": "run", "id": run_id}]
        if cr.get("id"):
            links.append({"kind": "change_request", "id": cr["id"]})
        self.tell(level="info", topic="I proposed a fix",
                  text=f"A run failed, and the cause is in the pipeline's definition. {decision['finding']}\n\n"
                       f"I proposed \"{decision['title']}\". It changes nothing until someone applies it.",
                  links=links)
        return {"outcome": "proposed", "change_request": cr.get("id"), "title": decision["title"]}

    def report(self, pipeline: str, run_id: str, decision: dict) -> dict:
        log(f"run {run_id}: not a definition problem; reporting. {decision['finding']}")
        log(decision["description"])
        self.tell(level="attention", topic="A failed run needs a person",
                  text=f"{decision['finding']}\n\n{decision['description']}",
                  links=[{"kind": "run", "id": run_id}, {"kind": "pipeline", "id": pipeline}])
        return {"outcome": "reported", "finding": decision["finding"]}

    def tell(self, **message):
        """Sends the agent's owner a message in Brokoli (ADR-048). A message
        that cannot be sent (an older server, a role without messages.send,
        a limit reached) is logged; the triage itself stands."""
        if self.dry_run or not self.messages:
            return
        try:
            self.brokoli.call("send_message", to="owner", **message)
        except BrokoliError as e:
            log(f"could not send the message: {e}")


# --- serve: receive webhooks


def serve(triage: Triage, secret: str, host: str, port: int):
    """Receives deliveries, answers at once, and triages them in order.

    Brokoli waits about 10 seconds for an answer and retries a delivery it
    did not get one for, so the work happens after the answer. A retried
    delivery has the same webhook-id; one seen before is acknowledged and
    not triaged again.
    """
    work: queue.Queue = queue.Queue()
    seen: collections.OrderedDict = collections.OrderedDict()

    class Handler(http.server.BaseHTTPRequestHandler):
        def do_POST(self):
            body = self.rfile.read(int(self.headers.get("Content-Length") or 0))
            if not verify_webhook(secret, self.headers, body):
                self.send_response(401)
                self.end_headers()
                return
            msg_id = self.headers["webhook-id"]
            fresh = msg_id not in seen
            if fresh:
                seen[msg_id] = True
                if len(seen) > 10000:
                    seen.popitem(last=False)
                work.put(json.loads(body))
            self.send_response(204)
            self.end_headers()

        def log_message(self, *args):
            pass

    def worker():
        while True:
            event = work.get()
            try:
                triage.handle(event)
            except BrokoliError as e:
                log(f"could not triage: {e}")

    threading.Thread(target=worker, daemon=True).start()
    log(f"listening on http://{host}:{port}")
    http.server.ThreadingHTTPServer((host, port), Handler).serve_forever()


# --- poll: read the feed


def poll(triage: Triage, once: bool, interval: float):
    """Reads the agent's feed, triages each event, then acknowledges it.

    An event is acknowledged only after it was triaged, so if the agent
    stops part way it reads the rest again when it starts. With once, it
    stops when the feed is empty: a scheduled job in CI.
    """
    cursor = ""
    while True:
        # Passing the cursor acknowledges every event up to it. One event
        # at a time, so each is acknowledged as soon as it is triaged.
        page = triage.brokoli.call("next_events", after=cursor, limit=1)
        events = page.get("events") or []
        if not events:
            if once:
                return 0
            time.sleep(interval)
            continue
        try:
            triage.handle(events[0])
        except BrokoliError as e:
            log(f"could not triage: {e}")
            return 1  # unacknowledged: it comes back on the next read
        cursor = page.get("cursor") or cursor


# --- main


DEFAULT_MODEL = "claude-opus-5-5"
ANTHROPIC_EFFORTS = ("low", "medium", "high", "xhigh", "max")


def make_model(p: argparse.ArgumentParser, args):
    # --model-command alone has always meant the command provider.
    provider = "command" if args.model_command and args.provider == "anthropic" else args.provider
    if provider == "command":
        if not args.model_command:
            p.error("--provider command needs --model-command")
        return CommandModel(args.model_command)
    if provider == "openai":
        if not args.model:
            p.error("--provider openai needs --model: the provider's name for it")
        return OpenAIModel(args.model, args.effort, args.base_url, args.api_key_env, args.response_format)
    effort = args.effort or "high"
    if effort not in ANTHROPIC_EFFORTS:
        p.error(f"--effort with anthropic is one of {', '.join(ANTHROPIC_EFFORTS)}")
    return AnthropicModel(args.model or DEFAULT_MODEL, effort)


def main(argv=None) -> int:
    p = argparse.ArgumentParser(description="A reference triage agent for Brokoli.")
    p.add_argument("--server", default=os.environ.get("BROKOLI_SERVER", "http://localhost:8080"))
    p.add_argument("--provider", default=os.environ.get("TRIAGE_PROVIDER", "anthropic"),
                   choices=["anthropic", "openai", "command"],
                   help="anthropic (default); openai for any OpenAI-compatible API; command for your own")
    p.add_argument("--model", default=os.environ.get("TRIAGE_MODEL"),
                   help=f"the model (default {DEFAULT_MODEL} with anthropic; required with openai)")
    p.add_argument("--effort", default=os.environ.get("TRIAGE_EFFORT"),
                   help="how hard the model thinks: anthropic takes low to max (default high); "
                        "openai sends it as reasoning_effort, for models that take one")
    p.add_argument("--base-url", default=os.environ.get("TRIAGE_BASE_URL"),
                   help="openai: where the API is, when it is not OpenAI's (Gemini, Ollama, OpenRouter, ...)")
    p.add_argument("--api-key-env", default=os.environ.get("TRIAGE_API_KEY_ENV", "OPENAI_API_KEY"),
                   help="openai: the variable that holds the API key (default %(default)s)")
    p.add_argument("--response-format", default=os.environ.get("TRIAGE_RESPONSE_FORMAT", "json_schema"),
                   choices=["json_schema", "json_object"],
                   help="openai: json_object for a service that does not support JSON schemas")
    p.add_argument("--model-command", default=os.environ.get("TRIAGE_MODEL_COMMAND"),
                   help="command: a command that reads the request on stdin and answers JSON")
    p.add_argument("--dry-run", action="store_true", help="decide, validate, but do not propose")
    p.add_argument("--no-messages", action="store_true",
                   help="only log findings; do not send them to the agent's owner in Brokoli")
    sub = p.add_subparsers(dest="mode", required=True)
    s = sub.add_parser("serve", help="receive webhook deliveries")
    s.add_argument("--host", default="127.0.0.1")
    s.add_argument("--port", type=int, default=8787)
    q = sub.add_parser("poll", help="read the agent's feed")
    q.add_argument("--once", action="store_true", help="stop when the feed is empty")
    q.add_argument("--interval", type=float, default=30)
    r = sub.add_parser("run", help="triage one failed run now")
    r.add_argument("run_id")
    r.add_argument("--pipeline", required=True)
    args = p.parse_args(argv)

    token = (os.environ.get("BROKOLI_TOKEN") or "").strip()
    if not token:
        p.error("set BROKOLI_TOKEN to the agent's token (brokoli agents token create)")
    if any(c.isspace() for c in token):
        p.error("BROKOLI_TOKEN holds more than a token: set it to the token alone")
    brokoli = Brokoli(args.server, token)
    model = make_model(p, args)
    triage = Triage(brokoli, model, dry_run=args.dry_run, messages=not args.no_messages)

    if args.mode == "serve":
        secret = os.environ.get("TRIAGE_WEBHOOK_SECRET")
        if not secret:
            p.error("set TRIAGE_WEBHOOK_SECRET to the trigger's signing secret")
        serve(triage, secret, args.host, args.port)
        return 0
    if args.mode == "poll":
        return poll(triage, args.once, args.interval)
    outcome = triage.handle({"type": "run.failed", "run": {"id": args.run_id}, "pipeline": {"id": args.pipeline}})
    return 0 if outcome["outcome"] in ("proposed", "would-propose", "reported") else 1


if __name__ == "__main__":
    sys.exit(main())
