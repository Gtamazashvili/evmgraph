# 002 · Graceful shutdown, binary storage and mmap loading

**Status:** Accepted · **Choice:** `signal.NotifyContext` for shutdown, plain binary files in `cache/`, loaded with `mmap` · **Decided:** 6 November 2026, by the project owner
**Read first:** [001](001-language.md) (why Go)

## In short

> **Changed on 8 November 2026 by decision [009](009-dataset-layout.md):** the address file now lives in a numbered dataset folder next to `balances.bin`, `cache/dataset-000001/addresses.bin`, and a small `cache/manifest` names the current folder and records the format (dropped in manifest version 4 on 9 November 2026, as addresses are always binary), seed and count; it replaces the `addresses.bin.seed` record. The file format, the safe save and mmap loading described here are unchanged; paths below written as `cache/addresses.bin` mean the address file in the current dataset folder.

- **Shutdown.** The program runs until it receives Ctrl+C (SIGINT), `docker stop` (SIGTERM) or a closed terminal or dropped SSH session (SIGHUP; added 8 November 2026, unless the program was started with SIGHUP ignored, as under `nohup`), which it hears through `signal.NotifyContext`. The signal does not kill it: it cancels a `context.Context`. Until 9 November 2026 the program then logged `saving addresses`, wrote the newly generated addresses with a live `x/y` counter, and exited with code 0. Since then it saves the dataset right after generating it, and the signal only folds in pending balance updates (decision [014](014-balance-updates.md)) before it exits with code 0; a signal before that first save stops the work and saves nothing new (see "Changed after the review" at the end). A second signal cancels a save without leaving a half-written file.
- **Storage.** Addresses are saved as **plain binary**: each address as its raw 20 bytes, one after another, in `cache/addresses.bin`. No header, separators or text.
- **Why binary, measured.** For 1 million addresses it is the smallest file (20 MB, against 43–50 MB for text, JSON and CSV) and loads **7× faster than a text file and 37× faster than JSON** (8 ms against 59 ms and 297 ms), allocating only the 20 MB the addresses need.
- **Loading with mmap.** The file is mapped into memory instead of read, so its bytes are used as the address list directly. Opening 1 million addresses takes **26 µs instead of 7.9 ms** (about 300× faster), and the whole program runs in **4.5 MB of memory and 8.5 ms for 1 million or 50 million addresses** alike (measured on 6 November 2026, from process start to exit, printing one address).

## Context

The brief asks for two things this decision answers:

1. *"Data must be preserved in a local folder on shutdown and loaded back on the next startup, instead of being regenerated."*
2. *"Minimum memory footprint"* and *"fastest possible response"*.

A Go program that receives Ctrl+C is killed immediately by default. If that happens while the data file is being written, the next start finds half a file and either crashes or, worse, loads half a dataset without noticing. So "preserved on shutdown" needs two parts: a way to hear the shutdown request, and a way of writing files that can never leave a half-written file in place.

The file format sets the disk size and how much work every start does to load the data. Each CLI command may start a new process (see [001](001-language.md)), so load time is paid again on every command.

## Part 1: shutdown

### Options

| Option | What happens on Ctrl+C or `docker stop` | Half-written file possible? |
|---|---|---|
| A. Do nothing (Go default) | Process dies at once, wherever it is | Yes |
| B. `signal.Notify` with a channel | We receive the signal on a channel and must wire it to every job by hand | No, if wired correctly |
| **C. `signal.NotifyContext`** | The signal cancels a `context.Context`; jobs already take a context and check `ctx.Err()` | **No** |

**C** is B packaged in the form Go code already uses for cancellation. Every long-running function takes `ctx` as its first argument, so no extra channels or global flags are needed, and the same functions can be cancelled by a test with `context.WithCancel`.

### How it works

> **Until 9 November 2026.** This section and the next describe the save-on-shutdown design of 6 November. Since 9 November the dataset is saved right after it is generated, a signal before that save stops the work and saves nothing new, and the shutdown signal folds in pending balance updates; see "Changed after the review (9 November 2026)" at the end.

The program is long-running. It loads `cache/addresses.bin`, or generates `-n` addresses if there is none, and then waits for a shutdown signal. Generated addresses live only in memory until that signal arrives.

