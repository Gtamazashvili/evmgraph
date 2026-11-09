# 012 · How connections are stored

**Status:** Accepted · **Choice:** compressed sparse row (CSR) files by row: `offsets.bin` and `neighbors.bin`, 4-byte entries, every list sorted; built in two passes; a top-up generates the whole dataset again; manifest version 3 · **Decided:** 8 November 2026, by the project owner
**Read first:** [009](009-dataset-layout.md) (one file per column), [010](010-address-order-and-lookup.md) (rows sorted by address), [011](011-graph-model.md) (the links)

## In short

```
cache/dataset-000001/
    addresses.bin   20 B per address       (decision 002, 009)
    balances.bin    16 B per address       (decision 006)
    offsets.bin     4 B per address, + 4   uint32: where each row's list starts
    neighbors.bin   4 B per entry          uint32 row numbers; degree entries per address on average
```

- Row *i*'s connections are `neighbors[offsets[i] : offsets[i+1]]`, as row numbers, sorted. Rows are in address order (decision 010), so every list is in address order, and level 1 of a query comes out sorted for free.
- **44 bytes per address** for the graph at the default degree of 10: 80 MB of offsets and 800 MB of neighbours at 20M. The whole dataset is 80 bytes per address, 1.6 GB at 20M (measured).
- **Built in two passes** that draw the links twice, so the list of links is never held: count each row's degree (atomics), turn the counts into offsets, then fill each list through a per-row cursor and sort every list on all cores.
- **A top-up generates the dataset again from the seed.** New addresses add entries to old addresses' lists, so the graph would be rebuilt in full anyway; generating everything again makes "a top-up equals a fresh run" true by construction (`TestFreshRunAndTopUpGiveIdenticalFiles` compares all four files). The merge of decision 010 is therefore no longer used, and was removed. Since 9 November 2026 the addresses the dataset already held keep their saved balances through a top-up, so balance updates survive it (decision 014).

## Options

| | **CSR by row (chosen)** | Adjacency lists as one file per address | Edge list (pairs) |
|---|---|---|---|
| Bytes per address at degree 10 | 44 | thousands of files | 80 (pairs of 4-byte rows, stored once) |
| Neighbours of one row | one slice, 2 reads | one file open | a scan or an index |
| Sorted lists | yes, so level 1 is sorted | yes | no |
| Fits mmap and zero-copy loading | yes | no | yes, but not by row |

## How it is built

`graph.BuildContext` in `evmgraph run` (`cmd/evmgraph/run.go`), then a `store.Draft` (`internal/store`), after the rows are generated and sorted:

1. **Position → row.** Links are drawn between generation positions; rows are in address order. Each row carries its position through the sort (`order.Row`), and `order.RowOf` turns that into a 4-byte map.
2. **Pass 1: degrees.** Draw every link, and count it for both rows with atomic adds. Prefix-sum the counts into `offsets`. If the total reaches 2^32 entries, which is about 429M addresses at degree 10, the build is refused; `evmgraph run` refuses it earlier, before generating, with the largest `-n` that fits.
3. **Pass 2: fill.** Draw every link again (the same links, decision 011), and write each endpoint into the other's list through its cursor.
4. **Sort** every list, rows handed out in chunks of 4,096, so a hub's list does not hold up a whole share of rows. The files then do not depend on which goroutine filled an entry first: 1 and 8 workers give byte-identical files (`TestBuildIndependentOfWorkers`).
5. **Write** `neighbors.bin` and `offsets.bin` sequentially into the new, still uncommitted dataset folder, then `addresses.bin` and `balances.bin`, then `Draft.Commit` replaces the manifest as before (decision 009). Since 9 November 2026 the whole save runs right after the build, before the program waits for a signal. Until then, the graph's files were written before the wait and the addresses and balances at shutdown.

### Memory, and a measurement that changed the plan

The plan was to fill `neighbors.bin` through a file mapped for writing, so its 40 bytes per address would sit in the OS file cache rather than the program's heap. On this Mac's own disk that worked (a 20M build in 7.2 s, peak memory 1.0 GB). But in a Docker container with the cache on a Docker Desktop bind mount (virtiofs), the random writes of pass 2 went through the virtual filesystem: **63 s to build 2M addresses, against 1.3 s when filled in memory and written once** (measured, same container). The neighbours are now filled in memory and written in one sequential pass. The cost is 40 more bytes per address while the graph is built: about 92 bytes per address at the peak, which the capacity check counts.

Measured, default dataset of 20M addresses, one run on 9 November 2026 (load average about 4):

