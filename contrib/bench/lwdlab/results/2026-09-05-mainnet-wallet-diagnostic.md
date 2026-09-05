# Wallet restore with uncached blocks

The later [three-pair comparison](2026-09-05-mainnet-wallet-repeats.md) includes
resource measurements and recorder-overhead checks. This page preserves the
initial diagnostic.

A real Vizor wallet restored from mainnet height 3,450,000 through 3,470,422
using the unchanged baseline and then the direct-hash change in
[draft PR 11](https://github.com/valargroup/lightwalletd/pull/11).
Both completed successfully and returned matching recorded response contents.
This is **one diagnostic run per version**, with the baseline run first.
It is not yet a repeated load test or evidence of production capacity.

| Work | Baseline | Direct-hash candidate |
| --- | ---: | ---: |
| Complete wallet restore | 2,984.847 s | 99.649 s |
| Sapling subtree download | 1,356.521 s | 1.134 s |
| Orchard subtree download | 1,507.249 s | 0.855 s |
| Ironwood subtree download | 0.031 s | 0.003 s |
| All block-range RPCs | 90.353 s | 66.857 s |

The wallet itself chose the requests. It fetched all missing subtree roots
(1,128 Sapling, 769 Orchard, two Ironwood), then downloaded 20,423 blocks in ten
2,000-block ranges and a final 423-block range. It also requested tree states,
checked the tip, queried transparent UTXOs and scanned the downloaded blocks.
All 40 RPCs in each run completed successfully. Matching request parameters,
response counts, byte counts and SHA-256 response digests were checked.

The baseline downloads and parses each uncached subtree-completing block merely
to obtain its hash. The candidate requests that hash directly. The improvement
applies to cache misses, including supported `--nocache` deployments and blocks
not yet ingested into a cache. These timings do not establish a benefit for
cache hits. The separate existing subtree PR improves cache hits and was not
exercised by these no-cache runs.

## Conditions and limits

- Fixed, checksum-verified mainnet archive served by Zakura 1.3.1, with no peers;
  finalized tip hash `000000000073dfbd7bf192181f1f0e060ef3c4f295504ca1c513fa49ce6b3723`.
- Same dedicated eight-core Linux host and node process for both runs. LWD had
  CPUs 0–1 and GOMAXPROCS=2; the node had CPUs 2–5; the recorder had CPUs 6–7.
  Cache construction was paused throughout each wallet run.
- Same immutable, disposable, unfunded wallet fixture; real Vizor sync core at
  `24258dcdc354b5b492bd8eb69fe92c026f55554f` on the Mac Studio, through the same SSH tunnel
  and RPC recorder. Each run copied the original saved database.
- Baseline `d79cd1100575ff909d70e00d5514a4092df94934`;
  candidate `bdc603c9176b468281a551490cff06f34b894a45`. Both ran with `--nocache`.
- Backend memory and filesystem caches were not reset or controlled. The runs
  were not alternated or repeated, and recorder overhead was not quantified.
  Differences in block download time cannot be attributed to this change.
- One wallet only. Server CPU savings and concurrent capacity have not been
  measured. The unfunded fixture did not exercise relevant-transaction fetching
  or the separately started mempool observer.

Repeated paired runs and concurrency tests are still required. Cached-server
comparisons remain pending full cache preparation. The candidate passed the full
Go test suite and targeted race tests for cached and uncached hashes, byte order,
backend errors, malformed responses and cancellation.

[Structured results](2026-09-05-mainnet-wallet-diagnostic.json),
[baseline RPC trace](2026-09-05-baseline-wallet-rpcs.jsonl),
[candidate RPC trace](2026-09-05-direct-hash-wallet-rpcs.jsonl).
