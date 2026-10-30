"""The door the release captain calls.

n8n's HTTP histogram counts the REST API and skips the webhook, so the time
the captain actually waits is recorded here and scraped by Prometheus.
"""

import json
import os
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.error import HTTPError, URLError
from urllib.request import Request, urlopen

UPSTREAM = os.environ.get(
    "GATE_UPSTREAM",
    "http://n8n:5678/webhook/V7fO3C79dIcOqxrA/webhook/release-gate",
)
BUCKETS = (1, 5, 15, 30, 60, 120, 300)
LOCK = threading.Lock()
COUNTS = {bound: 0 for bound in BUCKETS}
INF = 0
TOTAL = 0
SUM = 0.0


def observe(seconds: float) -> None:
    global INF, TOTAL, SUM
    with LOCK:
        TOTAL += 1
        SUM += seconds
        INF += 1
        for bound in BUCKETS:
            if seconds <= bound:
                COUNTS[bound] += 1


def metrics() -> bytes:
    lines = [
        "# HELP release_gate_duration_seconds Time the captain waits for a verdict.",
        "# TYPE release_gate_duration_seconds histogram",
    ]
    with LOCK:
        for bound in BUCKETS:
            lines.append(f'release_gate_duration_seconds_bucket{{le="{bound}"}} {COUNTS[bound]}')
        lines.append(f'release_gate_duration_seconds_bucket{{le="+Inf"}} {INF}')
        lines.append(f"release_gate_duration_seconds_sum {SUM}")
        lines.append(f"release_gate_duration_seconds_count {TOTAL}")
    return ("\n".join(lines) + "\n").encode()


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, fmt, *args):
        print(fmt % args, flush=True)

    def do_GET(self):
        if self.path.split("?", 1)[0] != "/metrics":
            self._send(404, b"not found", "text/plain")
            return
        self._send(200, metrics(), "text/plain; version=0.0.4")

    def do_POST(self):
        length = int(self.headers.get("Content-Length", "0"))
        payload = self.rfile.read(length) if length else b""
        started = time.monotonic()
        status = 502
        body = b""
        content_type = "application/json"
        try:
            request = Request(
                UPSTREAM,
                data=payload,
                method="POST",
                headers={"content-type": self.headers.get("Content-Type", "application/json")},
            )
            with urlopen(request, timeout=180) as response:
                status = response.status
                body = response.read()
                content_type = response.headers.get("content-type", content_type)
        except HTTPError as exc:
            status = exc.code
            body = exc.read()
        except URLError as exc:
            body = json.dumps({"error": str(exc.reason)}).encode()
        finally:
            observe(time.monotonic() - started)
        self._send(status, body, content_type)

    def _send(self, status: int, body: bytes, content_type: str) -> None:
        self.send_response(status)
        self.send_header("content-type", content_type)
        self.send_header("content-length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)


if __name__ == "__main__":
    ThreadingHTTPServer(("0.0.0.0", 5680), Handler).serve_forever()
