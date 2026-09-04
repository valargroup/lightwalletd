# Query performance improvements

This branch combines four small performance changes and a mempool correctness
fix. It introduces no configuration flags or disk format changes.

- Backend HTTP connections are reused, with up to 64 idle connections retained
  by the transport. Idle connections expire after 90 seconds. Backend RPC counts
  and application retry rules are unchanged.
- Block-range filtering removes unrequested components from each freshly decoded
  block instead of allocating replacement transactions. `GetBlock` still returns
  all pools, and default ranges still return all shielded pools.
- Subtree-root responses reuse the hash of each completing block after its first
  disk-cache decode. The memoized hashes belong to the current disk cache and are
  cleared on a reorg or cache reset. The subtree query itself still reaches the
  backend. Without disk caching, metadata continues to come from the backend.
- Hexadecimal RPC replies are decoded directly from their JSON bytes when they
  contain unescaped hex. Escaped strings use the standard JSON decoder. This
  primarily benefits large uncached blocks and raw mempool transaction responses.
- Mempool snapshots are sorted before publication so concurrent readers filter
  an immutable ordering.

The range producer channel remains in place. Tip and tree-state requests retain
their existing freshness behavior. There is no new whole-response cache, custom
protobuf decoder, or ingestion-parallelism change.

Performance gains depend on endpoint mix. Cached range serving, repeated subtree
metadata, backend polling, and large uncached replies should be measured separately
and as a defined combined workload. Improvements in these benchmarks should not
be added together or presented as a production capacity estimate.
