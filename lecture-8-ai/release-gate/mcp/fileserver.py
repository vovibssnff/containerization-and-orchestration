"""Filesystem MCP for the release repository and the verdict directory.

The agent may only see /data/repo and may only write verdicts under /data/out.
"""

import json
import os
from pathlib import Path

from mcp.server.fastmcp import FastMCP

REPO = Path(os.environ.get("REPO_DIR", "/data/repo"))
OUT = Path(os.environ.get("OUT_DIR", "/data/out"))

mcp = FastMCP("release-files", host="0.0.0.0", port=8000)


@mcp.tool()
def list_release_files(repo: str) -> str:
    """List the file names in one release repository.

    Input: repo, the repository name. The only repository is "release".
    """
    if repo != "release":
        return "not found: unknown repository"
    names = sorted(p.name for p in REPO.iterdir() if p.is_file())
    return "\n".join(names)


@mcp.tool()
def read_release_file(name: str) -> str:
    """Read one file from the release repository.

    Input: name, the file name only, for example CHANGELOG.md. A path is rejected.
    """
    if "/" in name or name.startswith("."):
        return "rejected: name must be a single file in the repository"
    path = REPO / name
    if not path.is_file():
        return f"not found: {name}"
    return path.read_text()


@mcp.tool()
def write_verdict(version: str, markdown: str) -> str:
    """Write the release verdict.

    Input: version, the release version from the request (used as the file name);
    markdown, the full note in Russian. Writes /data/out/release-<version>.md.
    """
    if not version or "/" in version or ".." in version:
        return "rejected: bad version"
    note = " ".join(markdown.split())
    source = " ".join((REPO / "CHANGELOG.md").read_text().split())
    if not any(source[i : i + 24] in note for i in range(0, max(1, len(source) - 24))):
        return "rejected: quote a sentence from CHANGELOG.md"
    board = json.loads(Path("/data/incidents.json").read_text())
    for item in board["incidents"]:
        if item["status"] == "open" and item["id"] not in markdown:
            return f"rejected: mention open incident {item['id']}"
    first = markdown.strip().splitlines()[0].split()
    if not first or first[0] not in ("GO", "NO-GO"):
        return "rejected: the first line must start with GO or NO-GO"
    if first[0] == "NO-GO":
        freeze = board["freeze"]["active"]
        blocked = freeze == "yes" or any(
            item["status"] == "open" and item["severity"] in ("sev1", "sev2")
            for item in board["incidents"]
        )
        if not blocked and "migration" not in markdown.lower() and "миграция схемы" not in markdown:
            return "rejected: nothing on the board blocks this release. The first line is GO."
    OUT.mkdir(parents=True, exist_ok=True)
    path = OUT / f"release-{version}.md"
    path.write_text(markdown)
    return f"wrote {path.name} ({len(markdown)} bytes)"


if __name__ == "__main__":
    mcp.run(transport="sse")
