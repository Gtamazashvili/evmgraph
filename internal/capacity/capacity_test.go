package capacity

import (
	"io/fs"
	"math"
	"strings"
	"testing"

	"evmgraph/internal/evm"
)

func TestCheck(t *testing.T) {
	big := disk{free: 100 * gb, size: 1000 * gb} // reserve: 2 GB (the cap)
	cases := []struct {
		name          string
		added, total  int
		memory        uint64
		disk          disk
		wantSubstring string // empty: no error
	}{
		{"fits", 1_000_000, 1_000_000, 16 * gb, big, ""},
		{"too much memory", 1e15, 1e15, 16 * gb, disk{1e18, 1e19}, "need 20.0 PB of memory, and it has 16.0 GB"},
		{"too little disk", 1_000_000_000, 1_000_000_000, 64 * gb, disk{10 * gb, 1000 * gb},
			"needs 20.0 GB of disk space, and 8.0 GB can be used (10.0 GB is free, and 2.0 GB is kept free for the system)"},
		{"top-up counts only new addresses in memory", 100_000_000, 900_000_000, 4 * gb, big, ""},
		{"unknown memory is not checked", 1e12, 1e12, 0, disk{1e18, 1e19}, ""},
		{"beyond any computer", 1e18, 1e18, 0, disk{1e19, 1e19}, "more memory than any computer has"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			need := Need{New: c.added, NewBytes: evm.AddressLen, Total: c.total, DiskBytes: evm.AddressLen}
			expect(t, check(need, memory{size: c.memory}, c.disk), c.wantSubstring)
		})
	}
}

// A save never fills the disk: it leaves 5% of the disk free, at most 2 GB.
func TestCheckDiskKeepsReserve(t *testing.T) {
	cases := []struct {
		name          string
		total         int
		disk          disk
		wantSubstring string
	}{
		// 500M addresses (10 GB) on a 494 GB disk with 12.9 GB free, the
		// project owner's machine on 8 November 2026: 10.9 GB can be used.
		{"fits above the reserve", 500_000_000, disk{12_900_000_000, 494 * gb}, ""},
		// 600M (12 GB) would leave less than 2 GB: refused, although it fits.
		{"would eat into the reserve", 600_000_000, disk{12_900_000_000, 494 * gb},
			"needs 12.0 GB of disk space, and 10.9 GB can be used (12.9 GB is free, and 2.0 GB is kept free for the system)"},
		// A 10 GB volume (a small Docker volume) keeps 5% = 0.5 GB.
		{"small disk keeps 5%", 220_000_000, disk{5 * gb, 10 * gb}, ""},
		{"small disk refuses past 5%", 230_000_000, disk{5 * gb, 10 * gb}, "4.5 GB can be used"},
		{"free space already below the reserve", 1, disk{1 * gb, 1000 * gb}, "0 B can be used"},
		{"exactly the usable space", 400_000_000, disk{10 * gb, 1000 * gb}, ""},
		{"one byte over", 400_000_001, disk{10 * gb, 1000 * gb}, "can be used"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			expect(t, checkDisk(c.total, evm.AddressLen, c.disk), c.wantSubstring)
		})
	}
}

// total × bytes per address must not wrap around to a small number that
// passes the check. With 62 bytes per address (CSV), this total wraps to 0.
func TestCheckDiskDoesNotOverflow(t *testing.T) {
	total := int(math.MaxUint64/62 + 1)
	if uint64(total)*62 > 1000 {
		t.Fatalf("test setup: %d × 62 should wrap to a tiny number", total)
	}
	expect(t, checkDisk(total, 62, disk{1e19, 1e19}), "larger than any disk")
	// The whole check, with memory unknown so that only the disk can stop it.
	expect(t, check(Need{New: total, NewBytes: 20, Total: total, DiskBytes: 62}, memory{}, disk{1e19, 1e19}), "larger than any disk")
}

func TestCheckDiskOnThisMachine(t *testing.T) {
	dir := t.TempDir()
	if err := CheckDisk(dir, 1, evm.AddressLen); err != nil {
		t.Fatalf("one address does not fit: %v", err)
	}
	expect(t, CheckDisk(dir, 1e17, evm.AddressLen), "can be used")
}

