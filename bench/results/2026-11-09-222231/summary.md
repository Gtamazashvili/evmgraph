# Benchmark results

Every benchmark ran in one session under the same conditions; 1,000,000 addresses unless stated. Times are the median over all runs; *spread* is (max − min) / median, a quick measure of how steady the runs were.

```
date:      2026-11-09 22:22:31 +04
go:        go version go1.26.3 darwin/arm64
os:        Darwin 25.5.0
macos:     26.5
cpu:       Apple M1 Pro, 6 performance + 2 efficiency cores
memory:    17179869184 bytes
power:     AC Power
load:      4.58 5.20 5.39  (1, 5, 15 min; before the run)
runs:      10 of each benchmark, -p 1
command:   go test ./... -run '^$' -bench . -benchmem -count 10 -p 1
load after: 4.42 4.96 5.15  (1, 5, 15 min; right after the run)
```

## Creating addresses

Generating addresses from a seed: 1,000,000 (one block, one core), 50,000,000 (all cores), positions 50,000,000 to 51,000,000 alone, and 1,000,000 with their balances from the balance model (decisions 006, 008).

| Benchmark | Median | Min | Max | Spread | Memory allocated | Allocations |
|---|---|---|---|---|---|---|
| /1M | 15.64 ms | 15.49 ms | 16.06 ms | 4% | 20.0 MB | 10 |
| /50M | 160.60 ms | 149.31 ms | 473.21 ms | 202% | 1000.0 MB | 78 |
| /top-up-50M-to-51M | 14.37 ms | 14.05 ms | 15.32 ms | 9% | 20.0 MB | 13 |
| /1M-with-balances | 41.31 ms | 40.94 ms | 42.25 ms | 3% | 36.0 MB | 13 |

10 runs each.

## Saving 1,000,000 addresses

The full save path: temporary file, fsync, rename.

| Benchmark | Median | Min | Max | Spread | Memory allocated | Allocations | vs binary | File |
|---|---|---|---|---|---|---|---|---|
| binary | 22.06 ms | 17.61 ms | 27.29 ms | 44% | 1.1 MB | 22 | 1× | 20.00 MB |
| gzip | 270.62 ms | 264.15 ms | 275.94 ms | 4% | 1.9 MB | 39 | 12× slower | 20.01 MB |
| text | 48.30 ms | 41.14 ms | 55.59 ms | 30% | 1.1 MB | 22 | 2.2× slower | 43.00 MB |
| text-mmap | 54.12 ms | 53.01 ms | 55.24 ms | 4% | 1.1 MB | 22 | 2.5× slower | 43.00 MB |
| csv | 172.60 ms | 170.06 ms | 193.05 ms | 13% | 152.8 MB | 4.0 million | 7.8× slower | 49.89 MB |
| json | 56.75 ms | 55.73 ms | 70.74 ms | 26% | 1.1 MB | 22 | 2.6× slower | 45.00 MB |
| gob | 246.35 ms | 241.44 ms | 261.02 ms | 8% | 18.7 MB | 112 | 11× slower | 31.00 MB |

10 runs each.

## Loading: open

The cost before the first address can be used.

| Benchmark | Median | Min | Max | Spread | Memory allocated | Allocations | vs binary |
|---|---|---|---|---|---|---|---|
| binary | 22.0 µs | 21.6 µs | 22.6 µs | 5% | 472 B | 5 | 1× |
| gzip | 17.23 ms | 16.90 ms | 18.00 ms | 6% | 21.1 MB | 14 | 783× slower |
| text | 34.06 ms | 33.48 ms | 36.98 ms | 10% | 21.1 MB | 9 | 1548× slower |
| text-mmap | 6.88 ms | 6.79 ms | 7.11 ms | 5% | 20.0 MB | 16 | 313× slower |
| csv | 106.61 ms | 103.96 ms | 109.74 ms | 5% | 142.0 MB | 2.0 million | 4844× slower |
| json | 409.47 ms | 400.62 ms | 416.20 ms | 4% | 221.1 MB | 7.0 million | 18607× slower |
| gob | 275.85 ms | 272.66 ms | 283.17 ms | 4% | 161.1 MB | 1.0 million | 12535× slower |

10 runs each.

## Loading: open and read every address

Fair to binary, whose mmap load defers reading until an address is used.

| Benchmark | Median | Min | Max | Spread | Memory allocated | Allocations | vs binary |
|---|---|---|---|---|---|---|---|
| binary | 1.70 ms | 1.62 ms | 1.76 ms | 8% | 472 B | 5 | 1× |
| gzip | 17.85 ms | 17.33 ms | 18.05 ms | 4% | 21.1 MB | 14 | 10× slower |
| text | 35.13 ms | 34.90 ms | 35.95 ms | 3% | 21.1 MB | 9 | 21× slower |
| text-mmap | 7.72 ms | 7.58 ms | 8.02 ms | 6% | 20.0 MB | 16 | 4.5× slower |
| csv | 105.75 ms | 103.96 ms | 108.07 ms | 4% | 142.0 MB | 2.0 million | 62× slower |
| json | 411.35 ms | 401.66 ms | 416.54 ms | 4% | 221.1 MB | 7.0 million | 241× slower |
| gob | 276.50 ms | 272.24 ms | 278.43 ms | 2% | 161.1 MB | 1.0 million | 162× slower |

