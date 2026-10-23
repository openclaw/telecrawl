# SQLCipher WAL fixture

Generated with SQLCipher 4.12.0 using raw key-and-salt bytes 0 through 47,
`cipher_plaintext_header_size=32`, and `journal_mode=WAL`. Table `t7` has
`key BLOB PRIMARY KEY, value BLOB`. Row `01` is checkpointed in the main
database; row `02` is committed only in the WAL.

The files are base64 encoded to keep the fixture portable and reviewable as text.
Tests decode exactly these SQLCipher-produced bytes, rather than generating them
with the implementation under test.

Decoded SHA-256:

- `db_sqlite`: `9c655a760cbe6fa30e56467127cd909704eb16fd841258573ba1fcba87e9c668`
- `db_sqlite-wal`: `277e0f27b2d86609ff2daa770e571cecc0aa66dbd905d413899d9040c7472eae`
