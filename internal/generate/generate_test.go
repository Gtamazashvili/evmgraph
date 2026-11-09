package generate

import (
	"context"
	"errors"
	"evmgraph/internal/evm"
	"runtime"
	"slices"
	"testing"
)

func TestGenerateIsReproducible(t *testing.T) {
	a, _ := addresses(context.Background(), 1000, 42)
	b, _ := addresses(context.Background(), 1000, 42)
	c, _ := addresses(context.Background(), 1000, 43)
	if !slices.Equal(a, b) {
		t.Fatal("same seed gave different addresses")
	}
	if slices.Equal(a, c) {
		t.Fatal("different seeds gave the same addresses")
	}
}

// The dataset depends on the seed only, never on how many cores made it.
func TestResultDoesNotDependOnWorkers(t *testing.T) {
	n := 3*BlockSize + 123
	want, err := generate(context.Background(), 7, 0, n, nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []int{2, 3, 8} {
		got, err := generate(context.Background(), 7, 0, n, nil, w)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(got, want) {
			t.Fatalf("%d workers gave a different dataset than 1 worker", w)
		}
	}
}

// Blocks are separate streams: no two blocks may start with the same bytes.
func TestBlocksDiffer(t *testing.T) {
	all, _ := addresses(context.Background(), 3*BlockSize, 1)
	if all[0] == all[BlockSize] || all[BlockSize] == all[2*BlockSize] {
		t.Fatal("two blocks start with the same address")
	}
}

func TestGenerateCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := addresses(ctx, 10, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context.Canceled", err)
	}
}

