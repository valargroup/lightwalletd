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

This is a candidate bootstrap source, not a downloaded or validated local fixture.
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

First capture each session at one client and validate the sequence. Replay that
sequence with its actual range sizes and timing against baseline and each candidate,
then the bundle. Test sustained load and slow readers separately from the finite
range driver. Report per-scenario results until there is evidence for a production
traffic mix. The current synthetic percentages must not be presented as measured
mainnet improvements.
