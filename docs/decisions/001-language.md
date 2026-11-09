# 001 · Programming language

**Status:** Accepted · **Choice:** Go · **Decided:** 6 November 2026, by the project owner

## In short

- The data layout does most of the work: flat arrays, 4-byte IDs and a memory-mapped file. Any language that controls raw bytes can hit the targets.
- Go and Rust were the two strong options. C/C++ and Zig add risk for no gain here. JVM/.NET and Python/Node work against the memory and startup goals.
- **We build it in Go.** Our prior knowledge is in Go, and we must defend every line in review. It builds in seconds, and its standard library covers nearly everything the task needs, including clean shutdown with `signal.NotifyContext`; only Keccak-256, for address checksums, comes from the Go team's `golang.org/x/crypto` (decision 007).

## Context

The task allows any language. The core data is four flat arrays: 20-byte addresses, 16-byte balances, and a neighbour list stored as offsets plus 4-byte neighbour IDs. The language has to hold those without hidden overhead and read them straight from a memory-mapped file.

## Criteria

| Criterion | Why it matters | Weight |
|---|---|---|
| Memory layout control | Flat arrays of raw bytes and fixed-width integers, no per-element objects or pointers | High |
| Memory mapping, zero-copy | Load instantly by mapping the file and viewing its bytes as arrays | High |
| Process startup | Each CLI command may start a new process; 100 ms startup would hide a 1 µs lookup | High |
| Fluency and defendability | Every line must be explained in review | High |
| Baseline runtime memory | Counts toward the footprint we report | Medium |
| Garbage-collector impact | A collector scanning hundreds of MB adds memory and pauses | Medium |
| Testing, benchmarks, profiling | Two bonus items and the README numbers depend on them | Medium |
| Build time and Docker image | Fast iteration; a static binary gives a tiny image | Low |

## Options compared

Startup and baseline memory are typical figures for a small static CLI, not measurements from this project.

| Language | Layout control | Zero-copy mmap | Typical startup | Baseline memory | Tooling | Fluency |
|---|---|---|---|---|---|---|
| **Go** | Strong | Yes (`unsafe` for the cast) | ~1 ms | a few MB | Built in | High |
| Rust | Full | Yes (one `unsafe` call) | < 1 ms | ~1 MB | Excellent | Lower |
| C/C++ | Full | Native | < 1 ms | < 1 MB | Fragmented | Lower |
| Zig | Full | Native | < 1 ms | < 1 MB | Young (pre-1.0) | Low |
| JVM/.NET | Partial | Yes | 50–300 ms | tens of MB | Mature | Varies |
| Python/Node | Weak | Partial | 20–60 ms | 10–50 MB | Mature | Varies |

## Why Go

1. **Fluency.** Our prior knowledge is in Go, so we can explain and defend every line under questioning.
2. **Flat, pointer-free data the collector ignores.** Slices like `[]byte`, `[]uint32` and `[]uint64` contain no pointers, so the garbage collector never scans them, however large they are.
3. **Zero-copy loading.** `syscall.Mmap` maps the data file, and `unsafe.Slice` views its bytes as typed arrays without copying or parsing.
4. **Fast startup.** A static Go binary starts in about a millisecond, so a CLI call costs about the same as the lookup.
5. **Minimal build time.** The compiler is built for speed: a project this size builds in a second or two from clean, and rebuilds faster thanks to the build cache. No build system to configure, just `go build`.
6. **One static binary.** `CGO_ENABLED=0 go build` produces a single file with no runtime to install. It drops into a `scratch` or distroless Docker image of a few MB, and `GOOS`/`GOARCH` cross-compile it for any platform.
7. **Clean shutdown built in.** `signal.NotifyContext` (in `os/signal`, since Go 1.16) turns Ctrl+C or a `docker stop` (SIGINT, SIGTERM) into a cancelled `context.Context`. Long jobs, such as generating the dataset, check that context and stop cleanly; a save, which is how the data is preserved, finishes unless a second signal comes (decision [002](002-shutdown-and-binary-storage.md)). A cancelled write removes its half-written temporary file instead of leaving a corrupt data folder, which is exactly what the "Saving safely" decision needs:

   ```go
   ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
   defer stop()

   if err := generate(ctx, cfg); err != nil { // generate checks ctx.Err() between batches
       os.Remove(tmpPath)                     // never leave a half-written file behind
       log.Fatal(err)
   }
   ```

8. **The standard library covers the whole task.** `flag` for the CLI, `encoding/binary` for the file format, `math/big` for printing 128-bit balances, `testing` with `testing.B` for tests and benchmarks (`go test -bench . -benchmem`), `pprof` for profiling, `runtime.MemStats` for memory numbers, and the race detector (`go test -race`). The one dependency is `golang.org/x/crypto`, for Keccak-256 (decision 007).
9. **Memory can be capped.** `GOMEMLIMIT` (since Go 1.19) gives the runtime a soft memory limit, useful when we report and defend memory numbers.
10. **Readable for reviewers.** `gofmt` and `go vet` keep the code in the one standard style every Go reader knows.

**The trade-off, stated openly:** Rust would save roughly 1–3 MB of baseline memory and make tight loops perhaps 10–30% faster. Both answer a lookup in microseconds, far below the roughly 1 ms process startup, so a CLI user would not notice. The README will say this.

## Consequences

- All graph data lives in pointer-free slices (`[]byte`, `[]uint32`, `[]uint64`), invisible to the garbage collector.
- `unsafe` is confined to one small package that turns mapped bytes into typed slices, with tests around it.
- The file format fixes byte order (little-endian) and aligns every section, so the zero-copy views are valid.
- `main` (`runRun` in `cmd/evmgraph/run.go`) creates the context with `signal.NotifyContext` and passes it to the long-running work: generating and building stop on it, and each save finishes unless a second signal comes. Writes go to a temporary file that is renamed into place only when complete.
- Go has no built-in 128-bit integer, so a balance is stored as two `uint64` halves (16 bytes) and formatted with `math/big` only when printed (see [006](006-balance-type.md)).
- Benchmarks use `testing.B` and report allocations (`-benchmem`). End to end (`bench/e2e`), memory is reported as the maximum RSS from the operating system and, on macOS, the peak memory footprint from `/usr/bin/time -l`.
- The `Dockerfile` builds the binary with `CGO_ENABLED=0` and runs it on Alpine (about 8 MB), chosen over `scratch` or distroless so `docker exec` has a shell; the binary runs on either.

## What would change this

- Measurements show the Go runtime's baseline exceeds about 5% of total memory at the reported dataset size.
- The reviewers are known to weight raw performance above everything else.

## Decision

**Accepted: Go.** Rust was the alternative. Chosen by the project owner on 6 November 2026 for fluency, fast builds, a single static binary, and a standard library that covers the task, including clean shutdown with `signal.NotifyContext`.
