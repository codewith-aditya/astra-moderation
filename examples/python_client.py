#!/usr/bin/env python3
"""Minimal stdlib client for astra-moderation (images + OpenAI-compatible mode)."""
import base64
import json
import mimetypes
import urllib.request


class ModerationClient:
    def __init__(self, base="http://localhost:8096", api_key="", timeout=180):
        self.base = base.rstrip("/")
        self.api_key = api_key
        self.timeout = timeout

    def _post(self, path, body):
        req = urllib.request.Request(
            self.base + path,
            data=json.dumps(body).encode(),
            headers={"Content-Type": "application/json", "X-API-Key": self.api_key},
            method="POST",
        )
        with urllib.request.urlopen(req, timeout=self.timeout) as resp:
            return json.loads(resp.read())

    def moderate_image(self, path):
        mime = mimetypes.guess_type(path)[0] or "image/jpeg"
        with open(path, "rb") as fh:
            data = base64.b64encode(fh.read()).decode()
        return self._post("/v1/moderate", {"image_url": f"data:{mime};base64,{data}"})

    def moderate_url(self, url):
        return self._post("/v1/moderate", {"image_url": url})

    def openai(self, inputs):
        return self._post("/v1/moderations", {"input": inputs})

    # convenience: True when the media should be blocked
    def is_blocked(self, path):
        return self.moderate_image(path)["verdict"] == "block"


if __name__ == "__main__":
    import sys

    if len(sys.argv) < 3:
        print("usage: python_client.py <api-key> <image | url>")
        raise SystemExit(1)

    client = ModerationClient(api_key=sys.argv[1])
    target = sys.argv[2]
    result = client.moderate_url(target) if target.startswith("http") else client.moderate_image(target)
    print(json.dumps({
        "verdict": result["verdict"],
        "flagged": result["flagged"],
        "reason": result.get("reason"),
    }, indent=2))
