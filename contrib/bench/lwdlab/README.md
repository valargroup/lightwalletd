# lightwalletd load lab

This lab compares real `lightwalletd` binaries under deterministic concurrent
gRPC load. It provides three components:

- `generate-cache` creates a valid on-disk compact-block cache with configurable
  transaction density.
- `backend` serves the JSON-RPC methods used by the measured handlers and
  records method, byte, and TCP connection counts.
- `load` drives one gRPC connection per worker and reports throughput, response
  volume, errors, and latency percentiles.

`measure.sh` starts one lightwalletd binary, warms the selected operation,
resets backend counters, snapshots Go process metrics, runs the load, and emits
one JSON result. It is intended for comparing binaries built from the same base
revision. The generated chain is structurally sufficient for the cache and RPC
paths under test, but it is not a consensus-valid replacement for integration
testing against Zebra or zcashd.

`matrix.sh` runs the standard set of isolated candidates against the same
baseline three times per side, alternating which binary runs first. Set
`LWD_LAB_DURATION` or `LWD_LAB_REPEATS` to shorten a smoke run or extend a
comparison. `LWD_LAB_WORKLOADS` selects a comma-separated subset, and
`LWD_LAB_COOLDOWN` inserts a delay after each run when intentionally testing
large volumes of short-lived backend connections.

The initial candidate comparison is recorded in
[`results/2026-09-04-macos-arm64.md`](results/2026-09-04-macos-arm64.md).

Example:

```sh
go build -o /tmp/lwdlab ./contrib/bench/lwdlab
/tmp/lwdlab generate-cache -data-dir /tmp/lwd-cache -blocks 2048 -tx-per-block 64
/tmp/lwdlab backend -tip-height 2047
contrib/bench/lwdlab/measure.sh \
  --label baseline-range \
  --server /tmp/lightwalletd-baseline \
  --lab /tmp/lwdlab \
  --data-dir /tmp/lwd-cache \
  --output-dir /tmp/lwd-results \
  -- -op range -concurrency 32 -duration 15s -start 100 -end 131 -pools sapling
```
