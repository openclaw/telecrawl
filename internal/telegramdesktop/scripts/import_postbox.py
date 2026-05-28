#!/usr/bin/env python3
"""Import native Telegram for macOS Postbox data.

This bridge is intentionally offline: it reads .tempkeyEncrypted plus local
Postbox db_sqlite files and emits Telecrawl's importer JSON on stdout.

Parts of the SQLCipher/Postbox decoding logic are adapted from
telegram-message-exporter, Copyright (c) 2026 Simon Oakes, MIT licensed.
See import_postbox.LICENSE for the upstream license notice.
"""

from __future__ import annotations

import argparse
import datetime as dt
import hashlib
import importlib
import io
import json
import os
import struct
import sys
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Iterable


TEMPKEY_MURMUR_SEED = 0xF7CA7FD2
DEFAULT_PASSCODE = b"no-matter-key"
INCOMING_FLAG = 4
MEDIA_TAGS = {
    1 << 0: "photo_or_video",
    1 << 1: "file",
    1 << 2: "music",
    1 << 3: "web_page",
    1 << 4: "voice_or_instant_video",
    1 << 7: "gif",
    1 << 8: "photo",
    1 << 9: "video",
}


@dataclass(frozen=True)
class PostboxSource:
    account_id: str
    key_path: Path
    db_path: Path


class ByteReader:
    def __init__(self, data: bytes, endian: str = "<") -> None:
        self.buf = io.BytesIO(data)
        self.endian = endian

    def read_fmt(self, fmt: str) -> int | float:
        data = self.buf.read(struct.calcsize(fmt))
        if len(data) != struct.calcsize(fmt):
            raise ValueError("short postbox payload")
        return struct.unpack(self.endian + fmt, data)[0]

    def read_int8(self) -> int:
        return int(self.read_fmt("b"))

    def read_uint8(self) -> int:
        return int(self.read_fmt("B"))

    def read_int32(self) -> int:
        return int(self.read_fmt("i"))

    def read_uint32(self) -> int:
        return int(self.read_fmt("I"))

    def read_int64(self) -> int:
        return int(self.read_fmt("q"))

    def read_bytes(self) -> bytes:
        size = self.read_int32()
        if size < 0:
            raise ValueError("negative postbox byte length")
        data = self.buf.read(size)
        if len(data) != size:
            raise ValueError("short postbox bytes")
        return data

    def read_str(self) -> str:
        return self.read_bytes().decode("utf-8", errors="replace")

    def read_short_str(self) -> str:
        size = self.read_uint8()
        data = self.buf.read(size)
        if len(data) != size:
            raise ValueError("short postbox string")
        return data.decode("utf-8", errors="replace")

    def read_double(self) -> float:
        return float(self.read_fmt("d"))


class PostboxDecoder:
    def __init__(self, data: bytes) -> None:
        self.reader = ByteReader(data)
        self.size = len(data)

    def decode_root_object(self) -> Any:
        for key, value_type, value in self.iter_kv():
            if key == "_" and value_type == 5:
                return value
        return None

    def iter_kv(self) -> Iterable[tuple[str, int, Any]]:
        while self.reader.buf.tell() < self.size:
            key = self.reader.read_short_str()
            value_type, value = self.read_value()
            yield key, value_type, value

    def read_value(self) -> tuple[int, Any]:
        value_type = self.reader.read_uint8()
        if value_type == 0:
            return value_type, self.reader.read_int32()
        if value_type == 1:
            return value_type, self.reader.read_int64()
        if value_type == 2:
            return value_type, self.reader.read_uint8() != 0
        if value_type == 3:
            return value_type, self.reader.read_double()
        if value_type == 4:
            return value_type, self.reader.read_str()
        if value_type == 5:
            return value_type, self.read_object()
        if value_type == 6:
            return value_type, [self.reader.read_int32() for _ in range(self.reader.read_int32())]
        if value_type == 7:
            return value_type, [self.reader.read_int64() for _ in range(self.reader.read_int32())]
        if value_type == 8:
            return value_type, [self.read_object() for _ in range(self.reader.read_int32())]
        if value_type == 9:
            return value_type, [(self.read_object(), self.read_object()) for _ in range(self.reader.read_int32())]
        if value_type == 10:
            return value_type, self.reader.read_bytes()
        if value_type == 11:
            return value_type, None
        if value_type == 12:
            return value_type, [self.reader.read_str() for _ in range(self.reader.read_int32())]
        if value_type == 13:
            return value_type, [self.reader.read_bytes() for _ in range(self.reader.read_int32())]
        raise ValueError(f"unknown postbox value type {value_type}")

    def read_object(self) -> dict[str, Any]:
        type_hash = self.reader.read_int32()
        size = self.reader.read_int32()
        if size < 0:
            raise ValueError("negative postbox object size")
        data = self.reader.buf.read(size)
        if len(data) != size:
            raise ValueError("short postbox object")
        payload = {key: value for key, _, value in PostboxDecoder(data).iter_kv()}
        payload["@type"] = type_hash
        return payload


