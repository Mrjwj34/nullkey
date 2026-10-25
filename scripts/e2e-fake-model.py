#!/usr/bin/env python3
"""A loopback, authentication-required fake for the Codex Responses API.

Used by scripts/e2e-codex-auth.sh. It logs every request and only answers when
the gateway injected the expected bearer token. The model catalog is copied from
the host's Codex cache so a real Codex TUI can start; run Codex once on the host
if ~/.codex/models_cache.json is missing.
"""
import argparse
import json
import os
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import urlparse

TOKEN = "fake-oauth-token"


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, fmt, *args):
        pass

    def _log(self, line):
        with open(ARGS.log, "a") as f:
            f.write(line + "\n")

    def _auth_ok(self):
        got = self.headers.get("Authorization", "")
        self._log(
            f"{self.command} {self.path} auth={got} "
            f"account={self.headers.get('ChatGPT-Account-Id', '')}"
        )
        if got != f"Bearer {TOKEN}":
            body = json.dumps({"error": {"message": "bad token"}}).encode()
            self.send_response(401)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
            return False
        return True

    def _json(self, status, payload):
        body = json.dumps(payload).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        if not self._auth_ok():
            return
        if urlparse(self.path).path.endswith("/models"):
            try:
                with open(ARGS.models) as f:
                    models = json.load(f).get("models", [])
            except Exception:
                models = []
            self._json(200, {"models": models})
            return
        self._json(404, {"error": {"message": "not found"}})

    def do_POST(self):
        length = int(self.headers.get("Content-Length", "0"))
        raw = self.rfile.read(length) if length else b""
        try:
            payload = json.loads(raw)
            self._log(
                f"BODY model={payload.get('model')} stream={payload.get('stream')} "
                f"input_items={len(payload.get('input', []))}"
            )
        except Exception:
            self._log(f"BODY non-json bytes={len(raw)}")
        if not self._auth_ok():
            return
        if not urlparse(self.path).path.endswith("/responses"):
            self._json(404, {"error": {"message": "not found"}})
            return

        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Cache-Control", "no-cache")
        self.send_header("Connection", "close")
        self.end_headers()

        def event(name, data):
            self.wfile.write(
                f"event: {name}\ndata: {json.dumps(data)}\n\n".encode()
            )
            self.wfile.flush()

        item = {
            "id": "msg_fake",
            "type": "message",
            "role": "assistant",
            "status": "completed",
            "content": [
                {
                    "type": "output_text",
                    "text": "pong from fake upstream",
                    "annotations": [],
                }
            ],
        }
        event(
            "response.created",
            {
                "type": "response.created",
                "response": {
                    "id": "resp_fake",
                    "object": "response",
                    "created_at": int(time.time()),
                    "status": "in_progress",
                    "model": "gpt-5",
                    "output": [],
                },
            },
        )
        event(
            "response.output_item.done",
            {"type": "response.output_item.done", "output_index": 0, "item": item},
        )
        event(
            "response.completed",
            {
                "type": "response.completed",
                "response": {
                    "id": "resp_fake",
                    "object": "response",
                    "created_at": int(time.time()),
                    "status": "completed",
                    "model": "gpt-5",
                    "output": [item],
                    "usage": {
                        "input_tokens": 2,
                        "output_tokens": 4,
                        "total_tokens": 6,
                    },
                },
            },
        )


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--port", type=int, required=True)
    parser.add_argument("--log", required=True)
    parser.add_argument(
        "--models",
        default=os.path.expanduser("~/.codex/models_cache.json"),
    )
    ARGS = parser.parse_args()
    open(ARGS.log, "w").close()
    server = ThreadingHTTPServer(("127.0.0.1", ARGS.port), Handler)
    with open(ARGS.log, "a") as f:
        f.write(f"LISTEN {ARGS.port}\n")
    server.serve_forever()
