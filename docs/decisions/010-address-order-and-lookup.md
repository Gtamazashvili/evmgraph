# 010 · Order of address IDs and finding an address

**Status:** Accepted · **Choice:** rows sorted by address, so an address's ID is its rank; found with interpolation search on the address file itself, no index file · **Decided:** 8 November 2026, by the project owner
**Read first:** [009](009-dataset-layout.md) (one file per column, row *i* of every file is the same address)

## In short

- **Rows are kept in address order.** Every column file (addresses, balances, and later the connection files) is keyed by row number. Row → address is a plain array read in any design, so the only search ever needed is address → row. With rows sorted by address, the row *is* the address's rank, and the search runs on `addresses.bin` itself: no index file, no extra memory, no extra disk.
- **Found with interpolation search.** Addresses are uniformly random (Keccak-256 output on a real chain, ChaCha8 here), so the first 8 bytes of the address sought predict its position. A guess lands within a few rows: about 5 addresses read per lookup, against about 24 for binary search at 20M. After 8 guesses it falls back to binary search, so skewed data still costs O(log n).
- **`evmgraph balance <address>`** is built on it (brief item 3a). It opens the dataset memory-mapped, finds the row, and prints the balance exactly, in ETH and wei (decision 006). It only reads, so it can run while the main program runs.

## Context

The brief asks for `balance <address>` and `connections <address>`, "the fastest possible response" and "minimum memory footprint". Decision 009 made the row number the shared key of all column files, and noted that the order of rows had to be settled before connections are stored, because connections refer to rows. The project owner asked for a way to retrieve an address's balance, "the best solution possible because we will be adding connections to the addresses in the future too", and chose this design.

## Options

Measured with a prototype of the search: 20M addresses, in memory, warm, Apple M1 Pro, single run. "Pages" counts distinct 16 KB pages touched per lookup, including the balance page: the cost of a cold lookup on a mapped file.

