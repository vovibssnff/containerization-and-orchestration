"""One-tool adapter in front of the local Ollama.

qwen2.5:3b writes tool calls as <tool_call> blocks, and it often drops a quote
(`"repo: "release"` instead of `"repo": "release"`). Ollama then discards the
whole message. n8n also asks for a stream and merges every call onto index 0,
so a batch of calls becomes one broken call. This adapter asks the model for
plain text, repairs the first block, and returns that single call.
"""

import json
import re
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.request import Request, urlopen

UPSTREAM = "http://ollama:11434"
BLOCK = re.compile(r"<tool_call>\s*(.*?)\s*</tool_call>", re.S)
BROKEN_KEY = re.compile(r'"([A-Za-z0-9_]+):\s+"')


def repair(raw: str):
    fixed = BROKEN_KEY.sub(r'"\1": "', raw)
    return json.loads(fixed)


def as_call(value):
    if not isinstance(value, dict):
        return None
    name = value.get("name")
    arguments = value.get("arguments")
    if isinstance(name, str) and isinstance(arguments, dict):
        return {"name": name, "arguments": arguments}
    return None


def first_call(content: str):
    text = content or ""
    for raw in BLOCK.findall(text):
        try:
            found = as_call(repair(raw))
        except json.JSONDecodeError:
            found = None
        if found:
            return found
    decoder = json.JSONDecoder()
    start = 0
    while True:
        brace = text.find("{", start)
        if brace < 0:
            return None
        try:
            value, _ = decoder.raw_decode(BROKEN_KEY.sub(r'"\1": "', text[brace:]))
        except json.JSONDecodeError:
            start = brace + 1
            continue
        found = as_call(value)
        if found:
            return found
        start = brace + 1


def catalog(tools) -> str:
    lines = []
    for tool in tools or []:
        function = tool.get("function", tool)
        lines.append(json.dumps({"type": "function", "function": function}, ensure_ascii=False))
    joined = "\n".join(lines)
    return (
        "\n\n# Tools\n"
        "Call exactly one tool, then stop. Do not call a second tool in the same reply.\n"
        "The whole reply is one JSON object with keys name and arguments. No other text.\n"
        "Return it inside <tool_call></tool_call>:\n"
        "<tool_call>\n"
        '{"name": "<function-name>", "arguments": {<args>}}\n'
        "</tool_call>\n"
        f"<tools>\n{joined}\n</tools>\n"
    )


def plain_tool_text(content):
    """n8n hands the model a JSON list of MCP blocks. The small model copies it back."""
    if isinstance(content, list):
        blocks = content
    elif isinstance(content, str) and content.startswith("["):
        try:
            blocks = json.loads(content)
        except json.JSONDecodeError:
            return content
    else:
        return content
    if isinstance(blocks, list) and blocks and all(isinstance(b, dict) and "text" in b for b in blocks):
        return "\n".join(b["text"] for b in blocks)
    return content


def rewrite(body: dict) -> dict:
    tools = body.pop("tools", None)
    body["stream"] = False
    options = dict(body.get("options") or {})
    options["temperature"] = 0
    options.setdefault("num_predict", 400)
    body["options"] = options
    messages = body.get("messages") or []
    for message in messages:
        if message.get("role") == "tool":
            message["content"] = plain_tool_text(message.get("content"))
    body["messages"] = messages
    if not tools:
        return body
    note = catalog(tools)
    if messages and messages[0].get("role") == "system":
        messages[0]["content"] = (messages[0].get("content") or "") + note
    else:
        messages.insert(0, {"role": "system", "content": note.strip()})
    body["messages"] = messages
    return body


def request_fields(messages) -> dict:
    fields = {"service": "order-api", "version": ""}
    for message in messages:
        if message.get("role") != "user":
            continue
        for line in (message.get("content") or "").splitlines():
            if line.startswith("Service:"):
                fields["service"] = line.split(":", 1)[1].strip()
            elif line.startswith("Version:"):
                fields["version"] = line.split(":", 1)[1].strip()
    return fields


