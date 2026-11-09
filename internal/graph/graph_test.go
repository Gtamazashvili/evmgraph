package graph

import (
	"context"
	"errors"
	"math/rand/v2"
	"slices"
	"sync"
	"testing"
	"time"

	"evmgraph/internal/generate"
)

// shuffled returns a random position → row map, as sorting by address makes.
func shuffled(n int, seed uint64) []uint32 {
	rowOf := make([]uint32, n)
	for i := range rowOf {
		rowOf[i] = uint32(i)
	}
	r := rand.New(rand.NewPCG(seed, 1))
	r.Shuffle(n, func(i, j int) { rowOf[i], rowOf[j] = rowOf[j], rowOf[i] })
	return rowOf
}

func heap(entries int) ([]uint32, error) { return make([]uint32, entries), nil }

func mustBuild(t testing.TB, seed uint64, n, degree, skew int, rowOf []uint32, workers int) Graph {
	t.Helper()
	off, nb, err := build(context.Background(), seed, n, degree, skew, rowOf, heap, nil, workers)
	if err != nil {
		t.Fatal(err)
	}
	return Graph{Offsets: off, Neighbors: nb}
}

// naive builds the same graph as a map of sets, straight from the links.
func naive(seed uint64, n, degree, skew int, rowOf []uint32) map[uint32]map[uint32]bool {
	adj := map[uint32]map[uint32]bool{}
	var mu sync.Mutex
	add := func(a, b uint32) {
		if adj[a] == nil {
			adj[a] = map[uint32]bool{}
		}
		adj[a][b] = true
	}
	generate.Edges(seed, n, degree, skew, 4, func(p, t uint32) {
		mu.Lock()
		add(rowOf[p], rowOf[t])
		add(rowOf[t], rowOf[p])
		mu.Unlock()
	}, nil)
	return adj
}

// The CSR holds exactly the links, in both lists, sorted, with no self-links.
func TestBuildMatchesNaiveAdjacency(t *testing.T) {
	for skew := 1; skew <= 3; skew++ {
		const n = 5000
		rowOf := shuffled(n, uint64(skew))
		g := mustBuild(t, 11, n, 10, skew, rowOf, 4)
		adj := naive(11, n, 10, skew, rowOf)
		if g.Len() != n || g.Offsets[0] != 0 || int(g.Offsets[n]) != len(g.Neighbors) {
			t.Fatalf("skew %d: offsets do not frame the neighbours", skew)
		}
		for r := uint32(0); r < n; r++ {
			if g.Offsets[r] > g.Offsets[r+1] {
				t.Fatalf("skew %d: offsets decrease at row %d", skew, r)
			}
			list := g.List(r)
			if !slices.IsSorted(list) || len(list) != len(adj[r]) {
				t.Fatalf("skew %d: row %d's list is unsorted or has %d entries, want %d", skew, r, len(list), len(adj[r]))
			}
			for i, x := range list {
				if x == r || !adj[r][x] || (i > 0 && list[i-1] == x) {
					t.Fatalf("skew %d: row %d lists %d wrongly", skew, r, x)
				}
				if _, back := slices.BinarySearch(g.List(x), r); !back {
					t.Fatalf("skew %d: link %d–%d is in one list only", skew, r, x)
				}
			}
		}
	}
}

// The files do not depend on how many cores built them.
func TestBuildIndependentOfWorkers(t *testing.T) {
	rowOf := shuffled(generate.BlockSize+3000, 5)
	one := mustBuild(t, 3, len(rowOf), 10, 3, rowOf, 1)
	many := mustBuild(t, 3, len(rowOf), 10, 3, rowOf, 8)
	if !slices.Equal(one.Offsets, many.Offsets) || !slices.Equal(one.Neighbors, many.Neighbors) {
		t.Fatal("1 and 8 workers built different graphs")
	}
}

