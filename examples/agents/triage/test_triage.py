"""Tests for the reference triage agent: python3 -m unittest test_triage.py

They need no server and no model: Brokoli and the model are stand-ins.
The Anthropic test runs only when the anthropic package is installed; it
checks the request the SDK sends, against a local server.
"""

import base64
import hashlib
import hmac
import http.server
import json
import os
import threading
import time
import unittest

import triage

PIPELINE = {"id": "orders", "nodes": [{"id": "fetch", "type": "source_file", "config": {"path": "/data/order.csv"}}], "edges": []}
FIXED = {**PIPELINE, "nodes": [{"id": "fetch", "type": "source_file", "config": {"path": "/data/orders.csv"}}]}
FAILED = {"type": "run.failed", "agent": "triage-bot", "pipeline": {"id": "orders"}, "run": {"id": "r1", "status": "failed"}}


class FakeBrokoli:
    def __init__(self, valid=lambda d: d == FIXED, open_requests=()):
        self.calls = []
        self.valid = valid
        self.open_requests = list(open_requests)

    def call(self, tool, **args):
        self.calls.append((tool, args))
        if tool == "list_change_requests":
            return {"change_requests": self.open_requests}
        if tool == "explain_run":
            return {"summary": "fetch failed", "failures": [{"node": "fetch", "error": "open /data/order.csv: no such file"}]}
        if tool == "get_pipeline":
            return PIPELINE
        if tool == "validate_pipeline":
            ok = self.valid(args["definition"])
            return {"valid": ok, "errors": [] if ok else ["node fetch: path is wrong"]}
        if tool == "propose_change":
            return {"id": "cr1"}
        raise AssertionError(tool)

    def tools(self):
        return [t for t, _ in self.calls]


class ScriptedModel:
    def __init__(self, *answers):
        self.answers = list(answers)
        self.seen = []

    def decide(self, messages):
        self.seen.append(list(messages))
        answer = self.answers.pop(0)
        if isinstance(answer, Exception):
            raise answer
        return answer, [{"role": "assistant", "content": json.dumps(answer)}]


def fix(definition):
    return {"verdict": "fix", "finding": "wrong path", "title": "Read orders.csv",
            "description": "## Why\nThe path was wrong.", "definition": json.dumps(definition)}


REPORT = {"verdict": "report", "finding": "the source was down", "title": "", "description": "Check the source.", "definition": ""}


def quiet(t):
    t.addCleanup(setattr, triage, "log", triage.log)
    triage.log = lambda *a: None


