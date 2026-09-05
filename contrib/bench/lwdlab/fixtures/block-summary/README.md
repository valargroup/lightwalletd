# Block summaries for cache preparation

This local JSONL helper reads public raw mainnet blocks and returns their hash,
transaction IDs and per-pool commitment counts using pinned wallet libraries.
It has no wallet access and makes no network requests. `import-cache` uses it
only during cache construction, before server measurements.

Build with `cargo build --release --locked`. Pin the resulting executable's
SHA-256 when passing it to `import-cache`. The parent importer verifies block
identity and commitment counts independently with the Go parser and checks
cumulative metadata against canonical node results.

Input is `{"height":123,"hex":"..."}`. Height must be positive; the library's
block reader does not support genesis, so the importer requires a canonical
cache prefix. Malformed input terminates the helper and invalidates the import.
The pinned library computes version-specific transaction IDs, including v5 and
v6. Do not replace it with a raw transaction-byte hash.