def murmur3_32(data: bytes, seed: int = TEMPKEY_MURMUR_SEED) -> int:
    seed &= 0xFFFFFFFF
    length = len(data)
    h1 = seed
    c1 = 0xCC9E2D51
    c2 = 0x1B873593
    rounded_end = length & 0xFFFFFFFC
    for i in range(0, rounded_end, 4):
        k1 = data[i] | (data[i + 1] << 8) | (data[i + 2] << 16) | (data[i + 3] << 24)
        k1 = (k1 * c1) & 0xFFFFFFFF
        k1 = ((k1 << 15) | (k1 >> 17)) & 0xFFFFFFFF
        k1 = (k1 * c2) & 0xFFFFFFFF
        h1 ^= k1
        h1 = ((h1 << 13) | (h1 >> 19)) & 0xFFFFFFFF
        h1 = (h1 * 5 + 0xE6546B64) & 0xFFFFFFFF
    k1 = 0
    tail = length & 3
    if tail == 3:
        k1 ^= data[rounded_end + 2] << 16
    if tail >= 2:
        k1 ^= data[rounded_end + 1] << 8
    if tail >= 1:
        k1 ^= data[rounded_end]
        k1 = (k1 * c1) & 0xFFFFFFFF
        k1 = ((k1 << 15) | (k1 >> 17)) & 0xFFFFFFFF
        k1 = (k1 * c2) & 0xFFFFFFFF
        h1 ^= k1
    h1 ^= length
    h1 ^= h1 >> 16
    h1 = (h1 * 0x85EBCA6B) & 0xFFFFFFFF
    h1 ^= h1 >> 13
    h1 = (h1 * 0xC2B2AE35) & 0xFFFFFFFF
    h1 ^= h1 >> 16
    return h1 - 0x100000000 if h1 & 0x80000000 else h1


def read_passcodes(value: str | None) -> list[bytes]:
    if value:
        return [value.encode("utf-8")]
    if os.environ.get("TG_LOCAL_PASSCODE"):
        return [os.environ["TG_LOCAL_PASSCODE"].encode("utf-8")]
    return [DEFAULT_PASSCODE, b""]


def tempkey_key(passcode: bytes) -> tuple[bytes, bytes]:
    digest = hashlib.sha512(passcode).digest()
    return digest[:32], digest[-16:]


def parse_tempkey(key_path: Path, passcodes: Iterable[bytes]) -> bytes:
    try:
        aes = importlib.import_module("Cryptodome.Cipher.AES")
    except ImportError as exc:
        raise SystemExit("missing dependency: pycryptodomex") from exc

    encrypted = key_path.read_bytes()
    if len(encrypted) % 16 != 0:
        raise SystemExit(f"invalid tempkey size: {key_path}")
    for passcode in passcodes:
        aes_key, aes_iv = tempkey_key(passcode)
        data = aes.new(aes_key, aes.MODE_CBC, aes_iv).decrypt(encrypted)
        if len(data) < 52:
            continue
        db_key = data[:32]
        db_salt = data[32:48]
        expected = int.from_bytes(data[48:52], "little", signed=True)
        actual = murmur3_32(db_key + db_salt)
        if expected == actual:
            return db_key + db_salt
    raise SystemExit(f"unable to decrypt tempkey: {key_path}")


