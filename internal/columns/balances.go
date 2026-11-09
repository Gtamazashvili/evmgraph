//go:build unix

package columns

import (
	"context"
	"io"
	"iter"
	"syscall"
	"unsafe"

	"evmgraph/internal/evm"
)

// Balances are stored as raw 16-byte little-endian u128 wei values (evm.Wei),
// one per address and in the same order: balance i belongs to address i. Like
// the binary address format there is no header; the count is the file size
// divided by 16. It is written and opened under the package rules.

// BalancesFileName is the balances file's name inside a dataset folder.
const BalancesFileName = "balances.bin"

// SaveBalances writes all parts, in order, to path, following the package
// rules for Save.
func SaveBalances(ctx context.Context, path string, progress func(saved int), parts ...[]evm.Wei) error {
	return SaveBalancesSeq(ctx, path, progress, Parts(parts...))
}

// SaveBalancesSeq is SaveBalances for balances produced one at a time.
func SaveBalancesSeq(ctx context.Context, path string, progress func(saved int), balances iter.Seq[*evm.Wei]) error {
	return Write(ctx, path, progress, balances, func(w io.Writer) (RecordWriter[evm.Wei], error) {
		return weiWriter{w}, nil
	})
}

type weiWriter struct{ w io.Writer }

func (w weiWriter) Write(_ int, v *evm.Wei) error { _, err := w.w.Write(v[:]); return err }
func (w weiWriter) Close() error                  { return nil }

// BalanceFile is a loaded balances file: a view of the file mapped into
// memory, so loading reads, copies and parses nothing.
type BalanceFile struct {
	Balances []evm.Wei // valid until Close
	data     []byte
}

// OpenBalances maps a balances file into memory. Errors follow the package
// rules for Load: a missing file wraps fs.ErrNotExist, and a file whose size is
// not a whole number of balances is ErrInvalidFile.
func OpenBalances(path string) (*BalanceFile, error) {
	data, err := MapFile(path, evm.WeiLen)
	if err != nil {
		return nil, err
	}
	if data == nil {
		return &BalanceFile{}, nil // an empty file
	}
	// Wei is [16]byte: no padding and no alignment requirement, so the n-th
	// 16 bytes of the file are exactly the n-th balance.
	bals := unsafe.Slice((*evm.Wei)(unsafe.Pointer(&data[0])), len(data)/evm.WeiLen)
	return &BalanceFile{Balances: bals, data: data}, nil
}

// Close unmaps the file. Balances must not be used afterwards.
func (f *BalanceFile) Close() error {
	if f.data == nil {
		f.Balances = nil
		return nil
	}
	err := syscall.Munmap(f.data)
	f.Balances, f.data = nil, nil
	return err
}
