package formats

// Benchmarks for loading a binary address file: how long until the addresses can
// be used, and what using them then costs. Run with:
//
//	go test ./bench/formats -run '^$' -bench Open -benchmem -count 5
//
// Three ways to load the same 1,000,000-address file:
//
//	copy-loop  the first version: read through a buffer, copy one address at a time
//	read-once  one read straight into the []evm.Address memory, no per-address loop
//	mmap       map the file; nothing is read or copied until an address is used
//
// Each runs in three scenarios: open only, open plus one lookup (what a
// balance command does), and open plus reading every address.

import (
	"bufio"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"evmgraph/internal/columns"
	"evmgraph/internal/evm"
)

type loader struct {
	name string
	open func(path string) (addrs []evm.Address, close func() error, err error)
}

var loaders = []loader{
	{"copy-loop", openCopyLoop},
	{"read-once", openReadOnce},
	{"mmap", openMmap},
}

func openCopyLoop(path string) ([]evm.Address, func() error, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, nil, err
	}
	addrs, err := readBinary(bufio.NewReaderSize(f, 1<<20), info.Size())
	return addrs, noClose, err
}

func openReadOnce(path string) ([]evm.Address, func() error, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, nil, err
	}
	addrs := make([]evm.Address, info.Size()/evm.AddressLen)
	if len(addrs) == 0 {
		return addrs, noClose, nil
	}
	buf := unsafe.Slice((*byte)(unsafe.Pointer(&addrs[0])), len(addrs)*evm.AddressLen)
	_, err = io.ReadFull(f, buf)
	return addrs, noClose, err
}

func openMmap(path string) ([]evm.Address, func() error, error) {
	f, err := columns.OpenAddresses(path)
	if err != nil {
		return nil, nil, err
	}
	return f.Addrs, f.Close, nil
}

func noClose() error { return nil }

func benchFile(b *testing.B) string {
	b.Helper()
	path := filepath.Join(b.TempDir(), "addresses.bin")
	if err := (Binary{}).Save(context.Background(), path, nil, testAddresses(benchN, 1)); err != nil {
		b.Fatal(err)
	}
	return path
}

func BenchmarkOpen(b *testing.B) {
	path := benchFile(b)
	scenarios := []struct {
		name string
		use  func(addrs []evm.Address)
	}{
		{"only", func([]evm.Address) {}},
		{"one-lookup", func(a []evm.Address) { sink ^= a[len(a)/2][0] }},
		{"read-all", func(a []evm.Address) {
			var x byte
			for i := range a {
				x ^= a[i][0] ^ a[i][19]
			}
			sink ^= x
		}},
	}
	for _, s := range scenarios {
		for _, l := range loaders {
			b.Run(s.name+"/"+l.name, func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					addrs, closeFn, err := l.open(path)
					if err != nil {
						b.Fatal(err)
					}
					s.use(addrs)
					if err := closeFn(); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

func TestLoadersAgree(t *testing.T) {
	path := filepath.Join(t.TempDir(), "addresses.bin")
	want := testAddresses(5000, 3)
	if err := (Binary{}).Save(context.Background(), path, nil, want); err != nil {
		t.Fatal(err)
	}
	for _, l := range loaders {
		got, closeFn, err := l.open(path)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != len(want) {
			t.Fatalf("%s: got %d addresses, want %d", l.name, len(got), len(want))
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("%s: address %d differs", l.name, i)
			}
		}
		closeFn()
	}
}

// readBinary is the first loader: copy one address at a time out of a buffer.
func readBinary(r io.Reader, size int64) ([]evm.Address, error) {
	addrs := make([]evm.Address, size/evm.AddressLen)
	for i := range addrs {
		if _, err := io.ReadFull(r, addrs[i][:]); err != nil {
			return nil, err
		}
	}
	return addrs, nil
}
