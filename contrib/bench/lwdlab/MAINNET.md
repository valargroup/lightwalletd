# Mainnet benchmark preparation

The existing September 4 measurements use synthetic data. Mainnet measurements
have not been run. This document records the source evidence and setup needed to
replace those measurements with results relevant to wallet traffic.

## Wallet request sizes

These are the exact source revisions inspected, not a claim about the versions
installed across the wallet population. Scan batch sizes and gRPC stream lengths
can differ.

| Client | Source behavior |
| --- | --- |
| Vizor desktop | Foreground batches of 2,000 blocks. |
| Vizor mobile | Foreground batches of 1,000 blocks. |
| Vizor background | Batches of 300 blocks. |
| Android SDK | Normally 1,000 blocks per download; 100 when a batch overlaps its configured historical sandblasting interval. |
| Swift SDK | Default scan batch is 100; the downloader reuses a stream across three batches. Its inclusive stream bounds and read-ahead mean this is not simply one 100-block RPC per scan batch. |

Sources:

- [Vizor batch sizing](https://github.com/valargroup/vizor-wallet/blob/24258dcdc354b5b492bd8eb69fe92c026f55554f/rust/src/wallet/sync_engine/mod.rs#L79-L98)
  and [range RPC construction](https://github.com/valargroup/vizor-wallet/blob/24258dcdc354b5b492bd8eb69fe92c026f55554f/rust/src/wallet/sync_engine/lwd.rs#L536-L560).
- [Android batches](https://github.com/valargroup/zcash-android-wallet-sdk/blob/8cb6629732a5aa4b11cddc1cacc1c4ad14445bb2/sdk-lib/src/main/java/cash/z/ecc/android/sdk/block/processor/CompactBlockProcessor.kt#L1165-L1181)
  and the `getBatchedBlockList` / `downloadBatchOfBlocks` call path in that file.
- [Swift scan batch](https://github.com/valargroup/zcash-swift-wallet-sdk/blob/4343518b774d6c9cb7878ad82c27aae8b70bbd2b/Sources/ZcashLightClientKit/Constants/ZcashSDK.swift#L91)
  and [stream reuse](https://github.com/valargroup/zcash-swift-wallet-sdk/blob/4343518b774d6c9cb7878ad82c27aae8b70bbd2b/Sources/ZcashLightClientKit/Block/Download/BlockDownloader.swift#L121-L141).

Vizor also requests subtree roots from its next missing index with `max_entries=0`
(all remaining entries), once during sync preparation. Restore and catch-up tests
must preserve that distinction. Repeated requests for the first 64 roots are an
endpoint stress test, not this wallet's sync sequence.

## Test environment

Run an isolated mainnet node and lightwalletd. Use an archive snapshot so old raw
blocks, transactions, and subtree completing blocks are available. Never point the
concurrent benchmark at `zec.rocks` or another public wallet service.

The public archive manifest at
`https://zakura.valargroup.dev/mainnet/latest.json` was checked on 2026-09-04:

- Node: Zakura 1.3.1, database format 28.1.5, archive mode.
- Height: 3,471,422.
- Archive: `zakura-mainnet-20260904T080905Z-3471422.tar.zst`.
- Compressed bytes: 270,281,049,101 (about 252 GiB).
- SHA-256: `65deeca1874a195e290a92f7096bab51ddfc2d14c86f966e6e7e6eed139fd5db`.

An isolated Linux host with eight dedicated CPUs, 16 GiB RAM, and a 600 GiB
volume has been provisioned. The archive is being downloaded and extracted;
checksum verification is still pending. No mainnet performance results are
available yet.

Record the actual node binary, snapshot checksum, network, and stable interval end
hash when executing. Identify Zakura as the backend in any result; a result with
this backend is not automatically a result with upstream Zebra or zcashd.

Allow space for the restored archive, lightwalletd cache, and any retained
compressed download. Keep independent cache directories for baseline and candidate.
Populate them from the same node before comparing cached serving. Measure cache
construction separately. Do not mix ingestion time with cached download throughput.

Use Linux CPU limits or CPU affinity and record the hardware. Give the backend and
load generator CPU headroom so they do not silently limit lightwalletd. Keep the
same node state, selected block interval, client count, and server resource limits
for each pair. Include ordinary recent blocks and a separate dense historical
interval; report their actual transaction counts, pool contents, and byte sizes.
Preserve the pools returned by the wallet's request, including other pools present
in real mainnet blocks.

## Sequential range driver

The load driver now supports finite range scans. Each client progresses through
the selected interval once. It does not wrap to the beginning. Each completed
batch counts as one request, while `messages_per_second` counts blocks per second.
For comparisons across batch sizes, prefer blocks/s, bytes/s, and total scan time.

After the isolated server and its real cache are ready, run from the repo root:

```sh
go build -o /tmp/lwdlab ./contrib/bench/lwdlab
/tmp/lwdlab load -address 127.0.0.1:19067 -require-mainnet \
  -op range -start 3450000 -end 3469999 -range-batch 1000 \
  -concurrency 8 -scan-timeout 30m
```

This example interval is below the candidate snapshot height. Verify it against
the actual restored node and populated cache before using it. Repeat with batch
sizes 2,000 and 300 for the source-derived download cases. Use 100 for the Android
historical exception only in an interval covered by that client's rule.

`-require-mainnet` checks the server's reported chain, tip coverage, and end block;
it records server information and the end block hash in wire byte order. This
preflight is outside the driver's elapsed time and does not prove the origin of
the entire cache. Keep fixture provenance separately. The driver checks every
returned height and batch count, stops a client on a failed batch, and reports
`range_scan.complete`. Reject runs with errors or incomplete clients even when
the command successfully emits JSON.

Run the same finite work at concurrency 1, 8, 16, and 32, alternating baseline and
candidate order over at least three repeats. Save raw results. Check matching
interval hashes, returned block counts, and response sizes before calculating
improvements. Compare complete response contents in a separate correctness pass.
Capture lightwalletd and backend CPU, allocation counters, RSS, and node RPC counts
around each run. Include p95/p99 and errors rather than throughput alone.

These scans reproduce the download sizes and progression only. They do not yet
reproduce wallet scan delays, prefetch, connection backpressure, or other endpoints.

## Wallet session tests still required

Use a fresh disposable wallet or an explicitly provided benchmark fixture, with
an exact wallet build pinned to the test. Direct it at the isolated server and
record its request sequence in the lab. Do not collect production wallet logs.

Measure these sessions separately rather than combining them with invented ratios:

1. Restore from a historical birthday. Include startup metadata, missing subtree
   roots, progressive block ranges, and tree-state requests at scan boundaries.
2. Resume a caught-up wallet after a defined absence. Request only the missing
   roots and blocks, followed by actual tip polling and mempool behavior.
3. Fetch full transactions for notes discovered during scanning, using a dedicated
   fixture that actually has relevant transactions. An unused empty wallet cannot
   validate this path or the raw-hex optimization.

First capture each session at one client and validate the sequence. Run multiple
independent wallet processes against baseline and each candidate, then the bundle.
Keep wallet scanning and prefetch active so the clients apply their own
backpressure. A fixed trace replay cannot establish wallet completion time or
capacity by itself. Record client CPU and memory as well as server resources;
client saturation can hide a server improvement.

Report each session separately until there is evidence for a production traffic
mix. The current synthetic percentages must not be presented as measured mainnet
improvements. A PR whose path is absent from the captured sessions has no measured
wallet speedup in those sessions.

## Disposable Vizor fixture

`fixtures/vizor_wallet.rs` calls the public `create_wallet` and
`run_full_sync_blocking` APIs in Vizor revision
`24258dcdc354b5b492bd8eb69fe92c026f55554f`. The blocking entry point uses the same
sync implementation as the Flutter API. It preserves range selection, download
prefetch, local scanning, subtree requests, and completion checks. It does not
run the Flutter polling loop or its separately started mempool observer.

Copy the fixture to `rust/examples/lwd_mainnet_wallet.rs` in an isolated checkout
of that revision, then run `cargo build --locked --release --example
lwd_mainnet_wallet` from its `rust` directory. This build and disposable database
creation have passed on macOS arm64. Mainnet sync has not run yet.

The binary takes four arguments. For example, with a private loopback connection
to the isolated server:

```sh
lwd_mainnet_wallet create /private/lab/restore.db 3450000 1
lwd_mainnet_wallet sync /private/lab/restore.db http://127.0.0.1:19068 1
```

Mode `1` runs foreground sync and `2` runs background sync. Creation refuses to
replace an existing database and does not print the seed or addresses. Sync
refuses a non-loopback endpoint and fails if the wallet reports incomplete sync.
Use only newly created benchmark databases. Save the closed disposable database
before syncing so each variant starts from the same state.

This is an unfunded wallet. It can exercise scanning and transparent address
discovery, but cannot establish the cost of fetching transactions belonging to
a funded wallet. It also cannot validate `GetMempoolTx` if the wallet does not
call that method. Those limitations must remain visible in any report.

## Record wallet requests

Run the recorder beside the isolated server or at the local end of an SSH tunnel:

```sh
go run ./contrib/bench/lwdlab record \
  -listen 127.0.0.1:19068 -upstream 127.0.0.1:19067 \
  -output /private/lab/wallet-rpcs.jsonl
```

It forwards single-request read RPCs, including server streams, and writes one
JSON line when each RPC ends. Sort by `started` to inspect request order; lines
are written in completion order. Records contain the method, selected chain
bounds, pool filters, subtree indices, response message and byte counts, timing,
status, and a SHA-256 over length-prefixed protobuf response messages. They omit
wallet addresses, transaction filters, authentication metadata, and message
bodies. The output file must be new and is created with owner-only permissions.

Headers, trailers, error statuses, and cancellation are forwarded. The recorder
does not forward authentication metadata and supports neither transaction
submission nor client-streaming balance calls. It is a lab tool, not a general
replacement for a wallet server. Its listener and upstream must use loopback IPs.

Checksums let us compare stable chain responses between variants without keeping
entire responses. Version metadata and live mempool contents can legitimately
differ and need separate interpretation. Measure recorder overhead separately
before using its timings as performance evidence. Discard runs with recorder
errors, wallet errors, or incomplete syncs.

## Concurrent wallets

`wallet_sessions.py prepare` creates distinct unfunded wallets through the fixture
binary. Each wallet has its own keys and database. `run` copies their saved states
into a new private run directory and starts one wallet process per client. It
does not replace wallet scanning with a request replay.

```sh
python3 contrib/bench/lwdlab/wallet_sessions.py prepare \
  --wallet-binary /private/lab/lwd_mainnet_wallet \
  --build-manifest /private/lab/wallet-build.json \
  --destination /private/lab/fixtures/restore --birthday 3450000 --count 32

python3 contrib/bench/lwdlab/wallet_sessions.py run \
  --wallet-binary /private/lab/lwd_mainnet_wallet \
  --fixture-dir /private/lab/fixtures/restore \
  --output /private/lab/runs/baseline-32-1 --label baseline-32-1 \
  --clients 32 --expected-tip 3471422 --url http://127.0.0.1:19068
```

The build manifest records `binary_sha256`, the wallet source revision, toolchain,
and fixture revision. The fixture manifest records each closed database's hash.
The runner checks those hashes before copying. Keep the same saved inputs for
each baseline/candidate pair. Output includes per-wallet completion time, CPU,
peak RSS, completion state, and chain height, plus batch completion time. Failed,
timed-out, or wrong-height wallets make the run fail. The p50/p95 fields describe
successful wallets only; discard the comparison if `all_complete` is false.

Start with one captured session, then compare 8, 16, and 32 simultaneous sessions.
Use at least three paired repeats with alternating order. Keep server CPU limits,
cache state, wallet mode, node height and hash, and client hardware fixed. Save
the closed databases after a successful sync for the resume test, then advance
the isolated node using actual mainnet blocks and pin its new tip. Record the
absence as the actual height/time difference between those states.

`sample_processes.py` runs on Linux and samples named server and node PIDs without
making RPC requests. It records process CPU counters, RSS, threads, and I/O;
process exit or PID reuse fails the capture. Use its samples to locate CPU or
memory pressure during the wallet run. RSS peaks are sampled, not exact maxima.
Also save lightwalletd `/metrics` and node metrics before and after each run for
allocation counters and backend RPC counts. Keep setup and cache construction
outside the cached-serving measurement window.

## Report format

Lead with a compact table containing one row per PR and one for the bundle.
For the same wallet session and concurrency, show baseline → candidate wallet
completion time, server CPU per completed wallet, and peak resident memory.
Include absolute units and the percentage change. Identify the paired runs and
show their spread so small changes are not presented as established wins.

Plot wallet completion time against concurrent wallets, and show server CPU per
wallet beside it. These answer different questions: does a wallet finish sooner,
and does serving it cost less? Resource savings alone do not prove how many more
wallets a production server can support. If the client, network, or node limits
throughput, state that beside the result.

Keep the recommendation for each PR short: measurable benefit in this session,
no clear difference, or not exercised. A faster individual endpoint must remain
an endpoint result unless the complete wallet session also improves. Put request
traces, hashes, hardware, test conditions, and reproduction commands below the
main results. Do not blend these mainnet measurements with the synthetic report.
