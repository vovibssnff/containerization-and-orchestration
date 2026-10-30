"""Incidents MCP. The data is a local JSON file, the stand-in for the on-call board."""

import json
import os
from pathlib import Path

from mcp.server.fastmcp import FastMCP

BOARD = Path(os.environ.get("BOARD_FILE", "/data/incidents.json"))
mcp = FastMCP("release-incidents", host="0.0.0.0", port=8000)


def load() -> dict:
    return json.loads(BOARD.read_text())


@mcp.tool()
def list_open_incidents(service: str) -> str:
    """List incidents that are still open.

    Input: service, the service name from the release request.
    Returns every open incident. Each line says whether it is for this service.
    """
    rows = []
    for item in load()["incidents"]:
        if item["status"] != "open":
            continue
        owns = "this service" if item["service"] == service else "other service"
        rows.append(
            f"{item['id']} {item['severity']} {owns} {item['service']}: {item['summary']}"
        )
    return "\n".join(rows) if rows else "no open incidents"


@mcp.tool()
def release_freeze(board: str) -> str:
    """Read the current release freeze from one on-call board.

    Input: board, the board name. The only board is "oncall".
    Returns active=yes or active=no, and the reason written by the release captain.
    """
    if board != "oncall":
        return "not found: unknown board"
    freeze = load()["freeze"]
    return f"active={freeze['active']}\nreason={freeze['reason']}"


if __name__ == "__main__":
    mcp.run(transport="sse")
