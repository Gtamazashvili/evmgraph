// Package graph builds and searches the connections between addresses
// (decisions 003, 004, 005 and 012).
//
// The graph is stored by row, in compressed sparse row (CSR) form: row i's
// neighbours are Neighbors[Offsets[i]:Offsets[i+1]], as row numbers, sorted.
// Rows are in address order (decision 010), so every list is in address
// order too. Links are undirected (decision 003): each one is in both
// endpoints' lists. With 4-byte offsets and neighbours, the whole graph costs
// 4 + 4·degree bytes per address, 44 at the default degree of 10.
package graph

import (
	"context"
	"errors"
	"fmt"
	"math"
	mbits "math/bits"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"

	"evmgraph/internal/generate"
)

// Graph is a CSR graph over rows 0..Len()-1, plus links added since the CSR
// was written (decision 014), which the search merges in.
type Graph struct {
	Offsets   []uint32 // Len()+1 entries; Offsets[Len()] is the number of neighbour entries
	Neighbors []uint32
	// Extra holds, per row, the added links not in its CSR list: sorted, no
	// duplicates, both directions (Overlay builds it). nil when there are none.
	Extra map[uint32][]uint32
}

// Len is the number of rows.
func (g Graph) Len() int { return max(len(g.Offsets)-1, 0) }

// List returns row r's neighbours in the CSR, sorted, without Extra.
func (g Graph) List(r uint32) []uint32 { return g.Neighbors[g.Offsets[r]:g.Offsets[r+1]] }

// Has reports whether rows a and b are linked, in the CSR or in Extra.
func (g Graph) Has(a, b uint32) bool {
	if _, ok := slices.BinarySearch(g.List(a), b); ok {
		return true
	}
	_, ok := slices.BinarySearch(g.Extra[a], b)
	return ok
}

// Overlay turns added links, as row pairs, into Extra for g: both
// directions, sorted, without self-links, duplicates or links g's CSR already
// has.
func Overlay(g Graph, links [][2]uint32) map[uint32][]uint32 {
	extra := map[uint32][]uint32{}
	for _, l := range links {
		a, b := l[0], l[1]
		if a == b {
			continue
		}
		if _, ok := slices.BinarySearch(g.List(a), b); ok {
			continue
		}
		extra[a] = append(extra[a], b)
		extra[b] = append(extra[b], a)
	}
	for r, l := range extra {
		slices.Sort(l)
		extra[r] = slices.Compact(l)
	}
	if len(extra) == 0 {
		return nil
	}
	return extra
}

// WithExtra writes g's CSR with Extra merged in: new offsets and neighbour
// lists, every list still sorted. It copies the whole graph, so it is for
// folding added links into new files, not for queries.
func WithExtra(g Graph) (offsets, neighbors []uint32, err error) {
	added := 0
	for _, l := range g.Extra {
		added += len(l)
	}
	if uint64(len(g.Neighbors))+uint64(added) > MaxEntries {
		return nil, nil, ErrTooManyLinks
	}
	n := g.Len()
	offsets = make([]uint32, n+1)
	neighbors = make([]uint32, 0, len(g.Neighbors)+added)
	for r := range n {
		a, b := g.List(uint32(r)), g.Extra[uint32(r)]
		for len(a) > 0 || len(b) > 0 {
			if len(b) == 0 || len(a) > 0 && a[0] < b[0] {
				neighbors, a = append(neighbors, a[0]), a[1:]
			} else {
				neighbors, b = append(neighbors, b[0]), b[1:]
			}
		}
		offsets[r+1] = uint32(len(neighbors))
	}
	return offsets, neighbors, nil
}

// ErrCorrupt marks graph files whose offsets or neighbours cannot be right.
var ErrCorrupt = errors.New("the graph's files are corrupt")

// Validate checks the whole graph: Len()+1 offsets from 0, never decreasing,
// ending at the number of neighbour entries; every list strictly increasing
// (sorted, no duplicates), without the row itself, every neighbour a row
// below Len(); and the added links in range. It reads every entry, so it
// suits a program that opens the graph once and queries it often; a one-shot
// query relies on the checks Run makes on the lists it reads, which keep it
// from crashing but not from a wrong answer on a list out of order.
func (g Graph) Validate() error {
	if len(g.Offsets) == 0 || g.Offsets[0] != 0 || int(g.Offsets[len(g.Offsets)-1]) != len(g.Neighbors) {
		return fmt.Errorf("%w: the offsets do not frame the neighbours", ErrCorrupt)
	}
	for i := 1; i < len(g.Offsets); i++ {
		if g.Offsets[i] < g.Offsets[i-1] {
			return fmt.Errorf("%w: offsets decrease at row %d", ErrCorrupt, i-1)
		}
	}
	n := uint32(g.Len())
	for r := range n {
		l := g.List(r)
		for i, x := range l {
			switch {
			case x >= n:
				return fmt.Errorf("%w: row %d lists row %d, beyond the last row %d", ErrCorrupt, r, x, n-1)
			case x == r:
				return fmt.Errorf("%w: row %d lists itself", ErrCorrupt, r)
			case i > 0 && x <= l[i-1]:
				return fmt.Errorf("%w: row %d's list is out of order or repeats row %d", ErrCorrupt, r, x)
			}
		}
	}
	for r, l := range g.Extra {
		if r >= n || slices.ContainsFunc(l, func(x uint32) bool { return x >= n }) {
			return fmt.Errorf("%w: an added link of row %d is beyond the last row %d", ErrCorrupt, r, n-1)
		}
	}
	return nil
}

