//go:build unix && !darwin && !linux

package capacity

// totalMemory returns 0 (unknown) on systems other than macOS and Linux, so
// the memory check is skipped there.
func totalMemory() uint64 { return 0 }

// memoryLimit returns false: cgroups, and with them container memory limits,
// exist only on Linux.
func memoryLimit() (uint64, bool) { return 0, false }

// PeakMemory returns 0 (unknown): the unit of the peak resident set size
// differs between the other systems.
func PeakMemory() uint64 { return 0 }
