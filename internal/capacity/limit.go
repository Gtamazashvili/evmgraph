package capacity

import (
	"strconv"
	"strings"
)

// memory is how much memory the program may use, and where that figure
// comes from.
type memory struct {
	size      uint64 // bytes; 0 means unknown, and then nothing is checked
	container bool   // size is a container's memory limit, below the computer's memory
}

// Memory a container's limit keeps for the program itself, besides what the
// dataset needs (Need). Past the physical memory the system swaps and the run
// slows down; past a container's limit the program is killed, so this room
// must really be there. Measured on 9 November 2026 (Docker Desktop, cgroup
// v2): the Go runtime, the code and the stacks took about 14 MB more than
// Need at 1M, 4M and 5.25M addresses; and the files being written sit in the
// page cache, which counts against the limit and which that kernel did not
// write back on its own before the limit was reached (at one kill, all
// 316 MB of the container's file pages were dirty). The room covers a page
// cache of a few tens of MB, so the save must sync its files as it writes
// them, not only at the end; until it does, TestRunsCloseToTheMemoryLimit in
// tests/docker fails. With a save that synced every 32 MB, a 32 MB minimum
// was not enough under 256 MiB (2.55M addresses were killed) and 64 MB was
// (2.2M and 2.4M saved).
const (
	limitShare = 10         // 1/10 of the limit
	limitMin   = 64_000_000 // but at least 64 MB
)

// reserve is how much of m stays free for the program itself: nothing for a
// computer's physical memory, part of a container's limit.
func (m memory) reserve() uint64 {
	if !m.container {
		return 0
	}
	return min(m.size, max(m.size/limitShare, limitMin))
}

// forData is how much of m the dataset may fill.
func (m memory) forData() uint64 { return m.size - m.reserve() }

// cgroupLimitFiles hold the memory limit of the cgroup the program runs in.
// Docker, Podman and Kubernetes put each container in its own cgroup and
// show it to the container at /sys/fs/cgroup. cgroup v2 (current Linux and
// Docker Desktop) keeps the limit in memory.max; v1 kept it in
// memory/memory.limit_in_bytes.
var cgroupLimitFiles = []string{
	"/sys/fs/cgroup/memory.max",
	"/sys/fs/cgroup/memory/memory.limit_in_bytes",
}

// noLimit: cgroup v1 has no word for "no limit" and stores the largest 64-bit
// number rounded down to a whole page instead (9223372036854771712 with 4 kB
// pages). Anything from 2^62 bytes (4.6 EB) up is taken as no limit.
const noLimit = 1 << 62

// cgroupLimit returns the limit in the first of cgroupLimitFiles that read
// can open, and false when there is none: no such file (not Linux, or no
// memory controller), or a file that says there is no limit. read is a
// parameter so tests can give it any file contents.
func cgroupLimit(read func(path string) ([]byte, error)) (uint64, bool) {
	for _, path := range cgroupLimitFiles {
		b, err := read(path)
		if err != nil {
			continue
		}
		return parseCgroupLimit(string(b))
	}
	return 0, false
}

// parseCgroupLimit reads the contents of a cgroup memory limit file: a number
// of bytes, or "max" (v2) for no limit. A limit of 0 is taken as none too: no
// program could have started under it.
func parseCgroupLimit(s string) (uint64, bool) {
	n, err := strconv.ParseUint(strings.TrimSpace(s), 10, 64)
	if err != nil || n == 0 || n >= noLimit {
		return 0, false
	}
	return n, true
}

// usableMemory is the memory the program may use: the computer's physical
// memory, or the container's limit when that is smaller. A limit above the
// physical memory never binds, because the computer runs out first. A
// physical memory of 0 is unknown, and then the limit is all there is.
func usableMemory(physical, limit uint64, limited bool) memory {
	if limited && (physical == 0 || limit < physical) {
		return memory{size: limit, container: true}
	}
	return memory{size: physical}
}
