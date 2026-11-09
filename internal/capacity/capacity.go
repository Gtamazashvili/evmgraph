//go:build unix

// Package capacity checks that this computer, or the container the program
// runs in, has the memory and disk space a dataset needs: before generating,
// and again for the disk just before saving.
package capacity

import (
	"errors"
	"fmt"
	"io/fs"
	"math"
	"math/bits"
	"path/filepath"
	"syscall"
)

const gb = 1_000_000_000

// Disk space a save must leave free, so the program never fills the disk:
// the operating system needs room for swap (generating keeps the dataset in
// memory, which can push other memory to disk), logs and updates, and other
// programs fail in odd ways on a full disk. The reserve is a share of the
// disk, like the 5% ext4 keeps back for the system, capped so a large disk is
// not held back by a percentage meant for small ones.
const (
	reserveShare = 20     // 1/20 = 5% of the disk's size
	reserveMax   = 2 * gb // but never more than 2 GB
)

// disk is the space of the file system that holds the cache folder.
type disk struct {
	free uint64 // bytes this user can still write
	size uint64 // bytes the file system holds in all
}

// reserve is how much of d a save must leave free.
func (d disk) reserve() uint64 { return min(d.size/reserveShare, reserveMax) }

// usable is how much of d a save may fill.
func (d disk) usable() uint64 { return d.free - min(d.free, d.reserve()) }

// Need is what a run holds in memory while it generates, and writes to disk
// when it saves.
type Need struct {
	New       int // addresses to generate
	NewBytes  int // bytes each new address holds in memory at the peak: its row and its share of the graph
	Total     int // addresses in the saved dataset
	DiskBytes int // bytes each saved address takes on disk, all its files together
}

// Check reports an error when this computer cannot hold what need describes:
// the generated rows and the graph are in memory until the save, and then the
// whole dataset is written to disk, so both must fit. It runs before generating, so an impossible -n fails with a clear
// message instead of crashing or filling the disk. In a container with a
// memory limit, the limit is the memory: past it the container is killed.
func Check(dir string, need Need) error {
	d, err := diskOf(dir)
	if err != nil {
		return err
	}
	return check(need, available(), d)
}

// CheckDisk reports an error when a file of total addresses, at most
// bytesPerAddress each, no longer fits in dir. The program calls it again just
// before saving, because the disk may have filled up while it was running.
func CheckDisk(dir string, total, bytesPerAddress int) error {
	d, err := diskOf(dir)
	if err != nil {
		return err
	}
	return checkDisk(total, bytesPerAddress, d)
}

// Memory returns the memory this program may use in bytes, or 0 if unknown:
// the computer's physical memory, or its container's memory limit when that
// is smaller.
func Memory() uint64 { return available().size }

// available is the memory this program may use, and whether that is a
// container's limit.
func available() memory {
	limit, limited := memoryLimit()
	return usableMemory(totalMemory(), limit, limited)
}

// Space returns the free and total bytes of the file system that holds dir.
// When dir does not exist yet, it asks about the nearest folder above it that
// does, which is where dir will be created.
func Space(dir string) (free, size uint64, err error) {
	for {
		d, err := diskOf(dir)
		if err == nil {
			return d.free, d.size, nil
		}
		parent := filepath.Dir(dir)
		if !errors.Is(err, fs.ErrNotExist) || parent == dir {
			return 0, 0, err
		}
		dir = parent
	}
}

// FormatBytes formats n with decimal units, such as "20.0 GB".
func FormatBytes(n uint64) string { return formatBytes(n) }

func diskOf(dir string) (disk, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return disk{}, err
	}
	return disk{
		free: uint64(st.Bavail) * uint64(st.Bsize),
		size: uint64(st.Blocks) * uint64(st.Bsize),
	}, nil
}

// check is Check without the system calls, so it can be tested
// with any memory and disk sizes. A memory of 0 means unknown and is not checked.
func check(need Need, mem memory, d disk) error {
	inMemory, ok := memoryOf(need)
	if !ok {
		return fmt.Errorf("cannot generate %d addresses: they would need more memory than any computer has", need.New)
	}
	if mem.size > 0 && inMemory > mem.forData() {
		// Inside a container the computer may have plenty; say which limit
		// it is, or the message would look wrong to whoever reads it.
		if mem.container {
			return fmt.Errorf("cannot generate %d addresses in this container: they need %s of memory, and %s can be used "+
				"(the container's memory limit is %s, and %s is kept for the program itself); raise the limit or use a smaller -n",
				need.New, formatBytes(inMemory), formatBytes(mem.forData()), formatBytes(mem.size), formatBytes(mem.reserve()))
		}
		return fmt.Errorf("cannot generate %d addresses on this computer: they need %s of memory, and it has %s",
			need.New, formatBytes(inMemory), formatBytes(mem.size))
	}
	return checkDisk(need.Total, need.DiskBytes, d)
}

// memoryOf is New × NewBytes, and false if that does not fit in 64 bits: a
// wrapped size is small and would pass any check.
func memoryOf(need Need) (uint64, bool) {
	hi, n := bits.Mul64(uint64(need.New), uint64(need.NewBytes))
	return n, hi == 0
}

// checkDisk is CheckDisk without the system call.
func checkDisk(total, bytesPerAddress int, d disk) error {
	// total × bytesPerAddress must not wrap around: a wrapped size is small
	// and would pass any check.
	if bytesPerAddress > 0 && uint64(total) > math.MaxUint64/uint64(bytesPerAddress) {
		return fmt.Errorf("cannot save %d addresses: the file would be larger than any disk", total)
	}
	need := uint64(total) * uint64(bytesPerAddress)
	if need > d.usable() {
		return fmt.Errorf("cannot save %d addresses on this computer: the file needs %s of disk space, and %s can be used (%s is free, and %s is kept free for the system)",
			total, formatBytes(need), formatBytes(d.usable()), formatBytes(d.free), formatBytes(d.reserve()))
	}
	return nil
}

// formatBytes formats n with decimal units, such as "20.0 GB".
func formatBytes(n uint64) string {
	const unit = 1000
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := uint64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "kMGTPE"[exp])
}
