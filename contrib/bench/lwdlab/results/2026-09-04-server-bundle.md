# Recommended server performance bundle

## Recommendation

Prioritize these four optimizations for a first server performance release:

1. **Backend HTTP connection reuse.** Applies across backend-dependent endpoints
   and removes almost all measured backend TCP connection churn.
2. **In-place range filtering.** Improves bulk serving of default ranges containing
   Orchard transactions, using the existing protobuf decoder and stream pipeline.
3. **Subtree completing-block hash memoization.** Avoids repeatedly decoding whole
   compact blocks for Orchard subtree metadata. Includes the no-cache fallback fix.
4. **Direct hex decoding.** Removes an intermediate large string from backend raw
   replies, using the standard JSON/hex decoders. Its benefit is concentrated in
   uncached block and raw-transaction paths.

The measured bundle also includes the small immutable-mempool-snapshot correction.
Treat that as a correctness fix; none of the workloads below calls `GetMempoolTx`,
so it is not being credited for modern wallet performance.

Keep the semantic-changing polling cache, custom selective protobuf decoder, and
channel-removal experiment out of this first set. This bundle has no tip-freshness
change, whole-response cache, new disk format, or ingestion-parallelism change.

The implementation is on `adam/lwd-perf-server-bundle` at
`ca3067ac538f44ba4995dbf6ccd9cf92845e88b5`. Individual changes remain separate commits.
The binary was built at `8359c751faea53d42ad3d05d34475dea1e7249ee`; the branch head
adds documentation and release notes only. Upstream was refreshed and remained at
`d79cd1100575ff909d70e00d5514a4092df94934`.

## Combined before/after measurements

Measured on 2026-09-04. These are the full bundle against upstream; do not add
individual improvement percentages together.

| Workload | Requests/s before → after | Throughput change | p95 ms before → after | Server CPU/request | Allocated bytes/request |
| --- | ---: | ---: | ---: | ---: | ---: |
| Default ranges, Orchard-only blocks | 790 → 885 | +12.0% | 15.83 → 14.29 | -10.2% | -22.9% |
| Repeated Orchard subtree roots | 536 → 2,914 | +444.0% | 21.00 → 3.43 | -91.5% | -97.7% |
| Status polling | 2,207 → 2,481 | +12.4% | 5.56 → 4.89 | -47.9% | -45.1% |
| Defined Orchard workload mix | 931 → 1,194 | +28.3% | 15.63 → 11.33 | -21.8% | -35.1% |

All 24 final samples completed without RPC errors. Every dedicated range request
returned 32 blocks; every dedicated subtree request returned 64 roots. Response
sizes per request matched between variants on those workloads. Both throughput
and p95 improved in every measured workload at the median.

Status polling opened roughly 29,000 backend connections per ten-second baseline
sample; the bundle used seven at the median. Backend RPC counts are not cached
away. The server continues to query the node for current tip and tree state.

The direct-hex change is included but these cached/status workloads do not exercise
its large-response benefit. The earlier isolated approximately 1 MB raw-block test
measured 125.8 → 157.0 requests/s (+24.8%). That is separate prior evidence, not an
additional percentage to add to this table. See the
[initial results](2026-09-04-macos-arm64.md).

## What these numbers mean

The strongest broad-serving result is the defined mixed workload and the improvement
in ordinary Orchard range serving. The much larger subtree result is specific to
repeated warm metadata queries. It should not be presented as a fivefold improvement
to every wallet operation or to overall server capacity.

The table is suitable for reporting controlled benchmark results. It is not a
measurement of production fleet capacity or an estimate of any operator's traffic
mix. A production multiplier would require replaying representative real cache data
and checking actual aggregate endpoint proportions on a comparable server.

## Reproduction and validation

- Apple M3 Ultra, macOS, Go 1.25.0; server `GOMAXPROCS=2` with eight concurrent gRPC
  clients. This is a Go parallelism setting, not a dedicated two-vCPU host or quota.
- Actual lightwalletd processes, loopback gRPC, deterministic JSON-RPC backend with
  a fixed 2 ms delay per RPC, and real on-disk compact-block cache reads.
- Synthetic cache: 2,048 blocks, 64 Orchard-only transactions per block. No Sapling
  workload is included in this table. The small index and synthetic blocks do not
  reproduce a full mainnet server's heap or block-size distribution.
- Default ranges cover 32 blocks. Subtree queries request 64 Orchard roots and are
  warmed once before measurement. The first cold lookup still decodes its block.
- Status mix cycles latest block, explicit `GetTreeState`, and `GetLightdInfo`.
- Mixed load repeats eight range requests, two explicit tree-state requests, one
  info request, and one Orchard subtree request. This is an illustrative workload,
  not a measured distribution of wallet usage.
- Three ten-second samples per side; variant order reverses on the second repeat.
  Active requests drain after the issuing window, and all RPC errors are counted.
- The combined branch passed `go test ./...` and `go test -race ./common ./frontend`.
  These include the pool-filter, transport, hash-cache/reorg, no-cache fallback,
  and mempool tests.

Use the updated lab with `LWD_LAB_SUITE=server-bundle LWD_LAB_DURATION=10s` and
`followup.sh LAB_DIRECTORY`. The lab directory needs `lightwalletd-baseline`,
`lightwalletd-bundle`, `lwdlab`, and `cache-shielded`; start its backend separately
with `-delay-us 2000`. The [README](../README.md) covers generation and execution.
Raw per-run results are in
[`2026-09-04-server-bundle.jsonl`](2026-09-04-server-bundle.jsonl).

No production servers were load-tested or changed. The branch remains local;
no PRs were opened and nothing was pushed.
