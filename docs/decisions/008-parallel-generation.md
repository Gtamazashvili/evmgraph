# 008 · Parallel address generation

**Status:** Accepted · **Choice:** generate addresses in independent blocks of 1,048,576, one random stream per block, on all CPU cores · **Decided:** 8 November 2026, by the project owner
**Read first:** [002](002-shutdown-and-binary-storage.md) (what is saved and when)

## In short

- **Before:** all addresses came from one random stream, read in order on one core. 50 million addresses took **774 ms**, and topping a 50M dataset up to 51M first had to generate and discard the existing 50M: **627 ms** for 1M new addresses.
- **Now:** the dataset is cut into blocks of 2²⁰ (1,048,576) addresses. Each block has its own stream, keyed by the seed and the block number, so blocks are generated on all 8 cores at once and any position can be reached directly.
- **Measured on the same machine:** 50M addresses in **146 ms (5.3× faster)**; the 50M → 51M top-up in **14.5 ms (43× faster)**. 1M addresses is one block, so it stays at **15 ms**.
- **Cost:** the same seed now gives different addresses than before, so an existing `cache/` must be generated again once. The dataset still depends only on the seed, never on the number of cores.

## Context

Generation runs on every first start and on every top-up (decision 002: when `-n` is larger than the cache, only the missing addresses were made; since decision [012](012-connection-storage.md) a top-up generates the whole dataset again). When this was decided it was the only step of the program limited by the CPU (sorting, decision 010, and the graph build, decision 012, came later): saving is limited by the disk, and loading binary with `mmap` takes 31 µs.

The old generator used one ChaCha8 stream. A stream must be read in order, so it could use only one core, and a top-up had to read past every existing address to reach the new ones.

## Criteria

1. **Reproducible:** the same seed gives the same dataset, whatever the machine or core count.
2. **Top-up equals a fresh run:** growing 3,000 to 10,000 gives the same 10,000 as generating 10,000 at once.
3. **Clean shutdown:** Ctrl+C during generation still stops quickly and keeps a correct prefix to save.
4. **Small:** no new dependencies, little code.

## Options

| Option | How | Speed at 50M | Top-up | Reproducible |
|---|---|---|---|---|
| A. One stream, one core (before) | read ChaCha8 in order | 774 ms | reads past all existing addresses | yes |
| **B. One stream per block, all cores** | key = seed + block number; workers take blocks in order | **146 ms** | starts at its block | **yes, independent of core count** |
| C. Split one stream between cores | not possible: Go's ChaCha8 cannot jump ahead | n/a | n/a | n/a |

## Measurements

`go test ./internal/generate -run '^$' -bench . -benchmem -count 3` on the Apple M1 Pro (8 cores), median of 3. The "before" row is the previous code, run with the same benchmarks.

| Benchmark | Before | After | Change |
|---|---|---|---|
| Generate 1M (one block) | 14.9 ms | 15.4 ms | the same |
| Generate 50M | 774 ms | 146 ms | 5.3× faster |
| Top up 50M → 51M | 627 ms | 14.5 ms | 43× faster |

Memory is unchanged: one slice of 20 bytes per address (1 GB at 50M), plus a 64 KB buffer per core.

## Recommendation and decision

Option B, chosen by the project owner on 8 November 2026.

## Consequences

- `internal/generate`: `Rows` (through `fill`) hands blocks to `GOMAXPROCS` workers in order. A range that starts inside a block skips at most one block (about 12 ms) instead of everything before it.
- **Shutdown:** on Ctrl+C the workers stop at their next batch of 65,536, and `Rows` returns the longest fully generated run from the start, always exactly the first addresses of the full dataset. Because blocks are handed out in order, that prefix is nearly all the work done. Until 9 November 2026 the program saved that prefix; now a signal before the save stops the work and nothing new is saved (decision 002), so the next run generates the same dataset again.
- **Progress:** the `x/y` counter still rises to the total; calls are serialised, so the printer needs no locking.
- **Tests:** the dataset is identical on 1, 2, 3 and 8 workers; ranges that start or end inside blocks, or span several, match a full run; a cancelled run returns an exact prefix; the race detector passes.
- **New dataset:** caches written before this change hold the old sequence. Delete them once (`task clean:cache`). A top-up of an old cache would mix the two sequences, and nothing in the file could detect that, because the file stored no seed or version (a header was then an open question in "storage format details"). Later the same day the manifest of decision [009](009-dataset-layout.md) began recording the seed and a version, and the program no longer reads caches in the layout before it.
- `BlockSize` and the key layout (seed, block number, tag `address`) are part of the dataset's definition. Changing either changes every dataset. The tag lets balances and connections derive their own streams from the same seed later.

## What would change this

- A file header that records the seed and generator version: then old caches could be detected instead of documented. (The manifest, decision 009, now records the seed and a version, and an older version is generated again.)
- Generation that must run on one core (a very small container): the code still works with one worker and gives the same dataset, just without the speed-up.
