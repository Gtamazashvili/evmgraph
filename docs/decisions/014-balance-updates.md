# 014 · Updates without a full rebuild

**Status:** Accepted · **Choice:** balance changes and new connections, each through an append-only log per dataset folder, applied by every lookup at once and folded into the column files by `evmgraph run` when it starts and stops; new addresses not supported · **Decided:** 9 November 2026, by the project owner (balance changes first, new connections later the same day)
**Read first:** [009](009-dataset-layout.md) (folders and the manifest), [012](012-connection-storage.md) (why the graph is rebuilt on growth)

## In short

- The brief's bonus asks for updates (new address, balance change, new connection) without rebuilding the whole dataset. The project owner chose **balance changes and new connections**; new addresses are not supported.
- `evmgraph update [-data folder] balance <address> <wei>` appends one 40-byte record to `updates.log` in the current dataset folder, and `evmgraph update [-data folder] link <address> <address>` one 44-byte record to `links.log`; each is fsynced before the command returns. `balance`, `connections` and `search` read the logs when they open the dataset, so a change is visible to the next lookup.
- `evmgraph run` **folds** the logs into a new dataset folder when it starts and when it stops: `balances.bin` is written if balances changed, `offsets.bin` and `neighbors.bin` if links were added, every other file is a hard link to the old one, and the manifest commit makes it current. The logs hold only the updates made since the last fold.
- A **write lock** (`flock` on `cache/lock`) keeps `run` and `update` from writing the cache at the same time. Lookups never take it.

## Context

The dataset is four column files in address order (decisions 009, 010, 012). Row numbers are the address IDs that the graph stores, so anything that inserts a row shifts every ID after it. That is why a growing dataset is generated again in full (decision 012).

A balance is different: it lives in its own column at a fixed row, and changing it moves nothing. That makes balance changes the update that fits the layout. A new connection changes the length of two lists, so it lives in a side structure merged into every search until a fold rewrites the graph's files. A new address shifts every later row number, which every list stores, so it would need a side table searched next to the files and a renumbering of the whole graph at fold time.

## Options

| | Balance changes | New links | New addresses |
|---|---|---|---|
| What changes on disk | one 16-byte value at a known row | entries inserted into two CSR lists | a row inserted: every later row number shifts |
| Without a rebuild | a log of changes, applied on read | a small side list of extra links, merged into every search | a side list of unsorted addresses, searched separately until a rebuild |
| Effect on lookups | one map lookup per balance answer | both levels of every search merge two sources | every lookup searches two places |
| Complexity | small | medium | large; also ends "a top-up regenerates from the seed" |
| **Chosen** | **yes** | **yes** | no |

For balance changes alone:

| | Rewrite balances.bin in place | **Append-only log (chosen)** | Separate key-value store |
|---|---|---|---|
| Cost of one update | one 16-byte write into a mapped file | one 40-byte append and fsync | a dependency and its own files |
| Crash during the update | a torn 16-byte value in the live file | a torn last record, detected and ignored | depends on the store |
| Readers running meanwhile | see a half-written value | unaffected; they read the log when they open | unaffected |
| Fits the "files are never written in place" rule | no | yes | yes |

## Recommendation

**Balance changes and new connections, each through an append-only log**, folded in by `run`. Neither ever writes a live file in place, so readers and crash safety keep the rules of decision 009, and neither needs a dependency. New addresses stay out: they would renumber every row.

## Consequences

- **Log formats:** a balance record is the address (20 bytes), the new balance (16, u128 little-endian like `balances.bin`) and a CRC-32 of those 36 bytes; a link record is the two addresses (20 + 20) and a CRC-32. Records hold addresses, not row numbers, because row numbers change when the dataset grows. Reading stops at a partial or garbled last record, which a crash during an append can leave. Every record before it was fsynced and counts. The latest balance record for an address wins.
- **Reading balances:** `store.Dataset.Balance(row)` checks the log's map first. With no updates the map is empty and the check costs nothing.
- **Reading links:** opening the dataset looks up both addresses of every link record and builds `graph.Graph.Extra`, the added links per row, both directions, sorted (`graph.Overlay`). The search merges a row's extra links into its list when it reads it (`graph.Graph.list`), so both levels of `connections` and `search` include them; rows without added links cost nothing more.
- **Folding:** `store.Cache.Fold` writes `balances.bin` with the balance updates applied if there are any, and `offsets.bin` and `neighbors.bin` with the added links merged in if there are any (`graph.WithExtra`, which copies the whole graph: 880 MB at 20M). Every other file is a hard link to the old folder's, falling back to a copy where the file system cannot link. Files are never written in place, so sharing them between folders is safe. Then it commits the manifest.
- **Growth keeps balances and links:** a top-up still regenerates the dataset from the seed. The addresses it already held keep their saved balances, updates included, merged by address in one pass (`TestTopUpKeepsSavedBalances`). Added links are kept too, folded or pending (`TestTopUpKeepsAddedLinks`): links only point backwards (decision 011), so the generated links among the saved addresses are the same in the larger dataset, and any saved link the new graph lacks must be an added one. Finding them is one pass over the saved graph, checking each link against the new one. A grown dataset with added links holds the graph twice for a moment, while the links are merged in.
- **Refusals:** an address outside the dataset (`ErrNotInDataset`, exit code 1); linking an address to itself (`ErrSameAddress`, exit code 2); linking two addresses already connected is not an error: `update` says so and exits 0 (`ErrAlreadyLinked`).
- **Locking:** `run` holds the lock while it prepares, generates and saves, and while it folds. It does not hold it while it waits, so updates work while `run` is running. `update` waits for the lock. A second `run` is refused at once with a clear message (`TestRunRefusesWhileLocked`). This also closes an earlier gap: two `run`s could write the same cache at once.
- **Cleanup never removes an update log:** a dataset folder holding `updates.log` or `links.log` is left in place, even when its dataset is broken and set aside, because updates cannot be generated again (`TestCleanKeepsBalanceUpdates`). The log and the copied files are owner-only (0600), like every other file the program writes.
- **Only current datasets** (manifest version 4) take updates, because an older one is generated again on the next run.
- **Tests:** `TestBalanceUpdates`, `TestLinkUpdates`, `TestFold`, `TestLock` and `TestCleanKeepsBalanceUpdates` in `internal/store`; `TestAddedLinks` in `internal/graph` (a search with added links against a plain BFS over the merged graph); `TestRunFoldsBalanceUpdates`, `TestTopUpKeepsSavedBalances` and `TestTopUpKeepsAddedLinks` for `run`; `TestUpdateCommand` and `TestUpdateLinkCommand` end to end.

## What would change this

- **New addresses without a rebuild:** a side table searched after the main file, plus renumbering every row at fold time. Worth it if addresses arrive often between runs.
- **Very frequent updates:** fold the log on a timer while `run` runs, not only at start and stop.

## Decision

**Accepted: balance changes and new connections, each through an append-only log, folded in by `evmgraph run`** (9 November 2026). The project owner first chose balance changes out of the three kinds of update the brief lists, then added new connections the same day; new addresses are not supported.
