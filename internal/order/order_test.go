package order_test

import (
	"context"
	"math/rand/v2"
	"slices"
	"testing"

	"evmgraph/internal/evm"
	"evmgraph/internal/generate"
	"evmgraph/internal/order"
)

func generated(t testing.TB, n int, seed uint64) []order.Row {
	t.Helper()
	a, err := generate.Rows(context.Background(), seed, 0, n, nil)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// Sort gives the same result as a plain sort, on any number of cores, and
// keeps every address with its own balance.
func TestSortMatchesPlainSort(t *testing.T) {
	accts := generated(t, 300_000, 1)
	want := slices.Clone(accts)
	slices.SortFunc(want, order.CompareRows)
	for _, workers := range []int{1, 3, 8} {
		got := slices.Clone(accts)
		order.SortRowsWith(got, workers)
		if !slices.Equal(got, want) {
			t.Fatalf("%d workers: differs from a plain sort", workers)
		}
	}
	// Every row is still a row from the input: address, balance and position.
	byPos := map[uint32]order.Row{}
	for _, a := range accts {
		byPos[a.Pos] = a
	}
	for _, a := range want {
		if byPos[a.Pos] != a {
			t.Fatal("a row came apart in the sort")
		}
	}
	// RowOf inverts the positions.
	rowOf := order.RowOf(want)
	for i, r := range want {
		if rowOf[r.Pos] != uint32(i) {
			t.Fatalf("RowOf[%d] = %d, want %d", r.Pos, rowOf[r.Pos], i)
		}
	}
}

// Skewed input: every address in one bucket, equal addresses with different
// balances, empty and single-element slices.
func TestSortEdgeCases(t *testing.T) {
	order.SortRowsWith(nil, 4)
	one := []order.Row{{Account: evm.Account{Address: evm.Address{9}}}}
	order.SortRowsWith(one, 4)
	r := rand.New(rand.NewPCG(1, 2))
	skewed := make([]order.Row, 10_000)
	for i := range skewed {
		skewed[i].Address[0] = 0x42 // all in one bucket
		skewed[i].Address[19] = byte(r.IntN(4))
		skewed[i].Balance[0] = byte(r.IntN(256))
	}
	want := slices.Clone(skewed)
	slices.SortFunc(want, order.CompareRows)
	order.SortRowsWith(skewed, 8)
	if !slices.Equal(skewed, want) {
		t.Fatal("skewed input sorted wrongly")
	}
}

func BenchmarkSort1M(b *testing.B) {
	accts := generated(b, 1_000_000, 1)
	work := make([]order.Row, len(accts))
	for b.Loop() {
		copy(work, accts)
		order.SortRows(work)
	}
}
