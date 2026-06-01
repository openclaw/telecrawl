#!/usr/bin/env python3
"""Import native Telegram for macOS Postbox data.

This bridge reads .tempkeyEncrypted plus local Postbox db_sqlite files and
emits Telecrawl's importer JSON on stdout. With --fetch-media, it can also fetch
missing cloud media from an existing native account auth key without launching
Telegram or starting an interactive login flow.

Parts of the SQLCipher/Postbox decoding logic are adapted from
telegram-message-exporter, Copyright (c) 2026 Simon Oakes, MIT licensed.
See import_postbox.LICENSE for the upstream license notice.
"""

from __future__ import annotations

import argparse
import asyncio
import base64
import datetime as dt
import hashlib
import importlib
import io
import json
import os
import re
import struct
import sys
import tempfile
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Iterable


TEMPKEY_MURMUR_SEED = 0xF7CA7FD2
DEFAULT_PASSCODE = b"no-matter-key"
INCOMING_FLAG = 4
MEDIA_CACHE_INDEXES: dict[str, dict[str, list[Path]]] = {}
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
RESOURCE_TYPE_CLOUD_PHOTO_SIZE = 1226791958
RESOURCE_TYPE_CLOUD_DOCUMENT_SIZE = -2129249780
RESOURCE_TYPE_CLOUD_DOCUMENT = 486562374
RESOURCE_TYPE_CLOUD_PEER_PHOTO_SIZE = 923090569
RESOURCE_TYPE_LOCAL_FILE = 711798229
RESOURCE_TYPE_LOCAL_FILE_REFERENCE = 1868491758
MESSAGE_METADATA_SERVICE_ACTION = -1132984447
MESSAGE_METADATA_LOCATION = -1138242673
MESSAGE_METADATA_POLL = -165764138
MESSAGE_METADATA_WEBPAGE = -661322156
URL_RE = re.compile(r"https?://[^\s<>()\"']+")
# Public Telegram for macOS app identity from TelegramSwift.
TELEGRAM_MAC_API_ID = 9
TELEGRAM_MAC_API_HASH = "3975f648bb682ee889f35483bc618d1c"  # gitleaks:allow
DC_ENDPOINTS = {
    1: ("149.154.175.53", 443),
    2: ("149.154.167.51", 443),
    3: ("149.154.175.100", 443),
    4: ("149.154.167.91", 443),
    5: ("91.108.56.130", 443),
}
REMOTE_CONNECT_TIMEOUT_SECONDS = 30
REMOTE_AUTH_TIMEOUT_SECONDS = 30
REMOTE_DIALOGS_TIMEOUT_SECONDS = 120
REMOTE_MESSAGE_TIMEOUT_SECONDS = 45
REMOTE_DOWNLOAD_TIMEOUT_SECONDS = 180


@dataclass(frozen=True)
class PostboxSource:
    account_id: str
    key_path: Path
    db_path: Path


@dataclass(frozen=True)
class NativeSession:
    account_id: str
    dc_id: int
    auth_key: bytes
    host: str
    port: int


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


def account_dir_record_id(account_dir: str) -> int:
    if not account_dir.startswith("account-"):
        raise ValueError(f"invalid account directory: {account_dir}")
    value = int(account_dir.removeprefix("account-"))
    if value >= 1 << 63:
        value -= 1 << 64
    return value


def dict_pairs(value: Any) -> Iterable[tuple[Any, Any]]:
    if isinstance(value, list):
        return zip(value[0::2], value[1::2])
    if isinstance(value, dict):
        return value.items()
    return []


def native_session_for_source(source: PostboxSource) -> NativeSession | None:
    account_path = source.db_path.parent.parent.parent
    lane_path = account_path.parent
    shared_path = lane_path / "accounts-shared-data"
    if not shared_path.exists():
        return None
    try:
        account_record_id = account_dir_record_id(account_path.name)
        shared = json.loads(shared_path.read_text())
    except Exception:
        return None
    for account in shared.get("accounts") or []:
        if int(account.get("id", 0)) != account_record_id:
            continue
        dc_id = int(account.get("primaryId", 0))
        host, port = DC_ENDPOINTS.get(dc_id, ("", 0))
        if not host:
            return None
        datacenters = {int(key): value for key, value in dict_pairs(account.get("datacenters"))}
        datacenter = datacenters.get(dc_id)
        if not isinstance(datacenter, dict):
            return None
        key_data = ((datacenter.get("masterKey") or {}).get("data") or "").strip()
        if not key_data:
            return None
        try:
            auth_key = base64.b64decode(key_data)
        except Exception:
            return None
        if len(auth_key) != 256:
            return None
        return NativeSession(source.account_id, dc_id, auth_key, host, port)
    return None


def postbox_peer_to_telethon_id(peer_id: int) -> int | None:
    parts = postbox_peer_parts(peer_id)
    if parts is None:
        return None
    namespace, raw_id = parts
    if namespace == 0:
        return raw_id
    if namespace == 1:
        return -raw_id
    if namespace == 2:
        return -1_000_000_000_000 - raw_id
    return None


def postbox_peer_parts(peer_id: int) -> tuple[int, int] | None:
    data = peer_id & ((1 << 64) - 1)
    legacy_namespace_bits = (data >> 32) & 0xFFFFFFFF
    id_low_bits = data & 0xFFFFFFFF
    if legacy_namespace_bits == 0x7FFFFFFF and id_low_bits == 0:
        return None
    namespace = (data >> 32) & 0x7
    id_high_bits = ((data >> 35) & 0xFFFFFFFF) << 32
    if id_high_bits == 0 and namespace == 3:
        raw_id = id_low_bits - (1 << 32) if id_low_bits >= 1 << 31 else id_low_bits
    else:
        raw_id = id_high_bits | id_low_bits
        if raw_id >= 1 << 63:
            raw_id -= 1 << 64
    return namespace, raw_id


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


def clean_text(value: Any) -> str:
    if value is None:
        return ""
    return str(value).strip()


def peer_type_for_id(peer_id: int) -> str:
    parts = postbox_peer_parts(peer_id)
    if parts is None:
        return "unknown"
    namespace, _ = parts
    if namespace == 0:
        return "user"
    if namespace == 1:
        return "group"
    if namespace == 2:
        return "channel"
    if namespace == 3:
        return "secret_chat"
    return "unknown"


def peer_access_hash(peer: Any) -> int:
    if not isinstance(peer, dict):
        return 0
    try:
        return int(peer.get("ah") or 0)
    except Exception:
        return 0


