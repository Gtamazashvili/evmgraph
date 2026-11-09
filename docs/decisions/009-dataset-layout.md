# 009 · Dataset layout: one file per column, committed by a manifest

**Status:** Accepted · **Choice:** one plain binary file per column (addresses, balances, later connections) in a numbered folder, made current by replacing a small manifest last · **Decided:** 8 November 2026, by the project owner
**Read first:** [002](002-shutdown-and-binary-storage.md) (binary files, mmap, safe save), [006](006-balance-type.md) (16-byte balances)

## In short

> **9 November 2026:** manifest version 4. It has no `format` line, since addresses are always binary now (decision 002), and it marks the realistic balance model (decision 006), so older datasets are generated again once. A folder may also hold `updates.log`, balance updates not yet folded in (decision [014](014-balance-updates.md)). The cache folder holds a `lock` file: `run` and `update` take an flock on it while they write. The dataset is saved right after it is generated, not at shutdown (decision 002). Code: `internal/store` (manifest, folders, `Draft`, commit, fold) and `internal/columns` (the files).
>
> **8 November 2026:** the connections arrived as two more column files, `offsets.bin` and `neighbors.bin` (decision [012](012-connection-storage.md)), committed by the same manifest, now version 3 (degree and skew added). A top-up now generates the whole dataset again instead of adding to it, and the graph's files are written into the new folder before the program waits, so the shutdown save only writes the addresses and balances.

- **Separate files, one per column.** Addresses stay as decision 002 defined them (20 raw bytes each); balances are a second file of 16 bytes each, in the same order. Row *i* of every file belongs to the same address, so the row number is the shared key, and the 4-byte address IDs that connections will use are exactly these row numbers.
- **A manifest is the commit point.** A save writes every column into a new numbered folder, fsyncs it, and only then replaces `cache/manifest`, which names the current folder. A crash at any moment leaves the old manifest pointing at the complete old dataset, whatever the number of files.
- **Chosen with connections in mind.** Connections are variable-length per address (a hub has thousands), so they can never sit in a fixed-size record: they will be two more column files (CSR offsets and neighbours). The layout is therefore several files either way; this decision makes that safe once, for any number of them.

```
cache/manifest                  which folder is current, seed, count, degree, skew
cache/lock                      the write lock (decision 014)
cache/dataset-000002/
    addresses.bin               20 B per address
    balances.bin                16 B per address, same order
    offsets.bin, neighbors.bin  the connections (decision 012, 8 November 2026)
    updates.log                 balance updates not yet folded in (decision 014)
```

## Context

Balances (decision 006) add a second per-address value. Two layouts were possible: separate files, or one file of 36-byte records (address + balance). The project owner asked for the trade-offs, then pointed out that connections will be added later.

## Options

| | Separate column files | One file of 36-byte records |
|---|---|---|
| Look up one address's balance | binary search over address pages, then 1 balance page (about 16 pages at 20M, 16 KB pages) | balance beside its address (about 16 pages) |
| Scan or sort all addresses | 20 B per address (400 MB at 20M) | 36 B per address (720 MB at 20M) |
| Memory while only addresses are used | balance pages are never touched | every page carries balances |
| Adding connections, nonces, ... | more files; nothing existing changes | redesign the record; existing caches invalid |
| Decision 002's address file | unchanged | changed |
| Files agree after a crash | needs a commit point (this decision) | one rename, always consistent |
| Disk | 36 B per address | 36 B per address |

Page counts are estimates from record sizes: a 16 KB page holds 819 addresses or 455 records of 36 bytes.

## Recommendation and decision

Separate column files under a manifest. Chosen by the project owner on 8 November 2026, after the comparison above and the remark that connections are coming.

## How a save works

1. Write the column files into a new folder, `dataset-<next number>` (a `store.Draft`), each as a temporary file that is fsynced every 32 MB and at the end, then renamed (decision 002's rules). A new dataset writes all four, `neighbors.bin`, `offsets.bin`, `addresses.bin` and `balances.bin`; a fold of balance updates (decision 014) writes only `balances.bin` and hard-links the others. When this was decided, this step wrote only `addresses.bin` and `balances.bin`.
2. fsync the folder.
3. Replace `cache/manifest` atomically: new temporary file, fsync, rename, then fsync `cache/`. **This is the commit.**
4. Delete the previous folder.

| The program dies... | What the next start sees | What it does |
|---|---|---|
| while writing the new folder | old manifest, old folder whole | loads the old dataset; removes the unfinished folder |
| after the new folder, before the manifest | old manifest | loads the old dataset; removes the new folder |
| after the manifest, before deleting the old folder | new manifest | loads the new dataset; removes the old folder |

Checked by `TestCrashAtEveryStepOfASave`, `TestCancelledWriteKeepsPreviousDataset` and, with a real SIGKILL (`docker kill`) during the save in a container, `TestKillDuringSaveKeepsPreviousDataset` (`tests/docker`).

## The manifest

A few lines of text, read strictly (`TestParseManifest`): anything unexpected makes it not a manifest.

```
evmgraph dataset
version 4
seed 100000
addresses 20000000
folder dataset-000002
degree 10
skew 3
```

- It replaces the `addresses.bin.seed` record of 8 November 2026: the seed is part of the dataset's description. A later run with another `-seed` continues the recorded dataset, with a warning, instead of mixing in another (`TestRecordedSeedWins`). Versions 1 to 3 also had a `format` line; only `format binary` can still be read.
- On start, the program loads the folder it names and checks that the address and balance files hold exactly `addresses` rows (and, from version 3, that the graph's files match them, decision 012). A manifest that is not one, names a missing folder, or describes files of the wrong length is moved aside (never deleted, following the data-safety rule) and a new dataset is generated (`TestRunRecovers`).

## Consequences

- Every column is plain binary and memory-mapped.
- Folders named `dataset-<number>` that the manifest does not name are removed on start, but only if they hold nothing except files a save writes; anything else is left with a warning (`TestCleanRemovesOnlyOurs`).
- Files of the layout before this decision (`cache/addresses.bin`, `cache/addresses.bin.seed`) are no longer read, and are left exactly as they are (`TestRunLeavesOldLayoutAlone`).
- A save needs room for the old and the new folder at once, as decision 002's temporary file already did. The capacity check counted 36 bytes per new address in memory and the address file's size plus 16 bytes per address on disk when this was decided. Now, with the graph (decision 012), it counts 40 + 12 + 4 × degree bytes per address in memory (92 at degree 10) and 20 + 16 + 4 + 4 × degree on disk (80), and a save leaves 5% of the disk, at most 2 GB, free.
- The file layout stays headerless; format version and counts live in the manifest. That answers the open "storage format details" question for now; a checksum is still not stored.

## What would change this

- **Updates** (bonus) became a log of balance changes per folder, folded into a new folder by the same commit (decision [014](014-balance-updates.md), 9 November 2026).
- **Sorting addresses for search** was decided on 8 November 2026 in [010](010-address-order-and-lookup.md): rows are kept in address order (manifest version 2), so a row number is an address's rank and lookups search `addresses.bin` itself. Row numbers therefore change when the dataset grows. Until decision [012](012-connection-storage.md) a top-up merged new rows into place; since then it generates the whole dataset again, connection files included, so they always match the new row numbers.
