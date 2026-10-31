#!/usr/bin/env python3
"""Webhook receiver that verifies astra-moderation HMAC signatures.

Run:  MOD_WEBHOOK_SECRET=<secret-from-job-creation> python3 webhook_receiver.py
Then submit a job with webhook_url=http://<your-host>:8787/hook
"""
import hashlib
import hmac
import json
import os
from http.server import BaseHTTPRequestHandler, HTTPServer

SECRET = os.environ.get("MOD_WEBHOOK_SECRET", "").encode()


class Handler(BaseHTTPRequestHandler):
    def do_POST(self):  # noqa: N802
        length = int(self.headers.get("Content-Length", 0))
        body = self.rfile.read(length)
        signature = self.headers.get("X-Astra-Signature", "")
        expected = "sha256=" + hmac.new(SECRET, body, hashlib.sha256).hexdigest()
        valid = hmac.compare_digest(signature, expected)

        print(f"signature valid={valid}")
        if valid:
            payload = json.loads(body)
            print(json.dumps({k: payload.get(k) for k in ("id", "status", "verdict", "error")}, indent=2))

        self.send_response(200 if valid else 401)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(b'{"ok":true}')

    def log_message(self, *args):  # silence default logging
        pass


if __name__ == "__main__":
    print("listening on :8787")
    HTTPServer(("0.0.0.0", 8787), Handler).serve_forever()