| Option | Extra space | Lookup, warm | Pages, average / worst |
|---|---|---|---|
| **Sorted + interpolation search** | **0** | **0.60 µs** | **2.8 / 18** |
| Sorted + binary search | 0 | 1.4 µs | 15.5 / 17 |
| Hash index (load 0.7) | 5.7 B per address | 0.12 µs | 4.2 / 96 |
| Sorted row-number index + interpolation | 4 B per address | 1.1 µs | 7.0 / 47 |
| Scan (today's unsorted order) | 0 | 18.8 ms | up to 24,414 |

- Probes per lookup: interpolation 4.7 on average (32 at worst, with the fallback); binary 23.3.
- Building: a parallel sort took 1.1 s at 20M; a hash index 0.9 s on one core. No duplicate addresses at 20M.
- At 100M, about 6 s to sort and 570 MB for a hash index: **estimates**, not measured.

Why not the hash index, the fastest when warm: a one-shot command spends about 8.5 ms just starting the process (decision 002), so 0.6 µs against 0.12 µs cannot be seen. What a person waits for is cold pages, and the hash index touches more of them in the worst case (96) and costs memory and disk for every address. Sorting costs nothing to store, and its order also helps later: connections can keep 4-byte row IDs that never change meaning.

## Measured in this build

On the same machine, after building it (8 November 2026):

| | 1M addresses | 20M addresses |
|---|---|---|
| `lookup.Find`, warm (`BenchmarkFind`) | 112 ns | 133 ns |
| Binary search, same keys | 177 ns | 439 ns |
| Sort after generating (`order.Sort`, 8 cores; now `order.SortRows`) | 30 ms (`BenchmarkSort1M`, with a copy) | 793 ms |

End to end, `evmgraph balance` on a saved 20M dataset, row 12,345,678 (one run each, in a terminal, so including the program's own timing):

- opening the manifest and mapping both files: about 100 µs;
- the lookup: **142 µs the first time** (its few pages read in), **5.5 µs the next time** (pages already in the OS file cache).

The warm figures differ from the prototype's table (133 ns against 0.60 µs) because `BenchmarkFind` repeats 4,096 keys, which stay in the CPU cache; the prototype's figure is the more conservative one.

## How it works

1. **Sorting.** New rows are generated as `[]order.Row` (address, balance and generation position, 40 bytes) and sorted in place right after generation, before the graph is built and the dataset saved. `order.SortRows` moves every row into the bucket of its first byte in one in-place pass (an American flag sort), then sorts the 256 buckets on all cores. No extra memory: the capacity check counts only the 40 bytes per row for them (plus the graph's own, decision 012). When this was decided, the rows were 36-byte `evm.Account`s sorted by `order.Sort` (decision 012 added the position), sorted early so that the save after a signal only wrote; since 9 November 2026 the save follows generation directly (decision 002).
2. **Ordering is total.** Rows compare by address, then by balance bytes. The balance only matters for two equal addresses, astronomically unlikely for random 20-byte values, but it makes the order cover every byte of a row, so any correct sort gives byte-identical files.
3. **Top-ups merged (until decision [012](012-connection-storage.md)).** Since 8 November 2026 a top-up generates the whole dataset again, because the graph is rebuilt in full anyway; `order.Merge` was removed, and the identity of a fresh run and a top-up holds by construction (unless balances were updated in between, which a top-up keeps, decision 014). Before that, a top-up sorted only the new rows and merged them with the saved ones while writing (`order.Merge`), reading the saved rows straight from the mapped files: nothing is copied into one big slice. A fresh run and a top-up give byte-identical files (`TestFreshRunAndTopUpGiveIdenticalFiles`). The formats gained `SaveSeq` to write such a stream (`TestSaveSeqEqualsSave`).
4. **The manifest marks the order.** Version 2 and later mean sorted by address. A version 1 dataset (in generation order, saved earlier on 8 November 2026) still loads in `run`, which generates it again from its seed at its size, as the current version (`TestOlderDatasetIsGeneratedAgain`); until decision 012 the next run sorted it and saved it as version 2. The queries refuse it (`TestBalanceRefusesUnsortedDataset`).
5. **Search.** `lookup.Find(addrs, a)` guesses `lo + (key − vlo)·(hi − lo) / (vhi − vlo)` from the 8-byte big-endian prefix, using the values already read as the bounds (0 and 2^64 − 1 at first), so a guess costs no extra reads. The product is computed in 128 bits (`bits.Mul64`, `bits.Div64`) so it cannot overflow; the divisor is `vhi − vlo`, not `vhi − vlo + 1`, which wraps to 0 on the first guess. The guess is clamped to the range, and after 8 guesses the search continues as binary search. It works on any `[]evm.Address`, mapped or decoded (until `-format` was removed on 9 November 2026, that let every format be searched).

## The command

```
$ evmgraph balance -data cache 0x… 
6.262207816403200000 ETH (6262207816403200000 wei)
```

(Balances from the model of decision 006, since 9 November 2026. When this was decided, balances were fully random 128-bit values, such as `144810679432386365739.610785263483557026 ETH`.)

| Exit code | Meaning |
|---|---|
| 0 | found; the balance is on stdout |
| 1 | not in the dataset, no dataset (with how to make one), or a dataset still in generation order |
| 2 | malformed address or arguments, or a mixed-case address that does not match its EIP-55 checksum |

- All-lowercase and all-uppercase addresses are accepted as they are; a mixed-case address must match its EIP-55 checksum (decision 007, accepted on 9 November 2026). Until then, any letter case was accepted without a check.
- It is read-only: it creates, moves and cleans nothing (`TestBalanceCommand` checks that a missing folder stays missing). If a save commits a new dataset and deletes the old folder between reading the manifest and opening the files, it reads the new manifest and tries again; once mapped, a deleted file stays readable.
- It sees only saved data, plus the balance updates waiting in the dataset's log (decision 014). Until 9 November 2026 the main program saved at shutdown; it now saves right after generating (decision 002), so a lookup sees a new dataset as soon as `run` reports it saved.
- In a terminal it adds one line on stderr: the row, the time to open and to find, and whether the file was mapped. Scripts get only the answer on stdout.

## Tests

- `Find` agrees with binary search on 20,000 random hits and misses, at about 4–5 reads per lookup (`TestFindMatchesBinarySearch`).
- Clustered keys stay within 8 + log₂ n reads (`TestFindSkewedStaysLogarithmic`).
- Edge cases: empty and 1-element arrays, keys below the first and above the last, equal 8-byte prefixes, duplicates (the first is returned) (`TestFindEdgeCases`).
- Sorting matches a plain sort on 1, 3 and 8 cores and keeps every address with its balance (`TestSortMatchesPlainSort`, `TestSortEdgeCases`). Until `order.Merge` was removed (decision 012), `TestMergeEqualsFreshSort` checked that a merge equals a fresh sort.
- The command end to end, as a real process: found (exact wei and ETH; lower case, upper case and EIP-55, with or without `0x`), not found, malformed, a wrong checksum, no dataset, version 1 refused (`TestBalanceCommand`, `TestBalanceRefusesUnsortedDataset`).

## Consequences

- An address's ID is its rank in the sorted dataset. A top-up inserts new addresses among the old, so **row numbers change when the dataset grows**. Connections stored as row IDs must therefore be rewritten (renumbered) when addresses are added; the update strategy decision has to plan for that. (Decided since: a top-up generates the whole dataset, graph included, again, decision 012; updates without a rebuild change balances only, decision 014.)
- Generated datasets now take the sort time once (793 ms at 20M, while the program is already running) in exchange for lookups that read about 5 addresses instead of scanning 400 MB.

## What would change this

- **Non-random addresses** (real data with clustered prefixes, or vanity addresses): the fallback keeps lookups O(log n), but if probes grow, a small sampled index of prefixes would restore the guesses.
- **Frequent top-ups with connections stored:** if renumbering connection IDs on every growth becomes too costly, stable IDs (generation order) plus a sorted row-number index (4 B per address, the fourth option above) would trade 4 bytes per address for IDs that never change.
