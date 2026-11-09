// Package order keeps the dataset in address order (decision 010): rows are
// sorted by address, so an address's row number is its rank, and the address
// file itself is the search structure, with no index file.
//
// Each row carries its generation position through the sort, so the graph
// (whose edges are drawn between positions, decision 011) can be stored by
// row: after sorting, rows[i].Pos is the position whose row is i.
//
// Rows compare by address, then by balance. The balance only matters for two
// equal addresses, which random 20-byte addresses make astronomically
// unlikely; it makes the order total over every byte the files hold, so any
// correct sort gives byte-identical files.
package order

import (
	"bytes"
	"iter"
	"runtime"
	"slices"
	"sync"

	"evmgraph/internal/evm"
)

// Row is a generated account and its generation position: 40 bytes.
type Row struct {
	evm.Account
	Pos uint32
}

// compare orders accounts by address, then by balance bytes.
func compare(a, b evm.Account) int {
	if c := bytes.Compare(a.Address[:], b.Address[:]); c != 0 {
		return c
	}
	return bytes.Compare(a.Balance[:], b.Balance[:])
}

// CompareRows orders rows by address, then by balance bytes.
func CompareRows(a, b Row) int { return compare(a.Account, b.Account) }

// SortRows sorts rows the same way, keeping each row's position with it.
func SortRows(rows []Row) {
	sortWith(rows, func(r *Row) byte { return r.Address[0] }, CompareRows, runtime.GOMAXPROCS(0))
}

func sortWith[T any](rows []T, firstByte func(*T) byte, cmp func(a, b T) int, workers int) {
	var count [256]int
	for i := range rows {
		count[firstByte(&rows[i])]++
	}
	var start, next, end [256]int
	sum := 0
	for b := range 256 {
		start[b], next[b] = sum, sum
		sum += count[b]
		end[b] = sum
	}
	// Each swap puts one element into its own bucket for good, so this is a
	// single pass of at most len(rows) swaps.
	for b := range 256 {
		for next[b] < end[b] {
			d := firstByte(&rows[next[b]])
			if int(d) == b {
				next[b]++
				continue
			}
			rows[next[b]], rows[next[d]] = rows[next[d]], rows[next[b]]
			next[d]++
		}
	}

	buckets := make(chan int)
	var wg sync.WaitGroup
	for range max(1, min(workers, 256)) {
		wg.Go(func() {
			for b := range buckets {
				slices.SortFunc(rows[start[b]:end[b]], cmp)
			}
		})
	}
	for b := range 256 {
		if count[b] > 1 {
			buckets <- b
		}
	}
	close(buckets)
	wg.Wait()
}

// RowOf returns, for every generation position, its row in sorted rows: the
// map the graph needs to store edges by row. 4 bytes per row.
func RowOf(rows []Row) []uint32 {
	rowOf := make([]uint32, len(rows))
	for i := range rows {
		rowOf[rows[i].Pos] = uint32(i)
	}
	return rowOf
}

// Columns yields each row's address and balance, in order, for writing the
// two column files.
func Columns(rows []Row) iter.Seq2[*evm.Address, *evm.Wei] {
	return func(yield func(*evm.Address, *evm.Wei) bool) {
		for i := range rows {
			if !yield(&rows[i].Address, &rows[i].Balance) {
				return
			}
		}
	}
}

// Addresses and Balances project a row sequence onto one column each.
func Addresses(rows iter.Seq2[*evm.Address, *evm.Wei]) iter.Seq[*evm.Address] {
	return func(yield func(*evm.Address) bool) {
		for a := range rows {
			if !yield(a) {
				return
			}
		}
	}
}

func Balances(rows iter.Seq2[*evm.Address, *evm.Wei]) iter.Seq[*evm.Wei] {
	return func(yield func(*evm.Wei) bool) {
		for _, b := range rows {
			if !yield(b) {
				return
			}
		}
	}
}
