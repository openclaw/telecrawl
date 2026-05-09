#!/usr/bin/env python3
import argparse
import asyncio
import hashlib
import json
from datetime import datetime, timezone

from telethon import functions, utils
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


def peer_id(peer):
    if not peer:
        return ""
    try:
        return str(utils.get_peer_id(peer))
    except Exception:
        pass
    for attr in ("channel_id", "chat_id", "user_id", "id"):
        value = getattr(peer, attr, None)
        if value:
            return str(value)
    return ""


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


def to_dict_json(value):
    if not value:
        return ""
    try:
        data = value.to_dict()
    except Exception:
        return ""
    if not data:
        return ""
    return json.dumps(data, ensure_ascii=False, default=str, sort_keys=True)


def media_title(message):
    file = getattr(message, "file", None)
    if file is not None:
        for attr in ("name", "title"):
            value = getattr(file, attr, None)
            if value:
                return str(value)
    media = getattr(message, "media", None)
    webpage = getattr(media, "webpage", None)
    for attr in ("title", "site_name", "url"):
        value = getattr(webpage, attr, None)
        if value:
            return str(value)
    return ""


def media_size(message):
    file = getattr(message, "file", None)
    value = getattr(file, "size", None) if file is not None else None
    try:
        return int(value or 0)
    except Exception:
        return 0


def title_text(value):
    if not value:
        return ""
    if isinstance(value, str):
        return value
    text = getattr(value, "text", None)
    if text:
        return text
    return str(value)


async def load_folders(client):
    folders = []
    memberships = {}
    try:
        result = await client(functions.messages.GetDialogFiltersRequest())
    except Exception:
        return folders, memberships
    filters = getattr(result, "filters", result) or []
    for folder in filters:
        folder_id = getattr(folder, "id", None)
        if folder_id is None:
            folder_id = 0
        title = title_text(getattr(folder, "title", "")) or ("All" if folder_id == 0 else "")
        flags = {
            "contacts": bool(getattr(folder, "contacts", False)),
            "non_contacts": bool(getattr(folder, "non_contacts", False)),
            "groups": bool(getattr(folder, "groups", False)),
            "broadcasts": bool(getattr(folder, "broadcasts", False)),
            "bots": bool(getattr(folder, "bots", False)),
            "exclude_muted": bool(getattr(folder, "exclude_muted", False)),
            "exclude_read": bool(getattr(folder, "exclude_read", False)),
            "exclude_archived": bool(getattr(folder, "exclude_archived", False)),
        }
        folders.append(
            {
                "id": str(folder_id),
                "title": title,
                "emoticon": getattr(folder, "emoticon", "") or "",
                "color": int(getattr(folder, "color", 0) or 0),
                "flags_json": json.dumps(flags, sort_keys=True),
            }
        )
        explicit = []
        for attr in ("include_peers", "pinned_peers"):
            for peer in getattr(folder, attr, None) or []:
                pid = peer_id(peer)
                if pid:
                    explicit.append(pid)
        memberships.setdefault(str(folder_id), set()).update(explicit)
        if folder_id:
            try:
                for dialog in await client.get_dialogs(limit=None, folder=int(folder_id)):
                    memberships.setdefault(str(folder_id), set()).add(str(dialog.id))
            except Exception:
                pass
    return folders, memberships


