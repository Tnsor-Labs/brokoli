#!/usr/bin/env python3
"""Sample interpreted plugin (ADR-016): speaks the JSONL protocol via a
vendored dependency, proving the package format carries more than a
single file. Kept dependency-free beyond the vendored module so the test
only needs a python3 on PATH."""
import json
import os
import sys

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "vendored"))
import greeting  # vendored

MANIFEST = {
    "protocol_version": 1,
    "name": "hello-py",
    "version": "0.1.0",
    "description": "Python sample plugin with a vendored dependency",
    "binary": "main.py",
    "node_types": [
        {"type": "source_hello_py", "kind": "source", "display_name": "Hello Py Source"}
    ],
}


def main():
    cmd = sys.argv[-1] if len(sys.argv) > 1 else ""
    if cmd == "spec":
        print(json.dumps(MANIFEST))
        return
    if cmd == "check":
        print(json.dumps({"type": "status", "status": "ok"}))
        return
    if cmd == "discover":
        print(json.dumps({"type": "stream", "stream": {"name": "greetings", "mode": "full"}}))
        return
    if cmd == "read":
        for i, name in enumerate(["alice", "bob"]):
            print(json.dumps({"type": "record", "data": {"id": i, "message": greeting.greet(name)}}))
        print(json.dumps({"type": "state", "value": {"cursor": 2}}))
        return
    print(json.dumps({"type": "error", "message": f"unknown command {cmd!r}"}))
    sys.exit(1)


if __name__ == "__main__":
    main()
