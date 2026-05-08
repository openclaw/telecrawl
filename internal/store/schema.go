package store

const schemaSQL = `
create table if not exists chats (
	id integer primary key,
	kind text not null,
	name text,
	username text,
	last_message_at integer,
	unread_count integer not null default 0,
	message_count integer not null default 0
);

create table if not exists contacts (
	jid text primary key,
	phone text,
	full_name text,
	first_name text,
	last_name text,
	business_name text,
	username text,
	lid text,
	about_text text,
	updated_at integer
);

create table if not exists groups (
	jid text primary key,
	name text,
	owner_jid text,
	created_at integer
);

create table if not exists group_participants (
	group_jid text not null,
	user_jid text not null,
	contact_name text,
	first_name text,
	is_admin integer not null default 0,
	is_active integer not null default 0,
	primary key (group_jid, user_jid)
);

create table if not exists messages (
	rowid integer primary key autoincrement,
	source_pk integer not null unique,
	chat_jid text not null,
	chat_name text,
	msg_id text not null,
	sender_jid text,
	sender_name text,
	ts integer not null,
	from_me integer not null,
	text text,
	raw_type integer not null default 0,
	message_type text,
	media_type text,
	media_title text,
	media_path text,
	media_url text,
	media_size integer,
	starred integer not null default 0
);

create index if not exists idx_messages_chat_ts on messages(chat_jid, ts);
create index if not exists idx_messages_chat_msg on messages(chat_jid, msg_id);
create index if not exists idx_messages_ts on messages(ts);
create index if not exists idx_messages_sender on messages(sender_jid);

create virtual table if not exists messages_fts using fts5(text, chat, sender, media);

create table if not exists sync_state (
	key text primary key,
	value text not null,
	updated_at integer not null
);
`
