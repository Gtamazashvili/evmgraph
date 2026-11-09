# EVMGraph

A take-home task for a back-end role (brief: `docs/task-brief.pdf`, on disk only: `.gitignore` keeps it out of the repository; the README's "The task" describes it). A Go program that generates simulated EVM addresses with balances and connections, saves them to a local folder, maps them back on the next start, and answers `balance <address>` and `connections <address>` (2 levels deep) with the least memory and the fastest lookups it can.

Brief bonus items, all done: tests, a benchmark script (`bench/`), updates without a full rebuild (balance changes and new links, decision 014; not new addresses), a Dockerfile.

This folder is not a git repository.

## Commands

All commands are in `Taskfile.yml`; run `task` to list them.

```sh
task run -- -n 1000000             # evmgraph run: generate and save (or load), run until Ctrl+C
task balance -- 0x…                # balance from the saved dataset
task connections -- -limit 20 0x…  # connections, 2 levels deep
task search                        # prompt: paste an address, see its balance and neighbours
task update -- balance 0x… <wei>   # change a balance without a rebuild
task update -- link 0x… 0x…        # connect two addresses without a rebuild
task check                         # format check, vet, all tests: run before calling work done
task test:race                     # all tests with the race detector
go test ./internal/graph -run TestSearch -v   # one package or test
task test:docker                   # tests in real containers (needs Docker running)
task bench                         # every benchmark, ~20 min, writes bench/results/<date-time>/
task bench:e2e N=1000000           # the README's Measured table: the program end to end
```

macOS has no `timeout` command: use `go test -timeout`.

## Code layout

```
cmd/evmgraph/              the command-line program: one file per command, plus terminal output
  main.go                  dispatch: run, balance, connections, search, update, help (bare evmgraph prints help)
  run.go                   evmgraph run: lock, load and fold updates, or generate, build and save at once;
                           wait; fold updates at shutdown
  balance.go               evmgraph balance
  connections.go           evmgraph connections, and writeConnections (shared with search)
  search.go                evmgraph search, the interactive prompt
  update.go                evmgraph update balance|link: append to the dataset's update logs
  query.go                 what the read-only commands share: exit codes, openForQuery, parseAddress
  ui.go, draw.go,          what the program tells the person (ui implements store.Events), how it is
  format.go, log.go        drawn (step lines, box, progress bars), number formatting, colours
  run_test.go              in-process tests of run; process_test.go runs main as a real process (the test
                           binary started again with EVMGRAPH_TEST_MAIN=1)
internal/
  store/                   the cache folder: Manifest (version 4), Cache (Prepare, TryLock, Open, Clean,
                           NewDraft, Fold), Draft (WriteGraph, WriteAddresses, WriteBalances, Commit,
                           Discard), OpenReadOnly, Dataset.Balance and Graph (apply pending updates), AppendBalance, AppendLink, WaitLock
  columns/                 the column files: Write (temp, fsync, rename), MapFile, addresses, balances, uint32s
  evm/                     Address (Parse, Checksum, Hex), Wei (ParseWei), Account
  generate/                Rows from a seed; balance.go, the balance model; edges.go, the graph model
  order/                   sorting rows by address
  graph/                   CSR Graph, BuildContext, Validate, Search (BFS to depth 2)
  lookup/                  Find: interpolation search on sorted addresses
  capacity/                refuse a dataset this computer (or container) cannot hold
tests/docker/              container tests of the image built from the Dockerfile, own module (testcontainers)
bench/                     run.sh (all benchmarks), e2e/ (the README's Measured table), formats/ (the seven
                           address formats decision 002 compared; benchmarks only), summarize/
Dockerfile                 multi-stage build of the image
.github/workflows/ci.yml   on every push: gofmt, vet, go test -race, the Docker test module compiles, image builds
docs/decisions/            one Markdown record per design choice
```

Imports point one way, with no cycles: `cmd/evmgraph` → `store` → `graph` → `generate` → `order` → `evm`, and `store` → `columns` → `evm`; `lookup` needs only `evm`, `capacity` nothing. `cmd/evmgraph` is the only place that talks to the person; libraries report through return values or small interfaces (`store.Events`, progress callbacks).

## How it works

1. **Generate** (`internal/generate`): addresses, balances and positions from a seed, in blocks of 2^20 with one ChaCha8 stream per block and column, on all cores. Balances come from `balance.go` (40% empty, dust, a thin rich tail; 16 stream bytes each). Links come from `edges.go`: position p links to degree/2 earlier positions, t = floor(p · u^skew) with u uniform in [0, 1), so early positions become hubs.
2. **Sort** (`internal/order`): rows by address. A row number is the address's rank and its 4-byte ID.
3. **Build the graph** (`internal/graph`): CSR (`Offsets`, `Neighbors`), every link in both lists, every list sorted, built in two passes.
4. **Save at once** (`internal/store`, a `Draft`): one file per column in a new `cache/dataset-NNNNNN/`, fsync, then replace `cache/manifest` last, the commit point. Then `run` frees its heap and waits.
5. **Load**: every file is memory-mapped, so nothing is parsed and only touched pages use memory.
6. **Look up**: `lookup.Find` does interpolation search on the sorted addresses (no index). `graph.Search` does a BFS to depth 2 with one n/8-byte bitset. The read-only commands `balance`, `connections` and `search` (an interactive prompt, `search.go`) answer from the mapped files; `connections` and `search` print through the same `writeConnections`.

7. **Update**: `update` appends a record to the folder's `updates.log` (address, balance, CRC-32: 40 B) or `links.log` (two addresses, CRC-32: 44 B); every open reads them (`Dataset.Balance`, and `graph.Graph.Extra`, which the search merges into each list); `run` folds them into a new folder (new `balances.bin` and/or graph files via `graph.WithExtra`, hard links for the rest) at start and at shutdown. A top-up keeps saved balances and added links (`addedLinks`: saved links the regenerated graph lacks).

Cache layout: `manifest`, `lock`, and `dataset-NNNNNN/` with `addresses.bin` (20 B per row), `balances.bin` (u128 wei, 16 B, little-endian), `offsets.bin` (uint32, n+1), `neighbors.bin` (uint32 row numbers), `updates.log`, `links.log`. Row i is the same account in every file.

## Rules the code depends on

- **The main module has one dependency,** `golang.org/x/crypto` (Keccak-256 for EIP-55 checksums, decision 007). testcontainers lives in `tests/docker/`, its own module.
- **People see EIP-55.** User input goes through `evm.Parse` (checksum checked), output through `Address.Checksum` or an `evm.Checksummer`. `Hex` / `ParseHex` are for the text formats in `bench/formats` only.
- **No floating point in generation.** The compiler fuses multiply-add on arm64 but not amd64, so floats would draw different data on a Mac and in Docker. `TestEdgesGolden` and `TestBalancesGolden` pin the hashes, and their `OnAMD64` twins check them under amd64.
- **A top-up regenerates the whole dataset**, keeping the saved balances and added links of the addresses it already held. New addresses change row numbers and add entries to old lists, so nothing is patched in place.
- **Column files are never written in place.** Every write is a new temp file renamed over the old one, which is what makes hard-linking files between dataset folders (Fold) safe. Only the logs (`updates.log`, `links.log`) are appended to, and Fold never links them.
- **Memory faults are errors, not crashes.** `main` runs every command under `guardFaults` (panic-on-fault), so a mapped file another program shrinks gives a clear error and exit 1 (`TestShrunkFileIsAnErrorNotACrash`).
- **`run` checks what it opens:** addresses strictly increasing, every list strictly increasing and in range (`checkSorted`, `graph.Validate`); a dataset that fails is set aside and generated again.
- **A signal before the save stops the work and saves nothing new; one during the save lets it finish.** This keeps `docker stop` (10 s) safe at 20M.
- **Only lock holders write the cache.** `run` holds `cache/lock` while it opens and cleans the cache, generates, saves and folds, not while it waits (only `Prepare`, which creates the folder the lock lives in, comes before it); `update` while it appends. Lookups never lock.
- **Never delete what the program did not write.** Files it cannot use are moved aside (`internal/store`), and a save keeps 5% of the disk free (at most 2 GB).
- **Fill large files on the heap, then write them sequentially.** Writing through mmap on a Docker Desktop bind mount (virtiofs) took 63 s against 1.3 s for 2M addresses.
- **Long work takes a context.** `RunContext`, `BuildContext` and `EdgesContext` stop on a deadline or a signal.
- **Exit codes** (`query.go`): 0 found, 1 not found / no dataset / error, 2 usage, 3 deadline passed. `run` also exits 130 when further signals cancel a save or a fold in progress (`run.go`, `saveContext`); a first signal, even before the save, ends it with 0.

## Decisions

Each design choice has a record in `docs/decisions/NNN-slug.md`, indexed in `docs/decisions/README.md`. A record has: In short, Context, Criteria, Options, Comparison, Recommendation, Consequences, What would change this, Decision.

- A status is **Proposed** until the user explicitly chooses an option. Never mark one **Accepted** on your own.
- Say whether a figure is measured or estimated. Never present an estimate as a measurement.
- Write plain, direct sentences. Never name the owner's other projects.
- Docs are the README and these Markdown records. Don't write HTML pages or translations.

## Open work

- When the dataset or the defaults change, re-run `task bench:e2e` and update the README's Measured table and decisions 005, 011, 012 and 013 together.
- New addresses without a rebuild (decision 014 says how).
