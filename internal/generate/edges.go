package generate

import (
	"context"
	"math/bits"
	"math/rand/v2"
	"slices"
	"sync"
)

// The graph model (decision 011): position p links to m = degree/2 distinct
// earlier positions (positions p ≤ m link to all earlier ones). Each target
// is t = floor(p · u^skew) with u uniform in [0, 1): skew 1 spreads links
// evenly, higher skews favour early positions, which become hubs. Skew 3
// gives a degree power law with an exponent of about 2.5, like real
// transaction graphs.
//
// Edges only point backwards, so the edges of positions below n never depend
// on n: a dataset of n1 positions is a prefix of one of n2 > n1. Every link is
// stored in both endpoints' lists (decision 003), so the average degree is
// 2m = degree.
//
// No floating point is used: Go lets the compiler fuse a multiply and an add,
// which it does on arm64 but not on amd64, so a float formula could draw a
// different graph from the same seed on a Mac and in the amd64 Docker image.
// u is drawn as x/2^32 with x uniform in [0, 2^32), and the target is computed
// exactly in integer arithmetic.

const (
	// MinSkew and MaxSkew bound the skew the exact integer formula supports.
	MinSkew, MaxSkew = 1, 4
	// edgeTag keys the edge streams, apart from the address and balance ones.
	edgeTag = "edges\x00"
	// maxTries bounds the draws for one position's distinct targets; a
	// position that cannot find m distinct ones in that many keeps fewer.
	maxTries = 64
)

// Edges calls emit(p, t) once for every link of the graph on positions
// [0, n): t < p, no self-links, no link twice. It runs on workers goroutines,
// block by block, each block with its own stream, so the set of links depends
// only on the arguments, never on workers; emit is called concurrently and in
// no particular order. progress, if not nil, is called with the number of
// positions done after each block; calls never overlap.
func Edges(seed uint64, n, degree, skew, workers int, emit func(p, t uint32), progress func(done int)) {
	EdgesContext(context.Background(), seed, n, degree, skew, workers, emit, progress)
}

// EdgesContext is Edges that stops when ctx ends, checking it before every
// batch of positions, and returns ctx's error then. The links emitted before
// the stop are a partial graph, to be discarded.
func EdgesContext(ctx context.Context, seed uint64, n, degree, skew, workers int,
	emit func(p, t uint32), progress func(done int)) error {
	m := degree / 2
	if n <= 0 || m <= 0 {
		return ctx.Err()
	}
	blocks := make(chan int)
	var mu sync.Mutex
	done := 0
	var wg sync.WaitGroup
	for range max(1, workers) {
		wg.Go(func() {
			picked := make([]uint32, 0, m)
			for b := range blocks {
				rng := rand.NewChaCha8(streamKey(seed, b, edgeTag))
				lo, hi := b*BlockSize, min((b+1)*BlockSize, n)
				for p := lo; p < hi; p++ {
					if (p-lo)%batch == 0 && ctx.Err() != nil {
						break
					}
					if p <= m {
						for t := range p {
							emit(uint32(p), uint32(t))
						}
						continue
					}
					picked = picked[:0]
					for tries := 0; len(picked) < m && tries < maxTries; tries++ {
						t := target(uint32(p), uint32(rng.Uint64()>>32), skew)
						if !slices.Contains(picked, t) {
							picked = append(picked, t)
						}
					}
					for _, t := range picked {
						emit(uint32(p), t)
					}
				}
				if progress != nil {
					mu.Lock()
					done += hi - lo
					progress(done)
					mu.Unlock()
				}
			}
		})
	}
	for b := 0; b*BlockSize < n && ctx.Err() == nil; b++ {
		select {
		case blocks <- b:
		case <-ctx.Done():
		}
	}
	close(blocks)
	wg.Wait()
	return ctx.Err()
}

// target is floor(p · x^skew / 2^(32·skew)): position p's link for the draw
// x, in [0, p). p · x^skew is below 2^32 · 2^128 = 2^160, so it is computed
// exactly in three 64-bit words.
func target(p, x uint32, skew int) uint32 {
	v := [3]uint64{uint64(p)} // low word first
	for range skew {
		var carry uint64
		for i := range v {
			hi, lo := bits.Mul64(v[i], uint64(x))
			var c uint64
			v[i], c = bits.Add64(lo, carry, 0)
			carry = hi + c // hi < 2^32, so this cannot overflow
		}
	}
	shift := 32 * skew
	w, s := shift/64, uint(shift%64)
	r := v[w] >> s
	if s != 0 && w+1 < len(v) {
		r |= v[w+1] << (64 - s)
	}
	return uint32(r)
}