10 runs each.

## Binary load strategies

The same binary file loaded three ways, for open only, one lookup, and reading everything.

| Benchmark | Median | Min | Max | Spread | Memory allocated | Allocations |
|---|---|---|---|---|---|---|
| only/copy-loop | 7.94 ms | 7.83 ms | 8.16 ms | 4% | 21.1 MB | 7 |
| only/read-once | 3.29 ms | 3.18 ms | 3.34 ms | 5% | 20.0 MB | 5 |
| only/mmap | 27.1 µs | 26.6 µs | 28.2 µs | 6% | 472 B | 6 |
| one-lookup/copy-loop | 8.06 ms | 7.91 ms | 9.59 ms | 21% | 21.1 MB | 7 |
| one-lookup/read-once | 3.34 ms | 3.20 ms | 3.60 ms | 12% | 20.0 MB | 5 |
| one-lookup/mmap | 28.7 µs | 27.8 µs | 29.9 µs | 7% | 472 B | 6 |
| read-all/copy-loop | 8.83 ms | 8.75 ms | 9.01 ms | 3% | 21.1 MB | 7 |
| read-all/read-once | 4.13 ms | 3.80 ms | 4.16 ms | 9% | 20.0 MB | 5 |
| read-all/mmap | 1.70 ms | 1.59 ms | 1.78 ms | 12% | 472 B | 6 |

10 runs each.

## Text decoding, 1 core against all cores

Decoding a mapped text file; the step text-mmap parallelises.

| Benchmark | Median | Min | Max | Spread | Memory allocated | Allocations |
|---|---|---|---|---|---|---|
| 1-core | 22.43 ms | 22.31 ms | 22.69 ms | 2% | 20.0 MB | 4 |
| all-cores | 5.64 ms | 5.61 ms | 5.83 ms | 4% | 20.0 MB | 11 |

10 runs each.

## Lazy text view (experiment)

A mapped text file decoded only where it is read; not used by the program.

| Benchmark | Median | Min | Max | Spread | Memory allocated | Allocations |
|---|---|---|---|---|---|---|
| open | 16.8 µs | 16.6 µs | 17.0 µs | 2% | 432 B | 5 |
| one-lookup | 41.3 µs | 39.6 µs | 42.9 µs | 8% | 432 B | 5 |
| read-all | 26.47 ms | 25.56 ms | 28.37 ms | 11% | 432 B | 5 |

10 runs each.

## Sorting rows by address

1,000,000 generated rows sorted in place on all cores (decision 010).

| Benchmark | Median | Min | Max | Spread | Memory allocated | Allocations |
|---|---|---|---|---|---|---|
|  | 34.86 ms | 33.71 ms | 37.09 ms | 10% | 5400 B | 21 |

10 runs each.

## Finding an address

Interpolation search on sorted addresses against binary search, 1M and 20M, warm: 4,096 repeated keys stay in the CPU cache (decision 010).

| Benchmark | Median | Min | Max | Spread | Memory allocated | Allocations |
|---|---|---|---|---|---|---|
| 1M | 107 ns | 103 ns | 111 ns | 7% | 0 B | 0 |
| 1M-binary | 176 ns | 168 ns | 189 ns | 12% | 0 B | 0 |
| 20M | 126 ns | 120 ns | 133 ns | 11% | 0 B | 0 |
| 20M-binary | 404 ns | 352 ns | 439 ns | 22% | 0 B | 0 |

10 runs each.

## Building the graph

The CSR graph of 1,000,000 addresses at degree 10 and skew 3, two passes on all cores (decision 012).

| Benchmark | Median | Min | Max | Spread | Memory allocated | Allocations |
|---|---|---|---|---|---|---|
|  | 503.10 ms | 461.10 ms | 564.56 ms | 21% | 48.0 MB | 72 |

10 runs each.

## Searching connections

The depth-2 search on 1M addresses: a typical address, and one next to the biggest hub (decisions 004, 005).

| Benchmark | Median | Min | Max | Spread | Memory allocated | Allocations |
|---|---|---|---|---|---|---|
| typical | 86.9 µs | 85.2 µs | 89.4 µs | 5% | 0 B | 0 |
| hub-adjacent | 1.04 ms | 1.02 ms | 1.06 ms | 3% | 0 B | 0 |

10 runs each.

## Printing an address

One EIP-55 checksum (Keccak-256), the cost per printed address (decision 007).

| Benchmark | Median | Min | Max | Spread | Memory allocated | Allocations |
|---|---|---|---|---|---|---|
|  | 449 ns | 440 ns | 530 ns | 20% | 0 B | 0 |

10 runs each.
