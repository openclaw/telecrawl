# Vision

Telecrawl is a local-first Telegram archive: imports should be predictable, inspectable, and safe to interrupt without corrupting or duplicating archived data.

## Telegram availability

All Telegram API clients must use one client-level flood-wait policy. Honor server-requested delays only within explicit retry and per-wait bounds; report each wait to the user; make waits context-cancellable; and return a clear error when a bound is exceeded. Retry the individual RPC, never an entire logical scan or paginated import.