// A cancelled run returns a correct prefix: exactly the first addresses a
// full run makes, with no gaps from blocks that were still in progress.
func TestCancelledRunReturnsPrefix(t *testing.T) {
	n := 4*BlockSize + 5
	full, _ := addresses(context.Background(), n, 3)
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	part, err := rangeOf(ctx, 3, 0, n, func(int) {
		if calls++; calls == 20 {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context.Canceled", err)
	}
	if len(part) >= n {
		t.Fatalf("cancelled run returned all %d addresses", len(part))
	}
	if !slices.Equal(part, full[:len(part)]) {
		t.Fatal("cancelled run is not a prefix of the full run")
	}
}

// Ranges that start and end inside blocks, or span several, match the same
// positions of a full run.
func TestRangeMatchesFullRun(t *testing.T) {
	n := 3*BlockSize + 77
	full, _ := addresses(context.Background(), n, 9)
	cases := [][2]int{
		{70_000, 200_000},                  // inside block 0
		{BlockSize - 1000, BlockSize + 10}, // across one block edge
		{BlockSize, 2 * BlockSize},         // exactly one block
		{BlockSize/2 + 3, n},               // across several, to the end
		{n, n},                             // empty
	}
	for _, c := range cases {
		part, err := rangeOf(context.Background(), 9, c[0], c[1], nil)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(part, full[c[0]:c[1]]) {
			t.Fatalf("rangeOf(%d, %d) differs from the same positions of a full run", c[0], c[1])
		}
	}
}

// Progress rises to the total, and its last call is the total.
func TestRangeReportsProgress(t *testing.T) {
	var got []int
	addrs, err := rangeOf(context.Background(), 1, 10, 10+2*BlockSize+5, func(n int) { got = append(got, n) })
	if err != nil {
		t.Fatal(err)
	}
	if len(got) < 2 || got[len(got)-1] != len(addrs) || !slices.IsSorted(got) {
		t.Fatalf("progress calls %v, want rising values ending at %d", got, len(addrs))
	}

	got = nil
	if _, err := rangeOf(context.Background(), 1, 0, 0, func(n int) { got = append(got, n) }); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []int{0}) {
		t.Fatalf("progress calls for nothing to generate %v, want [0]", got)
	}
}

// BenchmarkGenerate measures 1M addresses (one block, so one core), 50M (the
// program's default size, spread over all cores) and topping 50M up to 51M
// (the cost of starting in the middle of a dataset).
func BenchmarkGenerate(b *testing.B) {
	cases := []struct {
		name     string
		from, to int
	}{
		{"1M", 0, 1_000_000},
		{"50M", 0, 50_000_000},
		{"top-up-50M-to-51M", 50_000_000, 51_000_000},
	}
	for _, c := range cases {
		b.Run(c.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := rangeOf(context.Background(), 1, c.from, c.to, nil); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
	// What the program generates: addresses with their balances.
	b.Run("1M-with-balances", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if _, _, err := datasetOf(context.Background(), 1, 0, 1_000_000, nil); err != nil {
				b.Fatal(err)
			}
		}
	})
}

// Balances come with their addresses: the addresses equal Range's, and the
// balances are reproducible, differ between seeds, and do not depend on the
// number of cores.
func TestDataset(t *testing.T) {
	n := 2*BlockSize + 99
	addrs, bals, err := datasetOf(context.Background(), 5, 0, n, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(addrs) != n || len(bals) != n {
		t.Fatalf("got %d addresses and %d balances, want %d of each", len(addrs), len(bals), n)
	}
	only, _ := addresses(context.Background(), n, 5)
	if !slices.Equal(addrs, only) {
		t.Fatal("Dataset's addresses differ from Addresses'")
	}
	_, again, _ := dataset(context.Background(), 5, 0, n, nil, 3)
	if !slices.Equal(bals, again) {
		t.Fatal("balances depend on the number of workers")
	}
	_, other, _ := datasetOf(context.Background(), 6, 0, n, nil)
	if slices.Equal(bals, other) {
		t.Fatal("different seeds gave the same balances")
	}
	// The model's rich tail needs u128: balances above 18.4 ETH (2^64 wei)
	// use the high 64 bits. They are about 0.1% of addresses (balance.go).
	high := 0
	for _, b := range bals {
		if b.Hi() != 0 {
			high++
		}
	}
	if high == 0 || high > n/200 {
		t.Fatalf("%d of %d balances use the high 64 bits, want about 0.1%%", high, n)
	}
}

// A top-up range gives the same balances as the same positions of a full run.
func TestDatasetRangeMatchesFullRun(t *testing.T) {
	n := 2*BlockSize + 7
	fullA, fullB, _ := datasetOf(context.Background(), 9, 0, n, nil)
	partA, partB, err := datasetOf(context.Background(), 9, BlockSize-500, n, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(partA, fullA[BlockSize-500:]) || !slices.Equal(partB, fullB[BlockSize-500:]) {
		t.Fatal("a range differs from the same positions of a full run")
	}
}

// A cancelled run cuts addresses and balances to the same exact prefix.
func TestCancelledDatasetIsAPrefix(t *testing.T) {
	n := 4*BlockSize + 5
	fullA, fullB, _ := datasetOf(context.Background(), 3, 0, n, nil)
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	a, b, err := datasetOf(ctx, 3, 0, n, func(int) {
		if calls++; calls == 20 {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context.Canceled", err)
	}
	if len(a) != len(b) || len(a) >= n {
		t.Fatalf("got %d addresses and %d balances of %d", len(a), len(b), n)
	}
	if !slices.Equal(a, fullA[:len(a)]) || !slices.Equal(b, fullB[:len(b)]) {
		t.Fatal("the cancelled run is not a prefix of the full run")
	}
}

// Rows holds exactly what Dataset returns, with each row's position.
func TestRowsMatchDataset(t *testing.T) {
	n := BlockSize + 321
	addrs, bals, _ := datasetOf(context.Background(), 4, 100, n, nil)
	rows, err := Rows(context.Background(), 4, 100, n, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != len(addrs) {
		t.Fatalf("%d rows, want %d", len(rows), len(addrs))
	}
	for i, r := range rows {
		if r.Address != addrs[i] || r.Balance != bals[i] || r.Pos != uint32(100+i) {
			t.Fatalf("row %d differs", i)
		}
	}
}

// Shorthands for the tests: every position, a range, and both columns, on
// all cores.
func addresses(ctx context.Context, n int, seed uint64) ([]evm.Address, error) {
	return generate(ctx, seed, 0, n, nil, runtime.GOMAXPROCS(0))
}

func rangeOf(ctx context.Context, seed uint64, from, to int, progress func(int)) ([]evm.Address, error) {
	return generate(ctx, seed, from, to, progress, runtime.GOMAXPROCS(0))
}

func datasetOf(ctx context.Context, seed uint64, from, to int, progress func(int)) ([]evm.Address, []evm.Wei, error) {
	return dataset(ctx, seed, from, to, progress, runtime.GOMAXPROCS(0))
}
