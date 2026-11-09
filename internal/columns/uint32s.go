//go:build unix

package columns

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"syscall"
	"unsafe"
)

// The graph's two files (decision 012) are arrays of uint32 little-endian:
// offsets.bin (one entry per address, plus one) and neighbors.bin (row
// numbers). Like the other files there is no header; the manifest records
// what they hold.

const (
	OffsetsFileName   = "offsets.bin"
	NeighborsFileName = "neighbors.bin"
)

// errBigEndian refuses a big-endian computer: the files are little-endian
// and are viewed in place, without conversion. Every CPU Go builds this
// program for in practice (amd64, arm64) is little-endian.
var errBigEndian = errors.New("this computer is big-endian; the uint32 files are little-endian and viewed in place")

func littleEndian() bool { return binary.NativeEndian.Uint16([]byte{1, 0}) == 1 }

// SaveUint32s writes values to path as little-endian uint32s, following the
// package rules for Save.
func SaveUint32s(ctx context.Context, path string, progress func(saved int), values []uint32) error {
	return Write(ctx, path, progress, Parts(values), func(w io.Writer) (RecordWriter[uint32], error) {
		return &uint32Writer{w: w}, nil
	})
}

type uint32Writer struct {
	w   io.Writer
	buf [4]byte
}

func (w *uint32Writer) Write(_ int, v *uint32) error {
	binary.LittleEndian.PutUint32(w.buf[:], *v)
	_, err := w.w.Write(w.buf[:])
	return err
}
func (w *uint32Writer) Close() error { return nil }

// Uint32File is a file of uint32s mapped into memory, read-only.
type Uint32File struct {
	Values []uint32 // valid until Close
	data   []byte
}

// OpenUint32s maps a file of uint32s read-only. Errors follow the package
// rules for Load: a missing file wraps fs.ErrNotExist, and a size that is not
// a whole number of uint32s is ErrInvalidFile.
func OpenUint32s(path string) (*Uint32File, error) {
	if !littleEndian() {
		return nil, errBigEndian
	}
	data, err := MapFile(path, 4)
	if err != nil {
		return nil, err
	}
	return &Uint32File{Values: view(data), data: data}, nil
}

// Close unmaps the file.
func (u *Uint32File) Close() error {
	var err error
	if u.data != nil {
		err = syscall.Munmap(u.data)
	}
	u.Values, u.data = nil, nil
	return err
}

// view sees mapped bytes as uint32s. The mapping starts on a page boundary,
// so it is aligned for uint32.
func view(data []byte) []uint32 {
	if len(data) == 0 {
		return nil
	}
	return unsafe.Slice((*uint32)(unsafe.Pointer(&data[0])), len(data)/4)
}