// naiveBFS is the reference: levels by plain breadth-first search.
func naiveBFS(g Graph, q uint32) (level1, level2 []uint32) {
	dist := map[uint32]int{q: 0}
	frontier := []uint32{q}
	for level := 1; level <= 2; level++ {
		var next []uint32
		for _, x := range frontier {
			for _, y := range g.List(x) {
				if _, ok := dist[y]; !ok {
					dist[y] = level
					next = append(next, y)
				}
			}
		}
		slices.Sort(next)
		if level == 1 {
			level1 = next
		} else {
			level2 = next
		}
		frontier = next
	}
	return level1, level2
}

func levels(res Result) (level1, level2 []uint32) {
	level1 = slices.Clone(res.Level1)
	res.Level2(func(r uint32) bool { level2 = append(level2, r); return true })
	return level1, level2
}

// The search matches a plain BFS on random graphs of every skew.
func TestSearchMatchesNaiveBFS(t *testing.T) {
	r := rand.New(rand.NewPCG(9, 9))
	for skew := 1; skew <= 3; skew++ {
		const n = 3000
		g := mustBuild(t, uint64(skew), n, 6, skew, shuffled(n, 7), 4)
		s := NewSearch(g)
		for range 300 {
			q := uint32(r.IntN(n))
			res, err := s.Run(q)
			if err != nil {
				t.Fatal(err)
			}
			got1, got2 := levels(res)
			want1, want2 := naiveBFS(g, q)
			if !slices.Equal(got1, want1) || !slices.Equal(got2, want2) || res.Level2Count != len(want2) {
				t.Fatalf("skew %d, row %d: got %d+%d (count %d), want %d+%d",
					skew, q, len(got1), len(got2), res.Level2Count, len(want1), len(want2))
			}
		}
	}
}

// fromLists builds a graph from hand-written, already symmetric lists.
func fromLists(lists [][]uint32) Graph {
	g := Graph{Offsets: []uint32{0}}
	for _, l := range lists {
		g.Neighbors = append(g.Neighbors, l...)
		g.Offsets = append(g.Offsets, uint32(len(g.Neighbors)))
	}
	return g
}

func TestSearchHandMadeCases(t *testing.T) {
	cases := []struct {
		name           string
		lists          [][]uint32
		q              uint32
		level1, level2 []uint32
	}{
		// 1 and 2 are each other's neighbours: 2 is reached at level 1 and
		// again at level 2, and reported once, at level 1. The queried row
		// is reached back at level 2 and left out.
		{"triangle", [][]uint32{{1, 2}, {0, 2}, {0, 1}}, 0, []uint32{1, 2}, nil},
		{"star from a leaf", [][]uint32{{1, 2, 3, 4}, {0}, {0}, {0}, {0}}, 3, []uint32{0}, []uint32{1, 2, 4}},
		{"star from the hub", [][]uint32{{1, 2, 3, 4}, {0}, {0}, {0}, {0}}, 0, []uint32{1, 2, 3, 4}, nil},
		{"isolated", [][]uint32{{1}, {0}, {}}, 2, nil, nil},
		{"path", [][]uint32{{1}, {0, 2}, {1, 3}, {2}}, 0, []uint32{1}, []uint32{2}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res, err := NewSearch(fromLists(c.lists)).Run(c.q)
			if err != nil {
				t.Fatal(err)
			}
			got1, got2 := levels(res)
			if !slices.Equal(got1, c.level1) && !(len(got1) == 0 && len(c.level1) == 0) {
				t.Fatalf("level 1 %v, want %v", got1, c.level1)
			}
			if !slices.Equal(got2, c.level2) || res.Level2Count != len(c.level2) {
				t.Fatalf("level 2 %v (count %d), want %v", got2, res.Level2Count, c.level2)
			}
		})
	}
}

// A Search is reused: one query leaves nothing behind for the next.
func TestSearchReuse(t *testing.T) {
	g := fromLists([][]uint32{{1}, {0, 2}, {1}, {}})
	s := NewSearch(g)
	s.Run(0)
	res, _ := s.Run(3)
	if got1, got2 := levels(res); len(got1) != 0 || len(got2) != 0 {
		t.Fatalf("a reused search reported %v, %v for an isolated row", got1, got2)
	}
}

func benchGraph(b *testing.B, n int) Graph {
	b.Helper()
	rowOf := shuffled(n, 1)
	return mustBuild(b, 1, n, 10, 3, rowOf, 8)
}

