# 006 · Balance type

**Status:** Accepted · **Choice:** unsigned 128-bit integer of wei (16 bytes, little-endian); simulated balances follow a realistic model (changed 9 November 2026, see the end) · **Decided:** 8 November 2026, by the project owner

## In short

Balances are whole numbers of **wei** (1 ETH = 10^18 wei). The total ETH supply is about 1.2 × 10^26 wei.

- A 64-bit integer tops out near 1.8 × 10^19 wei, which is only **about 18 ETH**.
- A float64 cannot hold every whole number above about 9 × 10^15 wei (about 0.009 ETH).
- A **128-bit unsigned integer** is exact, takes 16 bytes, and has room for about 10^12 times the total supply.

## Options

| Option | Bytes | Exact? | Max value | Verdict |
|---|---|---|---|---|
| `uint64` wei | 8 | yes | ≈ 18.4 ETH | too small: real balances overflow |
| `float64` ETH | 8 | **no** | huge | loses wei; "is this balance right?" becomes unanswerable |
| **`u128` wei** | 16 | yes | ≈ 3.4 × 10^20 ETH | fits everything with huge headroom |
| `u256` wei (EVM native) | 32 | yes | far beyond any supply | doubles the cost for no benefit |
| Decimal string | ~20–30, variable | yes | any | variable size breaks fixed-width arrays and needs parsing |

## Recommendation

**`u128` wei, 16 bytes, little-endian.** Go has no built-in 128-bit integer, so it is stored as two `uint64` values (low half, then high half) in a flat `[]byte` or `[]uint64`. Arithmetic for updates uses `math/bits.Add64` with carry (as built, an update sets the new balance outright, decision [014](014-balance-updates.md), so no balance arithmetic exists). Printing converts to `math/big.Int` only for the one balance being shown.

## Consequences

- Balances cost 16 B × N: 16 MB per million addresses.
- `balance <address>` prints the exact wei value and an ETH value with all 18 decimals, for example `1.500000000000000000 ETH (1500000000000000000 wei)`.
- Until 9 November 2026, simulated balances were **fully random 128-bit values**, the project owner's choice over a realistic heavy-tailed spread (most empty or small, a few whales) and a uniform 0–100 ETH spread. Every bit is exercised on every run: about half of all balances have the top bit set, so nearly every balance is above 2^64 wei and uses the high half. The cost: the values are not believable as ETH holdings (around 10^20 ETH each, far above the real supply). Tests check the boundary values 0, 2^64 − 1, 2^64 and 2^128 − 1 (`TestWeiBoundaries`).
- In code it is `evm.Wei`, a `[16]byte`, little-endian, low half first (`TestWeiLayout`). A byte array rather than two `uint64` fields, so a mapped file is viewed as `[]Wei` on any CPU, with no alignment or byte-order assumptions. `Lo` and `Hi` give the halves, `String` the exact decimal wei, `ETH` the value with all 18 decimals.
- Balances are generated with the addresses, in the same blocks and with their own streams (tag `balance`, decision [008](008-parallel-generation.md)), and stored in `balances.bin` next to the addresses (decision [009](009-dataset-layout.md)). With fully random balances, 1M addresses with balances generated in about 28 ms on one block, against 16 ms for addresses alone (`BenchmarkGenerate/1M-with-balances`); with the balance model below, about 41 ms (measured again on 9 November 2026, same machine, median of 3).

## What would change this

- The brief is read as "any EVM token", including tokens with absurd supplies above 2^128: move to `u256` (32 bytes).

## Decision

**Accepted** on 8 November 2026 by the project owner: `u128` wei, 16 bytes, little-endian. Asked whether `u256` would be overkill, the owner agreed it is for ETH: it doubles the space (32 bytes, more than the 20-byte address) for headroom no ETH balance can use. Simulated balances are fully random 128-bit values.

## Changed: realistic balances (9 November 2026)

**Accepted** by the project owner after the review: fully random 128-bit balances gave every address about 10^20 ETH, roughly 10^12 times all ETH in existence, which the reviewer flagged as unrealistic. Balances now come from a model (`internal/generate/balance.go`):

| Share of addresses (per billion) | Balance |
|---|---|
| 40% | nothing |
| about 59% | 1 gwei to 1 ETH, most of it dust below 0.001 ETH |
| about 0.67% | 1 ETH or more |
| about 1 in 5 million (210 per billion) | 100,000 ETH or more, up to just under 10 million ETH |

- The shares follow the shape of public rich lists and are **illustrative, not measured**. Inside a decade of ETH the amount is uniform, in whole wei.
- The expected mean is about 0.54 ETH per address, so a dataset the size of mainnet (a few hundred million addresses, approximate) would hold roughly the real supply (about 120 million ETH, approximate). A 2M sample shows less (0.30 ETH), because the rare whale decades carry most of the expected total.
- Balances still need `u128`: about 0.1% exceed 18.4 ETH, the most 64 bits of wei can hold.
- Each balance takes exactly 16 bytes of its stream, as before, so a range can start inside a block. Only integer arithmetic is used, so a Mac and amd64 Linux draw the same balances (`TestBalancesGolden`, `TestBalancesGoldenOnAMD64`). `TestBalanceModelShape` checks the shares.
- Datasets saved before this have manifest version 3 or lower and are generated again on the next `evmgraph run` (decision 009).
