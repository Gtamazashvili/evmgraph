# 003 · Connection meaning

**Status:** Accepted · **Choice:** undirected, unweighted, stored in both lists · **Decided:** 8 November 2026, by the project owner

## In short

The brief says addresses have "connections" but never defines one. We propose: **two addresses are connected if at least one transfer went between them, in either direction.** The link has no direction, amount or time, and is stored once in each address's neighbour list. This is the simplest meaning that answers the `connections` command, and it costs 4 bytes per list entry.

## Context

On a real chain, A sending to B is a directed event with an amount and a time. The brief only asks for an address's connections and their connections, two levels deep. Whatever we choose here fixes the size of the neighbour arrays and what `connections` means to the user.

## Options

| Option | What a connection is | Cost per edge | `connections B` after A → B |
|---|---|---|---|
| **A. Undirected, unweighted** | "These two addresses transacted" | 8 B (4 B in each list) | shows A |
| B. Directed, outgoing only | "A sent to B" | 4 B | does **not** show A |
| C. Directed, both lists kept | Separate in- and out-lists | 8 B + a second offsets array | shows A, labelled "in" |
| D. Weighted | Plus an amount, count or time per link | 8 B + 16–32 B | shows A with details |

## Comparison

- **A** answers the natural question ("who has this address dealt with?") from either side, at the lowest cost that still does so.
- **B** is cheapest, but `connections` on a receiver misses everyone who paid it. That surprises the user.
- **C** keeps direction, but doubles the offsets and makes every query read two lists. Nothing in the brief asks for direction.
- **D** multiplies the per-entry cost by 3–9×. The brief never asks for amounts or times.

## Recommendation

**A. Undirected and unweighted, stored in both endpoints' lists.** Each list is sorted by neighbour ID and has no duplicates, and an address is never its own neighbour.

## Consequences

- Neighbour entries = 2 × edges. At 1 million addresses with an average degree of 10, that is 10 million entries × 4 B = 40 MB.
- The generator must write every edge into both lists, and the tests check this symmetry.
- Updates must add an edge to both lists.
- The README states the definition clearly, because it is our interpretation.

## What would change this

- The reviewer asks for "who did A send to" (direction matters): choose C.
- Amounts or transaction counts become part of the output: choose D, stored in a separate array so plain lookups stay small.

## Decision

**Accepted** on 8 November 2026, as recommended (option A). Built in `internal/graph` (decision [012](012-connection-storage.md)): every link the generator draws (decision [011](011-graph-model.md)) is written into both endpoints' lists, each list sorted with no duplicates and no self-links. `TestBuildMatchesNaiveAdjacency` checks symmetry, order, and the absence of duplicates and self-links against a plain adjacency map.