func BenchmarkBuild1M(b *testing.B) {
	rowOf := shuffled(1_000_000, 1)
	for b.Loop() {
		if _, _, err := build(context.Background(), 1, 1_000_000, 10, 3, rowOf, heap, nil, 8); err != nil {
			b.Fatal(err)
		}
	}
}

// Queries on 1M addresses at skew 3: a random row, and a neighbour of the
// biggest hub, whose level 2 is most of the hub's list.
func BenchmarkSearch(b *testing.B) {
	g := benchGraph(b, 1_000_000)
	s := NewSearch(g)
	r := rand.New(rand.NewPCG(2, 2))
	b.Run("typical", func(b *testing.B) {
		for b.Loop() {
			res, _ := s.Run(uint32(r.IntN(g.Len())))
			res.Level2(func(uint32) bool { return true })
		}
	})
	hub := uint32(0)
	for row := range uint32(g.Len()) {
		if len(g.List(row)) > len(g.List(hub)) {
			hub = row
		}
	}
	nextToHub := g.List(hub)[0]
	b.Run("hub-adjacent", func(b *testing.B) {
		for b.Loop() {
			res, _ := s.Run(nextToHub)
			res.Level2(func(uint32) bool { return true })
		}
	})
}

// Validate accepts a built graph and catches each kind of corruption; Run
// reports corruption in the lists it reads instead of crashing.
func TestValidateAndCorruption(t *testing.T) {
	g := mustBuild(t, 4, 2000, 10, 3, shuffled(2000, 4), 4)
	if err := g.Validate(); err != nil {
		t.Fatalf("a built graph: %v", err)
	}
	corrupt := func(change func(g *Graph)) Graph {
		c := Graph{Offsets: slices.Clone(g.Offsets), Neighbors: slices.Clone(g.Neighbors)}
		change(&c)
		return c
	}
	r := uint32(0) // a row with at least two neighbours
	for len(g.List(r)) < 2 {
		r++
	}
	at := g.Offsets[r]
	for name, bad := range map[string]Graph{
		"list out of order":    corrupt(func(c *Graph) { c.Neighbors[at], c.Neighbors[at+1] = c.Neighbors[at+1], c.Neighbors[at] }),
		"list repeats a row":   corrupt(func(c *Graph) { c.Neighbors[at+1] = c.Neighbors[at] }),
		"row lists itself":     corrupt(func(c *Graph) { c.Neighbors[at] = r }),
		"offsets decrease":     corrupt(func(c *Graph) { c.Offsets[10], c.Offsets[11] = c.Offsets[11], c.Offsets[10]+1 }),
		"neighbour past rows":  corrupt(func(c *Graph) { c.Neighbors[5] = 999_999 }),
		"offsets past the end": corrupt(func(c *Graph) { c.Offsets[len(c.Offsets)-1]++ }),
	} {
		if err := bad.Validate(); !errors.Is(err, ErrCorrupt) {
			t.Errorf("%s: Validate gave %v", name, err)
		}
	}
	bad := corrupt(func(c *Graph) { c.Neighbors[c.Offsets[7]] = 999_999 })
	if _, err := NewSearch(bad).Run(7); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("Run on a corrupt list gave %v", err)
	}
}

// cancelAfter is a context whose Err reports cancellation from its n-th call
// on, so a test can cancel a search in the middle, deterministically.
type cancelAfter struct {
	context.Context
	calls, n int
}

func (c *cancelAfter) Err() error {
	if c.calls++; c.calls >= c.n {
		return context.Canceled
	}
	return nil
}