def connect_postbox(db_path: Path, key: bytes) -> Any:
    try:
        sqlcipher = importlib.import_module("sqlcipher3")
    except ImportError:
        try:
            sqlcipher = importlib.import_module("pysqlcipher3.dbapi2")
        except ImportError as exc:
            raise SystemExit(
                "missing dependency: sqlcipher3 or pysqlcipher3; native SQLCipher is required"
            ) from exc

    conn = sqlcipher.connect(str(db_path))
    conn.execute("PRAGMA kdf_iter = 1")
    conn.execute("PRAGMA cipher_hmac_algorithm = HMAC_SHA512")
    conn.execute("PRAGMA cipher_kdf_algorithm = PBKDF2_HMAC_SHA512")
    conn.execute("PRAGMA cipher_plaintext_header_size = 32")
    conn.execute("PRAGMA cipher_default_plaintext_header_size = 32")
    conn.execute(f"PRAGMA key=\"x'{key.hex()}'\"")
    conn.execute("PRAGMA cipher_compatibility = 4")
    conn.execute("SELECT count(*) FROM sqlite_master").fetchone()
    return conn


def default_group_path() -> Path:
    return Path.home() / "Library" / "Group Containers" / "6N38VWS5BX.ru.keepcoder.Telegram"


def discover_sources(source_arg: str | None) -> list[PostboxSource]:
    root = Path(source_arg).expanduser() if source_arg else default_group_path()
    if (root / "postbox" / "db" / "db_sqlite").exists():
        return [PostboxSource(root.name, root.parent / ".tempkeyEncrypted", root / "postbox" / "db" / "db_sqlite")]

    lane_dirs = [root] if (root / ".tempkeyEncrypted").exists() else []
    for name in ("stable", "appstore"):
        lane = root / name
        if (lane / ".tempkeyEncrypted").exists():
            lane_dirs.append(lane)
    if not lane_dirs and root.exists():
        lane_dirs = [p for p in root.iterdir() if p.is_dir() and (p / ".tempkeyEncrypted").exists()]

    sources: list[PostboxSource] = []
    for lane_path in sorted(set(lane_dirs)):
        key_path = lane_path / ".tempkeyEncrypted"
        for account_path in sorted(lane_path.glob("account-*")):
            db_path = account_path / "postbox" / "db" / "db_sqlite"
            if key_path.exists() and db_path.exists():
                sources.append(PostboxSource(f"{lane_path.name}/{account_path.name}", key_path, db_path))
    return sources


def peer_display(peer: Any) -> str:
    if not isinstance(peer, dict):
        return ""
    first = str(peer.get("fn") or "").strip()
    last = str(peer.get("ln") or "").strip()
    if first or last:
        return f"{first} {last}".strip()
    if peer.get("t"):
        return str(peer["t"]).strip()
    if peer.get("un"):
        return f"@{peer['un']}"
    return ""


def load_peer_map(conn: Any) -> dict[int, str]:
    peers: dict[int, str] = {}
    for key, value in conn.execute("SELECT key, value FROM t2"):
        if not isinstance(key, int) or not isinstance(value, bytes):
            continue
        try:
            display = peer_display(PostboxDecoder(value).decode_root_object())
        except Exception:
            continue
        if display:
            peers[key] = display
    return peers