def load_peer_records(conn: Any, media_root: Path | None = None) -> dict[int, dict[str, Any]]:
    peers: dict[int, dict[str, Any]] = {}
    for key, value in conn.execute("SELECT key, value FROM t2"):
        if not isinstance(key, int) or not isinstance(value, bytes):
            continue
        try:
            peer = PostboxDecoder(value).decode_root_object()
        except Exception:
            continue
        display = peer_display(peer)
        access_hash = peer_access_hash(peer)
        first_name = clean_text(peer.get("fn") if isinstance(peer, dict) else "")
        last_name = clean_text(peer.get("ln") if isinstance(peer, dict) else "")
        title = clean_text(peer.get("t") if isinstance(peer, dict) else "")
        username = clean_text(peer.get("un") if isinstance(peer, dict) else "")
        phone = clean_text(peer.get("p") if isinstance(peer, dict) else "")
        avatar_path = cached_peer_avatar_path(peer, media_root) if media_root is not None else ""
        if display or access_hash or first_name or last_name or title or username or phone or avatar_path:
            peers[key] = {
                "display": display,
                "access_hash": access_hash,
                "first_name": first_name,
                "last_name": last_name,
                "title": title,
                "username": username,
                "phone": phone,
                "avatar_path": avatar_path,
            }
    return peers


def load_peer_map(conn: Any) -> dict[int, str]:
    return {peer_id: str(peer.get("display") or "") for peer_id, peer in load_peer_records(conn).items()}


def contact_for_peer(account_id: str, peer_id: int, peer: dict[str, Any], multi_account: bool) -> dict[str, Any] | None:
    jid = peer_store_id(account_id, peer_id, multi_account)
    peer_type = peer_type_for_id(peer_id)
    full_name = clean_text(peer.get("display") or peer.get("title"))
    first_name = clean_text(peer.get("first_name"))
    last_name = clean_text(peer.get("last_name"))
    username = clean_text(peer.get("username"))
    phone = clean_text(peer.get("phone"))
    avatar_path = clean_text(peer.get("avatar_path"))
    if not any([full_name, first_name, last_name, username, phone, avatar_path]) and peer_type == "unknown":
        return None
    return {
        "id": jid,
        "peer_type": peer_type,
        "phone": phone,
        "full_name": full_name,
        "first_name": first_name,
        "last_name": last_name,
        "business_name": "",
        "username": username,
        "lid": "",
        "about_text": "",
        "avatar_path": avatar_path,
        "updated_at": "",
    }