| Step | Time |
|---|---|
| generate addresses and balances | 230 ms |
| sort by address | 949 ms |
| build the graph: 99,999,985 links | 6.8 s |
| save at shutdown: 720 MB of addresses and balances | 613 ms |
| peak memory (max RSS and peak footprint) | 1.85 GB, about 92 B per address |

On a busier machine (load average about 12) the same steps took 0.6 s, 3.4 s, 9.5 s and 0.8 s. The save has since moved to right after the build and writes all four files: 2.9 s for 1.6 GB (`bench/e2e`, 9 November 2026, load average about 10).

`BenchmarkBuild1M`: 428 ms to build 1M addresses' graph on 8 cores.

## The manifest, version 3

Since 9 November 2026 the program writes version 4 (decision 009): the same fields without the `format` line. Version 3 as decided:

```
evmgraph dataset
version 3
format binary
seed 2000
addresses 20000000
folder dataset-000001
degree 10
skew 3
```

- `degree` and `skew` are required in versions 3 and 4 and refused in versions 1 and 2 (`TestParseManifest`).
- On load, `offsets.bin` must hold `count + 1` entries, start at 0 and end at the size of `neighbors.bin`; otherwise the manifest is moved aside and the dataset generated again (`TestRunRecovers`, "offsets of the wrong length").
- A version 1 or 2 dataset still loads, and the next run generates it again from its seed at its size, with connections (`TestOlderDatasetIsGeneratedAgain`); since 9 November 2026 a version 3 one is generated again too.
- `balance` reads versions 2 to 4. `connections` needs version 3 or 4 and says how to get it (`TestConnectionsRefusesOlderDataset`).

## Queries

`graph.Search` (decisions 004, 005) marks rows in one bitset of n/8 bytes. Level 1 is the queried row's own list. Level 2 is counted while marking and read back by scanning the bitset in row order, so no result is ever sorted or held. `BenchmarkSearch` on 1M addresses at skew 3: 80 µs for a typical query, 0.99 ms next to the biggest hub when it was built (87 µs and 1.04 ms in the full benchmark run of 9 November 2026, `bench/results/2026-11-09-222231`), including clearing and scanning the bitset. Figures for the default dataset are in decision 005.

## Docker

Before the save moved (9 November 2026), with the graph written before the program waited, the default dataset saved within `docker stop`'s default 10-second grace period: the Docker test (then `TestDefaultDatasetSavesWithinDockerStopGracePeriod`) measured 1.13 s from SIGTERM to exit, so the save no longer risked being killed by `docker stop` at the default size. Since 9 November 2026 the dataset is saved before the program waits and a signal before the save stops the work at once, so `docker stop` takes about 0.1 s while running and under 1 s during generation (`TestDefaultDatasetWithinDockerStopGracePeriod`). A container's memory limit is respected too: the capacity check reads the cgroup limit (`TestMemoryLimitIsRespected`, `TestRunsCloseToTheMemoryLimit`).

## Alternatives measured again (9 November 2026)

The project owner asked for the best possible way to store this graph, and then how heavy or many queries are kept from breaking the system. Everything below was measured on the owner's saved dataset: 20M addresses, 99,999,985 links, skew 3. The degrees: median 6, p90 15, p99 57, p99.9 254, max 543,942; 2,518 rows with degree ≥ 1,000 hold 3.8% of all entries.

| Form | Graph | Per address | Query, random (median / p99) | Next to the biggest hub |
|---|---|---|---|---|
| **Plain CSR (kept)** | **880 MB** | **44 B** | **41–59 µs / 0.8 ms** | **0.78–0.83 ms** |
| Bit-packed CSR, 25-bit row numbers | 705 MB | 35 B | 61 µs / 1.2 ms | 1.9 ms |
| Gap + varint lists | 725 MB | 36 B | 60–63 µs / 1.8 ms | 1.8 ms |
| Elias–Fano lists + Elias–Fano offsets | 565 MB | 28 B | not built; decoding like varint (estimate) | ≈ varint (estimate) |
| Renumbered for locality (degree or BFS order), then gap + varint | 768 MB: 608 MB of lists, 80 MB of offsets, 80 MB permutation | 38 B | slower | slower |
| Each link stored once | 480 MB | 24 B | cannot answer: a row's other neighbours need a full scan | — |
| Heap lists or maps; a graph database; an adjacency matrix | > 1.5 GB; ≈ 3.7 GB; 50 TB (estimates) | — | — | — |