def read_source_records(source: PostboxSource, conn: Any, multi_account: bool) -> tuple[dict[str, str], list[dict[str, Any]]]:
    raw_peers = load_peer_map(conn)
    peers = {
        peer_store_id(source.account_id, peer_id, multi_account): display
        for peer_id, display in raw_peers.items()
    }
    messages: list[dict[str, Any]] = []
    for key_blob, value in conn.execute("SELECT key, value FROM t7 ORDER BY key"):
        if not isinstance(key_blob, bytes) or len(key_blob) < 20 or not isinstance(value, bytes):
            continue
        try:
            peer_id, namespace, timestamp, message_id = struct.unpack(">qiii", key_blob[:20])
            msg = read_message(value)
        except Exception:
            continue
        if not msg:
            continue
        chat_id = peer_store_id(source.account_id, peer_id, multi_account)
        chat_name = raw_peers.get(peer_id, "")
        incoming = bool(int(msg["flags"]) & INCOMING_FLAG)
        author_id = msg.get("author_id")
        media_type = media_type_for(msg)
        if author_id:
            sender_id = peer_store_id(source.account_id, author_id, multi_account)
            sender_name = raw_peers.get(author_id, "")
        elif incoming:
            sender_id = chat_id
            sender_name = chat_name
        else:
            sender_id = ""
            sender_name = ""
        messages.append({
            "_ts": timestamp,
            "_raw_chat_id": str(peer_id),
            "source_pk": source_pk(source.account_id, peer_id, namespace, message_id, multi_account),
            "chat_id": chat_id,
            "chat_name": chat_name,
            "message_id": f"{namespace}:{message_id}",
            "sender_id": sender_id,
            "sender_name": sender_name,
            "timestamp": iso(timestamp),
            "from_me": not incoming,
            "text": msg.get("text") or "",
            "message_type": "message",
            "media_type": media_type,
        })
    return peers, messages


def read_forward_info(reader: ByteReader) -> None:
    flags = reader.read_int8()
    if flags == 0:
        return
    reader.read_int64()
    reader.read_int32()
    if flags & (1 << 1):
        reader.read_int64()
    if flags & (1 << 2):
        reader.read_int64()
        reader.read_int32()
        reader.read_int32()
    if flags & (1 << 3):
        reader.read_str()
    if flags & (1 << 4):
        reader.read_str()
    if flags & (1 << 5):
        reader.read_int32()


def read_message(value: bytes) -> dict[str, Any] | None:
    reader = ByteReader(value)
    if reader.read_int8() != 0:
        return None
    reader.read_uint32()
    reader.read_uint32()
    data_flags = reader.read_uint8()
    if data_flags & (1 << 0):
        reader.read_int64()
    if data_flags & (1 << 1):
        reader.read_uint32()
    if data_flags & (1 << 2):
        reader.read_int64()
    if data_flags & (1 << 3):
        reader.read_uint32()
    if data_flags & (1 << 4):
        reader.read_uint32()
    if data_flags & (1 << 5):
        reader.read_int64()
    flags = reader.read_uint32()
    tags = reader.read_uint32()
    read_forward_info(reader)
    author_id = None
    if reader.read_int8() == 1:
        author_id = reader.read_int64()
    text = reader.read_str()
    for _ in range(reader.read_int32()):
        reader.read_bytes()
    embedded_media_count = reader.read_int32()
    for _ in range(embedded_media_count):
        reader.read_bytes()
    referenced_media_ids = []
    for _ in range(reader.read_int32()):
        referenced_media_ids.append((reader.read_int32(), reader.read_int64()))
    return {
        "flags": flags,
        "tags": tags,
        "author_id": author_id,
        "text": text,
        "embedded_media_count": embedded_media_count,
        "referenced_media_ids": referenced_media_ids,
    }


def media_type_for(msg: dict[str, Any]) -> str:
    tags = int(msg.get("tags") or 0)
    for bit, label in MEDIA_TAGS.items():
        if tags & bit:
            return label
    if msg.get("embedded_media_count") or msg.get("referenced_media_ids"):
        return "media"
    return ""


def stable_int(*parts: object) -> int:
    digest = hashlib.sha256(":".join(str(part) for part in parts).encode("utf-8")).digest()
    return int.from_bytes(digest[:8], "big") & 0x7FFFFFFFFFFFFFFF


def peer_store_id(account_id: str, peer_id: int, multi_account: bool) -> str:
    if not multi_account:
        return str(peer_id)
    return str(stable_int("postbox-account", account_id, peer_id))


def source_pk(account_id: str, peer_id: int, namespace: int, message_id: int, multi_account: bool) -> int:
    if not multi_account:
        return stable_int(peer_id, namespace, message_id)
    return stable_int("postbox-message", account_id, peer_id, namespace, message_id)


def iso(ts: int) -> str:
    return dt.datetime.fromtimestamp(ts, tz=dt.timezone.utc).isoformat().replace("+00:00", "Z")


