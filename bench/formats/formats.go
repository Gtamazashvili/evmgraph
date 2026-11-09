// Package formats holds the address storage formats decision 002 compared:
// binary (the one the program uses, from internal/columns), gzip, text,
// text-mmap (the same text file, mapped and decoded in parallel), CSV, JSON
// and gob. Only the benchmarks use this package, so the comparison stays
// reproducible without shipping six formats in the program.
//
// Every format follows the same rules, checked for all of them by
// formats_test.go:
//
//   - Save writes through columns.Write: a new temporary file, fsync, rename,
//     ctx checked and progress reported every columns.Batch addresses.
//   - Save writes several parts in order, as one dataset.
//   - Load returns an error wrapping fs.ErrNotExist for a missing file and
//     columns.ErrInvalidFile for contents that are not addresses in that
//     format, including a directory in its place.
//   - An empty dataset saves and loads as zero addresses.
//   - Loading what was saved gives back exactly the same addresses, in order.
package formats

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"iter"
	"os"

	"evmgraph/internal/columns"
	"evmgraph/internal/evm"
)

// Format saves addresses to a file and loads them back in one encoding.
type Format interface {
	// Name identifies the format in benchmark names, such as "binary".
	Name() string
	// FileName is the format's file name.
	FileName() string
	// MaxBytesPerAddress is the most disk space one address takes.
	MaxBytesPerAddress() int
	// Save writes all parts, in order, to path.
	Save(ctx context.Context, path string, progress func(saved int), parts ...[]evm.Address) error
	// SaveSeq is Save for addresses produced one at a time.
	SaveSeq(ctx context.Context, path string, progress func(saved int), addrs iter.Seq[*evm.Address]) error
	// Load reads path.
	Load(path string) (*columns.AddressFile, error)
}

// All lists every format; binary, the program's, is first.
var All = []Format{Binary{}, Gzip{}, Text{}, TextMapped{}, CSV{}, JSON{}, Gob{}}

// Binary is the program's format: raw 20 bytes per address, opened mapped.
type Binary struct{}

func (Binary) Name() string            { return "binary" }
func (Binary) FileName() string        { return columns.AddressesFileName }
func (Binary) MaxBytesPerAddress() int { return evm.AddressLen }

func (b Binary) Save(ctx context.Context, path string, progress func(int), parts ...[]evm.Address) error {
	return b.SaveSeq(ctx, path, progress, columns.Parts(parts...))
}

func (Binary) SaveSeq(ctx context.Context, path string, progress func(int), addrs iter.Seq[*evm.Address]) error {
	return columns.SaveAddresses(ctx, path, progress, addrs)
}

func (Binary) Load(path string) (*columns.AddressFile, error) { return columns.OpenAddresses(path) }

// loadDecoded is the load path of every format except binary: open the file,
// decode it into a new slice, and report decoding failures as
// columns.ErrInvalidFile. bytesPerAddress estimates the count from the file
// size, so the slice is allocated once instead of growing.
func loadDecoded(path string, bytesPerAddress int,
	decode func(r *bufio.Reader, addrs []evm.Address) ([]evm.Address, error)) (*columns.AddressFile, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: %s is not a regular file", columns.ErrInvalidFile, path)
	}

	addrs := make([]evm.Address, 0, info.Size()/int64(bytesPerAddress)+1)
	addrs, err = decode(bufio.NewReaderSize(f, 1<<20), addrs)
	if err != nil {
		var pathErr *os.PathError
		if errors.As(err, &pathErr) {
			return nil, err // an I/O error, not a problem with the contents
		}
		return nil, fmt.Errorf("%w: %s: %v", columns.ErrInvalidFile, path, err)
	}
	return &columns.AddressFile{Addrs: addrs}, nil
}
