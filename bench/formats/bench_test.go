package formats

// Benchmarks for every implementation, with the same 1,000,000 addresses.
// Run with:
//
//	go test ./bench/formats -run '^$' -bench 'Save|Load' -benchmem -count 5
//
// Save is the real save path (temporary file, fsync, rename). Load/open is
// what a start-up pays before the first address can be used; Load/read-all
// also touches every address, which matters for binary, whose mmap load
// defers reading until an address is used.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"evmgraph/internal/evm"
)

const benchN = 1_000_000

// sink keeps the compiler from removing reads whose result is unused.
var sink byte

func readAll(a []evm.Address) {
	var x byte
	for i := range a {
		x ^= a[i][0] ^ a[i][19]
	}
	sink ^= x
}

func BenchmarkSave(b *testing.B) {
	addrs := testAddresses(benchN, 1)
	for _, c := range All {
		b.Run(c.Name(), func(b *testing.B) {
			path := filepath.Join(b.TempDir(), c.FileName())
			b.ReportAllocs()
			for b.Loop() {
				if err := c.Save(context.Background(), path, nil, addrs); err != nil {
					b.Fatal(err)
				}
			}
			info, _ := os.Stat(path)
			b.ReportMetric(float64(info.Size())/1e6, "file-MB")
		})
	}
}

func BenchmarkLoad(b *testing.B) {
	addrs := testAddresses(benchN, 1)
	for _, use := range []struct {
		name string
		fn   func([]evm.Address)
	}{
		{"open", func([]evm.Address) {}},
		{"read-all", readAll},
	} {
		for _, c := range All {
			b.Run(use.name+"/"+c.Name(), func(b *testing.B) {
				path := filepath.Join(b.TempDir(), c.FileName())
				if err := c.Save(context.Background(), path, nil, addrs); err != nil {
					b.Fatal(err)
				}
				b.ReportAllocs()
				for b.Loop() {
					f, err := c.Load(path)
					if err != nil {
						b.Fatal(err)
					}
					use.fn(f.Addrs)
					f.Close()
				}
			})
		}
	}
}