func expect(t *testing.T, err error, wantSubstring string) {
	t.Helper()
	switch {
	case wantSubstring == "" && err != nil:
		t.Fatalf("unexpected error: %v", err)
	case wantSubstring != "" && (err == nil || !strings.Contains(err.Error(), wantSubstring)):
		t.Fatalf("got %v, want an error containing %q", err, wantSubstring)
	}
}

func TestTotalMemoryIsKnown(t *testing.T) {
	if m := totalMemory(); m < 512_000_000 {
		t.Fatalf("totalMemory() = %d, expected at least 512 MB on a real machine", m)
	}
}

func TestFormatBytes(t *testing.T) {
	for n, want := range map[uint64]string{
		999:            "999 B",
		1000:           "1.0 kB",
		20_000_000:     "20.0 MB",
		17_179_869_184: "17.2 GB",
		2e16:           "20.0 PB",
	} {
		if got := formatBytes(n); got != want {
			t.Errorf("formatBytes(%d) = %q, want %q", n, got, want)
		}
	}
}

// Space answers for a folder that does not exist yet from the nearest folder
// above it, where it will be created.
func TestSpaceOfMissingFolder(t *testing.T) {
	dir := t.TempDir()
	free, size, err := Space(dir + "/not/made/yet")
	if err != nil || free == 0 || size < free {
		t.Fatalf("Space = %d free of %d, %v", free, size, err)
	}
}

func TestPeakMemoryIsKnown(t *testing.T) {
	if m := PeakMemory(); m < 1_000_000 || m > 1_000_000_000_000 {
		t.Fatalf("PeakMemory() = %d bytes, expected between 1 MB and 1 TB for a running test", m)
	}
}

// The memory a run needs is every new address times its bytes at the peak.
func TestCheckCountsEveryColumn(t *testing.T) {
	big := disk{free: 1000 * gb, size: 10000 * gb}
	// 50M new addresses at 92 B (row and graph, degree 10) = 4.6 GB.
	need := Need{New: 50_000_000, NewBytes: 92, Total: 50_000_000, DiskBytes: 80}
	expect(t, check(need, memory{size: 5 * gb}, big), "")
	expect(t, check(need, memory{size: 4 * gb}, big), "need 4.6 GB of memory, and it has 4.0 GB")
	// A memory size that wraps around 64 bits is refused, not passed.
	expect(t, check(Need{New: math.MaxInt64, NewBytes: 36}, memory{}, big), "more memory than any computer has")
}

// In a container the limit is the memory, less what is kept for the program
// itself, and the message says so: the default 20M addresses (92 bytes each
// while the graph is built) under docker run -m 512m.
func TestCheckInContainer(t *testing.T) {
	big := disk{free: 100 * gb, size: 1000 * gb}
	need := func(n int) Need { return Need{New: n, NewBytes: 92, Total: n, DiskBytes: 76} }
	limit := usableMemory(16*gb, 512<<20, true) // 536.9 MB, of which 472.9 MB for the data
	expect(t, check(need(20_000_000), limit, big),
		"cannot generate 20000000 addresses in this container: they need 1.8 GB of memory, and 472.9 MB can be used "+
			"(the container's memory limit is 536.9 MB, and 64.0 MB is kept for the program itself)")
	// 5.8M addresses (533.6 MB) were killed under this limit; they are refused.
	expect(t, check(need(5_800_000), limit, big), "can be used")
	expect(t, check(need(5_139_000), limit, big), "") // 472.8 MB
	expect(t, check(need(5_140_000), limit, big), "can be used")
	expect(t, check(need(20_000_000), usableMemory(16*gb, 4<<30, true), big), "")
	// The computer's own memory keeps no reserve: past it the system swaps.
	expect(t, check(need(5_800_000), usableMemory(512<<20, 0, false), big), "")
}

