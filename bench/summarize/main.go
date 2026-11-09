// Command summarize turns raw `go test -bench` output into a Markdown summary:
// for every benchmark the median, minimum and maximum time over all runs, the
// spread between them, and the median of every other metric (memory
// allocated, allocations, file size). Within each group of the format
// comparison (bench/formats), times are also shown relative to the binary
// format, the format decision 002 chose.
//
// Usage:
//
//	go run ./bench/summarize machine.txt raw.txt > summary.md
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

type result struct {
	name    string               // e.g. "Load/open/binary"
	runs    []float64            // ns/op of every run
	metrics map[string][]float64 // other units, every run
}

// groups lists the benchmarks in reading order, with a one-line explanation.
var groups = []struct{ prefix, title, about string }{
	{"Generate", "Creating addresses", "Generating addresses from a seed: 1,000,000 (one block, one core), 50,000,000 (all cores), positions 50,000,000 to 51,000,000 alone, and 1,000,000 with their balances from the balance model (decisions 006, 008)."},
	{"Save/", "Saving 1,000,000 addresses", "The full save path: temporary file, fsync, rename."},
	{"Load/open/", "Loading: open", "The cost before the first address can be used."},
	{"Load/read-all/", "Loading: open and read every address", "Fair to binary, whose mmap load defers reading until an address is used."},
	{"Open/", "Binary load strategies", "The same binary file loaded three ways, for open only, one lookup, and reading everything."},
	{"TextDecode/", "Text decoding, 1 core against all cores", "Decoding a mapped text file; the step text-mmap parallelises."},
	{"TextView/", "Lazy text view (experiment)", "A mapped text file decoded only where it is read; not used by the program."},
	{"Sort1M", "Sorting rows by address", "1,000,000 generated rows sorted in place on all cores (decision 010)."},
	{"Find/", "Finding an address", "Interpolation search on sorted addresses against binary search, 1M and 20M, warm: 4,096 repeated keys stay in the CPU cache (decision 010)."},
	{"Build1M", "Building the graph", "The CSR graph of 1,000,000 addresses at degree 10 and skew 3, two passes on all cores (decision 012)."},
	{"Search/", "Searching connections", "The depth-2 search on 1M addresses: a typical address, and one next to the biggest hub (decisions 004, 005)."},
	{"Checksummer", "Printing an address", "One EIP-55 checksum (Keccak-256), the cost per printed address (decision 007)."},
}

var line = regexp.MustCompile(`^Benchmark(\S+?)(-\d+)?\s+\d+\s+(.*)$`)

func main() {
	flag.Parse()
	if flag.NArg() != 2 {
		fmt.Fprintln(os.Stderr, "usage: summarize machine.txt raw.txt")
		os.Exit(2)
	}
	machine, err := os.ReadFile(flag.Arg(0))
	check(err)
	results, err := parse(flag.Arg(1))
	check(err)
	if len(results) == 0 {
		check(fmt.Errorf("%s: no benchmark results found", flag.Arg(1)))
	}

	fmt.Println("# Benchmark results")
	fmt.Println()
	fmt.Println("Every benchmark ran in one session under the same conditions; 1,000,000 addresses unless stated. Times are the median over all runs; *spread* is (max − min) / median, a quick measure of how steady the runs were.")
	fmt.Println()
	fmt.Println("```")
	fmt.Print(string(machine))
	fmt.Println("```")

	used := map[string]bool{}
	for _, g := range groups {
		var rows []*result
		for _, r := range results {
			if strings.HasPrefix(r.name, g.prefix) {
				rows = append(rows, r)
				used[r.name] = true
			}
		}
		if len(rows) > 0 {
			printGroup(g.title, g.about, g.prefix, rows)
		}
	}
	var rest []*result
	for _, r := range results {
		if !used[r.name] {
			rest = append(rest, r)
		}
	}
	if len(rest) > 0 {
		printGroup("Other", "", "", rest)
	}
}

func parse(path string) ([]*result, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var order []*result
	byName := map[string]*result{}
	s := bufio.NewScanner(f)
	for s.Scan() {
		m := line.FindStringSubmatch(s.Text())
		if m == nil {
			continue
		}
		r := byName[m[1]]
		if r == nil {
			r = &result{name: m[1], metrics: map[string][]float64{}}
			byName[m[1]] = r
			order = append(order, r)
		}
		fields := strings.Fields(m[3])
		for i := 0; i+1 < len(fields); i += 2 {
			v, err := strconv.ParseFloat(fields[i], 64)
			if err != nil {
				continue
			}
			if unit := fields[i+1]; unit == "ns/op" {
				r.runs = append(r.runs, v)
			} else {
				r.metrics[unit] = append(r.metrics[unit], v)
			}
		}
	}
	return order, s.Err()
}

func printGroup(title, about, prefix string, rows []*result) {
	fmt.Printf("\n## %s\n\n", title)
	if about != "" {
		fmt.Printf("%s\n\n", about)
	}
	var base float64 // the binary format's median, if this group has one
	for _, r := range rows {
		if strings.TrimPrefix(r.name, prefix) == "binary" {
			base = median(r.runs)
		}
	}
	header := "| Benchmark | Median | Min | Max | Spread | Memory allocated | Allocations |"
	sep := "|---|---|---|---|---|---|---|"
	if base > 0 {
		header += " vs binary |"
		sep += "---|"
	}
	hasFile := slices.ContainsFunc(rows, func(r *result) bool { return r.metrics["file-MB"] != nil })
	if hasFile {
		header += " File |"
		sep += "---|"
	}
	fmt.Println(header)
	fmt.Println(sep)
	for _, r := range rows {
		med, lo, hi := median(r.runs), slices.Min(r.runs), slices.Max(r.runs)
		row := fmt.Sprintf("| %s | %s | %s | %s | %.0f%% | %s | %s |",
			strings.TrimPrefix(r.name, prefix), duration(med), duration(lo), duration(hi),
			100*(hi-lo)/med, megabytes(median(r.metrics["B/op"])), count(median(r.metrics["allocs/op"])))
		if base > 0 {
			row += " " + ratio(med/base) + " |"
		}
		if hasFile {
			if v := r.metrics["file-MB"]; v != nil {
				row += fmt.Sprintf(" %.2f MB |", median(v))
			} else {
				row += " |"
			}
		}
		fmt.Println(row)
	}
	fmt.Printf("\n%d runs each.\n", len(rows[0].runs))
}

func median(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := slices.Sorted(slices.Values(v))
	if n := len(s); n%2 == 1 {
		return s[n/2]
	} else {
		return (s[n/2-1] + s[n/2]) / 2
	}
}

func duration(ns float64) string {
	switch {
	case ns >= 1e6:
		return fmt.Sprintf("%.2f ms", ns/1e6)
	case ns >= 1e3:
		return fmt.Sprintf("%.1f µs", ns/1e3)
	default:
		return fmt.Sprintf("%.0f ns", ns)
	}
}

func megabytes(b float64) string {
	if b < 1e5 {
		return fmt.Sprintf("%.0f B", b)
	}
	return fmt.Sprintf("%.1f MB", b/1e6)
}

func count(n float64) string {
	if n >= 1e6 {
		return fmt.Sprintf("%.1f million", n/1e6)
	}
	return strconv.FormatFloat(n, 'f', 0, 64)
}

func ratio(r float64) string {
	switch {
	case r == 1:
		return "1×"
	case r >= 10:
		return fmt.Sprintf("%.0f× slower", r)
	case r > 1:
		return fmt.Sprintf("%.1f× slower", r)
	default:
		return fmt.Sprintf("%.1f× faster", 1/r)
	}
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
