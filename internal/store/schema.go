package store

const schemaSQL = `
create table if not exists chats (
	id integer primary key,
	kind text not null,
	name text,
	username text,
	last_message_at integer,
	unread_count integer not null default 0,
	message_count integer not null default 0,
	folder_id text,
	forum integer not null default 0
);

create table if not exists folders (
	id text primary key,
	title text,
	emoticon text,
	color integer not null default 0,
	flags_json text
);

create table if not exists folder_chats (
	folder_id text not null,
	chat_jid text not null,
	position integer not null default 0,
	primary key (folder_id, chat_jid)
);

create table if not exists topics (
	chat_jid text not null,
	topic_id text not null,
	title text,
	top_message_id text,
	icon_color integer not null default 0,
	icon_emoji_id text,
	unread_count integer not null default 0,
	unread_mentions_count integer not null default 0,
	unread_reactions_count integer not null default 0,
	pinned integer not null default 0,
	closed integer not null default 0,
	hidden integer not null default 0,
	last_message_at integer,
	primary key (chat_jid, topic_id)
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
	metadata_type text,
	metadata_title text,
	metadata_url text,
	metadata_json text,
	starred integer not null default 0,
	topic_id text,
	reply_to_msg_id text,
	reply_to_chat_jid text,
	thread_id text,
	edit_ts integer,
	forward_json text,
	reactions_json text,
	views integer not null default 0,
	forwards integer not null default 0,
	replies_count integer not null default 0,
	pinned integer not null default 0
);

create index if not exists idx_messages_chat_ts on messages(chat_jid, ts);
create index if not exists idx_messages_chat_msg on messages(chat_jid, msg_id);
create index if not exists idx_messages_chat_topic_ts on messages(chat_jid, topic_id, ts);
create index if not exists idx_topics_chat on topics(chat_jid, last_message_at);
create index if not exists idx_folder_chats_chat on folder_chats(chat_jid);
create index if not exists idx_messages_ts on messages(ts);
create index if not exists idx_messages_sender on messages(sender_jid);

create virtual table if not exists messages_fts using fts5(text, chat, sender, media);

create table if not exists sync_state (
	key text primary key,
	value text not null,
	updated_at integer not null
);
`
