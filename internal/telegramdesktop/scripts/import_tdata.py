#!/usr/bin/env python3
import argparse
import asyncio
import hashlib
import json
from datetime import datetime, timezone

from opentele2.api import UseCurrentSession
from opentele2.td import TDesktop


def iso(dt):
    if not dt:
        return ""
    if dt.tzinfo is None:
        dt = dt.replace(tzinfo=timezone.utc)
    return dt.astimezone(timezone.utc).isoformat()


def stable_pk(chat_id, message_id):
    digest = hashlib.blake2b(f"{chat_id}:{message_id}".encode(), digest_size=8).digest()
    value = int.from_bytes(digest, "big", signed=False) & ((1 << 63) - 1)
    return value or 1


def entity_kind(entity):
    name = type(entity).__name__.lower()
    if "user" in name:
        return "user"
    if "channel" in name:
        return "channel"
    if "chat" in name:
        return "group"
    return name or "unknown"


def display_name(entity, fallback):
    for attr in ("title", "first_name", "last_name", "username"):
        value = getattr(entity, attr, None)
        if value:
            if attr == "first_name":
                last = getattr(entity, "last_name", None)
                return f"{value} {last}".strip() if last else value
            return value
    return fallback or str(getattr(entity, "id", ""))


def media_type(message):
    media = getattr(message, "media", None)
    if not media:
        return ""
    name = type(media).__name__
    return name.replace("MessageMedia", "").lower() or name.lower()


async def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--tdata", required=True)
    parser.add_argument("--session", required=True)
    parser.add_argument("--dialogs-limit", type=int, default=200)
    parser.add_argument("--messages-limit", type=int, default=500)
    args = parser.parse_args()

    started = datetime.now(timezone.utc)
    td = TDesktop(args.tdata)
    if not td.isLoaded():
        raise SystemExit("tdata did not load")
    client = await td.ToTelethon(session=args.session, flag=UseCurrentSession)
    await client.connect()
    if not await client.is_user_authorized():
        raise SystemExit("Telegram session is not authorized")

    dialogs = await client.get_dialogs(limit=None if args.dialogs_limit <= 0 else args.dialogs_limit)
    out_chats = []
    out_messages = []
    for dialog in dialogs:
        entity = dialog.entity
        chat_id = str(dialog.id)
        chat_name = display_name(entity, getattr(dialog, "name", ""))
        limit = None if args.messages_limit <= 0 else args.messages_limit
        messages = await client.get_messages(entity, limit=limit)
        last_message_at = None
        for msg in messages:
            if not getattr(msg, "id", None):
                continue
            if getattr(msg, "date", None) and (last_message_at is None or msg.date > last_message_at):
                last_message_at = msg.date
            sender_id = ""
            sender = getattr(msg, "sender", None)
            if sender is not None:
                sender_id = str(getattr(sender, "id", "") or "")
            elif getattr(msg, "sender_id", None):
                sender_id = str(msg.sender_id)
            sender_name = display_name(sender, "") if sender else ""
            text = getattr(msg, "message", "") or ""
            out_messages.append(
                {
                    "source_pk": stable_pk(chat_id, msg.id),
                    "chat_id": chat_id,
                    "chat_name": chat_name,
                    "message_id": str(msg.id),
                    "sender_id": sender_id,
                    "sender_name": sender_name,
                    "timestamp": iso(getattr(msg, "date", None)),
                    "from_me": bool(getattr(msg, "out", False)),
                    "text": text,
                    "message_type": type(msg).__name__,
                    "media_type": media_type(msg),
                    "media_title": "",
                }
            )
        out_chats.append(
            {
                "id": chat_id,
                "kind": entity_kind(entity),
                "name": chat_name,
                "username": getattr(entity, "username", "") or "",
                "last_message_at": iso(last_message_at),
                "unread_count": int(getattr(dialog, "unread_count", 0) or 0),
                "message_count": len(messages),
            }
        )

    await client.disconnect()
    print(
        json.dumps(
            {
                "source_path": args.tdata,
                "started_at": iso(started),
                "finished_at": iso(datetime.now(timezone.utc)),
                "chats": out_chats,
                "messages": out_messages,
            },
            ensure_ascii=False,
        )
    )


asyncio.run(main())