def apply_limits(messages: list[dict[str, Any]], dialogs_limit: int, messages_limit: int) -> list[dict[str, Any]]:
    by_chat: dict[str, list[dict[str, Any]]] = {}
    for msg in messages:
        by_chat.setdefault(msg["chat_id"], []).append(msg)
    ranked = sorted(by_chat.items(), key=lambda item: max(m["_ts"] for m in item[1]), reverse=True)
    if dialogs_limit > 0:
        ranked = ranked[:dialogs_limit]
    out: list[dict[str, Any]] = []
    for _, rows in ranked:
        rows = sorted(rows, key=lambda m: (m["_ts"], m["source_pk"]))
        if messages_limit > 0:
            rows = rows[-messages_limit:]
        out.extend(rows)
    return sorted(out, key=lambda m: (m["_ts"], m["source_pk"]))


def filter_chat(messages: list[dict[str, Any]], chat_id: str) -> list[dict[str, Any]]:
    chat_id = chat_id.strip()
    if not chat_id:
        return messages
    return [msg for msg in messages if msg["chat_id"] == chat_id or msg.get("_raw_chat_id") == chat_id]


def import_source(source: PostboxSource, passcodes: list[bytes], multi_account: bool) -> tuple[dict[str, str], list[dict[str, Any]]]:
    key = parse_tempkey(source.key_path, passcodes)
    conn = connect_postbox(source.db_path, key)
    try:
        return read_source_records(source, conn, multi_account)
    finally:
        conn.close()


def build_result(source_path: str, peers: dict[str, str], messages: list[dict[str, Any]], started: dt.datetime) -> dict[str, Any]:
    chats: dict[str, dict[str, Any]] = {}
    for msg in messages:
        msg.pop("_ts", None)
        msg.pop("_raw_chat_id", None)
        chat_id = msg["chat_id"]
        chat = chats.setdefault(chat_id, {
            "id": chat_id,
            "kind": "chat",
            "name": msg.get("chat_name") or peers.get(chat_id, ""),
            "username": "",
            "last_message_at": msg["timestamp"],
            "unread_count": 0,
            "message_count": 0,
            "folder_id": "",
            "forum": False,
        })
        chat["message_count"] += 1
        if msg["timestamp"] > chat["last_message_at"]:
            chat["last_message_at"] = msg["timestamp"]
    finished = dt.datetime.now(dt.timezone.utc)
    return {
        "source_path": source_path,
        "started_at": started.isoformat().replace("+00:00", "Z"),
        "finished_at": finished.isoformat().replace("+00:00", "Z"),
        "chats": sorted(chats.values(), key=lambda c: c["last_message_at"], reverse=True),
        "folders": [],
        "folder_chats": [],
        "topics": [],
        "messages": messages,
    }


# Synthetic Postbox-shaped fixture helpers used by the public Go test.
def fixture_short_str(value: str) -> bytes:
    data = value.encode("utf-8")
    return struct.pack("B", len(data)) + data


def fixture_bytes(value: bytes) -> bytes:
    return struct.pack("<i", len(value)) + value


def fixture_string(value: str) -> bytes:
    return fixture_bytes(value.encode("utf-8"))


def fixture_kv_string(key: str, value: str) -> bytes:
    return fixture_short_str(key) + struct.pack("B", 4) + fixture_string(value)


def fixture_object(payload: bytes, type_hash: int = 0x12345678) -> bytes:
    return struct.pack("<ii", type_hash, len(payload)) + payload


def fixture_root_object(payload: bytes) -> bytes:
    return fixture_short_str("_") + struct.pack("B", 5) + fixture_object(payload)


def fixture_peer(first: str, last: str = "") -> bytes:
    return fixture_root_object(fixture_kv_string("fn", first) + fixture_kv_string("ln", last))


def fixture_message(
    text: str = "fixture hello",
    author_id: int | None = 4242,
    tags: int = 1 << 0,
    referenced_media_ids: list[tuple[int, int]] | None = None,
) -> bytes:
    if referenced_media_ids is None:
        referenced_media_ids = [(7, 123456789)]
    out = bytearray()
    out += struct.pack("<bIIBII", 0, 11, 22, 0, INCOMING_FLAG, tags)
    out += struct.pack("<b", 0)
    if author_id is None:
        out += struct.pack("<b", 0)
    else:
        out += struct.pack("<bq", 1, author_id)
    out += fixture_string(text)
    out += struct.pack("<i", 0)
    out += struct.pack("<i", 0)
    out += struct.pack("<i", len(referenced_media_ids))
    for namespace, media_id in referenced_media_ids:
        out += struct.pack("<iq", namespace, media_id)
    return bytes(out)


