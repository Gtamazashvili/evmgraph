#!/bin/sh
# Runs every benchmark in the project in one session, under the same
# conditions, and writes the results to bench/results/<date-time>/:
#
#   machine.txt     the machine, OS, Go version, power source and load
#   raw.txt         the raw `go test` output
#   summary.md      median, min, max and spread of every benchmark
#   e2e.md          the README's "Measured" table: the program end to end, as
#                   separate processes, on a dataset of N addresses (bench/e2e)
#
# Usage: bench/run.sh                     (10 runs of each benchmark, N = 20M)
#        COUNT=5 N=1000000 bench/run.sh    (fewer runs, a smaller dataset, faster)
#
# Same conditions means: one build, one session, the same 1,000,000-address
# input (unless a benchmark's name gives another size, such as Find/20M),
# warm OS file cache, and -p 1 so packages run one after another
# instead of competing for the CPU. Close other heavy programs first and keep
# the laptop on mains power; machine.txt records both. Only one run can
# happen at a time: two runs at once compete for the CPU and spoil both.
set -eu
cd "$(dirname "$0")/.."

count=${COUNT:-10}
mkdir -p bench/results
lock=bench/results/.running
if ! mkdir "$lock" 2>/dev/null; then
	echo "a benchmark run is already in progress (started $(cat "$lock/started" 2>/dev/null || echo 'at an unknown time'))." >&2
	echo "wait for it to finish; if no run is active, remove $lock and try again." >&2
	exit 1
fi
date '+%Y-%m-%d %H:%M:%S' > "$lock/started"
trap 'rm -rf "$lock"' EXIT
trap 'exit 130' INT TERM

out=bench/results/$(date +%Y-%m-%d-%H%M%S)
if ! mkdir "$out"; then
	echo "$out already exists; not overwriting it." >&2
	exit 1
fi

{
	echo "date:      $(date '+%Y-%m-%d %H:%M:%S %Z')"
	echo "go:        $(go version)"
	echo "os:        $(uname -sr)"
	if [ "$(uname -s)" = Darwin ]; then
		echo "macos:     $(sw_vers -productVersion)"
		echo "cpu:       $(sysctl -n machdep.cpu.brand_string), $(sysctl -n hw.perflevel0.physicalcpu) performance + $(sysctl -n hw.perflevel1.physicalcpu) efficiency cores"
		echo "memory:    $(sysctl -n hw.memsize) bytes"
		echo "power:     $(pmset -g batt | head -1 | sed "s/.*'\(.*\)'.*/\1/")"
	else
		echo "cpu:       $(grep -m1 'model name' /proc/cpuinfo | cut -d: -f2 | sed 's/^ //'), $(nproc) cores"
		echo "memory:    $(awk '/MemTotal/ {print $2 * 1024}' /proc/meminfo) bytes"
	fi
	echo "load:      $(uptime | sed 's/.*load average[s]*: //')  (1, 5, 15 min; before the run)"
	echo "runs:      $count of each benchmark, -p 1"
	echo "command:   go test ./... -run '^\$' -bench . -benchmem -count $count -p 1"
} > "$out/machine.txt"
cat "$out/machine.txt"
echo

go test ./... -run '^$' -bench . -benchmem -count "$count" -p 1 -timeout 60m | tee "$out/raw.txt"
# A load much higher after the run than before means something else used the
# CPU meanwhile; the spread column shows which results it disturbed.
echo "load after: $(uptime | sed 's/.*load average[s]*: //')  (1, 5, 15 min; right after the run)" >> "$out/machine.txt"
go run ./bench/summarize "$out/machine.txt" "$out/raw.txt" > "$out/summary.md"
go run ./bench/e2e -n "${N:-20000000}" > "$out/e2e.md"
echo
echo "summary: $out/summary.md"
echo "e2e:     $out/e2e.md"
