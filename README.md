# EVMGraph

EVMGraph holds simulated EVM addresses, their balances and the addresses connected to them. On first start it generates the dataset from a seed and saves it to a local folder; on the next start it maps the saved files back into memory instead of generating them again. It answers lookups from the command line, and changes balances without rebuilding the dataset.

| Command | What it does |
|---|---|
| `evmgraph run [flags]` | Generate and save the dataset (or load the saved one), then run until Ctrl+C or `docker stop` |
| `evmgraph balance <address>` | Print an address's balance |
| `evmgraph connections <address>` | Print its connections, two levels deep |
| `evmgraph search` | A prompt: paste an address, see its balance and neighbours |
| `evmgraph update balance <address> <wei>` | Change a balance, without rebuilding the dataset |
| `evmgraph update link <address> <address>` | Connect two addresses, without rebuilding the dataset |

Bare `evmgraph` prints the help; every command takes `-h`. Every command is also a [Task](https://taskfile.dev) task: run `task` to list them. To run `evmgraph` by name from any folder, install it with `task install` or `go install ./cmd/evmgraph` (it goes to Go's `bin` folder, which must be on your `PATH`; install again after changing the code); otherwise run it through `task`, or as `./bin/evmgraph` after `task build`. Commands use `cache/` under the current folder unless given `-data`. Answers go to stdout and status to stderr; the examples below show stderr as a pipe or a log gets it, and a terminal draws the status as coloured step lines instead. Each design choice is recorded in [`docs/decisions/`](docs/decisions/README.md).

## The task

A take-home exercise for a back-end role: build a system that holds EVM addresses, their balances and the addresses connected to them.

- On first start, generate a simulated dataset whose size is configurable: addresses, each with a balance and the addresses it has transacted with.
- Keep the data in a local folder across a shutdown, and load it on the next start instead of generating it again.
- Offer a command line with at least `balance <address>` and `connections <address>`: the direct connections and their connections, two levels deep.
- Use as little memory and answer as fast as possible.
- Document the data structures, the storage format, the trade-offs, and the measured memory and lookup times.

Optional extras: tests, a benchmark script that reproduces the numbers, updates (new address, balance change, new connection) without rebuilding the dataset, and a Dockerfile. Where the task left things open, the interpretations and their reasons are in [`docs/decisions/`](docs/decisions/README.md).

## Quick start

You need Go 1.26.3 or newer. [Task](https://taskfile.dev) (`brew install go-task`) runs the commands below; without it, `go run ./cmd/evmgraph <command>` does the same. Docker is only needed for the image and its tests.

```bash
task run -- -n 1000000        # generate and save 1M addresses, then wait; Ctrl+C to stop
task balance -- 0x<40 hex digits>
task connections -- 0x<40 hex digits>
task search
task update -- balance 0x<40 hex digits> 1500000000000000000
task update -- link 0x<40 hex digits> 0x<40 hex digits>
```

Without `-n`, `run` generates 20,000,000 addresses ([decision 013](docs/decisions/013-default-size.md)): about 10 s and 1.85 GB of memory once, then 1.6 GB on disk.

## How it works

1. **Generate** addresses, balances and links from a seed, on all cores ([decisions 008](docs/decisions/008-parallel-generation.md), [011](docs/decisions/011-graph-model.md), [006](docs/decisions/006-balance-type.md)).
2. **Sort** the rows by address. A row number is then the address's rank, and the 4-byte ID the graph uses ([decision 010](docs/decisions/010-address-order-and-lookup.md)).
3. **Build the graph** as CSR, every link in both addresses' lists ([decisions 003](docs/decisions/003-connection-meaning.md), [012](docs/decisions/012-connection-storage.md)).
4. **Save at once**: four column files in a new folder, then the manifest, the commit point ([decisions 002](docs/decisions/002-shutdown-and-binary-storage.md), [009](docs/decisions/009-dataset-layout.md)). Then wait for a signal, holding nothing in memory.
5. **Look up** by mapping the files: `balance` and `connections` are separate processes that read only the pages they need ([decisions 004](docs/decisions/004-what-connections-returns.md), [005](docs/decisions/005-handling-hub-addresses.md)).
6. **Update** balances and connections through logs that every lookup reads; `run` folds them into the files when it starts and stops ([decision 014](docs/decisions/014-balance-updates.md)).

## Data structures

| Structure | Where | Size at 20M | Why |
|---|---|---|---|
| Addresses, sorted, as one array of 20-byte records | `addresses.bin`, mapped | 400 MB | The array is its own index: interpolation search finds an address in about 5 reads, with no hash table or index file. |
| Balances, one 16-byte `u128` per row, same order | `balances.bin`, mapped | 320 MB | Exact wei; row *i* is address *i*'s balance, so no key is stored. |
| Graph as CSR: `offsets` (4 bytes per row + 4) and `neighbors` (4-byte row numbers, each list sorted) | `offsets.bin`, `neighbors.bin`, mapped | 80 + 800 MB | A row's connections are one contiguous slice: two reads to find, then a straight scan. |
| One bitset, n/8 bytes, per `connections` query | heap | 2.5 MB | Marks addresses already seen in the 2-level search; nothing else grows with the result. |
| Pending balance updates, address → balance | `updates.log`, read into a map | 40 bytes per update | Lookups check it before `balances.bin`, so a change is visible at once. |
| Pending added links, row → extra neighbours | `links.log`, read into a map | 44 bytes per link | The search merges a row's added links into its list as it reads it, so both levels see them at once. |

The graph stores row numbers, not addresses, which keeps it at 44 bytes per address instead of more than 200.

## Storage format

```
cache/manifest                  version 4: which folder is current, seed, count, degree, skew
cache/lock                      the write lock: run and update hold it while they write
cache/dataset-000002/
    addresses.bin               20 bytes per address, raw, no header, sorted
    balances.bin                16 bytes per address: u128 wei, little-endian
    offsets.bin                 4 bytes per address, + 4: where each row's connections start (uint32)
    neighbors.bin               4 bytes per connection entry: row numbers (uint32), each list sorted
    updates.log                 balance updates not yet folded in: 40-byte records with a CRC-32
    links.log                   added links not yet folded in: 44-byte records (two addresses, a CRC-32)
```

- **Every file is a plain array of fixed-size records**, little-endian, with no header: the manifest says what they hold. Opening one maps it into memory in microseconds at any size, and nothing is parsed ([decision 002](docs/decisions/002-shutdown-and-binary-storage.md)).
- **A save is crash-safe.** Each file is written to a new temporary file, fsynced and renamed; the folder is fsynced; then the manifest is replaced, which is the single commit point. A crash at any moment leaves the old manifest pointing at the complete old dataset. Folders a crash left unfinished are removed on the next start, and nothing the program did not write is ever deleted: it is moved aside ([decision 009](docs/decisions/009-dataset-layout.md)).
- **A larger `-n` generates the dataset again** from the seed, because new addresses add entries to old addresses' lists. The addresses it already held keep their saved balances and added links, updates included. Datasets from older versions (manifest 1 to 3) are generated again on the next run.

## Balances

Balances are exact unsigned 128-bit numbers of wei, printed in ETH with all 18 decimals ([decision 006](docs/decisions/006-balance-type.md)). Simulated balances follow a model shaped like real rich lists: 40% of addresses hold nothing, most of the rest hold dust or a little ETH, about 0.67% hold 1 ETH or more, and about 1 in 5 million holds 100,000 ETH or more. The shares are illustrative, not measured. About 0.1% exceed 18.4 ETH, the most 64 bits of wei can hold, which is why balances are `u128`. The model uses integer arithmetic only, so a Mac and amd64 Linux generate the same balances; a golden-hash test runs on both.

## Addresses as text

Addresses are typed and printed the way wallets and block explorers show them ([decision 007](docs/decisions/007-address-input-and-output.md)):

- **Output** is EIP-55 checksummed: `0x2aA22D2D411f98B31014ae167a95999df17eAcDA`. The letter case encodes a Keccak-256 hash of the address, from `golang.org/x/crypto/sha3`, the program's one dependency.
- **Input** is 40 hex digits, with or without `0x`. All lower case and all upper case are accepted as they are. Mixed case must match the checksum, so a mistyped character in a copied address is rejected (exit code 2) instead of looked up.
- The address bytes need no change to look real: a real address is the last 20 bytes of a Keccak-256 hash, which is indistinguishable from the random bytes the generator draws.

## Finding an address

The sorted address file is the index. Addresses are uniformly random, so the first 8 bytes of the one sought predict where it sits; interpolation search lands within a few rows and reads about 5 addresses per lookup, against about 24 for binary search at 20M ([decision 010](docs/decisions/010-address-order-and-lookup.md)):

| Option | Extra space | Lookup, warm | Pages touched, average / worst |
|---|---|---|---|
| **Sorted + interpolation search (chosen)** | **0** | **0.60 µs** | **2.8 / 18** |
| Sorted + binary search | 0 | 1.4 µs | 15.5 / 17 |
| Hash index | 5.7 B per address | 0.12 µs | 4.2 / 96 |

## Connections

`evmgraph connections [-data folder] [-limit N | -all] [-timeout D] <address>` prints the address's direct connections and their connections, two levels deep ([decision 004](docs/decisions/004-what-connections-returns.md)):

```
$ evmgraph connections 0x40023569D53820Cabd5C6746F2Ca42B209FAc6DA
1 0x2aA22D2D411f98B31014ae167a95999df17eAcDA
1 0x36A902814a511b2b250683D4DBd0Eb70b3eAb3da
…
level 1: 6 addresses, level 2: 544211 addresses; showing 100 of 544217; -all for everything
```

- **A connection** means at least one transfer went between the two addresses, in either direction. It has no direction, amount or time, so either address finds the other ([decision 003](docs/decisions/003-connection-meaning.md)).
- Each address is reported once, at its shortest level, and the queried address is left out. Lines are ordered by level, then address.
- At most 100 lines by default, with the exact total of each level on stderr ([decision 005](docs/decisions/005-handling-hub-addresses.md)). `-all` streams everything, and `| head` stops it cleanly. `-timeout D` gives up a query that takes longer.
- Exit codes: 0 found (even with no connections), 1 not in the dataset or no dataset, 2 a malformed address, 3 `-timeout` passed.
- **The graph** ([decision 011](docs/decisions/011-graph-model.md)): each address links to 5 earlier ones (an average degree of 10), drawn so that early addresses become hubs, as on real chains. The default dataset's biggest hub has 543,942 connections; the example above is one of its neighbours (the second line is the hub).
- **Heavy and many queries cannot break the system** ([decision 012](docs/decisions/012-connection-storage.md#heavy-queries-and-many-queries)). A depth-2 search never reads more than the whole graph once, and its heap is one fixed bitset. The worst possible query, the biggest hub's own (9.4M results, 47% of all addresses), is answered with a few megabytes held by the process; 50 of them at once share one copy of the graph's pages.

## Updates

`evmgraph update` changes a balance, or connects two addresses, without rebuilding anything ([decision 014](docs/decisions/014-balance-updates.md)):

```
$ evmgraph update balance 0x2aA22D2D411f98B31014ae167a95999df17eAcDA 1500000000000000000
1.500000000000000000 ETH (1500000000000000000 wei)
$ evmgraph balance 0x2aA22D2D411f98B31014ae167a95999df17eAcDA
1.500000000000000000 ETH (1500000000000000000 wei)
$ evmgraph update link 0x2aA22D2D411f98B31014ae167a95999df17eAcDA 0x00101A9c9ea06a0EC590BC310872305869153184
0x2aA22D2D411f98B31014ae167a95999df17eAcDA and 0x00101A9c9ea06a0EC590BC310872305869153184 are now connected
```

- Each change is one record appended to a log in the dataset's folder, `updates.log` (40 bytes) or `links.log` (44 bytes), and fsynced before the command returns, so it survives a crash. A torn last record is detected by its CRC and ignored.
- Every lookup reads the logs when it opens the dataset, so a change is visible straight away: `balance` answers with the latest balance, and `connections` and `search` show an added link at level 1 of both addresses, and everything it brings within two steps.
- Linking two addresses that are already connected is not an error; `update` says so. An address cannot be linked to itself, and only addresses in the dataset can change.
- `evmgraph run` folds the logs into a new dataset folder when it starts and when it stops: it writes `balances.bin` if balances changed and the graph's two files if links were added, hard-links the rest, and the manifest commit makes it current.
- Growing the dataset keeps every saved balance and added link, folded or pending.
- `run` and `update` take a write lock (`cache/lock`), so they never write at the same time; `update` waits while `run` is generating, but works while `run` is merely running. Lookups never wait.
- A dataset folder that holds an update log is never cleaned away, even if its dataset turns out broken and is set aside: updates are what a person typed, and cannot be generated again.
- New addresses are not supported: a new address shifts every later row number, which every neighbour list stores, so it still needs `run` to generate the dataset again.

## Search prompt

`evmgraph search [-data folder] [-limit N]` opens the saved dataset once and answers one lookup per line, so each answer costs only the lookup and the search. Paste an address to see everything about it:

```
$ evmgraph search -limit 4
Searching 20,000,000 addresses in cache. Paste an address, or /help.
evmgraph> 0x40023569D53820Cabd5C6746F2Ca42B209FAc6DA
address     0x40023569D53820Cabd5C6746F2Ca42B209FAc6DA
balance     0.000000003676593969 ETH
neighbours  6 direct + 544,211 at level 2 = 544,217 (searched in 7.10 ms)
level 1     6 addresses, the first 4:
            0x2aA22D2D411f98B31014ae167a95999df17eAcDA
            0x36A902814a511b2b250683D4DBd0Eb70b3eAb3da
            …
level 2     544,211 addresses, the first 4:
            …
            … 544,207 more: /c 0x40023569D53820Cabd5C6746F2Ca42B209FAc6DA -all
```

| Input | Answer |
|---|---|
| `0x…` or `/s 0x…` | balance, connection totals, and the first `-limit` addresses of each level (10 by default) |
| `/b 0x…` | balance only, as `evmgraph balance` prints it |
| `/c 0x… [-limit N \| -all]` | connections, one per line, as `evmgraph connections` prints them, then the totals |
| `/help`, `/quit` | the commands; leave (also Ctrl+D, or Ctrl+C at the prompt) |

Ctrl+C during a long answer stops that answer and returns to the prompt. With piped input it prints no banner or prompt, so `cat addresses.txt | evmgraph search` gives one answer per line.

## Docker

```bash
docker build -t evmgraph .
docker run --name evmgraph -v evmgraph-data:/data evmgraph        # generate and save 20M, then wait
docker stop evmgraph                                              # exits in about 0.1 s: nothing left to save
docker run --rm -v evmgraph-data:/data evmgraph balance 0x…       # lookups and updates as one-off containers
```

- **The image** is 6.4 MB compressed: Alpine 3.24 and the static binary, built with Go 1.26.3 in a separate stage that cross-compiles for the target, so building an amd64 image on an arm64 Mac does not run the compiler under emulation. Only `go.mod`, `go.sum`, `cmd/` and `internal/` are sent to the build.
- **The dataset lives in the volume**, in `/data/cache`: the working folder is `/data`, and every command's default `-data` is `cache`, so lookups need no `-data` and the next `docker run` loads the dataset instead of generating it again.
- **It runs as an unprivileged user**, uid 10001, as process 1, so it receives `docker stop`'s SIGTERM directly. To use a host folder on Linux, make it writable for that uid, or add `--user "$(id -u):$(id -g)"`.
- **`docker stop` fits Docker's 10 s grace period at the default size:** about 0.1 s while running, under 1 s if the signal comes while it generates (it stops and saves nothing new; the next start makes the same dataset), and at most the save itself, about 4 s at 20M, if it comes during the save.
- **Under a memory limit** (`docker run -m`), the program reads the container's limit from its cgroup and refuses an `-n` that does not fit, before generating anything; a tenth of the limit, at least 64 MB, is kept for the Go runtime and the page cache of the save. Under `-m 512m` the default 20M (1.8 GB) is refused, and up to about 5.1M is accepted. Files are fsynced every 32 MB while they are written, because unwritten file data counts against the limit too.
- `task test:docker` runs the image in real containers: `docker stop` and restart, a small disk, a disk filling up while running, `kill -9` while running and during a save, the memory limit, and the grace period.

## Trade-offs

| Choice | What it gains | What it costs | Why the cost is acceptable |
|---|---|---|---|
| Plain binary columns, memory-mapped | Opening takes microseconds at any size; nothing is parsed; pages are shared between processes | Not human-readable; little-endian only | Loading is 7× faster than text even before mapping ([decision 002](docs/decisions/002-shutdown-and-binary-storage.md)); every CPU it builds for is little-endian |
| Sorted rows, interpolation search, no index | Zero extra bytes; about 3 pages read per lookup | 0.6 µs instead of a hash index's 0.12 µs; growing the dataset renumbers rows | A process starts in milliseconds, so half a microsecond cannot be seen; growth regenerates anyway |
| Plain CSR, uncompressed | Fastest heavy queries; the simplest code to check | 880 MB, where compressed forms take 565–705 MB | Compression made heavy queries 1.5–2.3× slower and barely reduces the pages a typical query reads ([decision 012](docs/decisions/012-connection-storage.md)) |
| Every link stored in both lists | Either address finds the other without a scan | Twice the neighbour entries (800 MB, not 400) | Storing each link once leaves a scan of all 100M stored links to find the other side |
| 100 results by default, exact totals always | Bounded output for hub-adjacent queries with 500,000+ results | `-all` is needed for everything | Totals answer the question; `-all` streams the rest ([decision 005](docs/decisions/005-handling-hub-addresses.md)) |
| Save right after generating | Crash-safe from the first minute; lookups work during the first run; no memory held while waiting | A signal during the save waits for it to finish | About 3 s at 20M, inside `docker stop`'s 10 s |
| Growth regenerates the whole dataset | A grown dataset equals a fresh one; the graph can never disagree with the rows | Any growth costs a full generation (about 10 s at 20M) | Saved balances, updates included, are carried over |
| Updates through logs, folded in by `run` | Crash-safe; readers unaffected; no file written in place | Balances and links only, no new addresses; the logs are read on every open | It stays short, since `run` folds it at start and stop ([decision 014](docs/decisions/014-balance-updates.md)) |
| EIP-55 output | Addresses look as on real chains; mistyped input is caught | About 0.45 µs per printed address: `-all` on the biggest hub takes 3× longer | Default output (100 lines) is unaffected |
| Lookups as separate processes, not a server | No daemon, exit codes, works in scripts | Each lookup pays process start and the first page reads | `search` amortises them for interactive use |

**Memory has three meanings here, and the Measured table shows two.** The *maximum RSS* the OS reports counts every file page a command touched. Those pages are clean copies of the mapped files: shared between processes and dropped by the OS when it needs memory, so a large RSS on a hub query is not memory the program holds. *Held by the process* is what it allocated itself, mostly one 2.5 MB bitset per `connections` query, whatever the result size. The first start is the exception: while it generates, it holds the rows and the graph on the heap (about 92 bytes per address), then returns that memory after the save.

## Measured

`go run ./bench/e2e` (or `task bench:e2e`) reproduces this table: it builds the program, generates a dataset in a temporary folder, restarts it, picks a typical address, the biggest hub and a hub neighbour from the files, and runs every lookup as a separate process, once and then 5 times warm, and `update` once. `bench/run.sh` (`task bench`) runs it together with every Go benchmark.

Measured on 9 November 2026 on an Apple M1 Pro (8 cores, 16 GB, on mains power), 20M addresses, seed 100,000, by `bench/run.sh` ([end to end](bench/results/2026-11-09-222231/e2e.md), [every Go benchmark](bench/results/2026-11-09-222231/summary.md)). Other work ran on the machine (load average about 5), so times are on the slow side; memory does not depend on load.

| | Time | Max RSS | Held by the process |
|---|---|---|---|
| First start: generate, sort, build the graph | 0.21 s + 0.85 s + 4.6 s | 1.85 GB | 1.85 GB |
| Save (all four files, 1.6 GB), right after generating | 2.8 s | | |
| Restart: open the saved dataset, check every address and graph entry once | 1.8 s | 1.29 GB | 4 MB |
| `balance` | 7.0 ms | 5 MB | 3 MB |
| `connections`, a typical address (289 results) | 8.1 ms | 10 MB | 6 MB |
| `connections`, next to the biggest hub (544,040 results counted, 100 printed) | 8.7 ms | 10 MB | 6 MB |
| `connections -all`, next to the biggest hub (544,040 lines) | 0.38 s | 410 MB | 6 MB |
| `connections`, the biggest hub itself (9,374,984 results counted) | 0.14 s | 886 MB | 6 MB |
| `connections -all`, the biggest hub itself (9,374,984 lines) | 5.8 s | 1.29 GB | 6 MB |
| `update balance` (append to the log, fsync) | 13 ms | 5 MB | 3 MB |
| `update link` (check, append to the log, fsync) | 13 ms | 5 MB | 3 MB |

- **Lookup and update times are whole processes**, start to exit, warm (median of 5; each `update` ran once). The first start, the save and the restart show the step times `run` logs. About 6 ms of each process is starting it under `/usr/bin/time`; the lookup itself is microseconds: `lookup.Find` takes 0.13 µs at 20M (`BenchmarkFind`, repeated keys that stay in the CPU cache; 0.60 µs for keys that do not, decision 010), and the depth-2 search 87 µs for a typical address and 1.04 ms next to the biggest hub at 1M (`BenchmarkSearch`; both in the [Go benchmark summary](bench/results/2026-11-09-222231/summary.md)).
- **First runs were as fast as warm ones**, because the restart before them had just read every address and graph entry to check them. A truly cold read, after the OS dropped its cache, took up to 1.6 s for the biggest hub's query (decision 005).
- **`-all` is slow on the biggest hub** because it prints 9.4M checksummed lines; counting them takes 0.14 s.
- **Memory:** *Max RSS* includes the mapped file pages each command touched, which are shared and reclaimable; *Held by the process* is macOS's peak memory footprint, what the process allocated itself (see Trade-offs).

## Code layout

| Path | What it holds |
|---|---|
| `cmd/evmgraph/` | The command-line program, one file per command: `main.go` (dispatch and help), `run.go`, `balance.go`, `connections.go`, `search.go`, `update.go`, and `query.go` for what the read-only commands share. `ui.go`, `draw.go`, `format.go` and `log.go` are the terminal output. |
| `internal/store/` | The cache folder: the manifest, opening a dataset for a run or read-only, writing a new one as a `Draft` and committing it, the update logs and folding them in, the write lock, cleaning up, and moving aside what it cannot use. |
| `internal/columns/` | The column files: crash-safe writes, and mapping addresses, balances and `uint32` columns back. |
| `internal/graph/` | The CSR graph: building it in two passes, validating it, and the depth-2 search. |
| `internal/generate/` | Addresses, the balance model and the graph's links from a seed. |
| `internal/order/`, `internal/lookup/` | Sorting rows by address, and finding an address by interpolation search. |
| `internal/evm/` | The address (EIP-55 parsing and printing) and the `u128` wei balance. |
| `internal/capacity/` | Refusing a dataset this computer, or this container, cannot hold in memory or on disk. |
| `bench/` | `run.sh` (every benchmark), `e2e/` (the Measured table), `formats/` (the seven address formats decision 002 compared, used only by benchmarks), `summarize/`. |
| `tests/docker/` | Tests that run the image in real containers (their own Go module). |

Imports point one way: `cmd/evmgraph` → `store` → `graph` → `generate` → `order` → `evm`, and `store` → `columns` → `evm`. Only `cmd/evmgraph` talks to the person; the libraries report through return values and small interfaces.

## Tests

`task check` runs the format check, `go vet` and every test; `task test:race` runs them with the race detector; `task test:docker` runs the container tests (Docker must be running). On GitHub, `.github/workflows/ci.yml` runs the format check, `go vet`, the tests with the race detector and the image build on every push. They cover, among others: every manifest and storage rule, crashes at every step of a save, recovery from a broken cache without deleting anything, the graph against a naive BFS, the golden hashes of the graph and the balances on arm64 and amd64, EIP-55 test vectors, the balance model's shape, updates and folding, the write lock, and every command as a real process.

## Not done yet

- **New addresses without a rebuild.** Balance changes and new links are supported; decision 014 says how new addresses would work.