def next_step(messages) -> dict | None:
    """The 3B model skips ahead. Hold it to the skill until the five reads are done."""
    done = sum(1 for message in messages if message.get("role") == "tool")
    fields = request_fields(messages)
    steps = [
        {"name": "list_release_files", "arguments": {"repo": "release"}},
        {"name": "read_release_file", "arguments": {"name": "CHANGELOG.md"}},
        {"name": "read_release_file", "arguments": {"name": "FREEZE.md"}},
        {"name": "list_open_incidents", "arguments": {"service": fields["service"]}},
        {"name": "release_freeze", "arguments": {"board": "oncall"}},
    ]
    if done < len(steps):
        return steps[done]
    return None


def upstream(method: str, path: str, payload: bytes | None, headers: dict):
    request = Request(UPSTREAM + path, data=payload, method=method, headers=headers)
    with urlopen(request, timeout=180) as response:
        return response.status, response.read(), response.headers.get("content-type", "application/json")


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, fmt, *args):
        print(fmt % args, flush=True)

    def do_GET(self):
        self._pass()

    def do_HEAD(self):
        self._pass()

    def do_POST(self):
        length = int(self.headers.get("Content-Length", "0"))
        payload = self.rfile.read(length) if length else b""
        if self.path.split("?", 1)[0] != "/api/chat":
            self._pass(payload)
            return
        self._chat(payload)

    def _chat(self, payload: bytes):
        incoming = json.loads(payload or b"{}")
        want_stream = incoming.get("stream", True)
        rewritten = rewrite(dict(incoming))
        try:
            status, raw, _ = upstream(
                "POST",
                "/api/chat",
                json.dumps(rewritten).encode(),
                {"content-type": "application/json"},
            )
        except Exception as exc:
            message = json.dumps({"error": str(exc)}).encode()
            self.send_response(504)
            self.send_header("content-type", "application/json")
            self.send_header("content-length", str(len(message)))
            self.end_headers()
            self.wfile.write(message)
            return
        data = json.loads(raw)
        message = data.setdefault("message", {"role": "assistant", "content": ""})
        content = message.get("content") or ""
        call = first_call(content)
        pending = next_step(rewritten.get("messages") or [])
        if pending is not None and (call is None or call["name"] != pending["name"]):
            print(f"hold {pending['name']} (model said {None if call is None else call['name']})", flush=True)
            call = pending
        elif call is None and "changelog:" in content.lower():
            version = request_fields(rewritten.get("messages") or [])["version"]
            call = {"name": "write_verdict", "arguments": {"version": version, "markdown": content}}
        if call is not None:
            message["content"] = ""
            message["tool_calls"] = [
                {"id": "call_release_gate", "function": {"index": 0, **call}}
            ]
            print(f"tool {call['name']}", flush=True)
        else:
            message.pop("tool_calls", None)
            snippet = (message.get("content") or "").replace("\n", " ")[:180]
            print(f"no tool {snippet}", flush=True)
        data["done"] = True
        body = json.dumps(data).encode()
        if want_stream:
            done = dict(data)
            done["message"] = {"role": "assistant", "content": ""}
            done["done"] = True
            first = dict(data)
            first["done"] = False
            body = (json.dumps(first) + "\n" + json.dumps(done) + "\n").encode()
        self.send_response(status)
        self.send_header("content-type", "application/x-ndjson" if want_stream else "application/json")
        self.send_header("content-length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def _pass(self, payload: bytes | None = None):
        headers = {}
        if payload is not None:
            headers["content-type"] = self.headers.get("content-type", "application/json")
        status, raw, content_type = upstream(self.command, self.path, payload, headers)
        self.send_response(status)
        self.send_header("content-type", content_type)
        self.send_header("content-length", str(len(raw)))
        self.end_headers()
        if self.command != "HEAD":
            self.wfile.write(raw)


if __name__ == "__main__":
    ThreadingHTTPServer(("0.0.0.0", 11434), Handler).serve_forever()
