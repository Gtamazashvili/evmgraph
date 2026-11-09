package capacity

import (
	"encoding/binary"
	"syscall"
)

// totalMemory returns the computer's physical memory in bytes, or 0 if it
// cannot be read.
func totalMemory() uint64 {
	s, err := syscall.Sysctl("hw.memsize")
	if err != nil {
		return 0
	}
	// hw.memsize is a little-endian uint64. syscall.Sysctl drops one trailing
	// zero byte, so pad the value back to 8 bytes with zeros.
	var buf [8]byte
	copy(buf[:], s)
	return binary.LittleEndian.Uint64(buf[:])
}

// memoryLimit returns false: macOS has no cgroups, so no container limits a
// program running on it. Docker on a Mac runs Linux containers in a virtual
// machine, where memory_linux.go applies.
func memoryLimit() (uint64, bool) { return 0, false }

// PeakMemory returns the most memory this process has held at once, its peak
// resident set size, in bytes, or 0 if it cannot be read. macOS reports it in
// bytes.
func PeakMemory() uint64 {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return 0
	}
	return uint64(ru.Maxrss)
}
