#!/usr/bin/env python3
"""Built-CLI proof with synthetic TL responses at the Telegram RPC boundary.

No Telegram credentials, network connection, or personal archive is used.
Only session bootstrap is replaced via Go's overlay; the production importer,
forum pagination, CLI error boundary, and SQLite archive remain unchanged.
"""
import argparse
import json
import os
from pathlib import Path
import sqlite3
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parent.parent
parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("--ref", help="Git revision to prove instead of the working tree")
parser.add_argument("--expect", choices=("fixed", "main-bug", "pr-bug"), default="fixed")
args = parser.parse_args()
source_file = ROOT / "internal/telegramdesktop/tdata.go"
source = (subprocess.check_output(["git", "show", f"{args.ref}:internal/telegramdesktop/tdata.go"], cwd=ROOT, text=True)
          if args.ref else source_file.read_text())
fixture = (ROOT / "scripts/fixtures/forum-import.go.txt").read_text()
start = source.index("func importTDataGo(")
end = source.index("\nfunc (s *tdataImportSession) importAccount", start)
source = source[:start] + fixture + source[end:]
source = source.replace('\t"github.com/gotd/td/session"\n', '')
source = source.replace('\t"github.com/gotd/td/session/tdesktop"\n', '')
source = source.replace('import (', 'import (\n"github.com/gotd/td/bin"', 1)

with tempfile.TemporaryDirectory(prefix="telecrawl-forum-proof-") as scratch:
    folder = Path(scratch)
    replacement = folder / "tdata.go"
    replacement.write_text(source)
    overlay = folder / "overlay.json"
    overlay.write_text(json.dumps({"Replace": {str(source_file): str(replacement)}}))
    binary = folder / "telecrawl"
    subprocess.run(["go", "build", "-o", str(binary), "-overlay", str(overlay), "./cmd/telecrawl"], cwd=ROOT, check=True)
    db = folder / "archive.db"
    src = folder / "source"
    src.mkdir()

    def run(scenario, restore=False):
        command = [str(binary), "--db", str(db), "--source", str(src), "--json", "import"]
        if restore:
            command.append("--restore")
        result = subprocess.run(command, env={**os.environ, "TELECRAWL_PROOF_SCENARIO": scenario}, capture_output=True, text=True, timeout=15)
        print(f"scenario={scenario} restore={restore} exit={result.returncode}")
        print(result.stderr.strip())
        return result

    def rows():
        with sqlite3.connect(db) as conn:
            return tuple(conn.execute(f"SELECT * FROM {table} ORDER BY 1,2").fetchall()
                         for table in ("chats", "topics", "messages", "message_revisions", "sync_state"))

    if args.expect != "fixed":
        result = run("stalled", restore=True)
        with sqlite3.connect(db) as conn:
            topics = conn.execute("SELECT count(*) FROM topics").fetchone()[0]
        print(f"reproduced={args.expect} archived_topics={topics} advertised=400")
        if args.expect == "pr-bug":
            assert result.returncode == 0 and topics == 100
        else:
            assert result.returncode != 0 and "deadline exceeded" in result.stderr and "topic_rpc=3" in result.stderr
    else:
        for scenario in ("success", "under-count", "over-count", "short-page", "missing-date", "deleted-boundary"):
            result = run(scenario, restore=True)
            assert result.returncode == 0, result.stderr
            before = rows()
            assert len(before[1]) == 101
        read = subprocess.run([str(binary), "--db", str(db), "--json", "topics", "--chat", "-1000000000042", "--limit", "200"], check=True, capture_output=True, text=True)
        assert len(json.loads(read.stdout)) == 101
        for scenario in ("stalled", "cycle", "rpc-error"):
            for restore in (False, True):
                result = run(scenario, restore)
                assert result.returncode != 0
                assert "incomplete" in result.stderr or "synthetic forum RPC failure" in result.stderr
                assert rows() == before, f"archive mutated after {scenario}, restore={restore}"
        print("PASS: 101 topics imported/read with exact/approximate counts, short pages, absent messages and deleted topics; 6 failures preserved chats, topics, messages, revisions and sync state")
