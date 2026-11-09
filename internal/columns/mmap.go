//go:build unix

package columns

import (
	"fmt"
	"os"
	"syscall"
)

// MapFile maps a file of fixed-size records read-only into memory. A file
// that is not a regular file, or whose size is not a whole number of records,
// is ErrInvalidFile. An empty file returns nil, because it cannot be mapped.
// The caller unmaps the result with syscall.Munmap.
func MapFile(path string, recordLen int) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	// The mapping stays valid after the file is closed.
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: %s is not a regular file", ErrInvalidFile, path)
	}
	size := info.Size()
	if size%int64(recordLen) != 0 {
		return nil, fmt.Errorf("%w: %s is %d bytes, not a multiple of %d", ErrInvalidFile, path, size, recordLen)
	}
	if size == 0 {
		return nil, nil
	}
	data, err := syscall.Mmap(int(f.Fd()), 0, int(size), syscall.PROT_READ, syscall.MAP_SHARED)
	if err != nil {
		return nil, fmt.Errorf("%s: mmap: %w", path, err)
	}
	return data, nil
}

// unmap releases a mapping MapFile returned; nil is a no-op.
func unmap(data []byte) error {
	if data == nil {
		return nil
	}
	return syscall.Munmap(data)
}
