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

## Follow-up comparisons

`followup.sh LAB_DIRECTORY` compares upstream, in-place filtering, and selective
decoding across four range workloads. It expects `lightwalletd-baseline`,
`lightwalletd-range-filter`, `lightwalletd-selective`, and `lwdlab` binaries in
the lab directory. Generate its three disk caches with the same lab binary:

```sh
LAB=/tmp/lwd-followup
"$LAB/lwdlab" generate-cache -data-dir "$LAB/cache-dense" -shape mixed
"$LAB/lwdlab" generate-cache -data-dir "$LAB/cache-segregated" -shape segregated
"$LAB/lwdlab" generate-cache -data-dir "$LAB/cache-shielded" -shape shielded
```

The mixed shape puts components from all four pools in every transaction.
At the default 64 transactions per block, the segregated shape has 55 transparent,
three Sapling, and six Orchard transactions. The shielded control has 64
Orchard transactions. These proportions are illustrative, not mainnet estimates.
Run the deterministic backend separately from the repository root before the
matrix. The caches and backend must use the same tip (2047 by default).

The follow-up runner sets `GOMAXPROCS=2` only on the server. This limits Go
parallelism, not host CPU quota. Override it with `LWD_LAB_SERVER_PROCS`.
`LWD_LAB_DURATION` and `LWD_LAB_REPEATS` default to five seconds and three repeats.
Variant order reverses on alternating repeats.

`LWD_LAB_SUITE=poll` runs the same three-way status mix using `GetTreeState`
instead of `GetLatestTreeState`. Supply `lightwalletd-backend-keepalive`,
`lightwalletd-poll-cache`, and `lightwalletd-poll-keepalive` alongside the baseline.
For a controlled backend latency comparison, start the backend with
`-delay-us 2000`. This is a status-path comparison, not a complete wallet sync
replay or a measured public-server latency distribution.

Duration-based loads stop issuing new requests at the target time and drain
active requests, bounded by another 30 seconds. Throughput uses total elapsed
time including the drain; all RPC errors are counted. This avoids silently
discarding server cancellations or classifying shutdown deadline races as
ordinary load failures.

Follow-up measurements and decisions are in
[`results/2026-09-04-followup.md`](results/2026-09-04-followup.md).

## Mainnet wallet results

The [32-wallet cached-server report](results/2026-09-05-cached-wallet-repeats.md)
compares individual PRs using actual wallet syncs and real mainnet blocks. It
includes three paired repeats per PR, a chart, raw measurements and the limits
of each result. Start there for the measured change recommendation.

The [sequential wallet comparison](results/2026-09-05-single-wallet-cache.md)
measures PR 9 with one wallet at a time, first after restart and then with the
same server's hash map populated by the previous wallet.

A separate [uncached wallet report](results/2026-09-05-mainnet-wallet-repeats.md)
measures direct hash lookup when completing blocks are absent from the cache.
[Mainnet preparation](MAINNET.md) documents the pinned fixture, wallet request
sizes and reproduction steps. The September 4 results below remain synthetic.

## Synthetic server bundle

`LWD_LAB_SUITE=server-bundle LWD_LAB_DURATION=10s followup.sh LAB_DIRECTORY`
compares `lightwalletd-baseline` with `lightwalletd-bundle` using `cache-shielded`.
Run the backend separately with `-delay-us 2000`. The suite covers default ranges
containing only Orchard transactions, Orchard subtree roots, status requests,
and a defined mixed load. The `wallet-load` operation repeats eight ranges,
two explicit tree-state requests, one `GetLightdInfo`, and one subtree-root request
per twelve operations. This is an illustrative mix, not measured wallet traffic.

Use `-subtree-pool orchard` to select Orchard roots; the default remains Sapling
so old benchmark commands retain their meaning. The bundle suite always selects
Orchard explicitly and warms each operation before the measured interval.

Synthetic results and the original bundle recommendation are in
[`results/2026-09-04-server-bundle.md`](results/2026-09-04-server-bundle.md).

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