// A search stops when its context ends: before starting, and in the middle of
// a query that reads more than checkEvery entries.
func TestRunContextStops(t *testing.T) {
	// A star: row 0 is a hub with 300,000 leaves. A leaf's query reads the
	// hub's whole list, several times checkEvery.
	const leaves = 300_000
	lists := make([][]uint32, leaves+1)
	for i := uint32(1); i <= leaves; i++ {
		lists[0] = append(lists[0], i)
		lists[i] = []uint32{0}
	}
	s := NewSearch(fromLists(lists))

	done, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.RunContext(done, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("a cancelled context: %v", err)
	}
	expired, stop := context.WithTimeout(context.Background(), -time.Second)
	defer stop()
	if _, err := s.RunContext(expired, 1); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a passed deadline: %v", err)
	}
	// From the root: level 1 is the hub's 300,000 leaves, each with a short
	// list, so the check runs several times; cancel at the second check.
	mid := &cancelAfter{Context: context.Background(), n: 3}
	if _, err := s.RunContext(mid, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled mid-query: %v", err)
	}
	if mid.calls != 3 {
		t.Fatalf("the context was checked %d times, want 3: the search went on after the cancel", mid.calls)
	}
	// And a context that never ends answers in full.
	if res, err := s.RunContext(context.Background(), 1); err != nil || len(res.Level1) != 1 || res.Level2Count != leaves-1 {
		t.Fatalf("uncancelled: %+v, %v", res.Level1, err)
	}
}

// BuildContext stops when its context ends.
func TestBuildContextStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := BuildContext(ctx, 1, 100_000, 10, 3, shuffled(100_000, 1), heap, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("a cancelled build: %v", err)
	}
}

// Added links (decision 014): a search over the CSR plus Extra matches a
// plain BFS over the merged CSR WithExtra writes, and the merged lists are
// sorted, symmetric and free of duplicates, self-links and repeats of CSR
// links.
func TestAddedLinks(t *testing.T) {
	const n = 3000
	r := rand.New(rand.NewPCG(4, 4))
	g := mustBuild(t, 4, n, 6, 3, shuffled(n, 5), 4)
	var links [][2]uint32
	for range 400 {
		links = append(links, [2]uint32{uint32(r.IntN(n)), uint32(r.IntN(n))})
	}
	links = append(links, links[0], [2]uint32{7, 7}, [2]uint32{links[1][1], links[1][0]}) // a repeat, a self-link, a reversed repeat
	if l := g.List(10); len(l) > 0 {
		links = append(links, [2]uint32{10, l[0]}) // already in the CSR
	}
	g.Extra = Overlay(g, links)
	if err := g.Validate(); err != nil {
		t.Fatal(err)
	}
	offsets, neighbors, err := WithExtra(g)
	if err != nil {
		t.Fatal(err)
	}
	merged := Graph{Offsets: offsets, Neighbors: neighbors}
	if err := merged.Validate(); err != nil {
		t.Fatal(err)
	}
	added := 0
	for _, l := range g.Extra {
		added += len(l)
	}
	if len(neighbors) != len(g.Neighbors)+added {
		t.Fatalf("%d entries, want %d + %d", len(neighbors), len(g.Neighbors), added)
	}
	for a := range uint32(n) {
		l := merged.List(a)
		if !slices.IsSorted(l) || len(slices.Compact(slices.Clone(l))) != len(l) || slices.Contains(l, a) {
			t.Fatalf("row %d's merged list is not sorted, unique and free of self-links: %v", a, l)
		}
		for _, b := range l {
			if !merged.Has(b, a) || !g.Has(a, b) {
				t.Fatalf("link %d-%d is not symmetric or not in the overlay", a, b)
			}
		}
	}
	for _, l := range links {
		if l[0] != l[1] && !merged.Has(l[0], l[1]) {
			t.Fatalf("added link %v missing", l)
		}
	}
	s := NewSearch(g)
	for range 300 {
		q := uint32(r.IntN(n))
		res, err := s.Run(q)
		if err != nil {
			t.Fatal(err)
		}
		got1, got2 := levels(res)
		want1, want2 := naiveBFS(merged, q)
		if !slices.Equal(got1, want1) || !slices.Equal(got2, want2) {
			t.Fatalf("row %d: the search with added links differs from a BFS over the merged graph", q)
		}
	}
	if Overlay(g, nil) != nil || Overlay(g, [][2]uint32{{3, 3}}) != nil {
		t.Fatal("no real added links should give no overlay")
	}
	bad := Graph{Offsets: g.Offsets, Neighbors: g.Neighbors, Extra: map[uint32][]uint32{1: {n + 5}}}
	if err := bad.Validate(); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("an added link beyond the last row: got %v", err)
	}
}
