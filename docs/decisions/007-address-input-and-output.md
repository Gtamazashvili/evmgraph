# 007 · Address input and output

**Status:** Accepted · **Choice:** accept 40 hex digits with or without `0x` in any case, check EIP-55 when the case is mixed, print EIP-55 checksummed; Keccak-256 from `golang.org/x/crypto/sha3` · **Decided:** 9 November 2026, by the project owner

## In short

- **Input.** An address is 40 hex digits, with or without `0x` (or `0X`). All-lowercase and all-uppercase are accepted as they are. Mixed case must match the **EIP-55 checksum**, which catches typos.
- **Storage and lookup.** The address is parsed to its 20 raw bytes and found by interpolation search in the sorted address file (decision 010). The files store raw bytes; the text storage formats, which since 9 November 2026 exist only in the comparison in `bench/formats`, keep lowercase hex.
- **Output.** Addresses are printed in EIP-55 form, as wallets and block explorers show them: `0x2aA22D2D411f98B31014ae167a95999df17eAcDA`.

## Context

An address is 20 bytes. As text it is 42 characters: `0x` plus 40 hex digits, where the letter case can carry a checksum (EIP-55, built on Keccak-256). Users paste addresses from wallets and explorers in all three styles. Wrong input must be rejected clearly, and a valid address that is not in our dataset must be reported differently from bad input.

The generated address bytes need no change to look real: a real address is the last 20 bytes of a Keccak-256 hash, which is indistinguishable from the random bytes the generator draws (decision 008). What made the output look unlike the real world was the all-lowercase text.

## Options

| Option | Input rule | Output | Typo protection |
|---|---|---|---|
| **A. Lenient, checked** | any case; mixed case must pass EIP-55 | EIP-55 | yes, for mixed case |
| B. Lowercase only | reject any uppercase | lowercase | none |
| C. Anything goes | ignore case, never check | lowercase | none |
| D. Strict EIP-55 only | reject all-lowercase too | EIP-55 | always, but rejects common valid input |

For Keccak-256 itself: write it ourselves (about 150 lines, no dependency) or use `golang.org/x/crypto/sha3`, the Go team's own module.

## Recommendation

**A.** It matches what wallets do. `0x` is optional on input, for convenience, but always printed.

| Situation | Message | Exit code |
|---|---|---|
| Found | the result | 0 |
| Valid address, not in the dataset | `… is not among the N addresses` (the dataset is simulated, so we do not invent a zero balance) | 1 |
| Malformed input | `… is not an address: expected 40 hex digits, with or without 0x` | 2 |
| Mixed case with a wrong checksum | `… does not match its EIP-55 checksum (the letter case): a character is probably mistyped` | 2 |

## Consequences

- `evm.Parse` reads what a person types; `evm.ParseHex` and `Address.Hex` stay lowercase for the text storage formats (now used only by `bench/formats`). `Address.Checksum` and `evm.Checksummer` print EIP-55; a `Checksummer` reuses one Keccak state, so printing allocates nothing per address.
- Tests: the eight official EIP-55 test vectors, every accepted input form, and malformed and mistyped input (`internal/evm/address_test.go`); end to end, `balance` accepts the five input forms and rejects a flipped letter with exit code 2 (`TestBalanceCommand`).
- **The program now has one dependency,** `golang.org/x/crypto` (with `golang.org/x/sys`). It is pure Go, so the binary still builds with `CGO_ENABLED=0` for any platform.
- **Cost, measured** (Apple M1 Pro, 9 November 2026): one checksum is 0.45 µs (`BenchmarkChecksummer`, 0 allocations). On the 20M dataset:

  | Output | Lowercase | EIP-55 |
  |---|---|---|
  | default limit, 100 lines | under 10 ms | under 10 ms |
  | `-all`, 544,217 lines | 0.14 s | 0.37 s |
  | `-all`, the biggest hub's 9,374,984 lines | 1.86 s | 5.85 s |

  Finding the address and the search are unchanged; only printing long lists costs more.

## What would change this

- **`-all` speed matters:** format the lines on all cores, in order (about 8× less), or add a `-lowercase` flag that skips the checksum.
- **No dependencies at all:** implement Keccak-256 ourselves against the same test vectors.

## Decision

**Accepted: option A, with Keccak-256 from `golang.org/x/crypto/sha3`** (9 November 2026). The project owner chose EIP-55 checksums to make addresses look as they do in the real world, and asked to use an existing package rather than write Keccak-256 ourselves.