class TriageTest(unittest.TestCase):
    def setUp(self):
        quiet(self)

    def test_a_fix_is_validated_then_proposed(self):
        b = FakeBrokoli()
        out = triage.Triage(b, ScriptedModel(fix(FIXED))).handle(FAILED)
        self.assertEqual(out["outcome"], "proposed")
        self.assertEqual(b.tools(), ["list_change_requests", "explain_run", "get_pipeline", "validate_pipeline", "propose_change"])
        proposed = b.calls[-1][1]
        self.assertEqual(proposed["definition"], FIXED)
        self.assertEqual(proposed["pipeline"], "orders")
        self.assertIn("run `r1`", proposed["description"])

    def test_a_cause_outside_the_definition_is_reported(self):
        b = FakeBrokoli()
        out = triage.Triage(b, ScriptedModel(REPORT)).handle(FAILED)
        self.assertEqual(out["outcome"], "reported")
        self.assertNotIn("propose_change", b.tools())

    def test_a_fix_that_does_not_validate_is_retried_with_the_errors(self):
        b = FakeBrokoli()
        model = ScriptedModel(fix(PIPELINE), fix(FIXED))
        out = triage.Triage(b, model).handle(FAILED)
        self.assertEqual(out["outcome"], "proposed")
        retry = model.seen[1]
        self.assertEqual([m["role"] for m in retry], ["user", "assistant", "user"])
        self.assertIn("path is wrong", retry[-1]["content"])

    def test_a_fix_that_never_validates_is_reported_not_proposed(self):
        b = FakeBrokoli(valid=lambda d: False)
        out = triage.Triage(b, ScriptedModel(fix(FIXED), fix(FIXED))).handle(FAILED)
        self.assertEqual(out["outcome"], "reported")
        self.assertNotIn("propose_change", b.tools())

    def test_a_definition_that_is_not_json_is_sent_back(self):
        b = FakeBrokoli()
        broken = {**fix(FIXED), "definition": "{not json"}
        model = ScriptedModel(broken, fix(FIXED))
        self.assertEqual(triage.Triage(b, model).handle(FAILED)["outcome"], "proposed")
        self.assertIn("not valid JSON", model.seen[1][-1]["content"])

    def test_a_dry_run_proposes_nothing(self):
        b = FakeBrokoli()
        out = triage.Triage(b, ScriptedModel(fix(FIXED)), dry_run=True).handle(FAILED)
        self.assertEqual(out["outcome"], "would-propose")
        self.assertNotIn("propose_change", b.tools())

    def test_no_decision_from_the_model_proposes_nothing(self):
        b = FakeBrokoli()
        out = triage.Triage(b, ScriptedModel(triage.Refused("declined"))).handle(FAILED)
        self.assertEqual(out["outcome"], "no-decision")
        self.assertNotIn("propose_change", b.tools())

    def test_a_waiting_proposal_is_not_proposed_again(self):
        mine = {"id": "cr0", "title": "Read orders.csv", "created_by_kind": "agent", "created_by_name": "triage-bot", "stale": False}
        b = FakeBrokoli(open_requests=[mine])
        out = triage.Triage(b, ScriptedModel()).handle(FAILED)
        self.assertEqual(out, {"outcome": "waiting", "change_request": "cr0"})
        self.assertEqual(b.tools(), ["list_change_requests"])
        # Someone else's request, or a stale one of ours, does not hold it back.
        for other in ({**mine, "created_by_name": "someone"}, {**mine, "created_by_kind": "person"}, {**mine, "stale": True}):
            b = FakeBrokoli(open_requests=[other])
            self.assertEqual(triage.Triage(b, ScriptedModel(fix(FIXED))).handle(FAILED)["outcome"], "proposed", other)

    def test_test_events_and_other_events_are_not_triaged(self):
        b = FakeBrokoli()
        t = triage.Triage(b, ScriptedModel())
        self.assertEqual(t.handle({**FAILED, "test": True})["outcome"], "test")
        self.assertEqual(t.handle({**FAILED, "type": "run.recovered"})["outcome"], "ignored")
        self.assertEqual(b.calls, [])


class FeedTest(unittest.TestCase):
    def setUp(self):
        quiet(self)

    def test_each_event_is_acknowledged_after_it_is_triaged(self):
        events = [{**FAILED, "run": {"id": f"r{i}"}} for i in range(3)]
        reads = []

        class Feed(FakeBrokoli):
            def call(self, tool, **args):
                if tool != "next_events":
                    return super().call(tool, **args)
                reads.append(args["after"])
                if len(reads) > 10:
                    raise AssertionError(f"the feed was read {len(reads)} times: {reads}")
                done = int(args["after"] or 0)
                return {"events": events[done:done + args["limit"]], "cursor": str(done + 1)}

        handled = []
        t = triage.Triage(Feed(), ScriptedModel())
        t.handle = lambda e: handled.append(e["run"]["id"])
        self.assertEqual(triage.poll(t, once=True, interval=0), 0)
        self.assertEqual(handled, ["r0", "r1", "r2"])
        # Each read acknowledges exactly the event triaged before it.
        self.assertEqual(reads, ["", "1", "2", "3"])

    def test_an_event_that_could_not_be_triaged_is_not_acknowledged(self):
        reads = []

        class Feed(FakeBrokoli):
            def call(self, tool, **args):
                if tool == "next_events":
                    reads.append(args["after"])
                    if len(reads) > 10:
                        raise AssertionError(f"the feed was read {len(reads)} times")
                    return {"events": [FAILED], "cursor": "c1"}
                raise triage.BrokoliError("down")

        self.assertEqual(triage.poll(triage.Triage(Feed(), ScriptedModel()), once=True, interval=0), 1)
        self.assertEqual(reads, [""])


def sign(secret, msg_id, ts, body):
    key = base64.b64decode(secret.removeprefix("whsec_"))
    return "v1," + base64.b64encode(hmac.new(key, f"{msg_id}.{ts}.".encode() + body, hashlib.sha256).digest()).decode()


