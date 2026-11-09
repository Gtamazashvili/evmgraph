// Package generate makes the simulated dataset from a seed: random EVM
// addresses, a balance in wei for each from the balance model (balance.go),
// and the links between them (edges.go). The same seed always gives the same
// dataset.
//
// The dataset is split into blocks of BlockSize positions. Each block has its
// own random stream per column (addresses, balances), keyed by the seed, the
// block's number and the column's tag, so blocks do not depend on each other:
// they are generated on all CPU cores at once, and a range that starts in the
// middle of the dataset starts at its block instead of generating and
// discarding everything before it. The result depends only on the seed, never
// on the number of cores.
package generate

import (
	"context"
	"encoding/binary"
	"math/rand/v2"
	"runtime"
	"sync"

	"evmgraph/internal/evm"
	"evmgraph/internal/order"
)

// BlockSize is how many positions one random stream covers. It is part of the
// dataset's definition: changing it changes the dataset every seed gives.
const BlockSize = 1 << 20

// batch is how many positions are generated between checks for shutdown and
// progress reports.
const batch = 1 << 16

// Column tags go into the stream keys, so every column gets its own streams
// from the same seed. Changing a tag changes that column for every seed.
const (
	addressTag = "address\x00"
	balanceTag = "balance\x00"
)

// Rows returns the dataset's positions from up to (not including) to as
// rows: each address with its balance and its position. A dataset of 3,000
// can grow to 10,000 with the same 3,000 rows a fresh run of 10,000 makes.
//
// When ctx is cancelled it stops early and returns ctx.Err() with the longest
// run of rows from `from` onwards that was completely generated, so the
// result is always a correct prefix of the full range. progress, if not nil,
// is called with the number of positions generated so far each time a batch
// finishes, and the last call is the total; calls never overlap.
func Rows(ctx context.Context, seed uint64, from, to int, progress func(generated int)) ([]order.Row, error) {
	rows := make([]order.Row, max(to-from, 0))
	n, err := fill(ctx, seed, from, to, progress, runtime.GOMAXPROCS(0), []column{
		{addressTag, evm.AddressLen, func(rng *rand.ChaCha8, i int) {
			rng.Read(rows[i].Address[:])
			rows[i].Pos = uint32(from + i)
		}},
		{balanceTag, evm.WeiLen, func(rng *rand.ChaCha8, i int) { rows[i].Balance = readBalance(rng) }},
	})
	return rows[:n], err
}

// readBalance draws one balance of the model from 16 bytes of rng.
func readBalance(rng *rand.ChaCha8) evm.Wei {
	var b [evm.WeiLen]byte
	rng.Read(b[:])
	return balanceFromBytes(b[:])
}

// generate returns the addresses of positions [from, to) on a chosen number
// of workers, so tests can check that the result does not depend on it.
func generate(ctx context.Context, seed uint64, from, to int, progress func(int), workers int) ([]evm.Address, error) {
	addrs := make([]evm.Address, max(to-from, 0))
	n, err := fill(ctx, seed, from, to, progress, workers, []column{
		{addressTag, evm.AddressLen, func(rng *rand.ChaCha8, i int) { rng.Read(addrs[i][:]) }},
	})
	return addrs[:n], err
}

// dataset returns the addresses and balances of positions [from, to): the
// same values Rows returns, as two columns, on a chosen number of workers.
func dataset(ctx context.Context, seed uint64, from, to int, progress func(int), workers int) ([]evm.Address, []evm.Wei, error) {
	addrs := make([]evm.Address, max(to-from, 0))
	bals := make([]evm.Wei, len(addrs))
	n, err := fill(ctx, seed, from, to, progress, workers, []column{
		{addressTag, evm.AddressLen, func(rng *rand.ChaCha8, i int) { rng.Read(addrs[i][:]) }},
		{balanceTag, evm.WeiLen, func(rng *rand.ChaCha8, i int) { bals[i] = readBalance(rng) }},
	})
	return addrs[:n], bals[:n], err
}

// column is one per-position value the generator fills: tag keys its streams,
// size is how many stream bytes one position takes, and read fills position i
// (counted from `from`) from the stream.
type column struct {
	tag  string
	size int
	read func(rng *rand.ChaCha8, i int)
}

// fill generates positions [from, to) of every column on workers goroutines
// and returns how many positions from `from` onwards are complete in all
// columns: to-from, or less when ctx was cancelled.
func fill(ctx context.Context, seed uint64, from, to int, progress func(int), workers int, cols []column) (int, error) {
	if to <= from {
		if progress != nil {
			progress(0)
		}
		return 0, ctx.Err()
	}

	first, last := from/BlockSize, (to-1)/BlockSize
	// done[b] counts the positions of block first+b finished in all columns.
	done := make([]int, last-first+1)

	var mu sync.Mutex // serialises progress calls
	generated := 0
	blocks := make(chan int)
	var wg sync.WaitGroup
	for range min(workers, len(done)) {
		wg.Go(func() {
			rngs := make([]*rand.ChaCha8, len(cols))
			for b := range blocks {
				// The part of block b that falls inside [from, to).
				lo, hi := max(b*BlockSize, from), min((b+1)*BlockSize, to)
				for c, col := range cols {
					rngs[c] = rand.NewChaCha8(streamKey(seed, b, col.tag))
					skipBytes(rngs[c], (lo-b*BlockSize)*col.size)
				}
				for i := lo; i < hi; i += batch {
					if ctx.Err() != nil {
						return
					}
					end := min(i+batch, hi)
					for c, col := range cols {
						for j := i; j < end; j++ {
							col.read(rngs[c], j-from)
						}
					}
					done[b-first] += end - i
					if progress != nil {
						mu.Lock()
						generated += end - i
						progress(generated)
						mu.Unlock()
					}
				}
			}
		})
	}
	// Blocks are handed out in order, so on cancellation the finished ones
	// are mostly at the front and the returned prefix is close to all the work.
	for b := first; b <= last && ctx.Err() == nil; b++ {
		select {
		case blocks <- b:
		case <-ctx.Done():
		}
	}
	close(blocks)
	wg.Wait()

	if err := ctx.Err(); err != nil {
		n := 0
		for b := first; b <= last; b++ {
			lo, hi := max(b*BlockSize, from), min((b+1)*BlockSize, to)
			n += done[b-first]
			if done[b-first] < hi-lo {
				break
			}
		}
		return n, err
	}
	return to - from, nil
}

// streamKey is the 32-byte ChaCha8 key of one block of one column: the seed,
// the block number and the column's tag.
func streamKey(seed uint64, block int, tag string) [32]byte {
	var k [32]byte
	binary.LittleEndian.PutUint64(k[0:], seed)
	binary.LittleEndian.PutUint64(k[8:], uint64(block))
	copy(k[16:], tag)
	return k
}

// skipBytes reads and discards n bytes of rng. It is only needed when a range
// starts inside a block, so n is less than one block of one column.
func skipBytes(rng *rand.ChaCha8, n int) {
	var buf [64 << 10]byte
	for ; n > 0; n -= len(buf) {
		rng.Read(buf[:min(n, len(buf))])
	}
}
