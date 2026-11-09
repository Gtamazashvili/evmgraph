package capacity

import (
	"os"
	"syscall"
)

// totalMemory returns the computer's physical memory in bytes, or 0 if it
// cannot be read. Inside a container this is still the whole computer's (or
// the Docker Desktop virtual machine's) memory; memoryLimit is the container's.
func totalMemory() uint64 {
	var info syscall.Sysinfo_t
	if err := syscall.Sysinfo(&info); err != nil {
		return 0
	}
	return uint64(info.Totalram) * uint64(info.Unit)
}

// memoryLimit returns the memory limit of the container the program runs in
// (docker run -m), and false when it runs in none or the limit is unset.
// Going past it gets the program killed, however much memory the computer has.
func memoryLimit() (uint64, bool) { return cgroupLimit(os.ReadFile) }

// PeakMemory returns the most memory this process has held at once, its peak
// resident set size, in bytes, or 0 if it cannot be read. Linux reports it in
// kilobytes.
func PeakMemory() uint64 {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return 0
	}
	return uint64(ru.Maxrss) * 1024
}