```
start ─► load cache/addresses.bin ──────────────────────► running … ─► signal ─► "no new addresses to save", exit 0
     └─► none: generate -n addresses (in memory) ───────► running … ─► signal ─┐
                    │ signal while generating                                  │
                    └─► keep the addresses generated so far ───────────────────┤
                                                                               ▼
                                                    "saving addresses"
                                                    65536/30000000
                                                    131072/30000000  … one update per 65,536 addresses
                                                    30000000/30000000
                                                    "saved 30000000 addresses", exit 0

second signal during the save ─► save cancelled, temp file deleted, previous file kept,
                                 "interrupted: nothing was saved", exit 130
```

```go
// shutdownSignals: os.Interrupt, syscall.SIGTERM, and syscall.SIGHUP unless it is ignored (nohup)
ctx, stop := signal.NotifyContext(context.Background(), shutdownSignals...)
err := run(ctx, *n, *seed, *dir, *show) // waits on <-ctx.Done(), then saves
stop()
```

Inside `run`, the save gets its own `signal.NotifyContext`, because the first signal has already cancelled `ctx`. That is what lets a second Ctrl+C stop a long save cleanly instead of killing the process mid-write. (Now `saveContext` in `cmd/evmgraph/run.go`: it listens with `signal.Notify` and cancels the save on the second signal, counting one that came before the save began.)

### Saving: on shutdown, then rename

Changed on 6 November 2026 at the project owner's request. The first version saved **as soon as the addresses were generated**; from then until 9 November 2026 the program ran until it was told to stop and **saved on shutdown**, logging `saving addresses` and a live `x/y` counter, where `y` is the number of addresses generated and `x` the number written so far.

The trade-off, stated openly: anything generated since the last save lives only in memory. A crash, a power cut or `kill -9` (SIGKILL, which no program can catch) loses it, and the next start generates it again from the seed. Because the data is reproducible from the seed, nothing is lost for good. Once updates are supported (bonus), unsaved changes would be lost, and periodic saves will be needed then.

The write itself can never leave a broken `addresses.bin`:

```
1. write all bytes to   cache/addresses.bin.<random>.tmp   (a new file, created exclusively)
2. flush and fsync      (the bytes are on disk, not just in OS memory)
3. rename the temp file → cache/addresses.bin   (a rename is atomic: all or nothing)
```

The temporary file gets a random name and is opened with `O_CREATE|O_EXCL` (`os.CreateTemp`), so the save never follows a symlink and never writes into a file that already exists. Changed on 8 November 2026: the first version used the fixed name `addresses.bin.tmp`, and a symlink planted under that name while the program ran made the save overwrite the file the link pointed to (found in a security review and reproduced). `TestSaveDoesNotWriteThroughSymlinks` checks every format.

| What happens | What is left on disk | Next start |
|---|---|---|
| Signal while running | complete `addresses.bin` with every generated address | loads it |
| Signal while generating | `addresses.bin` with the addresses generated so far | loads that smaller dataset |
| Signal before anything was generated | nothing ("nothing to save") | generates again |
| Second signal during the save | nothing new (temp file deleted); a previous `addresses.bin` is kept | loads the previous file, or generates again |
| `kill -9`, crash or power cut | a stale `addresses.bin.<digits>.tmp` at most; unsaved addresses are lost | removes it (`cacher.RemoveStaleTemps`, now `columns.RemoveStaleTemps`, only names a save can create), generates again |

### Verified

Run on 6 November 2026 with the built program and real signals, 30 million addresses (a 600 MB file):

- **Signal while running:** logged `shutdown signal received`, `saving addresses`, then 458 progress updates from `65536/30000000` to `30000000/30000000` (one per 65,536 addresses plus the final one), `saved 30000000 addresses`, exit code 0, a 600,000,000-byte file.
- **Restart:** `loaded 30000000 addresses`, then on the signal `no new addresses to save`, exit code 0.
- **Signal 0.15 s into generation:** `10420224 of 30000000 addresses generated`, saved those, exit code 0, a 208,404,480-byte file (10,420,224 × 20).
- **Second signal during the save:** `interrupted: nothing was saved`, exit code 130, no file and no temporary file left.

