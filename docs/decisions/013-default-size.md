# 013 · Default dataset size: 20 million addresses

**Status:** Accepted · **Choice:** `-n` defaults to 20,000,000 · **Decided:** 8 November 2026, by the project owner
**Read first:** [012](012-connection-storage.md) (what a dataset costs)

## In short

Without `-n`, `evmgraph run` generates **20,000,000 addresses**, each with a `u128` balance and on average 10 connections. That is large enough for lookups and hub queries to show real behaviour, and small enough to build and save in seconds on an ordinary laptop (when this was decided, the save ran at shutdown and had to fit `docker stop`'s grace period).

## Why 20M

All figures are measured on this project's machine (Apple M1 Pro, 8 cores, 16 GB), single runs, 9 November 2026:

| | 20M addresses |
|---|---|
| on disk | 1.6 GB (addresses 400 MB, balances 320 MB, offsets 80 MB, neighbours 800 MB) |
| peak memory while generating | 1.85 GB (about 92 B per address while the graph is built) |
| first start: generate, sort, build | about 8 s (0.23 + 0.95 + 6.8 s); 7.2 s at load average 10 the second time (`bench/e2e`) |
| save | 2.9 s for all four files (1.6 GB), right after generating (decision 002); `docker stop` then only folds balance updates in, if any |
| `balance` lookup | about 0.1 ms to open, microseconds to find |
| biggest hub | 543,942 connections at skew 3 (seed 100,000) |

- **Smaller** (1M) hides the effects worth showing: hubs stay small, and lookups and loading look instant whatever the design.
- **Larger** (100M+) needs more memory than many laptops have free while the graph is built (about 9 GB at 100M, an estimate), and its save, then at shutdown, would no longer have fit a default `docker stop`.
- The hard ceiling is about 429M addresses at degree 10 (4-byte offsets, decision 012). `evmgraph run` refuses more before generating, and the capacity check refuses anything the machine cannot hold.

## Consequences

- `-n` is the dataset size the program makes sure of. A cache holding fewer addresses is generated again at the larger size (decision 012); one holding more is kept as it is.
- The seed's default is not part of this decision; any seed gives a valid dataset, and the manifest records the one used.

## What would change this

- A machine class with less memory as the target (for example a 2 GB container): a smaller default. The memory check already reads a container's limit and refuses a default that does not fit, with a message naming the limit (since 9 November 2026).
