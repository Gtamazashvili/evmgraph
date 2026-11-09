package lookup

import (
	"bytes"
	"context"
	"math/bits"
	"math/rand/v2"
	"slices"
	"sort"
	"testing"

	"evmgraph/internal/evm"
	"evmgraph/internal/generate"
	"evmgraph/internal/order"
)

// sortedAddresses returns n generated addresses sorted, as the dataset holds
// them.
func sortedAddresses(t testing.TB, n int) []evm.Address {
	t.Helper()
	rows, err := generate.Rows(context.Background(), 1, 0, n, nil)
	if err != nil {
		t.Fatal(err)
	}
	order.SortRows(rows)
	addrs := make([]evm.Address, n)
	for i, r := range rows {
		addrs[i] = r.Address
	}
	return addrs
}

// binarySearch is the reference Find must agree with.
func binarySearch(addrs []evm.Address, a evm.Address) (int, bool) {
	i := sort.Search(len(addrs), func(i int) bool { return bytes.Compare(addrs[i][:], a[:]) >= 0 })
	return i, i < len(addrs) && addrs[i] == a
}

func maxProbes(n int) int { return maxGuesses + bits.Len(uint(n)) + 1 }

// Every present address is found at its row; random absent ones are not, and
// both agree with binary search.
func TestFindMatchesBinarySearch(t *testing.T) {
	addrs := sortedAddresses(t, 200_000)
	r := rand.New(rand.NewPCG(3, 4))
	total := 0
	for range 20_000 {
		i := r.IntN(len(addrs))
		row, ok, probes := find(addrs, addrs[i])
		if !ok || row != i {
			t.Fatalf("address at row %d: got row %d, %v", i, row, ok)
		}
		total += probes
		var miss evm.Address
		for j := range miss {
			miss[j] = byte(r.Uint32())
		}
		row, ok = Find(addrs, miss)
		wantRow, wantOK := binarySearch(addrs, miss)
		if ok != wantOK || (ok && row != wantRow) {
			t.Fatalf("absent address: got %d, %v; binary search %d, %v", row, ok, wantRow, wantOK)
		}
	}
	// Uniform keys: a handful of reads on average, against about 17 for
	// binary search at this size.
	if avg := float64(total) / 20_000; avg > 6 {
		t.Fatalf("%.1f reads per lookup on average, want about 4–5", avg)
	}
}

// Edge cases: empty and 1-element arrays, keys below the first and above the
// last, equal 8-byte prefixes, and duplicate addresses.
func TestFindEdgeCases(t *testing.T) {
	if _, ok := Find(nil, evm.Address{1}); ok {
		t.Fatal("found in an empty array")
	}
	one := []evm.Address{{5}}
	if row, ok := Find(one, evm.Address{5}); !ok || row != 0 {
		t.Fatal("not found in a 1-element array")
	}
	if _, ok := Find(one, evm.Address{4}); ok {
		t.Fatal("found an absent key below the only one")
	}
	addrs := sortedAddresses(t, 10_000)
	var low, high evm.Address
	for i := range high {
		high[i] = 0xff
	}
	if _, ok := Find(addrs, low); ok {
		t.Fatal("found the all-zero address")
	}
	if _, ok := Find(addrs, high); ok {
		t.Fatal("found the all-ones address")
	}
	// Equal prefixes: the first 8 bytes say nothing, only binary search helps.
	same := make([]evm.Address, 5000)
	for i := range same {
		same[i][0] = 0x77
		same[i][18], same[i][19] = byte(i>>8), byte(i)
	}
	for _, i := range []int{0, 1, 2499, 4999} {
		row, ok, probes := find(same, same[i])
		if !ok || row != i || probes > maxProbes(len(same)) {
			t.Fatalf("equal prefixes, row %d: got %d, %v after %d reads", i, row, ok, probes)
		}
	}
	// Duplicates: the first of them.
	dup := []evm.Address{{1}, {2}, {2}, {2}, {3}}
	if row, ok := Find(dup, evm.Address{2}); !ok || row != 1 {
		t.Fatalf("duplicates: got row %d, want 1", row)
	}
}

// Skewed or clustered keys defeat the guesses; the binary fallback keeps the
// cost at O(log n) reads.
func TestFindSkewedStaysLogarithmic(t *testing.T) {
	r := rand.New(rand.NewPCG(5, 6))
	n := 100_000
	clustered := make([]evm.Address, n)
	for i := range clustered {
		// 99% of the keys crowd near zero, 1% spread over the whole range.
		if i%100 == 0 {
			clustered[i][0] = byte(r.Uint32())
		}
		for j := 8; j < 20; j++ {
			clustered[i][j] = byte(r.Uint32())
		}
		clustered[i][7] = byte(r.Uint32())
	}
	slices.SortFunc(clustered, func(a, b evm.Address) int { return bytes.Compare(a[:], b[:]) })
	for range 2000 {
		i := r.IntN(n)
		row, ok, probes := find(clustered, clustered[i])
		if !ok || clustered[row] != clustered[i] {
			t.Fatalf("clustered: lost row %d", i)
		}
		if probes > maxProbes(n) {
			t.Fatalf("clustered: %d reads, more than %d", probes, maxProbes(n))
		}
	}
}

// BenchmarkFind measures one lookup of a present address, warm, for the
// README's lookup times.
func BenchmarkFind(b *testing.B) {
	for _, n := range []int{1_000_000, 20_000_000} {
		addrs := sortedAddresses(b, n)
		r := rand.New(rand.NewPCG(7, 8))
		keys := make([]evm.Address, 4096)
		for i := range keys {
			keys[i] = addrs[r.IntN(n)]
		}
		b.Run(map[int]string{1_000_000: "1M", 20_000_000: "20M"}[n], func(b *testing.B) {
			i := 0
			for b.Loop() {
				if _, ok := Find(addrs, keys[i%len(keys)]); !ok {
					b.Fatal("not found")
				}
				i++
			}
		})
		b.Run(map[int]string{1_000_000: "1M", 20_000_000: "20M"}[n]+"-binary", func(b *testing.B) {
			i := 0
			for b.Loop() {
				if _, ok := binarySearch(addrs, keys[i%len(keys)]); !ok {
					b.Fatal("not found")
				}
				i++
			}
		})
	}
}