async def load_topics(client, entity, chat_id, limit=100):
    if not bool(getattr(entity, "forum", False)):
        return []
    out = []
    offset_date = None
    offset_id = 0
    offset_topic = 0
    seen = set()
    while True:
        try:
            result = await client(
                functions.messages.GetForumTopicsRequest(
                    peer=entity,
                    offset_date=offset_date,
                    offset_id=offset_id,
                    offset_topic=offset_topic,
                    limit=limit,
                )
            )
        except Exception:
            return out
        topics = getattr(result, "topics", None) or []
        if not topics:
            return out
        for topic in topics:
            topic_id = int(getattr(topic, "id", 0) or 0)
            if not topic_id or topic_id in seen:
                continue
            seen.add(topic_id)
            out.append(
                {
                    "chat_id": chat_id,
                    "topic_id": str(topic_id),
                    "title": getattr(topic, "title", "") or "",
                    "top_message_id": str(getattr(topic, "top_message", "") or ""),
                    "icon_color": int(getattr(topic, "icon_color", 0) or 0),
                    "icon_emoji_id": str(getattr(topic, "icon_emoji_id", "") or ""),
                    "unread_count": int(getattr(topic, "unread_count", 0) or 0),
                    "unread_mentions_count": int(getattr(topic, "unread_mentions_count", 0) or 0),
                    "unread_reactions_count": int(getattr(topic, "unread_reactions_count", 0) or 0),
                    "pinned": bool(getattr(topic, "pinned", False)),
                    "closed": bool(getattr(topic, "closed", False)),
                    "hidden": bool(getattr(topic, "hidden", False)),
                    "last_message_at": iso(getattr(topic, "date", None)),
                }
            )
        last = topics[-1]
        offset_topic = int(getattr(last, "id", 0) or 0)
        offset_id = int(getattr(last, "top_message", 0) or 0)
        offset_date = getattr(last, "date", None)
        if len(topics) < limit:
            return out


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
    out_topics = []
    out_folders, folder_memberships = await load_folders(client)
    for dialog in dialogs:
        entity = dialog.entity
        chat_id = str(dialog.id)
        chat_name = display_name(entity, getattr(dialog, "name", ""))
        folder_id = getattr(dialog, "folder_id", None)
        if folder_id is not None:
            folder_memberships.setdefault(str(folder_id), set()).add(chat_id)
        out_topics.extend(await load_topics(client, entity, chat_id))
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
            reply = getattr(msg, "reply_to", None)
            reply_to_msg_id = getattr(reply, "reply_to_msg_id", None)
            reply_to_top_id = getattr(reply, "reply_to_top_id", None)
            forum_topic = bool(getattr(reply, "forum_topic", False))
            topic_id = ""
            if reply_to_top_id:
                topic_id = str(reply_to_top_id)
            elif forum_topic and reply_to_msg_id:
                topic_id = str(reply_to_msg_id)
            elif type(getattr(msg, "action", None)).__name__ == "MessageActionTopicCreate":
                topic_id = str(msg.id)
            replies = getattr(msg, "replies", None)
            out_messages.append(
                {
                    "source_pk": stable_pk(chat_id, msg.id),
                    "chat_id": chat_id,
                    "chat_name": chat_name,
                    "message_id": str(msg.id),
                    "topic_id": topic_id,
                    "reply_to_message_id": str(reply_to_msg_id or ""),
                    "thread_id": str(reply_to_top_id or ""),
                    "reply_to_chat_id": peer_id(getattr(reply, "reply_to_peer_id", None)),
                    "sender_id": sender_id,
                    "sender_name": sender_name,
                    "timestamp": iso(getattr(msg, "date", None)),
                    "edit_timestamp": iso(getattr(msg, "edit_date", None)),
                    "from_me": bool(getattr(msg, "out", False)),
                    "text": text,
                    "message_type": type(msg).__name__,
                    "media_type": media_type(msg),
                    "media_title": media_title(msg),
                    "media_size": media_size(msg),
                    "views": int(getattr(msg, "views", 0) or 0),
                    "forwards": int(getattr(msg, "forwards", 0) or 0),
                    "replies_count": int(getattr(replies, "replies", 0) or 0),
                    "pinned": bool(getattr(msg, "pinned", False)),
                    "forward_json": to_dict_json(getattr(msg, "fwd_from", None)),
                    "reactions_json": to_dict_json(getattr(msg, "reactions", None)),
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
                "folder_id": str(folder_id if folder_id is not None else ""),
                "forum": bool(getattr(entity, "forum", False)),
            }
        )

    out_folder_chats = []
    for folder_id, chat_ids in folder_memberships.items():
        for position, chat_id in enumerate(sorted(chat_ids)):
            out_folder_chats.append({"folder_id": folder_id, "chat_id": chat_id, "position": position})

    await client.disconnect()
    print(
        json.dumps(
            {
                "source_path": args.tdata,
                "started_at": iso(started),
                "finished_at": iso(datetime.now(timezone.utc)),
                "chats": out_chats,
                "folders": out_folders,
                "folder_chats": out_folder_chats,
                "topics": out_topics,
                "messages": out_messages,
            },
            ensure_ascii=False,
        )
    )


asyncio.run(main())
