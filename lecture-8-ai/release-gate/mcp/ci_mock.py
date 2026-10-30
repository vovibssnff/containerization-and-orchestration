"""Plain CI status. Not an MCP server and not an AI step: a service the workflow calls over HTTP."""

import json
from http.server import BaseHTTPRequestHandler, HTTPServer


class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        if not self.path.startswith("/status"):
            self.send_response(404)
            self.end_headers()
            return
        body = json.dumps(
            {
                "service": "order-api",
                "pipeline": "green",
                "tests": "142 passed",
                "commit": "b7e1c0a",
            }
        ).encode()
        self.send_response(200)
        self.send_header("content-type", "application/json")
        self.send_header("content-length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, fmt, *args):
        print(fmt % args)


if __name__ == "__main__":
    HTTPServer(("0.0.0.0", 8080), Handler).serve_forever()