def read_source_records(source: PostboxSource, conn: Any, multi_account: bool) -> tuple[dict[str, str], list[dict[str, Any]], list[dict[str, Any]]]:
    media_root = source.db_path.parent.parent / "media"
    raw_peer_records = load_peer_records(conn, media_root)
    raw_peers = {peer_id: str(peer.get("display") or "") for peer_id, peer in raw_peer_records.items()}
    peers = {
        peer_store_id(source.account_id, peer_id, multi_account): display
        for peer_id, display in raw_peers.items()
    }
    contacts = [
        contact
        for peer_id, peer in raw_peer_records.items()
        if (contact := contact_for_peer(source.account_id, peer_id, peer, multi_account)) is not None
    ]
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
        media_path, media_size = cached_media_for(msg, media_root)
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
            "_account_id": source.account_id,
            "_access_hash": str((raw_peer_records.get(peer_id) or {}).get("access_hash") or 0),
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
            "media_title": media_title_for(msg),
            "media_path": media_path,
            "media_size": media_size,
            "embedded_media": msg.get("embedded_media") or [],
            "referenced_media_ids": msg.get("referenced_media_ids") or [],
        })
    return peers, contacts, messages


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
    embedded_media: list[Any] = []
    for _ in range(embedded_media_count):
        try:
            embedded_media.append(PostboxDecoder(reader.read_bytes()).decode_root_object())
        except Exception:
            continue
    referenced_media_ids = []
    for _ in range(reader.read_int32()):
        referenced_media_ids.append((reader.read_int32(), reader.read_int64()))
    return {
        "flags": flags,
        "tags": tags,
        "author_id": author_id,
        "text": text,
        "embedded_media_count": embedded_media_count,
        "embedded_media": embedded_media,
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


def media_resource_ids(value: Any) -> list[str]:
    ids: list[str] = []

    def visit(item: Any) -> None:
        if isinstance(item, dict):
            resource_type = item.get("@type")
            if resource_type == RESOURCE_TYPE_CLOUD_PHOTO_SIZE:
                ids.append(f"telegram-cloud-photo-size-{item.get('d')}-{item.get('i')}-{item.get('s')}")
            elif resource_type == RESOURCE_TYPE_CLOUD_DOCUMENT_SIZE:
                ids.append(f"telegram-cloud-document-size-{item.get('d')}-{item.get('i')}-{item.get('s')}")
            elif resource_type == RESOURCE_TYPE_CLOUD_DOCUMENT:
                ids.append(f"telegram-cloud-document-{item.get('d')}-{item.get('f')}")
            elif resource_type == RESOURCE_TYPE_CLOUD_PEER_PHOTO_SIZE:
                ids.append(f"telegram-peer-photo-size-{item.get('d')}-{item.get('s')}-{item.get('v')}-{item.get('l')}")
            elif resource_type == RESOURCE_TYPE_LOCAL_FILE:
                ids.append(f"telegram-local-file-{item.get('f')}")
            elif resource_type == RESOURCE_TYPE_LOCAL_FILE_REFERENCE:
                ids.append(f"local-file-{item.get('r')}")
            for nested in item.values():
                visit(nested)
        elif isinstance(item, list):
            for nested in item:
                visit(nested)

    visit(value)
    return list(dict.fromkeys(resource_id for resource_id in ids if resource_id and "None" not in resource_id))


def cached_media_for(msg: dict[str, Any], media_root: Path) -> tuple[str, int]:
    candidates: list[tuple[int, Path]] = []
    for item in msg.get("embedded_media") or []:
        for resource_id in media_resource_ids(item):
            for path in cached_media_paths(resource_id, media_root):
                candidates.append((path.stat().st_size, path))
    if not candidates:
        return "", 0
    size, path = max(candidates, key=lambda item: item[0])
    return str(path), size


def cached_peer_avatar_path(peer: Any, media_root: Path) -> str:
    if not isinstance(peer, dict):
        return ""
    candidates: list[tuple[int, Path]] = []
    for item in peer.get("ph") or []:
        for resource_id in media_resource_ids(item):
            for path in cached_media_paths(resource_id, media_root):
                candidates.append((path.stat().st_size, path))
    if not candidates:
        return ""
    _, path = max(candidates, key=lambda item: item[0])
    return str(path)


def cached_media_paths(resource_id: str, media_root: Path) -> list[Path]:
    paths: list[Path] = []
    exact = media_root / resource_id
    if is_complete_cache_file(exact, resource_id):
        paths.append(exact)
    paths.extend(media_cache_index(media_root).get(resource_id, []))
    return paths


def media_cache_index(media_root: Path) -> dict[str, list[Path]]:
    key = str(media_root)
    if key in MEDIA_CACHE_INDEXES:
        return MEDIA_CACHE_INDEXES[key]
    index: dict[str, list[Path]] = {}
    try:
        entries = list(media_root.iterdir())
    except OSError:
        MEDIA_CACHE_INDEXES[key] = index
        return index
    for path in entries:
        name = path.name
        if "." not in name or "_partial" in name or name.endswith(".meta") or not path.is_file():
            continue
        resource_id = name.rsplit(".", 1)[0]
        if resource_id:
            index.setdefault(resource_id, []).append(path)
    MEDIA_CACHE_INDEXES[key] = index
    return index


def is_complete_cache_file(path: Path, resource_id: str) -> bool:
    name = path.name
    if name != resource_id and not name.startswith(f"{resource_id}."):
        return False
    if "_partial" in name or name.endswith(".meta"):
        return False
    return path.is_file()


def media_title_for(msg: dict[str, Any]) -> str:
    def visit(item: Any) -> str:
        if isinstance(item, dict):
            value = item.get("fn")
            if isinstance(value, str) and value.strip():
                return value.strip()
            for nested in item.values():
                found = visit(nested)
                if found:
                    return found
        elif isinstance(item, list):
            for nested in item:
                found = visit(nested)
                if found:
                    return found
        return ""

    for item in msg.get("embedded_media") or []:
        found = visit(item)
        if found:
            return found
    return ""


def json_safe(value: Any) -> Any:
    if isinstance(value, dict):
        return {str(key): json_safe(nested) for key, nested in sorted(value.items(), key=lambda item: str(item[0]))}
    if isinstance(value, list):
        return [json_safe(nested) for nested in value]
    if isinstance(value, tuple):
        return [json_safe(nested) for nested in value]
    if isinstance(value, bytes):
        return {"base64": base64.b64encode(value).decode("ascii")}
    return value


def metadata_json(value: Any) -> str:
    return json.dumps(json_safe(value), ensure_ascii=False, sort_keys=True, separators=(",", ":"))


def first_string(value: Any, keys: Iterable[str]) -> str:
    if not isinstance(value, dict):
        return ""
    for key in keys:
        text = clean_text(value.get(key))
        if text:
            return text
    return ""


def first_url(text: str) -> str:
    match = URL_RE.search(text or "")
    if not match:
        return ""
    return match.group(0).rstrip(".,;:")


def service_action_title(value: dict[str, Any]) -> str:
    if any(key in value for key in ("d", "dr", "vc")):
        return "call"
    if "t" in value:
        return "title_change"
    if "m" in value:
        return "member_change"
    if "p" in value:
        return "pin"
    return "service_action"


def message_metadata(msg: dict[str, Any]) -> tuple[str, str, str, str]:
    for item in msg.get("embedded_media") or []:
        if not isinstance(item, dict):
            continue
        type_hash = item.get("@type")
        if type_hash == MESSAGE_METADATA_WEBPAGE:
            url = first_string(item, ("u", "du", "url"))
            title = first_string(item, ("ti", "title", "t", "tx"))
            return "web_page", title, url, metadata_json(item)
        if type_hash == MESSAGE_METADATA_LOCATION:
            title = first_string(item, ("adr", "t", "title"))
            return "location", title, "", metadata_json(item)
        if type_hash == MESSAGE_METADATA_POLL:
            title = first_string(item, ("t", "title"))
            return "poll", title, "", metadata_json(item)
        if type_hash == MESSAGE_METADATA_SERVICE_ACTION:
            title = service_action_title(item)
            return "service_action", title, "", metadata_json(item)

    url = first_url(str(msg.get("text") or ""))
    if url and msg.get("media_type") == "web_page":
        return "web_page", "", url, metadata_json({"url": url})
    return "", "", "", ""


def attach_message_metadata(messages: list[dict[str, Any]]) -> int:
    count = 0
    for msg in messages:
        metadata_type, title, url, raw_json = message_metadata(msg)
        if not metadata_type:
            continue
        msg["metadata_type"] = metadata_type
        msg["metadata_title"] = title
        msg["metadata_url"] = url
        msg["metadata_json"] = raw_json
        count += 1
    return count


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


def filter_contacts_for_messages(contacts: list[dict[str, Any]], messages: list[dict[str, Any]]) -> list[dict[str, Any]]:
    peer_ids = {
        str(value)
        for msg in messages
        for value in (msg.get("chat_id"), msg.get("sender_id"))
        if str(value or "").strip()
    }
    return [contact for contact in contacts if str(contact.get("id") or "") in peer_ids]


def import_source(source: PostboxSource, passcodes: list[bytes], multi_account: bool) -> tuple[dict[str, str], list[dict[str, Any]], list[dict[str, Any]]]:
    key = parse_tempkey(source.key_path, passcodes)
    conn = connect_postbox(source.db_path, key)
    try:
        return read_source_records(source, conn, multi_account)
    finally:
        conn.close()


def cloud_message_id(message_id: str) -> int | None:
    try:
        namespace, raw_id = message_id.split(":", 1)
        if int(namespace) != 0:
            return None
        value = int(raw_id)
    except Exception:
        return None
    return value if value > 0 else None


def cloud_media_key(msg: dict[str, Any]) -> tuple[int, int] | None:
    message_id = cloud_message_id(str(msg.get("message_id") or ""))
    if message_id is None:
        return None
    try:
        peer_id = postbox_peer_to_telethon_id(int(msg.get("_raw_chat_id") or 0))
    except Exception:
        return None
    if peer_id is None:
        return None
    return peer_id, message_id


def duplicate_media_key(msg: dict[str, Any]) -> tuple[str, int, int, str, str, str] | None:
    key = cloud_media_key(msg)
    if key is None:
        return None
    return (
        str(msg.get("_account_id") or ""),
        key[0],
        key[1],
        str(msg.get("timestamp") or ""),
        str(msg.get("media_type") or ""),
        str(msg.get("media_title") or ""),
    )


def message_resource_ids(msg: dict[str, Any]) -> list[str]:
    ids: list[str] = []
    for item in msg.get("embedded_media") or []:
        ids.extend(media_resource_ids(item))
    return list(dict.fromkeys(ids))


def has_remote_media_identity(msg: dict[str, Any]) -> bool:
    # Referenced-only Postbox rows still carry enough chat/message identity for Telethon fetch.
    return bool(message_resource_ids(msg) or msg.get("referenced_media_ids"))


def share_duplicate_media(messages: list[dict[str, Any]]) -> int:
    known: dict[tuple[str, int, int, str, str, str], tuple[str, int]] = {}
    for msg in messages:
        key = duplicate_media_key(msg)
        media_path = str(msg.get("media_path") or "")
        if key is None or not media_path:
            continue
        known.setdefault(key, (media_path, int(msg.get("media_size") or 0)))

    filled = 0
    for msg in messages:
        if msg.get("media_path") or not msg.get("media_type"):
            continue
        key = duplicate_media_key(msg)
        if key is None or key not in known:
            continue
        msg["media_path"], msg["media_size"] = known[key]
        filled += 1
    return filled


def share_resource_media(messages: list[dict[str, Any]]) -> int:
    known: dict[str, tuple[str, int]] = {}
    for msg in messages:
        media_path = str(msg.get("media_path") or "")
        if not media_path:
            continue
        media_size = int(msg.get("media_size") or 0)
        for resource_id in message_resource_ids(msg):
            previous = known.get(resource_id)
            if previous is None or media_size > previous[1]:
                known[resource_id] = (media_path, media_size)

    filled = 0
    for msg in messages:
        if msg.get("media_path") or not msg.get("media_type"):
            continue
        matches = [known[resource_id] for resource_id in message_resource_ids(msg) if resource_id in known]
        if not matches:
            continue
        msg["media_path"], msg["media_size"] = max(matches, key=lambda item: item[1])
        filled += 1
    return filled


def remote_media_candidates(messages: list[dict[str, Any]]) -> list[dict[str, Any]]:
    candidates = []
    for msg in messages:
        if msg.get("media_path") or not msg.get("media_type"):
            continue
        if not has_remote_media_identity(msg):
            continue
        if cloud_media_key(msg) is None:
            continue
        candidates.append(msg)
    return candidates


def remote_media_missing_count(messages: list[dict[str, Any]]) -> int:
    return len({key for msg in remote_media_candidates(messages) if (key := duplicate_media_key(msg)) is not None})


def load_existing_media_refs(path: str) -> dict[str, dict[str, Any]]:
    if not path:
        return {}
    data = json.loads(Path(path).read_text())
    refs: dict[str, dict[str, Any]] = {}
    for item in data:
        source_pk = str(item.get("source_pk") or "")
        media_path = str(item.get("media_path") or "")
        if source_pk and media_path:
            refs[source_pk] = item
    return refs


def apply_existing_media_refs(messages: list[dict[str, Any]], refs: dict[str, dict[str, Any]]) -> int:
    if not refs:
        return 0
    restored = 0
    for msg in messages:
        if msg.get("media_path"):
            continue
        ref = refs.get(str(msg.get("source_pk") or ""))
        if not ref:
            continue
        media_path = str(ref.get("media_path") or "")
        if not media_path:
            continue
        msg["media_path"] = media_path
        msg["media_size"] = int(ref.get("media_size") or 0)
        if not msg.get("media_type"):
            msg["media_type"] = str(ref.get("media_type") or "")
        if not msg.get("media_title"):
            msg["media_title"] = str(ref.get("media_title") or "")
        restored += 1
    return restored


def clear_unarchived_placeholder_media(messages: list[dict[str, Any]]) -> int:
    cleared = 0
    for msg in messages:
        if msg.get("media_path") or msg.get("media_type") not in {"web_page", "media"}:
            continue
        msg["media_type"] = ""
        msg["media_title"] = ""
        msg["media_size"] = 0
        cleared += 1
    return cleared


async def wait_remote(awaitable: Any, seconds: int) -> Any:
    return await asyncio.wait_for(awaitable, timeout=seconds)


def empty_remote_media_stats(missing: int = 0) -> dict[str, int]:
    return {
        "candidates": missing,
        "attempted": 0,
        "downloaded": 0,
        "missing": missing,
        "unavailable": 0,
        "timeouts": 0,
        "errors": 0,
    }


def add_remote_media_stats(left: dict[str, int], right: dict[str, int]) -> None:
    for key, value in right.items():
        if key == "missing" or key.startswith("_"):
            continue
        left[key] = int(left.get(key, 0)) + int(value or 0)


def remote_error_key(exc: Exception) -> str:
    return "timeouts" if isinstance(exc, TimeoutError) else "errors"


def input_peer_for_message(msg: dict[str, Any], telethon: Any) -> Any:
    try:
        peer_id = int(msg.get("_raw_chat_id") or 0)
        access_hash = int(msg.get("_access_hash") or 0)
    except Exception:
        return None
    parts = postbox_peer_parts(peer_id)
    if parts is None:
        return None
    namespace, raw_id = parts
    if namespace == 0 and access_hash:
        return telethon.types.InputPeerUser(raw_id, access_hash)
    if namespace == 1:
        return telethon.types.InputPeerChat(raw_id)
    if namespace == 2 and access_hash:
        return telethon.types.InputPeerChannel(raw_id, access_hash)
    return None


async def get_remote_message(client: Any, msg: dict[str, Any], telethon: Any) -> Any:
    message_id = cloud_message_id(str(msg.get("message_id") or ""))
    if message_id is None:
        return None
    peers: list[Any] = []
    try:
        peer_id = postbox_peer_to_telethon_id(int(msg.get("_raw_chat_id") or 0))
    except Exception:
        peer_id = None
    if peer_id is not None:
        peers.append(peer_id)
    input_peer = input_peer_for_message(msg, telethon)
    if input_peer is not None:
        peers.append(input_peer)
    first_error: Exception | None = None
    for peer in peers:
        try:
            return await wait_remote(client.get_messages(peer, ids=message_id), REMOTE_MESSAGE_TIMEOUT_SECONDS)
        except Exception as exc:
            if first_error is None:
                first_error = exc
            continue
    if first_error is not None:
        raise first_error
    return None


async def download_remote_message(client: Any, telegram_message: Any, message_dir: Path) -> tuple[str, int]:
    targets = [telegram_message]
    media = getattr(telegram_message, "media", None)
    if media is not None:
        targets.append(media)
        webpage = getattr(media, "webpage", None)
        if webpage is not None:
            targets.append(webpage)
            for attr in ("photo", "document"):
                nested = getattr(webpage, attr, None)
                if nested is not None:
                    targets.append(nested)
    seen: set[int] = set()
    for target in targets:
        if target is None or id(target) in seen:
            continue
        seen.add(id(target))
        path = await wait_remote(client.download_media(target, file=str(message_dir)), REMOTE_DOWNLOAD_TIMEOUT_SECONDS)
        if path and Path(path).is_file() and Path(path).stat().st_size > 0:
            return str(path), Path(path).stat().st_size
    return "", 0


async def download_remote_media_for_account(
    native_session: NativeSession,
    messages: list[dict[str, Any]],
    output_dir: Path,
    telethon: Any,
) -> dict[str, int]:
    from telethon.crypto import AuthKey
    from telethon.sessions import SQLiteSession

    session_dir = output_dir / ".sessions"
    session_dir.mkdir(parents=True, exist_ok=True)
    session_path = session_dir / hashlib.sha256(native_session.account_id.encode("utf-8")).hexdigest()
    session = SQLiteSession(str(session_path))
    session.set_dc(native_session.dc_id, native_session.host, native_session.port)
    session.auth_key = AuthKey(native_session.auth_key)
    session.save()

    client = telethon.TelegramClient(session, TELEGRAM_MAC_API_ID, TELEGRAM_MAC_API_HASH, receive_updates=False)
    stats = empty_remote_media_stats()
    dialogs_loaded = False
    try:
        await wait_remote(client.connect(), REMOTE_CONNECT_TIMEOUT_SECONDS)
        if not await wait_remote(client.is_user_authorized(), REMOTE_AUTH_TIMEOUT_SECONDS):
            stats["_auth_unavailable"] = len(messages)
            return stats
        for msg in messages:
            if cloud_message_id(str(msg.get("message_id") or "")) is None:
                stats["unavailable"] += 1
                continue
            stats["attempted"] += 1
            try:
                telegram_message = await get_remote_message(client, msg, telethon)
            except Exception as exc:
                if dialogs_loaded:
                    stats[remote_error_key(exc)] += 1
                    continue
                try:
                    await wait_remote(client.get_dialogs(limit=None), REMOTE_DIALOGS_TIMEOUT_SECONDS)
                    dialogs_loaded = True
                    telegram_message = await get_remote_message(client, msg, telethon)
                except Exception as fallback_exc:
                    stats[remote_error_key(fallback_exc)] += 1
                    continue
            if not telegram_message:
                stats["unavailable"] += 1
                continue
            message_dir = output_dir / hashlib.sha256(
                f"{native_session.account_id}:{msg['source_pk']}".encode("utf-8")
            ).hexdigest()
            message_dir.mkdir(parents=True, exist_ok=True)
            try:
                path, size = await download_remote_message(client, telegram_message, message_dir)
            except Exception as exc:
                stats[remote_error_key(exc)] += 1
                continue
            if path:
                msg["media_path"] = str(path)
                msg["media_size"] = size
                stats["downloaded"] += 1
            else:
                stats["unavailable"] += 1
    finally:
        try:
            await wait_remote(client.disconnect(), REMOTE_CONNECT_TIMEOUT_SECONDS)
        except Exception:
            pass
    return stats


async def download_remote_media_async(
    messages: list[dict[str, Any]],
    sources_by_account: dict[str, PostboxSource],
    output_dir: Path,
    telethon: Any,
) -> dict[str, int]:
    share_duplicate_media(messages)
    share_resource_media(messages)
    candidates = remote_media_candidates(messages)
    if not candidates:
        return empty_remote_media_stats()
    stats = empty_remote_media_stats(remote_media_missing_count(messages))
    sessions = {
        account_id: native_session
        for account_id, source in sources_by_account.items()
        if (native_session := native_session_for_source(source)) is not None
    }
    if not sessions:
        clear_unarchived_placeholder_media(messages)
        stats["unavailable"] = len(candidates)
        stats["missing"] = remote_media_missing_count(messages)
        return stats

    by_account: dict[str, list[dict[str, Any]]] = {}
    for msg in candidates:
        by_account.setdefault(str(msg.get("_account_id") or ""), []).append(msg)
    for account_id, rows in by_account.items():
        preferred = [sessions[account_id]] if account_id in sessions else []
        fallbacks = [session for session_id, session in sessions.items() if session_id != account_id]
        for native_session in preferred + fallbacks:
            try:
                result = await download_remote_media_for_account(native_session, rows, output_dir, telethon)
            except Exception:
                stats["errors"] += len(rows)
                break
            if result.get("_auth_unavailable"):
                continue
            add_remote_media_stats(stats, result)
            break
        else:
            stats["unavailable"] += len(rows)
        share_duplicate_media(messages)
        share_resource_media(messages)
    clear_unarchived_placeholder_media(messages)
    stats["missing"] = remote_media_missing_count(messages)
    return stats


def download_remote_media(
    messages: list[dict[str, Any]],
    sources_by_account: dict[str, PostboxSource],
    media_output_dir: str,
) -> dict[str, int]:
    missing = remote_media_missing_count(messages)
    if not media_output_dir or missing == 0:
        return empty_remote_media_stats(missing)
    try:
        telethon = importlib.import_module("telethon")
    except ImportError:
        clear_unarchived_placeholder_media(messages)
        stats = empty_remote_media_stats(missing)
        stats["missing"] = remote_media_missing_count(messages)
        stats["errors"] = stats["missing"]
        return stats
    output_dir = Path(media_output_dir).expanduser()
    output_dir.mkdir(parents=True, exist_ok=True)
    try:
        return asyncio.run(download_remote_media_async(messages, sources_by_account, output_dir, telethon))
    except Exception:
        clear_unarchived_placeholder_media(messages)
        stats = empty_remote_media_stats(remote_media_missing_count(messages))
        stats["errors"] = stats["missing"]
        return stats


def build_result(
    source_path: str,
    peers: dict[str, str],
    contacts: list[dict[str, Any]],
    messages: list[dict[str, Any]],
    started: dt.datetime,
    remote_media: dict[str, int] | None = None,
) -> dict[str, Any]:
    attach_message_metadata(messages)
    clear_unarchived_placeholder_media(messages)
    chats: dict[str, dict[str, Any]] = {}
    for msg in messages:
        msg.pop("_ts", None)
        msg.pop("_raw_chat_id", None)
        msg.pop("_account_id", None)
        msg.pop("_access_hash", None)
        msg.pop("embedded_media", None)
        msg.pop("referenced_media_ids", None)
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
        "remote_media": remote_media or {"downloaded": 0, "missing": 0},
        "chats": sorted(chats.values(), key=lambda c: c["last_message_at"], reverse=True),
        "contacts": sorted(contacts, key=lambda c: c["id"]),
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


def fixture_kv_int32(key: str, value: int) -> bytes:
    return fixture_short_str(key) + struct.pack("<Bi", 0, value)


def fixture_kv_int64(key: str, value: int) -> bytes:
    return fixture_short_str(key) + struct.pack("<Bq", 1, value)


def fixture_kv_double(key: str, value: float) -> bytes:
    return fixture_short_str(key) + struct.pack("<Bd", 3, value)


def fixture_kv_object(key: str, payload: bytes, type_hash: int) -> bytes:
    return fixture_short_str(key) + struct.pack("B", 5) + fixture_object(payload, type_hash)


def fixture_object(payload: bytes, type_hash: int = 0x12345678) -> bytes:
    return struct.pack("<ii", type_hash, len(payload)) + payload


def fixture_root_object(payload: bytes) -> bytes:
    return fixture_short_str("_") + struct.pack("B", 5) + fixture_object(payload)


def fixture_root_typed_object(payload: bytes, type_hash: int) -> bytes:
    return fixture_short_str("_") + struct.pack("B", 5) + fixture_object(payload, type_hash)


def fixture_peer(first: str, last: str = "", username: str = "", phone: str = "") -> bytes:
    payload = fixture_kv_string("fn", first) + fixture_kv_string("ln", last)
    if username:
        payload += fixture_kv_string("un", username)
    if phone:
        payload += fixture_kv_string("p", phone)
    return fixture_root_object(payload)


def fixture_message(
    text: str = "fixture hello",
    author_id: int | None = 4242,
    tags: int = 1 << 0,
    embedded_media: list[bytes] | None = None,
    referenced_media_ids: list[tuple[int, int]] | None = None,
) -> bytes:
    if embedded_media is None:
        embedded_media = []
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
    out += struct.pack("<i", len(embedded_media))
    for media in embedded_media:
        out += fixture_bytes(media)
    out += struct.pack("<i", len(referenced_media_ids))
    for namespace, media_id in referenced_media_ids:
        out += struct.pack("<iq", namespace, media_id)
    return bytes(out)


def fixture_document_media(file_id: int = 987654321, file_name: str = "fixture.mp4") -> bytes:
    resource = (
        fixture_kv_int32("d", 2)
        + fixture_kv_int64("f", file_id)
        + fixture_kv_int64("a", 123)
        + fixture_kv_int64("n64", 5)
        + fixture_kv_string("fn", file_name)
    )
    media = fixture_kv_object("r", resource, RESOURCE_TYPE_CLOUD_DOCUMENT) + fixture_kv_string("mt", "video/mp4")
    return fixture_root_typed_object(media, 665733176)


def fixture_photo_media(photo_id: int = 123456789) -> bytes:
    small = (
        fixture_kv_int32("d", 4)
        + fixture_kv_int64("i", photo_id)
        + fixture_kv_int64("a", 321)
        + fixture_kv_string("s", "s")
    )
    large = (
        fixture_kv_int32("d", 4)
        + fixture_kv_int64("i", photo_id)
        + fixture_kv_int64("a", 321)
        + fixture_kv_string("s", "x")
    )
    media = (
        fixture_kv_object("small", small, RESOURCE_TYPE_CLOUD_PHOTO_SIZE)
        + fixture_kv_object("large", large, RESOURCE_TYPE_CLOUD_PHOTO_SIZE)
    )
    return fixture_root_typed_object(media, -1951522668)


def fixture_webpage_media() -> bytes:
    media = fixture_kv_string("u", "https://example.com/article") + fixture_kv_string("ti", "Example Article")
    return fixture_root_typed_object(media, MESSAGE_METADATA_WEBPAGE)


def fixture_location_media() -> bytes:
    media = (
        fixture_kv_double("la", 52.1)
        + fixture_kv_double("lo", 4.3)
        + fixture_kv_string("adr", "Example Place")
    )
    return fixture_root_typed_object(media, MESSAGE_METADATA_LOCATION)


def fixture_poll_media() -> bytes:
    media = fixture_kv_string("t", "Example Poll")
    return fixture_root_typed_object(media, MESSAGE_METADATA_POLL)


def fixture_service_action_media(action: str = "call") -> bytes:
    if action == "title_change":
        media = fixture_kv_string("t", "Example Title")
    elif action == "member_change":
        media = fixture_kv_int32("m", 1001)
    elif action == "pin":
        media = fixture_kv_int32("p", 2002)
    else:
        media = fixture_kv_int32("d", 42) + fixture_kv_int32("dr", 10)
    return fixture_root_typed_object(media, MESSAGE_METADATA_SERVICE_ACTION)


def fixture_message_key(peer_id: int, namespace: int, timestamp: int, message_id: int) -> bytes:
    return struct.pack(">qiii", peer_id, namespace, timestamp, message_id)


def fixture_postbox_peer_id(namespace: int, raw_id: int) -> int:
    value = ((raw_id >> 32) << 35) | ((namespace & 7) << 32) | (raw_id & 0xFFFFFFFF)
    return value - (1 << 64) if value >= 1 << 63 else value


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

    media_message = read_message(fixture_message(embedded_media=[fixture_document_media()], referenced_media_ids=[]))
    if not media_message:
        raise AssertionError("embedded media fixture decode returned no message")
    resource_ids = media_resource_ids(media_message["embedded_media"])
    if resource_ids != ["telegram-cloud-document-2-987654321"]:
        raise AssertionError(f"embedded media resource decode failed: {resource_ids!r}")
    if media_title_for(media_message) != "fixture.mp4":
        raise AssertionError(f"embedded media title decode failed: {media_title_for(media_message)!r}")
    with tempfile.TemporaryDirectory() as tmp:
        cached = Path(tmp) / "telegram-cloud-document-2-987654321"
        cached.write_bytes(b"media")
        cached_path, cached_size = cached_media_for(media_message, Path(tmp))
        if cached_path != str(cached) or cached_size != 5:
            raise AssertionError(f"cached media lookup failed: {(cached_path, cached_size)!r}")

    photo_message = read_message(fixture_message(embedded_media=[fixture_photo_media()], referenced_media_ids=[]))
    if not photo_message:
        raise AssertionError("photo media fixture decode returned no message")
    photo_resource_ids = media_resource_ids(photo_message["embedded_media"])
    expected_photo_ids = [
        "telegram-cloud-photo-size-4-123456789-s",
        "telegram-cloud-photo-size-4-123456789-x",
    ]
    if photo_resource_ids != expected_photo_ids:
        raise AssertionError(f"photo media resource decode failed: {photo_resource_ids!r}")
    with tempfile.TemporaryDirectory() as tmp:
        small = Path(tmp) / "telegram-cloud-photo-size-4-123456789-s"
        large = Path(tmp) / "telegram-cloud-photo-size-4-123456789-x.jpg"
        partial = Path(tmp) / "telegram-cloud-photo-size-4-123456789-x_partial"
        small.write_bytes(b"1")
        large.write_bytes(b"larger")
        partial.write_bytes(b"not complete")
        cached_path, cached_size = cached_media_for(photo_message, Path(tmp))
        if cached_path != str(large) or cached_size != 6:
            raise AssertionError(f"largest cached photo lookup failed: {(cached_path, cached_size)!r}")

    peer_photo = {
        "@type": 1774815102,
        "r": {"@type": RESOURCE_TYPE_CLOUD_PEER_PHOTO_SIZE, "d": 2, "s": 1, "v": 333, "l": 444},
    }
    if media_resource_ids(peer_photo) != ["telegram-peer-photo-size-2-1-333-444"]:
        raise AssertionError(f"peer photo resource decode failed: {peer_photo!r}")
    with tempfile.TemporaryDirectory() as tmp:
        avatar = Path(tmp) / "telegram-peer-photo-size-2-1-333-444.jpg"
        avatar.write_bytes(b"avatar")
        got = cached_peer_avatar_path({"ph": [peer_photo]}, Path(tmp))
        if got != str(avatar):
            raise AssertionError(f"cached peer avatar lookup failed: {got!r}")

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

    if account_dir_record_id("account-10833815886710207757") != -7612928186999343859:
        raise AssertionError("native account directory id decode failed")
    if postbox_peer_to_telethon_id(fixture_postbox_peer_id(0, 777000)) != 777000:
        raise AssertionError("user peer id conversion failed")
    if postbox_peer_to_telethon_id(fixture_postbox_peer_id(1, 42)) != -42:
        raise AssertionError("group peer id conversion failed")
    if postbox_peer_to_telethon_id(fixture_postbox_peer_id(2, 42)) != -1_000_000_000_042:
        raise AssertionError("channel peer id conversion failed")
    if postbox_peer_to_telethon_id(fixture_postbox_peer_id(3, 42)) is not None:
        raise AssertionError("secret chat peer id should not be remotely fetched")
    remote_resource = PostboxDecoder(fixture_photo_media(77)).decode_root_object()
    remote_sample = [
        {"_raw_chat_id": str(fixture_postbox_peer_id(0, 777000)), "message_id": "0:1", "media_type": "photo", "media_path": "", "embedded_media": [remote_resource]},
        {"_raw_chat_id": str(fixture_postbox_peer_id(0, 777000)), "message_id": "0:2", "media_type": "photo", "media_path": "cached", "embedded_media": [remote_resource]},
        {"_raw_chat_id": str(fixture_postbox_peer_id(3, 42)), "message_id": "0:3", "media_type": "photo", "media_path": "", "embedded_media": [remote_resource]},
        {"_raw_chat_id": str(fixture_postbox_peer_id(0, 777000)), "message_id": "1:4", "media_type": "photo", "media_path": "", "embedded_media": [remote_resource]},
        {"_raw_chat_id": str(fixture_postbox_peer_id(0, 777000)), "message_id": "0:5", "media_type": "", "media_path": "", "embedded_media": [remote_resource]},
        {"_raw_chat_id": str(fixture_postbox_peer_id(0, 777000)), "message_id": "0:6", "media_type": "web_page", "media_path": "", "embedded_media": []},
        {"_raw_chat_id": str(fixture_postbox_peer_id(0, 777000)), "message_id": "0:7", "media_type": "media", "media_path": "", "embedded_media": [], "referenced_media_ids": [(7, 123456789)]},
    ]
    if [row["message_id"] for row in remote_media_candidates(remote_sample)] != ["0:1", "0:7"]:
        raise AssertionError(f"remote media candidate selection failed: {remote_sample!r}")
    if remote_media_missing_count(remote_sample) != 2:
        raise AssertionError(f"remote missing count failed: {remote_sample!r}")
    existing_ref_sample = [
        {"source_pk": 10, "message_id": "0:10", "media_type": "photo", "media_path": ""},
        {"source_pk": 11, "message_id": "0:11", "media_type": "", "media_path": ""},
    ]
    restored = apply_existing_media_refs(existing_ref_sample, {
        "10": {"source_pk": 10, "media_path": "/tmp/already-archived", "media_size": 44},
        "11": {"source_pk": 11, "media_type": "gif", "media_title": "loop", "media_path": "/tmp/loop", "media_size": 55},
    })
    if restored != 2 or remote_media_candidates(existing_ref_sample):
        raise AssertionError(f"existing media refs should suppress remote fetch: {existing_ref_sample!r}")
    if existing_ref_sample[1]["media_type"] != "gif" or existing_ref_sample[1]["media_title"] != "loop":
        raise AssertionError(f"existing media refs should restore durable metadata: {existing_ref_sample!r}")
    placeholder_sample = [
        {"media_type": "web_page", "media_title": "preview", "media_path": "", "media_size": 10},
        {"media_type": "media", "media_title": "generic", "media_path": "", "media_size": 10},
        {"media_type": "web_page", "media_title": "cached", "media_path": "/tmp/cached", "media_size": 10},
        {"media_type": "photo", "media_title": "", "media_path": "", "media_size": 0},
    ]
    if clear_unarchived_placeholder_media(placeholder_sample) != 2:
        raise AssertionError(f"placeholder clear count failed: {placeholder_sample!r}")
    if [row["media_type"] for row in placeholder_sample] != ["", "", "web_page", "photo"]:
        raise AssertionError(f"placeholder media clear failed: {placeholder_sample!r}")

    metadata_sample = [
        {"text": "", "media_type": "web_page", "embedded_media": [PostboxDecoder(fixture_webpage_media()).decode_root_object()]},
        {"text": "", "media_type": "media", "embedded_media": [PostboxDecoder(fixture_location_media()).decode_root_object()]},
        {"text": "", "media_type": "media", "embedded_media": [PostboxDecoder(fixture_poll_media()).decode_root_object()]},
        {"text": "", "media_type": "media", "embedded_media": [PostboxDecoder(fixture_service_action_media()).decode_root_object()]},
        {"text": "see https://example.com/from-text.", "media_type": "web_page", "embedded_media": []},
    ]
    if attach_message_metadata(metadata_sample) != 5:
        raise AssertionError(f"metadata attach count failed: {metadata_sample!r}")
    if [row["metadata_type"] for row in metadata_sample] != ["web_page", "location", "poll", "service_action", "web_page"]:
        raise AssertionError(f"metadata type decode failed: {metadata_sample!r}")
    if metadata_sample[0]["metadata_url"] != "https://example.com/article":
        raise AssertionError(f"web page URL decode failed: {metadata_sample!r}")
    if metadata_sample[1]["metadata_title"] != "Example Place" or metadata_sample[2]["metadata_title"] != "Example Poll":
        raise AssertionError(f"location/poll metadata title failed: {metadata_sample!r}")
    if metadata_sample[3]["metadata_title"] != "call":
        raise AssertionError(f"service metadata title failed: {metadata_sample!r}")
    if metadata_sample[4]["metadata_url"] != "https://example.com/from-text":
        raise AssertionError(f"text URL metadata failed: {metadata_sample!r}")
    service_samples = [
        {"text": "", "media_type": "media", "embedded_media": [PostboxDecoder(fixture_service_action_media(action)).decode_root_object()]}
        for action in ("call", "title_change", "member_change", "pin")
    ]
    if attach_message_metadata(service_samples) != 4:
        raise AssertionError(f"service metadata attach count failed: {service_samples!r}")
    if [row["metadata_title"] for row in service_samples] != ["call", "title_change", "member_change", "pin"]:
        raise AssertionError(f"service metadata subtype failed: {service_samples!r}")
    import_module = importlib.import_module
    try:
        def missing_telethon(name: str, package: str | None = None) -> Any:
            if name == "telethon":
                raise ImportError("fixture missing telethon")
            return import_module(name, package)

        importlib.import_module = missing_telethon
        result = download_remote_media(remote_sample, {}, "/tmp/telecrawl-unused-media")
    finally:
        importlib.import_module = import_module
    if result != {
        "candidates": 2,
        "attempted": 0,
        "downloaded": 0,
        "missing": 1,
        "unavailable": 0,
        "timeouts": 0,
        "errors": 1,
    }:
        raise AssertionError(f"remote media import failure should be best-effort: {result!r}")

    async def never_finishes() -> None:
        await asyncio.sleep(1)

    try:
        asyncio.run(wait_remote(never_finishes(), 0.001))
        raise AssertionError("remote timeout did not fire")
    except (TimeoutError, asyncio.TimeoutError):
        pass

    duplicate_sample = [
        {
            "_account_id": "account-a",
            "_raw_chat_id": str(fixture_postbox_peer_id(0, 777000)),
            "message_id": "0:7",
            "timestamp": "2026-01-01T00:00:00Z",
            "media_type": "photo",
            "media_title": "",
            "media_path": "/tmp/media-a",
            "media_size": 12,
        },
        {
            "_account_id": "account-a",
            "_raw_chat_id": str(fixture_postbox_peer_id(0, 777000)),
            "message_id": "0:7",
            "timestamp": "2026-01-01T00:00:00Z",
            "media_type": "photo",
            "media_title": "",
            "media_path": "",
            "media_size": 0,
        },
        {
            "_account_id": "account-a",
            "_raw_chat_id": str(fixture_postbox_peer_id(0, 777000)),
            "message_id": "0:7",
            "timestamp": "2026-01-01T00:00:01Z",
            "media_type": "photo",
            "media_title": "",
            "media_path": "",
            "media_size": 0,
        },
        {
            "_account_id": "account-b",
            "_raw_chat_id": str(fixture_postbox_peer_id(0, 777000)),
            "message_id": "0:7",
            "timestamp": "2026-01-01T00:00:00Z",
            "media_type": "photo",
            "media_title": "",
            "media_path": "",
            "media_size": 0,
        },
    ]
    if share_duplicate_media(duplicate_sample) != 1:
        raise AssertionError(f"duplicate media sharing count failed: {duplicate_sample!r}")
    if duplicate_sample[1]["media_path"] != "/tmp/media-a" or duplicate_sample[1]["media_size"] != 12:
        raise AssertionError(f"duplicate media sharing failed: {duplicate_sample!r}")
    if duplicate_sample[2]["media_path"]:
        raise AssertionError(f"duplicate media sharing ignored timestamp: {duplicate_sample!r}")
    if duplicate_sample[3]["media_path"]:
        raise AssertionError(f"duplicate media sharing crossed accounts: {duplicate_sample!r}")

    resource_photo = PostboxDecoder(fixture_photo_media(photo_id=99)).decode_root_object()
    resource_sample = [
        {
            "_account_id": "account-a",
            "media_type": "photo",
            "media_path": "/tmp/resource-a",
            "media_size": 12,
            "embedded_media": [resource_photo],
        },
        {
            "_account_id": "account-b",
            "media_type": "photo",
            "media_path": "",
            "media_size": 0,
            "embedded_media": [resource_photo],
        },
    ]
    if share_resource_media(resource_sample) != 1:
        raise AssertionError(f"resource media sharing count failed: {resource_sample!r}")
    if resource_sample[1]["media_path"] != "/tmp/resource-a" or resource_sample[1]["media_size"] != 12:
        raise AssertionError(f"resource media sharing failed: {resource_sample!r}")

    public_sources = [
        PostboxSource("stable/account-a", Path("unused-key-a"), Path("account-a.db")),
        PostboxSource("stable/account-b", Path("unused-key-b"), Path("account-b.db")),
    ]
    public_connections = [
        FixturePostboxConnection(
            {
                100: fixture_peer("Fixture", "A"),
                4242: fixture_peer("Sender", "A", "sendera", "+15550000001"),
                9999: fixture_peer("Unused", "A", "unuseda", "+15559990001"),
            },
            [(fixture_message_key(100, 0, 1_421_404_800, 1), fixture_message("public account a"))],
        ),
        FixturePostboxConnection(
            {
                100: fixture_peer("Fixture", "B"),
                4242: fixture_peer("Sender", "B", "senderb", "+15550000002"),
                9999: fixture_peer("Unused", "B", "unusedb", "+15559990002"),
            },
            [(fixture_message_key(100, 0, 1_421_404_801, 1), fixture_message("public account b"))],
        ),
    ]
    public_peers: dict[str, str] = {}
    public_contacts: list[dict[str, Any]] = []
    public_messages: list[dict[str, Any]] = []
    if len(public_sources) != len(public_connections):
        raise AssertionError("public fixture source/connection mismatch")
    for source, conn in zip(public_sources, public_connections):
        peers, contacts, messages = read_source_records(source, conn, multi_account=True)
        public_peers.update(peers)
        public_contacts.extend(contacts)
        public_messages.extend(messages)
    public_filtered = filter_chat(public_messages, "100")
    if len(public_filtered) != 2:
        raise AssertionError(f"public raw chat filter returned {len(public_filtered)} messages")
    if public_filtered[0]["chat_id"] == public_filtered[1]["chat_id"]:
        raise AssertionError("public multi-account import collapsed distinct chats")
    if public_filtered[0]["source_pk"] == public_filtered[1]["source_pk"]:
        raise AssertionError("public multi-account import collided source keys")
    public_filtered_contacts = filter_contacts_for_messages(public_contacts, public_filtered)
    if len(public_contacts) != 6 or len(public_filtered_contacts) != 4:
        raise AssertionError(f"public contact filtering shape failed: {public_contacts!r} -> {public_filtered_contacts!r}")
    if any(contact["username"].startswith("unused") for contact in public_filtered_contacts):
        raise AssertionError(f"public contact filtering kept unrelated contacts: {public_filtered_contacts!r}")
    public_result = build_result("fixture-postbox", public_peers, public_filtered_contacts, public_filtered, dt.datetime(2026, 1, 1, tzinfo=dt.timezone.utc))
    if len(public_result["chats"]) != 2 or len(public_result["messages"]) != 2:
        raise AssertionError(f"public import result shape failed: {public_result!r}")
    if len(public_result["contacts"]) != 4:
        raise AssertionError(f"public contacts shape failed: {public_result!r}")
    if not any(contact["phone"] == "+15550000001" and contact["username"] == "sendera" for contact in public_result["contacts"]):
        raise AssertionError(f"public contact enrichment failed: {public_result!r}")
    if any("embedded_media" in msg for msg in public_result["messages"]):
        raise AssertionError(f"public import leaked internal media records: {public_result!r}")
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
    parser.add_argument("--fetch-media", action="store_true")
    parser.add_argument("--media-output-dir", default="")
    parser.add_argument("--existing-media-refs", default="")
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
    sources_by_account = {source.account_id: source for source in sources}
    all_peers: dict[str, str] = {}
    all_contacts: dict[str, dict[str, Any]] = {}
    by_identity: dict[tuple[str, str, str], dict[str, Any]] = {}
    for source in sources:
        peers, contacts, messages = import_source(source, passcodes, multi_account)
        all_peers.update(peers)
        for contact in contacts:
            all_contacts[contact["id"]] = contact
        for msg in messages:
            by_identity[(source.account_id, msg["chat_id"], msg["message_id"])] = msg

    filtered = filter_chat(list(by_identity.values()), args.chat)
    if args.chat and not filtered:
        raise SystemExit(f"could not find chat in Postbox cache: {args.chat}")
    limited = apply_limits(filtered, args.dialogs_limit, args.messages_limit)
    share_duplicate_media(limited)
    share_resource_media(limited)
    if apply_existing_media_refs(limited, load_existing_media_refs(args.existing_media_refs)):
        share_duplicate_media(limited)
        share_resource_media(limited)
    remote_media = {"downloaded": 0, "missing": 0}
    if args.fetch_media:
        remote_media = download_remote_media(limited, sources_by_account, args.media_output_dir)
        share_duplicate_media(limited)
        share_resource_media(limited)
    source_path = str(Path(args.source).expanduser()) if args.source else str(default_group_path())
    contacts = list(all_contacts.values())
    if args.chat:
        contacts = filter_contacts_for_messages(contacts, limited)
    json.dump(build_result(source_path, all_peers, contacts, limited, started, remote_media), sys.stdout, separators=(",", ":"))


if __name__ == "__main__":
    main()
