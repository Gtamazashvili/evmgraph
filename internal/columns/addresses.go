//go:build unix

package columns

import (
	"context"
	"iter"
	"unsafe"

	"evmgraph/internal/evm"
)

// Addresses are stored as their raw 20 bytes, one after another, with no
// header, separators or text encoding; the number of addresses is the file
// size divided by 20 (decision 002).

// AddressesFileName is the addresses file's name inside a dataset folder.
const AddressesFileName = "addresses.bin"

// SaveAddresses writes addrs to path, following the package rules.
func SaveAddresses(ctx context.Context, path string, progress func(saved int), addrs iter.Seq[*evm.Address]) error {
	return writeRaw(ctx, path, progress, addrs, func(a *evm.Address) []byte { return a[:] })
}

// AddressFile is an opened addresses file. Addrs is valid until Close.
type AddressFile struct {
	Addrs []evm.Address
	data  []byte // the mapping; nil when Addrs is memory the program owns
}

// Mapped reports whether Addrs is a view of a mapped file rather than memory
// owned by the program. Only mapped addresses cost no heap memory.
func (f *AddressFile) Mapped() bool { return f.data != nil }

// OpenAddresses maps an addresses file into memory. Addrs is a view of the
// mapped bytes: opening copies and parses nothing, and the operating system
// reads a page of the file from disk only when an address on it is first
// used.
func OpenAddresses(path string) (*AddressFile, error) {
	data, err := MapFile(path, evm.AddressLen)
	if err != nil {
		return nil, err
	}
	if data == nil {
		return &AddressFile{}, nil // an empty file
	}
	// Address is [20]byte, so it has no alignment requirement and no padding:
	// the n-th 20 bytes of the file are exactly the n-th Address.
	addrs := unsafe.Slice((*evm.Address)(unsafe.Pointer(&data[0])), len(data)/evm.AddressLen)
	return &AddressFile{Addrs: addrs, data: data}, nil
}

// Close unmaps the file. Addrs must not be used afterwards.
func (f *AddressFile) Close() error {
	err := unmap(f.data)
	f.Addrs, f.data = nil, nil
	return err
}
