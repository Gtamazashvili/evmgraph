# 005 · Handling hub addresses

**Status:** Accepted · **Choice:** a default limit of 100 with the exact total count, `-limit` / `-all` flags, and a bitset to skip duplicates · **Decided:** 8 November 2026, by the project owner

## In short

Real transaction graphs have **hubs**: exchanges, contracts and bridges with up to millions of connections. Because of the friendship paradox, an ordinary address very likely has a hub one step away. Then level 2 jumps from about 90 addresses to about a million: roughly 43 MB of text from one command.

We propose to **print at most 100 results by default, always followed by the exact total**, with `--limit N` and `--all` to change that. Duplicates are skipped with a bitset.

## Context

The brief asks for the fastest possible lookups but says nothing about result size. Our generated data may or may not contain hubs (see [011](011-graph-model.md), the graph model), but a defensible CLI must not fall over when one appears.

## Options

| Option | Behaviour | Problem |
|---|---|---|
| **A. Limit + exact total** | print the first N, then "showing N of T" | none worth noting; the full count still costs one BFS |
| B. No limit | print everything | one hub produces ~1 M lines, ~43 MB |
| C. Do not expand hubs | stop at any address above a degree threshold | silently wrong answers; an arbitrary threshold |
| D. Cap degrees in the data | the generator never makes hubs | hides the problem instead of handling it; unrealistic |

## Duplicate removal

| Structure | Memory for 1 M addresses | Check cost |
|---|---|---|
| **Bitset** (1 bit per address ID) | 125 KB, fixed | one bit read |
| Hash set of IDs | typically tens of MB when full | hashing plus a probe |
| Sorting the results | the result size × 4 B | O(r log r) |

## Recommendation

**A, with a bitset.**

- Default limit 100.
- `--limit N` changes the limit, and `--all` streams everything.
- The total is always exact: the BFS still visits every level-2 address, but it only counts the ones beyond the limit instead of printing them.
- The bitset is cleared after each query by resetting only the bits that were set, so a small query never pays for clearing all 125 KB.

## Consequences

- Worst-case query time is bounded by the edges near the start (a hub of a million costs about a million list reads, roughly milliseconds, as estimated here; measured, the biggest hub's own query takes 0.14 s warm, because its lists hold nearly the whole graph, see Decision), and output size is bounded by the limit.
- The benchmark reports two cases: a typical address, and an address next to the largest hub.
- `--all` writes through a buffered writer, so a million lines stream instead of building up in memory.

## What would change this

- The reviewer insists on complete output by default: make `--all` the default and keep `--limit` as an option.

## Decision

**Accepted** on 8 November 2026, as recommended. Built with Go's single-dash flags: `-limit N` (default 100) and `-all`.

- If level 1 alone exceeds the limit, the first `limit` addresses of level 1 are printed.
- stderr always says `level 1: X addresses, level 2: Y addresses`, plus `showing N of T; -all for everything` when lines were left out (as plain log lines; when stderr is a terminal, the dashboard shows the same totals in its own layout).
- `-all` streams through a buffered writer. When the reader goes away (`| head`), writing fails and the program stops instead of hanging (`TestConnectionsStopsWhenOutputCloses`).
- Memory: one bitset of n/8 bytes (2.5 MB at 20M) and nothing proportional to the result, so even a hub-adjacent query with half a million results costs no more. Instead of resetting only the bits that were set, each query clears the whole bitset; that costs well under a millisecond at 20M and keeps no list of touched rows, which is what would grow with the result.

Measured on the default dataset (20M addresses, seed 100,000, skew 3; 9 November 2026, single runs, warm unless stated):

| Query | Results | Time | Peak memory (RSS) |
|---|---|---|---|
| default limit, an address next to the biggest hub | 6 + 544,211 (100 printed) | under 10 ms | 10 MB |
| `-all`, the same address | 6 + 544,211 printed | 0.15 s (0.79 s on the first run) | 417 MB, nearly all clean pages of `addresses.bin` touched to print them, which the OS can drop; 12 MB held by the process itself |
| default limit, the biggest hub itself, the worst query | 543,942 + 8,831,042 (100 printed) | 0.14 s (1.64 s on the first run) | 887 MB of clean graph pages; 6 MB held by the process itself |

**Deadlines (9 November 2026):** `-timeout D` stops a query that takes longer than D, with exit code 3, covering the search and the printing. The worst query on the default dataset, the biggest hub's own (543,942 + 8,831,042 results), takes 0.14–1.41 s, so a deadline matters mostly for larger datasets and for programs that query often. Details and measurements of heavy and simultaneous queries are in [012](012-connection-storage.md#heavy-queries-and-many-queries).

The friendship paradox from the context above is visible here: a random address, row 12,345,678, had the biggest hub among its 6 direct connections.