// A tenth of a container's limit stays free, at least 64 MB; a limit below
// that leaves nothing for the data.
func TestMemoryReserve(t *testing.T) {
	cases := []struct {
		mem           memory
		reserve, data uint64
	}{
		{memory{size: 16 * gb}, 0, 16 * gb},
		{memory{size: 4 * gb, container: true}, 400_000_000, 3_600_000_000},
		{memory{size: 512 << 20, container: true}, 64_000_000, 472_870_912},
		{memory{size: 100_000_000, container: true}, 64_000_000, 36_000_000},
		{memory{size: 20_000_000, container: true}, 20_000_000, 0},
	}
	for _, c := range cases {
		if r, d := c.mem.reserve(), c.mem.forData(); r != c.reserve || d != c.data {
			t.Errorf("%+v: reserve %d, for data %d; want %d and %d", c.mem, r, d, c.reserve, c.data)
		}
	}
}

// The contents of cgroup memory limit files, as Linux writes them.
func TestParseCgroupLimit(t *testing.T) {
	cases := []struct {
		contents string
		want     uint64 // 0: no limit
	}{
		{"536870912\n", 536870912},                   // v2 or v1: docker run -m 512m
		{"max\n", 0},                                 // v2: no limit
		{"9223372036854771712\n", 0},                 // v1: no limit, 4 kB pages
		{"9223372036854710272\n", 0},                 // v1: no limit, 64 kB pages
		{"4611686018427387903", 4611686018427387903}, // just under 2^62: still a limit
		{"0\n", 0},
		{"", 0},
		{"-1", 0},
		{"512m", 0},
	}
	for _, c := range cases {
		got, ok := parseCgroupLimit(c.contents)
		if got != c.want || ok != (c.want > 0) {
			t.Errorf("parseCgroupLimit(%q) = %d, %v; want %d, %v", c.contents, got, ok, c.want, c.want > 0)
		}
	}
}

// cgroupLimit reads v2's file first and falls back to v1's.
func TestCgroupLimitFiles(t *testing.T) {
	const v2, v1 = "/sys/fs/cgroup/memory.max", "/sys/fs/cgroup/memory/memory.limit_in_bytes"
	cases := []struct {
		name  string
		files map[string]string
		want  uint64 // 0: no limit
	}{
		{"v2 with a limit", map[string]string{v2: "268435456\n"}, 268435456},
		{"v2 without a limit", map[string]string{v2: "max\n"}, 0},
		{"v1 with a limit", map[string]string{v1: "268435456\n"}, 268435456},
		{"v1 without a limit", map[string]string{v1: "9223372036854771712\n"}, 0},
		{"v2 decides when both exist", map[string]string{v2: "max\n", v1: "268435456\n"}, 0},
		{"no cgroup files: not Linux, or no memory controller", map[string]string{}, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			read := func(path string) ([]byte, error) {
				s, ok := c.files[path]
				if !ok {
					return nil, fs.ErrNotExist
				}
				return []byte(s), nil
			}
			got, ok := cgroupLimit(read)
			if got != c.want || ok != (c.want > 0) {
				t.Fatalf("cgroupLimit = %d, %v; want %d, %v", got, ok, c.want, c.want > 0)
			}
		})
	}
}

// The smaller of the computer's memory and the container's limit applies.
func TestUsableMemory(t *testing.T) {
	cases := []struct {
		name            string
		physical, limit uint64
		limited         bool
		want            memory
	}{
		{"no container", 16 * gb, 0, false, memory{size: 16 * gb}},
		{"a limit below the memory", 16 * gb, 512 << 20, true, memory{size: 512 << 20, container: true}},
		{"a limit above the memory never binds", 16 * gb, 64 * gb, true, memory{size: 16 * gb}},
		{"memory unknown: the limit is all there is", 0, 512 << 20, true, memory{size: 512 << 20, container: true}},
		{"both unknown: not checked", 0, 0, false, memory{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := usableMemory(c.physical, c.limit, c.limited); got != c.want {
				t.Fatalf("usableMemory = %+v, want %+v", got, c.want)
			}
		})
	}
}

// On this machine the memory the program may use is known and is never more
// than the computer has. Run inside a container with a limit (docker run -m),
// it is the limit.
func TestAvailableMemory(t *testing.T) {
	m := available()
	if m.size == 0 || m.size > totalMemory() {
		t.Fatalf("available() = %+v; the computer has %d bytes", m, totalMemory())
	}
	t.Logf("available: %s (container limit: %v); the computer has %s", formatBytes(m.size), m.container, formatBytes(totalMemory()))
}