// list is row r's neighbours, Extra merged in, with the checks Run needs, so
// corrupt files give an error, not a crash. A row with added links gets a new
// merged slice; every other row's list is a view of the CSR.
func (g Graph) list(r uint32) ([]uint32, error) {
	lo, hi := g.Offsets[r], g.Offsets[r+1]
	if lo > hi || int(hi) > len(g.Neighbors) {
		return nil, fmt.Errorf("%w: row %d's offsets %d..%d", ErrCorrupt, r, lo, hi)
	}
	l := g.Neighbors[lo:hi]
	n := uint32(g.Len())
	for _, x := range l {
		if x >= n {
			return nil, fmt.Errorf("%w: row %d lists row %d, beyond the last row %d", ErrCorrupt, r, x, n-1)
		}
	}
	if extra := g.Extra[r]; len(extra) > 0 {
		merged := make([]uint32, 0, len(l)+len(extra))
		for len(l) > 0 || len(extra) > 0 {
			if len(extra) == 0 || len(l) > 0 && l[0] < extra[0] {
				merged, l = append(merged, l[0]), l[1:]
			} else {
				merged, extra = append(merged, extra[0]), extra[1:]
			}
		}
		for _, x := range merged {
			if x >= n {
				return nil, fmt.Errorf("%w: row %d has an added link to row %d, beyond the last row %d", ErrCorrupt, r, x, n-1)
			}
		}
		return merged, nil
	}
	return l, nil
}

// ErrTooManyLinks refuses a graph with 2^32 neighbour entries or more, which
// 4-byte offsets cannot address: about 429 million addresses at degree 10.
var ErrTooManyLinks = errors.New("the graph would have 2^32 neighbour entries or more")

// MaxEntries is the largest number of neighbour entries 4-byte offsets allow.
const MaxEntries = math.MaxUint32

// BuildContext builds the graph of the dataset's first n positions, drawn
// from seed with the given average degree and skew (decision 011), by row:
// rowOf maps a generation position to its row. It never holds the list of
// links: it draws them twice, first to count each row's degree, then to fill
// the lists. neighbors is called once with the number of entries and returns
// where to put them. progress, if not nil, gets the positions done in each
// pass, 0..2n. It stops when ctx ends, between batches of links, and returns
// ctx's error; nothing it built is usable then.
func BuildContext(ctx context.Context, seed uint64, n, degree, skew int, rowOf []uint32,
	neighbors func(entries int) ([]uint32, error), progress func(done int)) (offsets []uint32, nb []uint32, err error) {
	return build(ctx, seed, n, degree, skew, rowOf, neighbors, progress, runtime.GOMAXPROCS(0))
}

