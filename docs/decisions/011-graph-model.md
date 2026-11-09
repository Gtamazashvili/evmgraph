# 011 · Graph model: how the connections are generated

**Status:** Accepted · **Choice:** each position links to `degree/2` earlier positions drawn as `floor(p · u^skew)`; skew 3 and an average degree of 10 by default; integer arithmetic only · **Decided:** 8 November 2026, by the project owner
**Read first:** [003](003-connection-meaning.md) (what a connection is), [005](005-handling-hub-addresses.md) (hubs), [008](008-parallel-generation.md) (block streams)

## In short

- **Rule.** Position *p* links to *m* = degree/2 distinct **earlier** positions; positions *p* ≤ *m* link to all earlier ones. Each target is *t* = floor(*p* · *u*^skew), with *u* uniform in [0, 1). Every link goes into both endpoints' lists (decision 003), so the average degree is 2*m*.
- **Skew sets the hubs.** Skew 1 spreads links evenly: no hubs. Higher skews favour early positions, whose in-degree grows like (*n*/*t*)^(1−1/skew). **Skew 3**, the default, gives a degree power law with an exponent of about 2.5, as real transaction graphs show, so the hub handling of decision 005 is exercised on every dataset.
- **Reproducible everywhere.** One ChaCha8 stream per block of positions, keyed by the seed, the block number and the tag `edges`, like the address and balance columns (decision 008). It is parallel and independent of the number of cores. **No floating point** is used, so a Mac and an amd64 Linux machine draw the same graph from the same seed.
- **Flags:** `-degree` (even, 0–1000, default 10) and `-skew` (1–4, default 3). Both are recorded in the manifest; a later run with other values continues the recorded ones with a warning, as `-seed` does (and `-format` did until it was removed on 9 November 2026).

## Context

The brief says addresses have connections but not how many or how they are distributed. Real chains have hubs (exchanges, bridges, popular contracts), and those are what make `connections` hard: an ordinary address is very likely one step from a hub (the friendship paradox), and level 2 then explodes. A generator without hubs would hide that.

## Options and measurements

Measured with a prototype of the model: 20M addresses, 5 links each (average degree 10), in memory, Apple M1 Pro, single run. The prototype used floating point; the built generator uses the same rule in integer arithmetic.

| | skew 1 | skew 2 | **skew 3** |
|---|---|---|---|
| degree, median / max | 8 / 95 | 7 / 44,641 | 6 / 544,320 |
| addresses with degree ≥ 1,000 | 0 | 506 | 2,513 |
| results for a random address, median | 97 | 167 | 509 |
| random queries with ≥ 10,000 results | 0% | 0.6% | 11.9% |
| query µs, median / p99 / max | <1 / 1 / 31 | <1 / 20 / 173 | 1 / 1,459 / 4,663 |
| the biggest hub's own query | 3,875 results, 48 µs | 1.4M, 31 ms | 8.8M, 193 ms |

- The largest degree matches the model's prediction: for skew 2, *m* · √*n* / ½ = 44,721 predicted against 44,641 measured.
- In this build, the default 20M dataset (seed 100,000) has a biggest hub of 543,942 connections (measured), in line with the prototype's 544,320 for the same model.

Skew 3 was chosen because it is the realistic case: about one random query in nine meets a hub, so the default limit and exact totals of decision 005 are not theoretical.

## Why integer arithmetic

The Go specification allows the compiler to fuse a multiply and an add into one instruction. It does so on arm64 and not on amd64, so `p * math.Pow(u, 3)` can round differently on a Mac and on amd64 Linux, and the same seed would draw two different graphs. Instead, *u* = *x* / 2^32 with *x* uniform in [0, 2^32), and the target is

  *t* = floor(*p* · *x*^skew / 2^(32·skew))

computed exactly in three 64-bit words with `math/bits` (the product is below 2^160). `TestTargetIsExact` checks it against `math/big` for every skew on random and extreme inputs.

## Properties, tested

- **Deterministic and independent of cores:** 1 and 8 workers draw the same links (`TestEdgesAreDeterministic`).
- **A prefix property:** links only point backwards, so the links of positions below *n*₁ are identical in graphs of *n*₁ and *n*₂ > *n*₁. When this was decided, a dataset stopped by Ctrl+C mid-generation was saved with a correct graph of the positions it had; since 9 November 2026 a signal before the save saves nothing new (decision 002).
- **Clean:** no self-links, no link twice; duplicate draws within one position are redrawn, within at most 64 draws per position, and a position that still lacks *m* distinct targets keeps fewer (`TestEdgesShape`).
- **The model holds:** position 0's degree is within 25% of the prediction for skews 1, 2 and 3 at 200,000 positions (`TestHubDegreeMatchesTheModel`).
- **Golden hash:** the SHA-256 of the links for seed 1, *n* 100,000, degree 10, skew 3 is fixed in `TestEdgesGolden`. `TestEdgesGoldenOnAMD64` runs the same test as an amd64 build (through Rosetta on Apple silicon), the architecture of the Docker image; it passes.

## Consequences

- The graph depends only on (seed, *n*, degree, skew), so the files can always be rebuilt from the manifest.
- Generation draws the links twice (once to count, once to fill, decision [012](012-connection-storage.md)) rather than storing them.
- Every dataset now has hubs; hub-adjacent queries are part of normal use, and the benchmarks measure them (`BenchmarkSearch/hub-adjacent`).

## What would change this

- Real transaction data instead of simulation: the generator would be replaced, the storage (012) would not.
- A need for weighted or directed links: decision 003 would change first.