Query times are in memory on a busy machine (load average 5–9), 2,000 random queries plus the hub-adjacent one. Typical medians are mostly the clearing of the 2.5 MB bitset, so the differences show in the p99 and hub columns.

**Decision (9 November 2026, by the project owner): keep plain CSR.** Every compressed form was 1.5–2.3× slower on heavy queries, exactly the ones that strain a system. Compression barely helps cold queries, because a typical query reads about 16 short lists, each on a different page whatever the encoding. Renumbering gains nothing on this graph (hubs, no communities): with the 80 MB permutation it needs to keep "row = address rank", it is larger than plain varint (768 against 725 MB). Elias–Fano's size depends only on list lengths, so ordering cannot improve it. **When to change:** past about 400M addresses (17.6 GB of plain CSR), bit-packing first (simple, readable at any index), then Elias–Fano if space still binds. The measuring program was in the session's scratchpad (`graphstore/`); its method is the table above.

### Heavy queries and many queries

Measured on the same dataset, with the real command:

| Query | Results | Time | Memory |
|---|---|---|---|
| a typical address | 7 + 89 | 0.01 s | 11 MB RSS |
| the biggest hub's own query, the worst possible | 543,942 + 8,831,042 (47% of all addresses) | 1.41 s cold, 0.14–0.25 s warm | 888 MB RSS: the graph's clean, shared file pages; 2.5 MB of heap |
| 50 of that worst query at once, 50 processes | all answered | 1.8 s in total | the 880 MB held once, shared |

Why heavy or many queries cannot break the system:

- **Work has a ceiling:** a depth-2 search never reads more than the whole graph once, O(N + E).
- **Heap memory is fixed:** one n/8-byte bitset and a 64 KB output buffer per query, nothing that grows with the result.
- **The large RSS is the mapped graph:** clean file pages shared by every process, which the OS reclaims under pressure, so they cannot run the machine out of memory.
- **Output is capped by default** (100 lines), and queries are read-only, so they never block or damage a save.

**Added (9 November 2026, chosen by the project owner): deadlines and cancellation.**

- `graph.Search.RunContext` and `graph.BuildContext` (with `generate.EdgesContext`) stop when their context ends, checking it every 64K neighbour entries or every batch of positions. `Search.Run` and `generate.Edges` remain as the same calls without a context; the graph is built only through `BuildContext` (`TestRunContextStops`, `TestBuildContextStops`, `TestEdgesContextStops`).
- `evmgraph connections -timeout D` gives up after D, covering opening, searching and printing. At 20M the worst query stopped at `-timeout 20ms` and answers in full without it (`TestConnectionsTimeout`).
- `evmgraph run -generate-timeout D` gives up generating (addresses, sorting, connections) after D. Nothing new is saved and the previous dataset stays current; measured, the default 20M with `-generate-timeout 3s` stopped at 3.49 s, inside the graph build. A shutdown signal before the save does the same since 9 November 2026 (until then it saved what was generated); one during the save lets it finish. `TestGenerateTimeout`, `TestSignalBeforeSaveKeepsPreviousDataset`.
- `evmgraph run -save-timeout D` gives up a save after D, with the previous dataset kept: the new dataset's save right after generating, or a fold of balance updates at start or stop (decision 014). Until 9 November 2026 it bounded the shutdown save. Set to 9 s, it ends cleanly inside `docker stop`'s 10 s instead of being killed mid-write (`TestSaveTimeout`).
- **Exit code 3 means a deadline passed**, in every command that takes one, `run` and `connections` (`TestMainDeadlines`, `TestConnectionsTimeout`). It is distinct from 1 (not found, no dataset, other errors), 2 (usage) and 130 (interrupted).
- Not added, and kept for later: a cost estimate before heavy queries, a pool of searches for long-running hosts, and pre-computed hub totals.

## Consequences

- An address's ID is its row, and a top-up renumbers rows (decision 010). Because a top-up regenerates everything, the graph is always consistent with the rows.
- Growing a dataset costs a full generation and save, about 10 s at 20M on this machine (7.2 s to generate, sort and build and 2.9 s to save in `bench/e2e`, 9 November 2026), instead of only the new part.
- 4-byte offsets cap a dataset at 2^32 − 1 neighbour entries, about 429M addresses at degree 10. Beyond that, offsets would become 8 bytes (+4 B per address).

## What would change this

- Updates without a full rebuild (bonus): appending to lists in place does not fit CSR. A small overlay of new links, merged at the next full save, would be the next step.
- Datasets past 429M addresses at degree 10: 8-byte offsets.