class SignatureTest(unittest.TestCase):
    secret = "whsec_" + base64.b64encode(b"k" * 32).decode()
    old = "whsec_" + base64.b64encode(b"o" * 32).decode()

    def headers(self, sig, ts=None):
        return {"webhook-id": "msg_1", "webhook-timestamp": str(ts or int(time.time())), "webhook-signature": sig}

    def test_a_signed_body_verifies(self):
        body, ts = b'{"type":"run.failed"}', int(time.time())
        self.assertTrue(triage.verify_webhook(self.secret, self.headers(sign(self.secret, "msg_1", ts, body), ts), body))

    def test_either_secret_verifies_during_a_rotation(self):
        body, ts = b"{}", int(time.time())
        both = sign(self.old, "msg_1", ts, body) + " " + sign(self.secret, "msg_1", ts, body)
        self.assertTrue(triage.verify_webhook(self.secret, self.headers(both, ts), body))
        self.assertTrue(triage.verify_webhook(self.old, self.headers(both, ts), body))

    def test_what_must_not_verify(self):
        body, ts = b"{}", int(time.time())
        good = sign(self.secret, "msg_1", ts, body)
        self.assertFalse(triage.verify_webhook(self.secret, self.headers(good, ts), b"{ }"), "a changed body")
        self.assertFalse(triage.verify_webhook(self.old, self.headers(good, ts), body), "another secret")
        stale = ts - 600
        self.assertFalse(triage.verify_webhook(self.secret, self.headers(sign(self.secret, "msg_1", stale, body), stale), body), "old")
        self.assertFalse(triage.verify_webhook(self.secret, self.headers("v2," + good[3:], ts), body), "another scheme")
        self.assertFalse(triage.verify_webhook(self.secret, {}, body), "no headers")


try:
    import anthropic

    SDK = int(anthropic.__version__.split(".")[0]) >= 2
except ImportError:
    SDK = False


@unittest.skipUnless(SDK, "anthropic from requirements.txt is not installed")
class AnthropicRequestTest(unittest.TestCase):
    """What the default provider sends, caught by a local server."""

    def setUp(self):
        self.requests = []
        answer = json.dumps(fix(FIXED))
        outer = self

        class Handler(http.server.BaseHTTPRequestHandler):
            def do_POST(self):
                body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
                outer.requests.append((dict(self.headers), body))
                msg = {"id": "msg_1", "type": "message", "role": "assistant", "model": body["model"],
                       "content": [], "stop_reason": None, "stop_sequence": None,
                       "usage": {"input_tokens": 1, "output_tokens": 1}}
                events = [
                    ("message_start", {"type": "message_start", "message": msg}),
                    ("content_block_start", {"type": "content_block_start", "index": 0, "content_block": {"type": "text", "text": ""}}),
                    ("content_block_delta", {"type": "content_block_delta", "index": 0, "delta": {"type": "text_delta", "text": answer}}),
                    ("content_block_stop", {"type": "content_block_stop", "index": 0}),
                    ("message_delta", {"type": "message_delta", "delta": {"stop_reason": "end_turn", "stop_sequence": None}, "usage": {"output_tokens": 5}}),
                    ("message_stop", {"type": "message_stop"}),
                ]
                self.send_response(200)
                self.send_header("Content-Type", "text/event-stream")
                self.end_headers()
                for name, data in events:
                    self.wfile.write(f"event: {name}\ndata: {json.dumps(data)}\n\n".encode())

            def log_message(self, *a):
                pass

        self.srv = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        threading.Thread(target=self.srv.serve_forever, daemon=True).start()
        self.addCleanup(self.srv.shutdown)
        env = {"ANTHROPIC_BASE_URL": f"http://127.0.0.1:{self.srv.server_port}", "ANTHROPIC_API_KEY": "test"}
        for k, v in env.items():
            self.addCleanup(os.environ.__setitem__, k, os.environ[k]) if k in os.environ else self.addCleanup(os.environ.pop, k, None)
            os.environ[k] = v

    def test_the_request_asks_for_the_decision_with_a_fallback(self):
        decision, reply = triage.AnthropicModel("claude-opus-5-5", "high").decide([{"role": "user", "content": "x"}])
        self.assertEqual(decision["verdict"], "fix")
        self.assertEqual(reply[0]["role"], "assistant")
        headers, body = self.requests[0]
        self.assertEqual(body["model"], "claude-opus-5-5")
        self.assertEqual(body["thinking"], {"type": "adaptive"})
        self.assertEqual(body["output_config"]["effort"], "high")
        self.assertEqual(body["output_config"]["format"], {"type": "json_schema", "schema": triage.DECISION})
        self.assertEqual(body["fallbacks"], "default")
        self.assertIn("server-side-fallback-2026-07-01", headers.get("anthropic-beta", ""))
        self.assertTrue(body["stream"])
        self.assertNotIn("temperature", body)


if __name__ == "__main__":
    unittest.main()