def fixture_message_key(peer_id: int, namespace: int, timestamp: int, message_id: int) -> bytes:
    return struct.pack(">qiii", peer_id, namespace, timestamp, message_id)


class FixturePostboxConnection:
    def __init__(self, peers: dict[int, bytes], messages: list[tuple[bytes, bytes]]) -> None:
        self.peers = peers
        self.messages = messages

    def execute(self, query: str) -> list[tuple[Any, Any]]:
        if "FROM t2" in query:
            return list(self.peers.items())
        if "FROM t7" in query:
            return self.messages
        raise AssertionError(f"unexpected fixture query: {query}")


def run_self_test(fixture_dir: str) -> None:
    expected = {
        "peer_display": "Fixture Person",
        "text": "fixture hello",
        "author_id": 4242,
        "media_type": "photo_or_video",
        "referenced_media_ids": [[7, 123456789]],
        "chat_filter_source_pks": [1, 2],
        "limited_source_pks": [3],
        "single_account_peer_id": "100",
        "raw_chat_filter_source_pks": [4, 5],
    }
    peer_bytes = fixture_peer("Fixture", "Person")
    message_bytes = fixture_message()
    if fixture_dir:
        root = Path(fixture_dir)
        expected = json.loads((root / "postbox_expected.json").read_text())
        peer_bytes = bytes.fromhex((root / "postbox_peer.hex").read_text())
        message_bytes = bytes.fromhex((root / "postbox_message.hex").read_text())

    peer = PostboxDecoder(peer_bytes).decode_root_object()
    if peer_display(peer) != expected["peer_display"]:
        raise AssertionError(f"peer display decode failed: {peer!r}")

    message = read_message(message_bytes)
    if not message:
        raise AssertionError("message decode returned no message")
    if message["text"] != expected["text"]:
        raise AssertionError(f"message text decode failed: {message!r}")
    if message["author_id"] != expected["author_id"]:
        raise AssertionError(f"author decode failed: {message!r}")
    if media_type_for(message) != expected["media_type"]:
        raise AssertionError(f"media tag decode failed: {message!r}")
    referenced_media_ids = [list(item) for item in message["referenced_media_ids"]]
    if referenced_media_ids != expected["referenced_media_ids"]:
        raise AssertionError(f"referenced media decode failed: {message!r}")

    sample = [
        {"chat_id": "1", "_raw_chat_id": "1", "_ts": 10, "source_pk": 1},
        {"chat_id": "1", "_raw_chat_id": "1", "_ts": 20, "source_pk": 2},
        {"chat_id": "2", "_raw_chat_id": "2", "_ts": 30, "source_pk": 3},
    ]
    if [row["source_pk"] for row in filter_chat(sample, "1")] != expected["chat_filter_source_pks"]:
        raise AssertionError("chat filter failed")
    limited = apply_limits(sample, dialogs_limit=1, messages_limit=1)
    if [row["source_pk"] for row in limited] != expected["limited_source_pks"]:
        raise AssertionError(f"limit decode failed: {limited!r}")

    if peer_store_id("stable/account-a", 100, False) != expected["single_account_peer_id"]:
        raise AssertionError("single-account peer id should stay readable")
    account_a_chat = peer_store_id("stable/account-a", 100, True)
    account_b_chat = peer_store_id("stable/account-b", 100, True)
    if account_a_chat == account_b_chat:
        raise AssertionError("multi-account peer ids collided")
    account_a_pk = source_pk("stable/account-a", 100, 0, 1, True)
    account_b_pk = source_pk("stable/account-b", 100, 0, 1, True)
    if account_a_pk == account_b_pk:
        raise AssertionError("multi-account message source keys collided")
    multi_sample = [
        {"chat_id": account_a_chat, "_raw_chat_id": "100", "_ts": 10, "source_pk": 4},
        {"chat_id": account_b_chat, "_raw_chat_id": "100", "_ts": 20, "source_pk": 5},
    ]
    if [row["source_pk"] for row in filter_chat(multi_sample, "100")] != expected["raw_chat_filter_source_pks"]:
        raise AssertionError("raw chat filter failed")

    public_sources = [
        PostboxSource("stable/account-a", Path("unused-key-a"), Path("account-a.db")),
        PostboxSource("stable/account-b", Path("unused-key-b"), Path("account-b.db")),
    ]
    public_connections = [
        FixturePostboxConnection(
            {100: fixture_peer("Fixture", "A"), 4242: fixture_peer("Sender", "A")},
            [(fixture_message_key(100, 0, 1_421_404_800, 1), fixture_message("public account a"))],
        ),
        FixturePostboxConnection(
            {100: fixture_peer("Fixture", "B"), 4242: fixture_peer("Sender", "B")},
            [(fixture_message_key(100, 0, 1_421_404_801, 1), fixture_message("public account b"))],
        ),
    ]
    public_peers: dict[str, str] = {}
    public_messages: list[dict[str, Any]] = []
    for source, conn in zip(public_sources, public_connections, strict=True):
        peers, messages = read_source_records(source, conn, multi_account=True)
        public_peers.update(peers)
        public_messages.extend(messages)
    public_filtered = filter_chat(public_messages, "100")
    if len(public_filtered) != 2:
        raise AssertionError(f"public raw chat filter returned {len(public_filtered)} messages")
    if public_filtered[0]["chat_id"] == public_filtered[1]["chat_id"]:
        raise AssertionError("public multi-account import collapsed distinct chats")
    if public_filtered[0]["source_pk"] == public_filtered[1]["source_pk"]:
        raise AssertionError("public multi-account import collided source keys")
    public_result = build_result("fixture-postbox", public_peers, public_filtered, dt.datetime(2026, 1, 1, tzinfo=dt.timezone.utc))
    if len(public_result["chats"]) != 2 or len(public_result["messages"]) != 2:
        raise AssertionError(f"public import result shape failed: {public_result!r}")
    if {msg["text"] for msg in public_result["messages"]} != {"public account a", "public account b"}:
        raise AssertionError(f"public import message text mismatch: {public_result!r}")
    if sum(1 for msg in public_result["messages"] if msg["media_type"]) != 2:
        raise AssertionError(f"public import media tagging failed: {public_result!r}")

    print(json.dumps({"ok": True, "fixture": "sanitized-postbox-format"}))


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--self-test", action="store_true")
    parser.add_argument("--fixture-dir", default="")
    parser.add_argument("--source", default="")
    parser.add_argument("--dialogs-limit", type=int, default=200)
    parser.add_argument("--messages-limit", type=int, default=500)
    parser.add_argument("--chat", default="")
    parser.add_argument("--passcode", default="")
    args = parser.parse_args()
    if args.self_test:
        run_self_test(args.fixture_dir)
        return

    started = dt.datetime.now(dt.timezone.utc)
    sources = discover_sources(args.source)
    if not sources:
        raise SystemExit("no Telegram for macOS Postbox account databases found")

    passcodes = read_passcodes(args.passcode)
    multi_account = len(sources) > 1
    all_peers: dict[str, str] = {}
    by_identity: dict[tuple[str, str, str], dict[str, Any]] = {}
    for source in sources:
        peers, messages = import_source(source, passcodes, multi_account)
        all_peers.update(peers)
        for msg in messages:
            by_identity[(source.account_id, msg["chat_id"], msg["message_id"])] = msg

    filtered = filter_chat(list(by_identity.values()), args.chat)
    if args.chat and not filtered:
        raise SystemExit(f"could not find chat in Postbox cache: {args.chat}")
    limited = apply_limits(filtered, args.dialogs_limit, args.messages_limit)
    source_path = str(Path(args.source).expanduser()) if args.source else str(default_group_path())
    json.dump(build_result(source_path, all_peers, limited, started), sys.stdout, separators=(",", ":"))


if __name__ == "__main__":
    main()
