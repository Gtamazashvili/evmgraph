Measured with `go run ./bench/e2e -n 20000000 -runs 5` on darwin/arm64, 8 cores, 9 November 2026; load averages: 5.27 5.24 5.24.

| | Time | Max RSS | Held by the process |
|---|---|---|---|
| First start: generate, sort, build the graph | 214 ms + 853 ms + 4.6 s | 1.85 GB | 1.85 GB |
| Save (all four files), right after generating | 2.8 s | — | — |
| Restart: open the saved dataset | 1.8 s | 1.29 GB | 4 MB |
| `balance` | 7.0 ms warm, 8.1 ms first run | 5 MB | 3 MB |
| `connections`, a typical address, default limit (289 results) | 8.1 ms warm, 8.0 ms first run | 10 MB | 6 MB |
| `connections`, next to the biggest hub, default limit (544,040 results) | 8.7 ms warm, 11 ms first run | 10 MB | 6 MB |
| `connections -all`, next to the biggest hub (544,040 results) | 379 ms warm, 381 ms first run | 410 MB | 6 MB |
| `connections`, the biggest hub itself (543,942 links) (9,374,984 results) | 144 ms warm, 146 ms first run | 886 MB | 6 MB |
| `connections -all`, the biggest hub itself (9,374,984 results) | 5.80 s warm, 5.83 s first run | 1.29 GB | 6 MB |
| `update balance` (append to the log, fsync) | 13 ms | 5 MB | 3 MB |
| `update link` (check, append to the log, fsync) | 13 ms | 5 MB | 3 MB |
