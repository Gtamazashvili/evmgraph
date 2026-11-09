# Benchmark results

Every benchmark ran in one session under the same conditions; 1,000,000 addresses unless stated. Times are the median over all runs; *spread* is (max − min) / median, a quick measure of how steady the runs were.

```
date:      2026-11-06 21:20:09 +04
go:        go version go1.26.3 darwin/arm64
os:        Darwin 25.5.0
macos:     26.5
cpu:       Apple M1 Pro, 6 performance + 2 efficiency cores
memory:    17179869184 bytes
power:     AC Power
load:      23.93 12.61 8.45  (1, 5, 15 min; before the run)
runs:      10 of each benchmark, -p 1
command:   go test ./... -run '^$' -bench . -benchmem -count 10 -p 1
load after: 11.93 14.61 11.15  (1, 5, 15 min; measured by hand right after the run)
note:      Docker Desktop's virtual machine started about 8 minutes into this run and used about 110% CPU, and another project's tests ran at the same time; results marked noisy were disturbed by them
```

## Creating addresses

Generating 1,000,000 addresses from a seed. The same for every format.

| Benchmark | Median | Min | Max | Spread | Memory allocated | Allocations |
|---|---|---|---|---|---|---|
|  | 21.97 ms | 14.78 ms | 40.56 ms | 117% | 21.3 MB | 3 |

10 runs each.

## Saving 1,000,000 addresses

The full save path: temporary file, fsync, rename.

| Benchmark | Median | Min | Max | Spread | Memory allocated | Allocations | vs binary | File |
|---|---|---|---|---|---|---|---|---|
| binary | 21.79 ms | 16.91 ms | 25.56 ms | 40% | 1.0 MB | 14 | 1× | 20.00 MB |
| gzip | 272.29 ms | 270.58 ms | 278.64 ms | 3% | 1.9 MB | 31 | 12× slower | 20.01 MB |
| text | 41.08 ms | 40.64 ms | 41.75 ms | 3% | 1.0 MB | 14 | 1.9× slower | 43.00 MB |
| text-mmap | 53.41 ms | 40.96 ms | 67.14 ms | 49% | 1.0 MB | 14 | 2.5× slower | 43.00 MB |
| csv | 170.43 ms | 166.71 ms | 180.87 ms | 8% | 152.8 MB | 4.0 million | 7.8× slower | 49.89 MB |
| json | 57.35 ms | 49.78 ms | 91.13 ms | 72% | 1.0 MB | 14 | 2.6× slower | 45.00 MB |
| gob | 255.03 ms | 247.38 ms | 266.97 ms | 8% | 18.7 MB | 104 | 12× slower | 31.00 MB |

10 runs each.

## Loading: open

The cost before the first address can be used.

| Benchmark | Median | Min | Max | Spread | Memory allocated | Allocations | vs binary |
|---|---|---|---|---|---|---|---|
| binary | 31.0 µs | 21.8 µs | 51.4 µs | 96% | 472 B | 5 | 1× |
| gzip | 17.61 ms | 16.73 ms | 22.99 ms | 36% | 21.1 MB | 14 | 569× slower |
| text | 35.40 ms | 34.88 ms | 37.07 ms | 6% | 21.1 MB | 9 | 1143× slower |
| text-mmap | 7.82 ms | 7.04 ms | 9.27 ms | 28% | 20.0 MB | 16 | 252× slower |
| csv | 123.19 ms | 117.06 ms | 150.40 ms | 27% | 142.0 MB | 2.0 million | 3977× slower |
| json | 436.62 ms | 417.22 ms | 794.58 ms | 86% | 221.1 MB | 7.0 million | 14096× slower |
| gob | 283.15 ms | 274.83 ms | 293.33 ms | 7% | 161.1 MB | 1.0 million | 9141× slower |

10 runs each.

## Loading: open and read every address

Fair to binary, whose mmap load defers reading until an address is used.

| Benchmark | Median | Min | Max | Spread | Memory allocated | Allocations | vs binary |
|---|---|---|---|---|---|---|---|
| binary | 1.62 ms | 1.59 ms | 1.75 ms | 10% | 472 B | 5 | 1× |
| gzip | 17.49 ms | 17.37 ms | 19.00 ms | 9% | 21.1 MB | 14 | 11× slower |
| text | 38.13 ms | 35.07 ms | 51.20 ms | 42% | 21.1 MB | 9 | 24× slower |
| text-mmap | 10.86 ms | 7.64 ms | 14.07 ms | 59% | 20.0 MB | 16 | 6.7× slower |
| csv | 155.11 ms | 120.95 ms | 170.24 ms | 32% | 142.0 MB | 2.0 million | 96× slower |
| json | 531.19 ms | 493.80 ms | 628.87 ms | 25% | 221.1 MB | 7.0 million | 329× slower |
| gob | 305.54 ms | 293.96 ms | 371.90 ms | 26% | 161.1 MB | 1.0 million | 189× slower |

10 runs each.

## Binary load strategies

The same binary file loaded three ways, for open only, one lookup, and reading everything.

| Benchmark | Median | Min | Max | Spread | Memory allocated | Allocations |
|---|---|---|---|---|---|---|
| only/copy-loop | 9.34 ms | 9.08 ms | 10.24 ms | 12% | 21.1 MB | 7 |
| only/read-once | 3.07 ms | 2.92 ms | 3.38 ms | 15% | 20.0 MB | 5 |
| only/mmap | 23.7 µs | 20.5 µs | 36.4 µs | 67% | 472 B | 6 |
| one-lookup/copy-loop | 9.38 ms | 8.93 ms | 10.83 ms | 20% | 21.1 MB | 7 |
| one-lookup/read-once | 3.06 ms | 2.92 ms | 3.30 ms | 13% | 20.0 MB | 5 |
| one-lookup/mmap | 39.1 µs | 36.3 µs | 46.4 µs | 26% | 472 B | 6 |
| read-all/copy-loop | 9.89 ms | 9.43 ms | 10.85 ms | 14% | 21.1 MB | 7 |
| read-all/read-once | 4.07 ms | 3.95 ms | 4.17 ms | 6% | 20.0 MB | 5 |
| read-all/mmap | 1.84 ms | 1.78 ms | 2.11 ms | 18% | 472 B | 6 |

10 runs each.

## Text decoding, 1 core against all cores

Decoding a mapped text file; the step text-mmap parallelises.

| Benchmark | Median | Min | Max | Spread | Memory allocated | Allocations |
|---|---|---|---|---|---|---|
| 1-core | 23.85 ms | 23.44 ms | 25.00 ms | 7% | 20.0 MB | 4 |
| all-cores | 6.97 ms | 6.03 ms | 8.34 ms | 33% | 20.0 MB | 11 |

10 runs each.

## Lazy text view (experiment)

A mapped text file decoded only where it is read; not used by the program.

| Benchmark | Median | Min | Max | Spread | Memory allocated | Allocations |
|---|---|---|---|---|---|---|
| open | 36.1 µs | 34.0 µs | 37.7 µs | 10% | 432 B | 5 |
| one-lookup | 37.6 µs | 35.6 µs | 40.6 µs | 13% | 432 B | 5 |
| read-all | 28.34 ms | 27.93 ms | 32.13 ms | 15% | 432 B | 5 |

10 runs each.
