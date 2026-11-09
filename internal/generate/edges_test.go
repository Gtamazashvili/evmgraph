package generate

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"math"
	"math/big"
	"math/rand/v2"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
)

// collect returns every link of the graph as (p, t) pairs, sorted.
func collect(seed uint64, n, degree, skew, workers int) [][2]uint32 {
	var mu sync.Mutex
	var links [][2]uint32
	Edges(seed, n, degree, skew, workers, func(p, t uint32) {
		mu.Lock()
		links = append(links, [2]uint32{p, t})
		mu.Unlock()
	}, nil)
	slices.SortFunc(links, func(a, b [2]uint32) int {
		if a[0] != b[0] {
			return int(a[0]) - int(b[0])
		}
		return int(a[1]) - int(b[1])
	})
	return links
}

// target is exactly floor(p · x^skew / 2^(32·skew)), checked against
// math/big on random and extreme inputs.
func TestTargetIsExact(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	inputs := [][2]uint32{{1, 0}, {1, math.MaxUint32}, {math.MaxUint32, math.MaxUint32}, {math.MaxUint32, 1}, {7, 1 << 31}}
	for range 5000 {
		inputs = append(inputs, [2]uint32{r.Uint32(), r.Uint32()})
	}
	for skew := MinSkew; skew <= MaxSkew; skew++ {
		for _, in := range inputs {
			p, x := in[0], in[1]
			want := new(big.Int).Exp(big.NewInt(int64(x)), big.NewInt(int64(skew)), nil)
			want.Mul(want, big.NewInt(int64(p)))
			want.Rsh(want, uint(32*skew))
			if got := target(p, x, skew); uint64(got) != want.Uint64() {
				t.Fatalf("target(%d, %d, %d) = %d, want %s", p, x, skew, got, want)
			}
			if p > 0 && target(p, x, skew) >= p {
				t.Fatalf("target(%d, %d, %d) is not below p", p, x, skew)
			}
		}
	}
}

// The graph depends on the seed only, not on the number of cores, and the
// links of positions below n1 are the same in a graph of n2 > n1.
func TestEdgesAreDeterministic(t *testing.T) {
	n := BlockSize + 50_000
	one := collect(7, n, 10, 3, 1)
	if !slices.Equal(collect(7, n, 10, 3, 8), one) {
		t.Fatal("8 workers drew a different graph than 1")
	}
	small := collect(7, 100_000, 10, 3, 4)
	var prefix [][2]uint32
	for _, l := range one {
		if l[0] < 100_000 {
			prefix = append(prefix, l)
		}
	}
	if !slices.Equal(small, prefix) {
		t.Fatal("the links of the first 100,000 positions depend on n")
	}
	if slices.Equal(collect(8, 100_000, 10, 3, 4), small) {
		t.Fatal("different seeds drew the same graph")
	}
}

// Links point backwards, never to the position itself, and never twice; each
// position past the first m has m links (fewer only when draws run out).
func TestEdgesShape(t *testing.T) {
	for skew := MinSkew; skew <= MaxSkew; skew++ {
		links := collect(3, 50_000, 10, skew, 4)
		out := map[uint32]int{}
		for i, l := range links {
			if l[1] >= l[0] {
				t.Fatalf("skew %d: link %v does not point backwards", skew, l)
			}
			if i > 0 && links[i-1] == l {
				t.Fatalf("skew %d: link %v twice", skew, l)
			}
			out[l[0]]++
		}
		short := 0
		for p := uint32(6); p < 50_000; p++ {
			if out[p] < 5 {
				short++
			}
		}
		// Only skew 4's earliest positions may run out of distinct draws.
		if short > 100 {
			t.Fatalf("skew %d: %d positions have fewer than 5 links", skew, short)
		}
	}
}

// The biggest hub, position 0, has about the in-degree the model predicts:
// m · Σ p^(−1/skew), about m · n^(1−1/skew) / (1 − 1/skew) for skew > 1, and
// about m · ln n for skew 1 (no hubs).
func TestHubDegreeMatchesTheModel(t *testing.T) {
	const n, m = 200_000, 5
	for skew, want := range map[int]float64{
		1: m * math.Log(n),
		2: m * math.Pow(n, 0.5) / 0.5,
		3: m * math.Pow(n, 2.0/3) / (2.0 / 3),
	} {
		var mu sync.Mutex
		hub := 0
		Edges(9, n, 2*m, skew, 8, func(p, t uint32) {
			if t == 0 {
				mu.Lock()
				hub++
				mu.Unlock()
			}
		}, nil)
		if got := float64(hub); got < want*0.75 || got > want*1.25 {
			t.Fatalf("skew %d: position 0 has %d links, the model predicts about %.0f", skew, hub, want)
		}
	}
}

// goldenEdges is the SHA-256 of the sorted links of seed 1, n 100,000, degree
// 10, skew 3, each link as two little-endian uint32. If it changes, every
// saved dataset's graph changes.
const goldenEdges = "53a3a243c9a0b2e433f1493e50002d4cb64faa8940bfc054397e7f3c45b00a10"

func edgesHash() string {
	h := sha256.New()
	var buf [8]byte
	for _, l := range collect(1, 100_000, 10, 3, runtime.GOMAXPROCS(0)) {
		binary.LittleEndian.PutUint32(buf[:4], l[0])
		binary.LittleEndian.PutUint32(buf[4:], l[1])
		h.Write(buf[:])
	}
	return hex.EncodeToString(h.Sum(nil))
}

func TestEdgesGolden(t *testing.T) {
	if got := edgesHash(); got != goldenEdges {
		t.Fatalf("edges hash %s, want %s", got, goldenEdges)
	}
}

// The same graph on amd64: on an Apple silicon Mac, Rosetta runs the amd64
// build of TestEdgesGolden, the architecture of the Docker image.
func TestEdgesGoldenOnAMD64(t *testing.T) {
	if runtime.GOARCH == "amd64" || os.Getenv("EVMGRAPH_IN_AMD64") != "" {
		t.Skip("already amd64")
	}
	if runtime.GOOS != "darwin" || testing.Short() {
		t.Skip("needs Rosetta (macOS on Apple silicon) and a full run")
	}
	cmd := exec.Command("go", "test", "-count=1", "-run", "^TestEdgesGolden$", ".")
	cmd.Env = append(os.Environ(), "GOARCH=amd64", "EVMGRAPH_IN_AMD64=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		if strings.Contains(string(out), "bad CPU type") {
			t.Skip("Rosetta is not installed")
		}
		t.Fatalf("amd64 run: %v\n%s", err, out)
	}
}

// EdgesContext stops when its context ends, and reports why.
func TestEdgesContextStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	emitted := 0
	err := EdgesContext(ctx, 1, 3*BlockSize, 10, 3, 4, func(p, t uint32) { emitted++ }, nil)
	if !errors.Is(err, context.Canceled) || emitted != 0 {
		t.Fatalf("a cancelled context: %v after %d links", err, emitted)
	}
	if err := EdgesContext(context.Background(), 1, 1000, 10, 3, 4, func(p, t uint32) {}, nil); err != nil {
		t.Fatalf("an open context: %v", err)
	}
}
