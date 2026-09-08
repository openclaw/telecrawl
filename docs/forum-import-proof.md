# Forum import proof

Run `python3 scripts/proof-forum-import.py` from a checkout with Go and Python 3.
The script builds and runs the real CLI with a temporary Go overlay that replaces
only Telegram session bootstrap. A synthetic RPC invoker returns binary TL
responses through gotd's generated client; the account importer, pagination,
CLI error propagation and SQLite writes use the production source unchanged.
No Telegram login or personal archive is accessed. This is RPC-boundary fault
injection, not a live Telegram service or MTProto authentication test.

Successful cases import 101 topics over two data pages followed by an empty
page, including approximate totals, a short intermediate page, absent top messages
and a deleted boundary topic, and read them through
`telecrawl --json topics`. Repeated pages, alternating cycles and RPC failures must exit nonzero in both merge
and `--restore` mode. All archive rows, message revisions and sync state must
remain identical after each failure. Temporary binaries and databases are
removed automatically.

The same fixture reproduces both earlier defects:

```sh
python3 scripts/proof-forum-import.py --ref 3631b3ce95087ec8ec970aeffb972ee34ff4d154 --expect main-bug
python3 scripts/proof-forum-import.py --ref 6531bf0087ecdd64350dbb956e98e34d106d3d4c --expect pr-bug
```

Main repeats the same page until the fixture's two-second deadline; the original
PR exits successfully with only 100 of 400 advertised topics. The fixed source
returns an incomplete-import error before writing the partial result.
