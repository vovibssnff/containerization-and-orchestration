#!/usr/bin/env python3
"""Create the Ollama credential and the release-gate workflow in a local n8n."""

import json
import subprocess
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
SKILL = (ROOT / "skills" / "release-gate.md").read_text()
BASE = "http://127.0.0.1:5678"


def api(method, path, body=None):
    cmd = [
        "curl", "-sS", "-m", "30", "-b", "/tmp/n8n-cookie",
        "-X", method, "-H", "content-type: application/json",
    ]
    if body is not None:
        cmd += ["-d", json.dumps(body)]
    cmd.append(BASE + path)
    return json.loads(subprocess.check_output(cmd))


def node(name, type_, version, parameters, x, y, credentials=None):
    item = {
        "id": name.lower().replace(" ", "-"),
        "name": name,
        "type": type_,
        "typeVersion": version,
        "position": [x, y],
        "parameters": parameters,
    }
    if credentials:
        item["credentials"] = credentials
    return item


existing = api("GET", "/rest/credentials")
cred_id = next(
    (item["id"] for item in existing["data"] if item["name"] == "Local Ollama"),
    None,
)
ollama_data = {"baseUrl": "http://ollama-shim:11434"}
if cred_id is None:
    cred = api(
        "POST",
        "/rest/credentials",
        {
            "name": "Local Ollama",
            "type": "ollamaApi",
            "data": ollama_data,
        },
    )
    cred_id = cred["data"]["id"]
else:
    api(
        "PATCH",
        f"/rest/credentials/{cred_id}",
        {"name": "Local Ollama", "type": "ollamaApi", "data": ollama_data},
    )
ollama_cred = {"ollamaApi": {"id": cred_id, "name": "Local Ollama"}}

validate = r"""
const body = $json.body ?? $json;
if (!body.version || !body.service) {
  throw new Error('version and service are required');
}
return [{ json: { version: String(body.version), service: String(body.service) } }];
""".strip()

build = r"""
const req = $('Validate').first().json;
const ci = $json;
const prompt = [
  `Service: ${req.service}`,
  `Version: ${req.version}`,
  `CI status: ${JSON.stringify(ci)}`,
  'Follow the skill. Write the verdict file. Then stop.',
].join('\n');
return [{ json: { prompt, version: req.version, service: req.service } }];
""".strip()

read_verdict = r"""
const fs = require('fs');
const version = $('Build prompt').first().json.version;
const path = `/data/out/release-${version}.md`;
if (!fs.existsSync(path)) {
  throw new Error('verdict file was not written');
}
return [{ json: { path, text: fs.readFileSync(path, 'utf8') } }];
""".strip()

nodes = [
    node(
        "Webhook",
        "n8n-nodes-base.webhook",
        2.1,
        {
            "httpMethod": "POST",
            "path": "release-gate",
            "responseMode": "responseNode",
            "options": {},
        },
        0,
        300,
    ),
    node(
        "Validate",
        "n8n-nodes-base.code",
        2,
        {"language": "javaScript", "mode": "runOnceForAllItems", "jsCode": validate},
        260,
        300,
    ),
    node(
        "CI status",
        "n8n-nodes-base.httpRequest",
        4.2,
        {
            "method": "GET",
            "url": "=http://ci-mock:8080/status?service={{ $json.service }}",
            "options": {},
        },
        520,
        300,
    ),
    node(
        "Build prompt",
        "n8n-nodes-base.code",
        2,
        {"language": "javaScript", "mode": "runOnceForAllItems", "jsCode": build},
        780,
        300,
    ),
    node(
        "Release agent",
        "@n8n/n8n-nodes-langchain.agent",
        2.2,
        {
            "promptType": "define",
            "text": "={{ $json.prompt }}",
            "options": {
                "systemMessage": SKILL,
                "maxIterations": 16,
                "returnIntermediateSteps": True,
            },
        },
        1040,
        300,
    ),
    node(
        "Ollama",
        "@n8n/n8n-nodes-langchain.lmChatOllama",
        1,
        {"model": "qwen2.5:3b", "options": {"temperature": 0.1}},
        980,
        520,
        ollama_cred,
    ),
    node(
        "Files MCP",
        "@n8n/n8n-nodes-langchain.mcpClientTool",
        1.2,
        {
            "endpointUrl": "http://files-mcp:8000/sse",
            "serverTransport": "sse",
            "authentication": "none",
            "include": "all",
        },
        1160,
        520,
    ),
    node(
        "Incidents MCP",
        "@n8n/n8n-nodes-langchain.mcpClientTool",
        1.2,
        {
            "endpointUrl": "http://incidents-mcp:8000/sse",
            "serverTransport": "sse",
            "authentication": "none",
            "include": "all",
        },
        1340,
        520,
    ),
    node(
        "Read verdict",
        "n8n-nodes-base.code",
        2,
        {"language": "javaScript", "mode": "runOnceForAllItems", "jsCode": read_verdict},
        1300,
        300,
    ),
    node(
        "Respond",
        "n8n-nodes-base.respondToWebhook",
        1.5,
        {"respondWith": "json", "responseBody": "={{ $json }}", "options": {}},
        1560,
        300,
    ),
]


def link(src, dst, type_="main", index=0):
    return src, {
        type_: [[{"node": dst, "type": type_, "index": index}]]
    }


connections = {}
for src, edge in (
    link("Webhook", "Validate"),
    link("Validate", "CI status"),
    link("CI status", "Build prompt"),
    link("Build prompt", "Release agent"),
    link("Release agent", "Read verdict"),
    link("Read verdict", "Respond"),
    link("Ollama", "Release agent", "ai_languageModel"),
    link("Files MCP", "Release agent", "ai_tool"),
    link("Incidents MCP", "Release agent", "ai_tool"),
):
    connections[src] = edge

workflow = {
    "name": "release-gate",
    "nodes": nodes,
    "connections": connections,
    "settings": {"executionOrder": "v1"},
    "active": False,
}
(ROOT / "workflow" / "release-gate.import.json").write_text(
    json.dumps(workflow, indent=2, ensure_ascii=False) + "\n"
)
listed = api("GET", "/rest/workflows")
wf_id = next(
    (item["id"] for item in listed["data"] if item["name"] == "release-gate"),
    None,
)
if wf_id is None:
    created = api("POST", "/rest/workflows", workflow)
    wf_id = created["data"]["id"]
update = dict(workflow)
update["versionId"] = ""
update["active"] = True
activated = api("PATCH", f"/rest/workflows/{wf_id}", update)
print("workflow", wf_id, "active", activated.get("data", {}).get("active"))
print(f"webhook http://127.0.0.1:5678/webhook/{wf_id}/webhook/release-gate")
