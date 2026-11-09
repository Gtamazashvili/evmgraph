# Decisions

One record per design choice, numbered in the order they were written. Each record follows the same shape: In short, Context, Options, Comparison, Recommendation, Consequences, What would change this, Decision.

**Status:** **Accepted** means the project owner chose it. **Proposed** means the options are written and a recommendation is made, waiting for a choice. All 14 are Accepted.

| # | Decision | Status | Choice / recommendation |
|---|---|---|---|
| [001](001-language.md) | Programming language | **Accepted** | **Go**: fluency, fast builds, one static binary, `signal.NotifyContext` for clean shutdown |
| [002](002-shutdown-and-binary-storage.md) | Graceful shutdown, binary storage and mmap loading | **Accepted** | `signal.NotifyContext` (SIGINT, SIGTERM, SIGHUP unless ignored); saves right after generating, folds balance updates in at shutdown; plain binary files in `cache/` (the other six formats compared live in `bench/formats`), written to a new, exclusively created temp file then renamed; anything in its way is moved aside, never deleted (only dataset folders holding nothing but its own files are removed); 20 MB per million addresses, loads 7× faster than text; loaded with `mmap`: opens in 26 µs instead of 7.9 ms, 4.5 MB of memory at 1M or 50M addresses |
| [003](003-connection-meaning.md) | Connection meaning | **Accepted** | undirected, unweighted, stored in both lists |
| [004](004-what-connections-returns.md) | What `connections` returns | **Accepted** | BFS to depth 2, each address once at its shortest level, `1 0x…` / `2 0x…` lines, level then address order |
| [005](005-handling-hub-addresses.md) | Handling hub addresses | **Accepted** | limit 100 + exact totals on stderr, `-limit` / `-all`, one n/8-byte bitset, memory bounded even for the biggest hub's 9.4M-result query |
| [006](006-balance-type.md) | Balance type | **Accepted** | **`u128` wei**, 16 bytes, little-endian (`evm.Wei`); simulated balances from a realistic model (40% empty, dust, a thin rich tail; integer-only); `u256` judged overkill for ETH |
| [007](007-address-input-and-output.md) | Address input and output | **Accepted** | any case in, `0x` optional, EIP-55 checked when mixed, EIP-55 out; Keccak-256 from `golang.org/x/crypto/sha3`, the one dependency |
| [008](008-parallel-generation.md) | Parallel address generation | **Accepted** | blocks of 2²⁰ addresses, one ChaCha8 stream per block (seed + block number), on all cores: 50M in 146 ms instead of 774 ms |
| [009](009-dataset-layout.md) | Dataset layout: one file per column, committed by a manifest | **Accepted** | `cache/dataset-NNNNNN/` holds one file per column, row *i* = the same address; `cache/manifest` (version 4) is replaced last as the commit point; a `lock` file serialises writers |
| [010](010-address-order-and-lookup.md) | Order of address IDs and finding an address | **Accepted** | rows **sorted by address**, so an ID is the address's rank; **interpolation search** on `addresses.bin` itself, no index: about 5 reads per lookup, 133 ns warm at 20M against 439 ns for binary search |
| [011](011-graph-model.md) | Graph model | **Accepted** | each position links to degree/2 earlier ones, t = floor(p · u^skew); skew 3, degree 10 by default; integer arithmetic only, so Mac and amd64 Docker draw the same graph (golden hash on both) |
| [012](012-connection-storage.md) | How connections are stored | **Accepted** | plain CSR by row, kept after measuring 8 alternatives (compressed forms 13–36% smaller but 1.5–2.3× slower on heavy queries): `offsets.bin` + `neighbors.bin`, 4-byte entries, sorted lists, 44 B per address; top-up regenerates the graph and keeps saved balances |
| [013](013-default-size.md) | Default dataset size | **Accepted** | `-n` 20,000,000: 1.6 GB on disk, 1.85 GB peak memory, about 7–8 s to build, about 3 s to save |
| [014](014-balance-updates.md) | Updates without a full rebuild | **Accepted** | balance changes and new links: `evmgraph update balance` / `update link`, append-only logs per folder read by every lookup (links merged into the search), folded into the column files by `run` at start and stop; not new addresses |

## Not decided yet

- New addresses without a rebuild (decision 014 covers balance changes and new links)