func build(ctx context.Context, seed uint64, n, degree, skew int, rowOf []uint32,
	neighbors func(entries int) ([]uint32, error), progress func(done int), workers int) ([]uint32, []uint32, error) {
	if len(rowOf) < n {
		return nil, nil, fmt.Errorf("rowOf has %d positions, the graph %d", len(rowOf), n)
	}
	pass := func(offset int) func(int) {
		if progress == nil {
			return nil
		}
		return func(done int) { progress(offset + done) }
	}

	// Pass 1: each row's degree.
	count := make([]uint32, n)
	if err := generate.EdgesContext(ctx, seed, n, degree, skew, workers, func(p, t uint32) {
		atomic.AddUint32(&count[rowOf[p]], 1)
		atomic.AddUint32(&count[rowOf[t]], 1)
	}, pass(0)); err != nil {
		return nil, nil, err
	}

	offsets := make([]uint32, n+1)
	var total uint64
	for i, c := range count {
		offsets[i] = uint32(total)
		total += uint64(c)
		if total > MaxEntries {
			return nil, nil, ErrTooManyLinks
		}
	}
	offsets[n] = uint32(total)

	nb, err := neighbors(int(total))
	if err != nil {
		return nil, nil, err
	}
	if len(nb) != int(total) {
		return nil, nil, fmt.Errorf("room for %d neighbour entries, need %d", len(nb), total)
	}

	// Pass 2: fill each row's list through its cursor, reusing count.
	cursor := count
	copy(cursor, offsets[:n])
	if err := generate.EdgesContext(ctx, seed, n, degree, skew, workers, func(p, t uint32) {
		a, b := rowOf[p], rowOf[t]
		nb[atomic.AddUint32(&cursor[a], 1)-1] = b
		nb[atomic.AddUint32(&cursor[b], 1)-1] = a
	}, pass(n)); err != nil {
		return nil, nil, err
	}

	// Sort every list, so the files do not depend on which goroutine filled
	// an entry first. Rows are handed out in chunks, so a hub's long list
	// does not hold up a whole share of the rows.
	const chunk = 4096
	var next atomic.Int64
	var wg sync.WaitGroup
	for range max(1, workers) {
		wg.Go(func() {
			for {
				lo := int(next.Add(chunk)) - chunk
				if lo >= n {
					return
				}
				for r := lo; r < min(lo+chunk, n); r++ {
					slices.Sort(nb[offsets[r]:offsets[r+1]])
				}
			}
		})
	}
	wg.Wait()
	return offsets, nb, nil
}

// Search runs the connections query (decision 004): a breadth-first search to
// depth 2. It keeps one bitset of the rows seen, n/8 bytes, and nothing else
// proportional to the result, so even a hub's millions of results cost no
// more memory (decision 005). Level 2 comes out in row order by scanning the
// bitset, so nothing is ever sorted. A Search is reused query after query.
type Search struct {
	g    Graph
	seen []uint64
}

// NewSearch prepares queries on g.
func NewSearch(g Graph) *Search {
	return &Search{g: g, seen: make([]uint64, (g.Len()+63)/64)}
}

// Result is one query's answer. Level1 is the queried row's own list, in row
// order. Level2Count is the exact number of rows two steps away and not
// nearer; Level2 yields them in row order. A Result is valid until the next
// Run on the same Search.
type Result struct {
	s           *Search
	q           uint32
	Level1      []uint32
	Level2Count int
}

// Run answers the query for row q, which must be below Len(). Each row is
// reported once, at its shortest level, and q itself is left out. Every list
// it reads is checked, so corrupt files give ErrCorrupt rather than a crash.
// A Search is not safe for concurrent use: it has one bitset.
func (s *Search) Run(q uint32) (Result, error) {
	return s.RunContext(context.Background(), q)
}

// checkEvery is how many neighbour entries a search reads between checks of
// its context: about 0.1 ms of work, so a cancel or deadline is answered
// within that, and the checks cost nothing measurable.
const checkEvery = 1 << 16

// RunContext is Run that stops when ctx is cancelled or its deadline passes,
// returning ctx's error. The worst query on 20M addresses, the biggest hub's
// own, reads nearly the whole graph: 0.14 to 1.4 s measured. A deadline
// bounds it on any dataset size, and a long-running caller can drop a query
// nobody wants any more.
func (s *Search) RunContext(ctx context.Context, q uint32) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	clear(s.seen)
	s.mark(q)
	level1, err := s.g.list(q)
	if err != nil {
		return Result{}, err
	}
	for _, x := range level1 {
		s.mark(x) // lists hold no duplicates and no self-links
	}
	level2, read := 0, 0
	for _, x := range level1 {
		next, err := s.g.list(x)
		if err != nil {
			return Result{}, err
		}
		for _, y := range next {
			if s.mark(y) {
				level2++
			}
		}
		if read += len(next) + 1; read >= checkEvery {
			read = 0
			if err := ctx.Err(); err != nil {
				return Result{}, err
			}
		}
	}
	return Result{s: s, q: q, Level1: level1, Level2Count: level2}, nil
}

// mark sets row r's bit and reports whether it was new.
func (s *Search) mark(r uint32) bool {
	w, bit := r>>6, uint64(1)<<(r&63)
	if s.seen[w]&bit != 0 {
		return false
	}
	s.seen[w] |= bit
	return true
}

// Level2 yields the rows two steps away, in row order: the seen rows that are
// neither the queried row nor on level 1.
func (r Result) Level2(yield func(row uint32) bool) {
	if r.Level2Count == 0 {
		return
	}
	for w, bits := range r.s.seen {
		for bits != 0 {
			b := uint32(w*64 + mbits.TrailingZeros64(bits))
			bits &= bits - 1
			if b == r.q {
				continue
			}
			if _, onLevel1 := slices.BinarySearch(r.Level1, b); onLevel1 {
				continue
			}
			if !yield(b) {
				return
			}
		}
	}
}
