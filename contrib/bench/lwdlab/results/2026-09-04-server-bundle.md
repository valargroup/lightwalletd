# Orchard server performance measurements

The clearest bulk-serving result is **12% more cached block downloads per second**,
with **23% fewer allocated bytes per download**. Separate tests show lower overhead
for node metadata queries and a large improvement for repeated subtree metadata.
These are controlled server benchmarks, not measurements of wallet sync time or
production capacity.

## Workloads and results

All results compare the full optimization bundle with upstream. A request means
one completed gRPC call, including the entire response stream. Compare before and
after within a row; the rows deliver different amounts of data.

| Workload | Requests/s before → after | Throughput change | p95 latency, ms before → after |
| --- | ---: | ---: | ---: |
| Cached block downloads (32 blocks/request) | 790 → 885 | 12.0% | 15.83 → 14.29 |
| Node metadata queries (one endpoint/request) | 2,207 → 2,481 | 12.4% | 5.56 → 4.89 |
| Repeated Orchard subtree metadata (64 roots/request, warm) | 536 → 2,914 | 5.44× baseline | 21.00 → 3.43 |

### 1. Cached block downloads

Like repeatedly fetching the same batch of pages, eight clients download blocks
100–131 through `GetBlockRange`. Each call returns all 32 blocks. Each synthetic
block contains 64 Orchard-only transactions. The request leaves `PoolTypes` unset,
so it exercises default filtering, not an explicit Orchard-only filter.

This measures reading compact blocks from the existing cache, decoding and
filtering them, and streaming them to clients. In-place filtering removes redundant
copies. It does not measure downloading new blocks from the node or a wallet
scanning transactions. Repeating a warmed range also does not measure cold disk
performance or a scan across mainnet history.

**Result:** about 25,300 → 28,300 blocks/s in this fixture. Server CPU time per
32-block request fell 10.2%; allocated bytes per request fell 22.9%.

### 2. Node metadata queries

Eight clients continuously cycle through three questions, with one question per
request and no pause between requests:

- `GetLatestBlock`: what is the node's current tip height and hash?
- `GetTreeState(2047)`: what is the note commitment tree state at this block?
- `GetLightdInfo`: what server, network, and chain information is available?

This stresses the connection between lightwalletd and its backend node. It is not
a measured wallet polling cadence. The backend is a fixture with fixed replies and
a 2 ms delay per node RPC. `GetLightdInfo` makes two node RPCs; the other two make
one each. The reported requests/s counts individual gRPC calls, not three-call cycles.

**Result:** CPU time per request fell 47.9%; allocated bytes fell 45.1%. Newly opened
backend TCP connections fell from roughly 29,000 to seven per ten-second sample
(medians, counted after warmup). Connection reuse avoids setup overhead; it does
not eliminate node queries. The fixed node delay remains, which helps explain why
throughput improves less than CPU cost.

### 3. Repeated Orchard subtree metadata

A subtree root is a compact cryptographic summary of a completed section of the
note commitment tree. This endpoint returns those roots plus the height and hash
of each completing block. Eight clients repeatedly request the same 64 Orchard
roots from index zero through `GetSubtreeRoots`, after one warmup request.

Upstream decodes the associated compact blocks on every request to obtain their
hashes. The optimization remembers those hashes after their first lookup. The
endpoint still queries the backend for roots on every request; it does not compute
roots or serve a cached whole response.

**Result:** CPU time per request fell 91.5%; allocated bytes fell 97.7%. The 5.44×
throughput result applies to repeated warm metadata requests. It does not measure
a cold first lookup or a 5.44× improvement in wallet sync.

## Supporting experiment: an illustrative request mix

Each client repeats eight block-range calls, two `GetTreeState` calls, one
`GetLightdInfo` call, and one Orchard subtree call, using the same inputs above.
These are proportions of request counts, not bytes or server time. Each call counts
as one request; the twelve-call cycle does not count as one wallet session.

This mix measured 931 → 1,194 requests/s (+28.3%), p95 15.63 → 11.33 ms,
CPU time/request −21.8%, and allocated bytes/request −35.1%.

The ratio was chosen for the experiment, not derived from production traffic.
Its result depends on the inclusion of repeated subtree queries. Use it as evidence
that the changes work together, not as a claim of 28% greater fleet capacity.

## Scope and measurement notes

- Measured 2026-09-04 on an Apple M3 Ultra, macOS, Go 1.25.0. The server used
  `GOMAXPROCS=2`, a Go execution parallelism limit, not a two-vCPU host or CPU quota.
- Real lightwalletd processes and on-disk cache format, loopback gRPC, synthetic
  cache of 2,048 blocks, and a deterministic backend with 2 ms delay per RPC.
  This does not reproduce mainnet data, network conditions, or node contention.
- Eight clients keep at most one request each in flight, immediately starting the
  next after completion. This measures throughput at that concurrency, not maximum
  sustainable capacity across increasing client counts.
- Three ten-second runs per variant and workload, with variant order reversed on
  the second repeat. Values are medians across runs; percentages use unrounded
  values. All 24 runs completed without RPC errors. In-flight requests drain after
  the issuing window. Dedicated range/root response counts and sizes matched.
- p95 is the time within which 95% of completed requests finished in a run.
  CPU/request is lightwalletd process CPU time divided by completed requests;
  backend and load-generator CPU are excluded. Allocated bytes/request measures
  allocation churn, not resident memory or peak RAM. No confidence intervals or
  long-duration stability measurements are claimed.

The bundle includes HTTP connection reuse, in-place range filtering, subtree block
hash memoization with a no-cache fallback, direct backend hex decoding, and an
immutable mempool snapshot fix. These tests compare the bundle, not each change
independently. They do not exercise `GetMempoolTx` or the large raw-reply path that
motivates direct hex decoding. A separate earlier raw-block measurement is in the
[initial results](2026-09-04-macos-arm64.md); its improvement is not additive here.

## Reproduction and validation

Baseline: `d79cd1100575ff909d70e00d5514a4092df94934`. Measured bundle binary:
`8359c751faea53d42ad3d05d34475dea1e7249ee`. Bundle branch documentation/release-note
commit: `ca3067ac538f44ba4995dbf6ccd9cf92845e88b5`.
The bundle passed `go test ./...` and `go test -race ./common ./frontend`.

Use `LWD_LAB_SUITE=server-bundle LWD_LAB_DURATION=10s` with
`followup.sh LAB_DIRECTORY`. The lab directory needs `lightwalletd-baseline`,
`lightwalletd-bundle`, `lwdlab`, and `cache-shielded`; start its backend separately
with `-delay-us 2000`. See the [README](../README.md),
[workload definitions](../load.go), [suite configuration](../followup.sh), and
[raw results](2026-09-04-server-bundle.jsonl).

No production servers were load-tested or changed. The changes are published as
separate draft PRs in the Valar fork for review.