`TestRunSavesOnShutdown` and `TestRunShutdownBeforeGenerating` in `cmd/evmgraph/run_test.go`, and `TestRoundTrip` and `TestCancelledFirstSaveLeavesNothing` in `bench/formats/formats_test.go` (then `internal/cacher/cacher_test.go`), check the same in the test suite. Since 9 November 2026, `TestDatasetIsSavedBeforeWaiting`, `TestSignalBeforeSaveKeepsPreviousDataset` and `TestSaveTimeout` in `cmd/evmgraph/run_test.go` check the save right after generating.

## Part 2: storage format

### Options

| Format | Bytes per address | What it costs |
|---|---|---|
| **Plain binary** | **20** | Not human-readable; we define the layout ourselves |
| Binary + gzip | 20.01 | Compression time for nothing: the addresses are random, so there is no pattern to compress |
| gob (Go's own binary encoding) | 31 | Go-only, adds a length prefix per address, decodes through reflection |
| Hex text, one per line | 43 | Every byte becomes 2 characters, plus `0x` and a newline; must be parsed back |
| JSON array | 45 | Text plus quotes and commas; a full parser on every start |
| CSV (`id,address`) | 49.9 | Text plus an ID column and commas |

### Benchmarks

**Setup.** 1,000,000 addresses, Apple M1 Pro, 16 GB, macOS, Go 1.26.3. Each benchmark ran 5 times (`-count 5`); the table shows the median. Files were in the OS file cache (warm), so the numbers measure encoding and parsing, not the disk. Writes are not fsynced in the benchmark.

| Format | File size | Write | Load | Memory allocated by load | Allocations by load |
|---|---|---|---|---|---|
| **Plain binary** | **20.0 MB** | **7.6 ms** | **8.0 ms** | **21.1 MB** | **7** |
| Binary + gzip | 20.0 MB | 260 ms | 22.0 ms | 111.3 MB | 51 |
| Hex text | 43.0 MB | 102 ms | 59.0 ms | 159.3 MB | 1.0 million |
| CSV | 49.9 MB | 185 ms | 112 ms | 191.3 MB | 2.0 million |
| JSON | 45.0 MB | 128 ms | 297 ms | 291.3 MB | 1.0 million |
| gob | 31.0 MB | 236 ms | 295 ms | 204.2 MB | 1.0 million |

Load time, relative to plain binary (each `█` ≈ 10 ms):

```
binary      8 ms  █
binary+gzip 22 ms ██
hex text    59 ms ██████
csv        112 ms ███████████
gob        295 ms ██████████████████████████████
json       297 ms ██████████████████████████████
```

### What the numbers say

1. **Binary is the only format whose load allocates just the data.** 21.1 MB is the 20 MB of addresses plus a 1 MB read buffer, in 7 allocations. Every other format allocates 5.5–14.5× the data size: text formats turn each line into a temporary string before decoding it, and gzip, gob and JSON add decoder buffers and growing slices.
2. **Text costs 2.15× the disk and 7× the load time.** Hex spends two characters per byte, and parsing 1 million lines is most of the load time.
3. **Compression does not help.** gzip saved 0 bytes (20.01 MB against 20.00 MB) and made writing 34× slower. Addresses are random bytes, which do not compress.
4. **Binary is the only format that can be used without loading at all.** Because the bytes on disk are exactly the bytes in memory, the file can be mapped into memory (`mmap`) instead of read, so start-up cost drops towards zero. No text format allows that. This is now implemented: see "Part 3: loading" below.

Lookup speed is the same for every format once the addresses are in memory, so it is not in this table; it belongs to the "Finding an address" decision.

### Measured again: the real implementations, side by side

The table above came from test-only copies of each format. Since 6 November 2026 every format is a real implementation of one interface, `AddressCacher` in the `cacher` package (since 9 November 2026 `Format` in `bench/formats`), one file per format (`binary.go`, `gzip.go`, `text.go`, `csv.go`, `json.go`, `gob.go`; in `bench/formats`, binary is `Binary` in `formats.go`, which uses the program's own `internal/columns`). All six share the same save path (temporary file, fsync, rename, cancellation, the `x/y` counter) and are held to the same rules by one set of contract tests (`bench/formats/formats_test.go` (then `internal/cacher/cacher_test.go`)): the same input must load back as the same output, a missing file is `fs.ErrNotExist`, unreadable contents are `ErrInvalidFile`, a cancelled save keeps the previous file, and so on. Until 9 November 2026 the program could run with any of them (`-format json`), binary by default; since then it writes binary only (see the end).

**Setup.** 1,000,000 addresses, Apple M1 Pro, Go 1.26.3, median of 5 runs, warm OS file cache. Save is the full production path, **including fsync**, which the first table left out. *Open* is the cost before the first address can be used; *read all* also touches every address, which is what makes the comparison fair for binary, whose mmap load defers reading.

| Format | File | Save | Open | Open + read all | Memory allocated by load | Allocations by load |
|---|---|---|---|---|---|---|
| **binary (mmap)** | **20.0 MB** | **16.2 ms** | **0.026 ms** | **1.9 ms** | **0 MB** | **5** |
| gzip | 20.0 MB | 269 ms | 16.8 ms | 17.7 ms | 21.1 MB | 14 |
| text | 43.0 MB | 38.6 ms | 33.7 ms | 37.1 ms | 21.1 MB | 9 |
| csv | 49.9 MB | 164 ms | 111 ms | 107 ms | 142.0 MB | 2.0 million |
| json | 45.0 MB | 47.9 ms | 410 ms | 415 ms | 221.1 MB | 7.0 million |
| gob | 31.0 MB | 252 ms | 278 ms | 282 ms | 161.1 MB | 1.0 million |

Creating the addresses (generation, the same for every format): **15.8 ms** and 21.3 MB for 1,000,000.

What changed against the first table, and why:

- **Text and JSON save 2–3× faster** (38.6 and 47.9 ms against 102 and 128 ms). The real implementations encode each address into a reused buffer instead of building a new string per address.
- **Text, gzip and the others load into a slice allocated once**, sized from the file, instead of one that grows; text and gzip now allocate only the 21 MB the data needs.
- **JSON loads more slowly** (410 ms against 297 ms) because it is now read as a stream, one string at a time, so it never holds the whole document as text. That trades speed for memory: 221 MB allocated against 291 MB.
- **The ranking did not change.** Binary is still the smallest file, the fastest save and by far the fastest load: 650× faster to open than gzip, the next fastest, and 16,000× faster than JSON. Even reading every address, binary (1.9 ms) is 9× faster than gzip (17.7 ms).

### Reproduce

```bash
go test ./bench/formats -run '^$' -bench 'Save|Load' -benchmem -count 5   # the six formats
go test ./internal/generate -run '^$' -bench Generate -benchmem -count 5 # creating the addresses
```

## Part 3: loading (map, do not read)

Added on 6 November 2026, at the project owner's request to optimise loading and parsing too, not just the file size.

### What a loading strategy is

The loading strategy is how the data on disk gets into the program when it starts. It matters here because every CLI command (`balance`, `connections`) starts a new process, so the data is loaded again for every command. Whatever loading costs in time and memory, every command pays it before it can answer.

### What mmap is

`mmap` (memory map) asks the operating system to make a file appear as part of the program's memory.

- **Reading** a file copies its bytes into memory the program owns: all 20 MB are copied before the first address can be used.
- **Mapping** a file returns an address range at once and copies nothing. The program reads that range like an ordinary array. The first time it touches a page that is not loaded yet, the CPU pauses the program (a *page fault*), the operating system loads that one page from the file, and the program continues without noticing.

```
read:   disk ──copy all 20 MB──► program memory ──► use addrs[500000]

mmap:   program memory  ◄─ mapped ─  file on disk        (nothing copied yet)
        use addrs[500000] ─► page fault ─► OS loads that one page ─► value
        pages never touched are never loaded
```

Think of a book: reading the file is photocopying the whole book before you start; mapping it is being handed the book and opening only the page you need.

This works only because the format is plain binary. The file holds 20 raw bytes per address, one after another, which is exactly how Go lays out `[]Address` in memory, so the mapped bytes *are* the address list: no conversion, copying or parsing. A text, CSV or JSON file must be parsed into a different shape first, so it can never be used in place.

### Measurements

Plain binary needs no parsing, but the first loader still copied every address out of a read buffer, one at a time: 7.9 ms and 21 MB for a million addresses. `OpenMapped` (`internal/cacher/mmap.go`; now `columns.OpenAddresses` in `internal/columns/addresses.go`) maps the file instead and views its bytes as `[]Address` with `unsafe.Slice`. Nothing is read, copied or parsed when the file is opened; the operating system loads a page (16 KB on Apple silicon, 4 KB on most Linux machines) from disk only when an address on it is first used.

**Setup.** Same machine and file as above (1,000,000 addresses, warm OS file cache), median of 5 runs, `go test ./bench/formats -run '^$' -bench Open -benchmem -count 5` (`bench/formats/loading_test.go`, then `internal/cacher/loading_test.go`).

| Loader | Open only | Open + one lookup | Open + read all | Heap allocated |
|---|---|---|---|---|
| Copy loop (first version) | 7.94 ms | 8.06 ms | 9.17 ms | 21.1 MB |
| One read into `[]Address` | 3.39 ms | 3.42 ms | 4.08 ms | 20.0 MB |
| **mmap** | **26 µs** | **31 µs** | **1.73 ms** | **472 bytes** |

Whole program, from process start to exit, printing one address (`/usr/bin/time -l`, 50 runs averaged):

| Dataset | File | Peak memory (RSS) | Wall time per run |
|---|---|---|---|
| 1,000,000 addresses | 20 MB | 4.4 MB | 8.5 ms |
| 50,000,000 addresses | 1 GB | 4.6 MB | 8.5 ms |

What this shows:

- **Opening is about 300× faster** than the copy loop and no longer depends on the file size: a 1 GB file opens as fast as a 20 MB one.
- **Memory follows use, not file size.** A command that touches one address pages in one page. The 8.5 ms per run is almost all process start-up, not loading.
- **Even reading every address is faster with mmap** (1.73 ms against 9.17 ms), because the bytes are used where they are and never copied.

Trade-offs, stated openly:

- `unsafe` is used in one function (`OpenMapped`, now `columns.OpenAddresses`; since the balance and graph files, `internal/columns` views them the same way in `OpenBalances` and `OpenUint32s`, and nowhere else in the program). It is sound because `Address` is `[20]byte`: no padding and no alignment requirement, so every 20 bytes of the file are exactly one `Address`. `TestLoadersAgree` checks all three loaders return the same addresses.
- `syscall.Mmap` is Unix-only (`//go:build unix`): macOS and Linux, which includes Linux containers. Windows is not supported.
- The slice is valid only until `Close`. If another process truncated the file while it is mapped, reading past the new end would crash the process (SIGBUS); our own writes never truncate it, because they replace the file by rename.
- The first lookup after a reboot (cold cache) waits for a disk read. Cold-start numbers belong to the benchmark-method decision.

### Can text use mmap too? Measured

Text is the only other format where mmap can do real work, because every line is exactly 43 bytes (`0x`, 40 hex digits, a newline): line *i* starts at byte *i* × 43, so the file can be split into chunks or read at any position. Two variants were measured on 6 November 2026, same machine, 1,000,000 addresses, median of 5 runs:

- **`text-mmap`**, a real format (then an `AddressCacher`, now `TextMapped`, a `formats.Format`) in `bench/formats/text_mmap.go` that passes all the contract tests: it maps the file instead of reading it, then decodes all lines on every CPU core at once (8 on this M1 Pro: 6 performance, 2 efficiency).
- **A lazy text view**, an experiment in `bench/formats/text_mmap_test.go`: it maps the file and decodes a line only when that address is asked for. It cannot be an `AddressCacher` (now a `Format`), because the interface hands back decoded addresses, and the program does not use it.

| Loader | Open | Open + one lookup | Open + read all | Heap memory | Disk |
|---|---|---|---|---|---|
| text, read + decode (before) | 34.3 ms | 34.3 ms | 34.8 ms | 21.1 MB | 43 MB |
| text, mmap + decode on 1 core | 22.9 ms | 22.9 ms | 22.9 ms | 20.0 MB | 43 MB |
| **text-mmap** (mmap + decode on 8 cores) | **6.8 ms** | **6.8 ms** | **7.6 ms** | 20.0 MB | 43 MB |
| text, lazy view | **0.015 ms** | **0.040 ms** | 26.5 ms | **0 MB** | 43 MB |
| **binary (mmap)** | **0.015 ms** | 0.031 ms | **1.8 ms** | **0 MB** | **20 MB** |

The 1-core and 8-core rows are the decode step alone (`BenchmarkTextDecode`), on a file that is already mapped. The binary one-lookup figure is from the loader table above.

What it shows:

- **mmap plus parallel decoding makes text 5× faster to load** (34.3 → 6.8 ms). About two-fifths of the gain (34.3 → 22.9 ms) is mmap with a simpler decoder (no line scanner, no read copy); the rest is using all 8 cores (22.9 → 5.8 ms for the decode alone).
- **It still loads 450× slower than binary** (6.8 ms against 0.015 ms) and needs 20 MB of heap, because every address is decoded and copied before the first one can be used.
- **The lazy view matches binary for a CLI command.** Opening costs the same 15 µs, one lookup 40 µs against 31 µs, and no heap. For a program that starts, answers one `balance` query and exits, text could be as fast as binary.
- **But it pays elsewhere.** The file and the OS file cache are 2.15× larger (43 against 20 bytes per address), so the memory footprint of a long-running or busy process is about twice binary's; reading every address is 14× slower (26.5 against 1.8 ms), because each read decodes hex; and every use of an address goes through a decode step that can fail, where binary hands back the bytes themselves.
- **Binary stays the choice.** It is the only format that is both instant to open and cheap to read in bulk, at the smallest size, with no decoding anywhere.

Reproduce: `go test ./bench/formats -run '^$' -bench 'Load/.*/(binary|text|text-mmap)$|TextDecode|TextView' -benchmem -count 5`.

### The full benchmark run

Every benchmark in the project, run together under the same conditions, is summarised in [`bench/results/2026-11-06-212009/summary.md`](../../bench/results/2026-11-06-212009/summary.md), with the raw output in the same folder. `bench/run.sh` reproduces it: it runs every benchmark 10 times, one package at a time (`-p 1`), refuses to start while another run is in progress, records the machine and its load before and after, and writes a new dated folder with the summary.

This run (6 November 2026) confirms every ranking above, but its exact times are less steady than the earlier measurements: Docker Desktop's virtual machine started about 8 minutes into the run and another project's tests ran at the same time. The summary's spread column shows how much each result varied between its 10 runs; only 2 of 36 varied by less than 5%. For README figures, rerun `bench/run.sh` with Docker and other heavy programs closed.

## Decision

> **Deadlines (9 November 2026):** `-save-timeout D` gives up a save after D (the new dataset right after generating, or balance updates folded in at start or shutdown) and keeps the previous dataset, exiting with code 3. Set just under `docker stop`'s grace period (for example 9 s), it ends cleanly instead of being killed mid-write. `-generate-timeout D` does the same for generating a dataset. See [012](012-connection-storage.md#heavy-queries-and-many-queries).

> Paths and the seed record were changed on 8 November 2026 by decision [009](009-dataset-layout.md) (one file per column in a numbered folder, committed by a manifest); the save moment and the formats were changed on 9 November 2026 (see "Changed after the review" at the end); everything else below stands.

**Accepted.** Chosen by the project owner on 6 November 2026:

- Shutdown is handled with `signal.NotifyContext` (SIGINT, SIGTERM, and SIGHUP from 8 November 2026 unless it is ignored), so the program shuts down gracefully instead of being killed mid-write.
- Data is stored as plain binary files, for memory efficiency, in the `cache/` folder (`-data` flag to change it).
- The program runs until a shutdown signal and saves newly generated addresses then, logging `saving addresses` and an `x/y` counter that rises as they are written. Changed from save-on-generate on 6 November 2026, at the project owner's request. (Changed back on 9 November 2026: the dataset is saved right after it is generated; see the end.)
- The address file is loaded with `mmap` and used in place (`columns.OpenAddresses` in `internal/columns/addresses.go`), for the fastest load and parse and the smallest memory footprint. Added at the project owner's request on 6 November 2026, with the measurements above.

## Consequences

- `cache/addresses.bin` is 20 × N bytes. The address count is the file size divided by 20; a size that is not a multiple of 20 is rejected as corrupt (`TestAddressesEdgeCases` in `internal/columns`).
- Every long-running function except the sort takes a `context.Context` and checks it in batches of 65,536 items, so a shutdown request is answered within milliseconds. Sorting the rows (`order.SortRows`, about 1 s at 20M) takes none; the program checks the context right after it.
- Every column file and the manifest is written to a new, randomly named temporary file, fsynced, then renamed; the balance-update log (decision 014) is appended to instead. Stale temporary files of the program's own naming, and dataset folders a save left behind that hold nothing but files a save writes (`TestCleanRemovesOnlyOurs`), are removed on start; nothing else is. The saved file is readable by its owner only (mode 0600, as `os.CreateTemp` creates it; before 8 November 2026 it was 0644).
- A missing, invalid or broken cache never stops the program. A missing `cache/` is created and logged; a file in its place is **moved aside** to `<name>.invalid-<date>-<time>`. Until decision 009 an `addresses.bin` that was not a regular file or whose size was not a multiple of 20 was moved aside the same way (`cache/addresses.bin is invalid; moved it to cache/addresses.bin.invalid-20261108-193402 (delete it if you do not need it)`). Now the **manifest** is moved aside when the files it names are missing, not regular files or of the wrong size, with a plain log line (`cache/manifest is invalid (the dataset it describes is incomplete: invalid column file: cache/dataset-000002/addresses.bin is 33 bytes, not a multiple of 20); moved it to cache/manifest.invalid-20261109-214749 (delete it if you do not need it)`), the broken dataset folder, which holds only files a save writes, is removed as the program's own, and the dataset is generated again (`TestRunRecovers`). Only those cases count as invalid (`ErrInvalidFile`); a file that cannot be read, for example for lack of permission, is reported and never touched (`TestRunKeepsUnreadableFile`). Changed on 8 November 2026: the first version deleted what it could not use, and a security review showed that destroys other people's data: `-data notes.txt` deleted `notes.txt`, and `-format csv -data ~/Documents` deleted an unrelated `addresses.csv`. The program cannot tell its own broken cache from someone else's file, so it never deletes either (`TestRunLeavesOldLayoutAlone`, `TestRunKeepsFileGivenAsDataFolder`, `TestSymlinkedManifestIsMovedNotFollowed`, `TestMoveAsideNeverReplaces`). The cost: what is moved aside keeps its disk space until it is deleted by hand, which the log line says. Without a checksum, an address or balance file of the right size with damaged bytes cannot be detected yet (the graph's files are range-checked when `run` opens them, decision 012); that belongs to the "storage format details" decision.
- `-n` is the dataset size the program makes sure of. If the cache holds fewer addresses (3,000 cached, `-n 10000`), it generates all 10,000 again from the recorded seed and saves them at once: new addresses add links to old addresses' lists, so the graph is rebuilt anyway (decision [012](012-connection-storage.md)). The result equals a fresh run of 10,000 with the same seed (`TestRunTopsUpCache`, `TestFreshRunAndTopUpGiveIdenticalFiles`), except that the 3,000 addresses it already held keep their saved balances, updates included (`TestTopUpKeepsSavedBalances`). If the cache holds more than `-n`, all of them are kept. The seed is recorded in `cache/manifest` (decision [009](009-dataset-layout.md)), and a top-up always continues it: with another `-seed`, the program logs `cache holds the dataset of seed 1; using that seed, not -seed 2 (delete the cache to change it)` (`TestRecordedSeedWins`).

  How this worked before: until decision 012 (8 November 2026) a top-up generated only the missing 7,000, the addresses at positions 3,000 to 9,999 of the seed's sequence (since decision [008](008-parallel-generation.md) starting at the block that holds position 3,000), and until 9 November 2026 it saved all 10,000 on shutdown. Until decision 009 the seed was recorded next to the address file, in `cache/addresses.bin.seed` (the seed in decimal and a newline; one record per format file), written atomically just before a fresh dataset was saved; a cache without a record, made before 8 November 2026, still loaded but refused a top-up, because its seed was unknown, and a record that was not a number was moved aside. Changed on 8 November 2026, after a code review: until then the file recorded no seed, and a top-up with another `-seed` silently mixed two datasets. The record was a separate file so that the address file keeps this decision's layout, raw addresses from byte 0, mapped as they are; the manifest keeps it that way.
- Before generating, `capacity.Check` (`internal/capacity/capacity.go`) refuses a size this computer cannot hold, with exit code 1 and a plain message instead of a crash. Today every address it generates takes 40 + 12 + 4 × degree bytes of memory until the save (its row, then its share of the graph being built: 92 bytes at degree 10), which must fit in physical memory, or in a container's memory limit less a reserve for the program itself (since 9 November 2026); and the whole dataset, all four column files at 20 + 16 + 4 + 4 × degree bytes per address (80 at degree 10), must fit in the free disk space of the cache folder **while leaving 5% of the disk free, at most 2 GB**, for swap, logs and other programs (`TestCheck`, `TestCheckDiskKeepsReserve`, `TestCheckCountsEveryColumn`, `TestCheckInContainer`). When this was decided, it counted 20 bytes per address for both, and the new addresses stayed in memory until shutdown. The disk is checked again just before saving (`capacity.CheckDisk`), because generating can take long at large sizes and the disk may fill meanwhile, and before folding balance updates in; if the files no longer fit, nothing is written and the previous dataset is kept. Sizes are checked for overflow, so a huge `-n` cannot wrap around to a small size that passes (`TestCheckDiskDoesNotOverflow`). Changed on 8 November 2026: the first version let a save fill the disk to the last byte, checked it only once, and could overflow for formats larger than 20 bytes per address. Examples from 8 November 2026, before the graph: `cannot save 800000000 addresses on this computer: the file needs 50.4 GB of disk space, and 33.1 GB can be used (35.1 GB is free, and 2.0 GB is kept free for the system)`, and `-n 1000000000000000` on a 16 GB machine gave `cannot generate 999999999999990 addresses on this computer: they need 20.0 PB of memory, and it has 17.2 GB`. Since the graph (decision 012), a size whose neighbour entries do not fit 4-byte offsets (about 429M addresses at degree 10) is refused first, with `cannot connect … addresses at degree 10 …`. Physical memory comes from `hw.memsize` on macOS and `sysinfo` on Linux, and a container's limit from its cgroup (`memory.max`, or `memory.limit_in_bytes` on cgroup v1).
- The file has no byte-order question yet (addresses are plain bytes). Balances and connections will be integers, so their files must fix byte order (little-endian, per [001](001-language.md)). (They do now: `balances.bin`, `offsets.bin` and `neighbors.bin` are little-endian, decisions 006 and 012.)
- Plain binary cannot be read with a text editor. A future `dump` command or `xxd cache/addresses.bin` covers inspection.

## Still open, decided elsewhere

- **Storage format details:** a header with a magic number, version and checksum; one file per array or one file for everything. (Decided on 8 November 2026 by [009](009-dataset-layout.md): one file per column, no header; the version and counts live in the manifest, and no checksum is stored.)
- **Order of address IDs:** the file was in generation order when this was written; sorting it enables binary search. (Decided on 8 November 2026 by [010](010-address-order-and-lookup.md): rows sorted by address, found with interpolation search.)

## What would change this

- A reviewer requires the data to be human-readable on disk: keep binary as the working format and add an export command.
- A header becomes necessary (versioning, checksums): it stays binary, with a fixed-size header in front of the same 20-byte records.

## Changed after the review (9 November 2026)

Both changes were **accepted** by the project owner, following the external review:

- **The dataset is saved right after it is generated,** not at shutdown. The brief asks that data be "preserved in a local folder on shutdown", and saving at once preserves it from that moment. A crash or `kill -9` while the program waits no longer loses a first run, lookups work during the first run, and the program holds nothing in memory while it waits (its heap is returned to the OS). Shutdown still matters: `evmgraph run` folds pending balance updates in when it stops (decision 014). A signal before the save (while generating, sorting or building the graph) stops the work at once and saves nothing new: the previous dataset stays, and the seed makes the same dataset again next time. This keeps `docker stop` inside its 10 s at 20M, where sorting, building and saving take about that long together. A signal during the save does not stop it; a second signal does, and `-save-timeout` still applies. Saves fsync every 32 MB, so unwritten file data never piles up against a container's memory limit. The signal handling of Part 1 is otherwise unchanged.
- **The six other formats left the program.** `-format` is gone and the program writes binary only (`internal/columns`). The comparison above is still reproducible: the seven formats and their benchmarks live in `bench/formats`, which only the benchmarks use. Manifest version 4 has no `format` line (decision 009).
