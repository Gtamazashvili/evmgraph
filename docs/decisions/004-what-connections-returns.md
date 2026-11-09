# 004 · What `connections` returns

**Status:** Accepted · **Choice:** BFS to depth 2, each address once at its shortest distance, labelled by level · **Decided:** 8 November 2026, by the project owner

## In short

The brief asks for "direct connections plus their connections, 2 levels deep". We propose a **breadth-first search stopped at depth 2**:

- each address is reported **once**, at its **shortest** distance (level 1 or 2);
- the queried address itself is left out;
- results are ordered by level, then by address.

## Context

"Their connections" is ambiguous. Should a direct neighbour that is also two hops away appear twice? Should the start address appear, since it is its own neighbours' neighbour? Should we show which level-1 address led to each level-2 one? Each answer changes the output size and the work per query.

## Options

| Option | Output | Size for 10 neighbours of degree 10 |
|---|---|---|
| **A. Set by shortest distance** | each address once, tagged L1 or L2 | ≈ 100 lines |
| B. Only level 2 | the "friends of friends", excluding direct ones | ≈ 90 lines |
| C. Tree with paths | every L1 address with its own L2 list, repeats allowed | ≈ 100 lines, but repeats grow fast around hubs |
| D. Raw two-hop walk | every path of length ≤ 2, duplicates included | ≈ 110 lines, unbounded repeats |

## Comparison

- **A** matches "direct connections plus their connections" literally, with no duplicates. BFS reads each list once: the cost is about 1 + d lists for degree d.
- **B** drops the direct connections, which the brief explicitly includes.
- **C** shows how each address was reached, but the same address can appear under many parents, and that grows quadratically near hubs.
- **D** is the cheapest to code but not a meaningful answer.

## Recommendation

**A.** Run BFS from the queried address and mark seen addresses in a bitset (1 bit per address, 125 KB per million; see [005](005-handling-hub-addresses.md)). Print level 1, then level 2, each sorted by address. Because address IDs are already sorted by address, sorting the IDs gives that order for free.

## Consequences

- The bitset is reused between queries (one `graph.Search` in tests, benchmarks and the `search` prompt). As built there is no queue: level 2 is read back from the bitset (see Decision).
- The output always includes a level tag and a total count per level.
- Tests cover a triangle (a direct neighbour also reachable in 2 hops appears once, as L1), a self-cycle back to the start (excluded), and isolated addresses (empty result, not an error).

## What would change this

- The reviewer wants to see how each address was reached: add an optional `--paths` mode (option C), off by default.

## Decision

**Accepted** on 8 November 2026, as recommended (option A). Built as `evmgraph connections [-data folder] [-limit N | -all] <address>` (`-timeout D` was added on 9 November 2026, decision [005](005-handling-hub-addresses.md)):

- stdout holds one line per address, `1 0x…` or `2 0x…`, ordered by level, then by address. The lists are stored in row order, which is address order (decision [010](010-address-order-and-lookup.md)), so level 1 needs no sorting. Level 2 is read back in row order from the seen-bitset, so it needs none either.
- stderr carries the exact total of each level.
- Exit codes: 0 when the address is in the dataset, even with no connections; 1 when it is not, when there is no dataset, or when the dataset predates connections; 2 for a malformed address or usage; 3 when `-timeout` passes (since 9 November 2026).
- `graph.Search` is checked against a plain BFS on random graphs of every skew (`TestSearchMatchesNaiveBFS`), and on hand-made cases: a triangle (reported once, at level 1), a star from a leaf and from the hub, an isolated address, a path, and the queried address excluded (`TestSearchHandMadeCases`). `TestConnectionsCommand` runs the command as a real process against the in-process answer.
