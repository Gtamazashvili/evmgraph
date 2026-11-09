package formats

// Contract tests: every rule in the package documentation, checked for every
// implementation in All with the same input. A new format only has to be
// added to All to be held to the same rules.

import (
	"context"
	"errors"
	"io/fs"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"evmgraph/internal/columns"
	"evmgraph/internal/evm"
)

// testAddresses returns n random addresses; the same seed gives the same ones.
func testAddresses(n int, seed uint64) []evm.Address {
	rng := rand.NewChaCha8([32]byte{byte(seed), byte(seed >> 8)})
	addrs := make([]evm.Address, n)
	for i := range addrs {
		rng.Read(addrs[i][:])
	}
	return addrs
}

// forEach runs f as a subtest for every implementation.
func forEach(t *testing.T, f func(t *testing.T, c Format, path string)) {
	for _, c := range All {
		t.Run(c.Name(), func(t *testing.T) {
			f(t, c, filepath.Join(t.TempDir(), "cache", c.FileName()))
		})
	}
}

func TestNamesAndFilesAreUnique(t *testing.T) {
	names, files := map[string]bool{}, map[string]bool{}
	for _, c := range All {
		if names[c.Name()] || files[c.FileName()] {
			t.Fatalf("%s: duplicate name or file name", c.Name())
		}
		names[c.Name()], files[c.FileName()] = true, true
	}
}

// Only binary, the program's format, opens as a mapped view rather than a copy.
func TestOnlyBinaryIsMapped(t *testing.T) {
	addrs := testAddresses(100, 1)
	for _, c := range All {
		path := filepath.Join(t.TempDir(), c.FileName())
		if err := c.Save(context.Background(), path, nil, addrs); err != nil {
			t.Fatal(err)
		}
		f, err := c.Load(path)
		if err != nil {
			t.Fatal(err)
		}
		if want := c.Name() == "binary"; f.Mapped() != want {
			t.Errorf("%s: Mapped() = %v, want %v", c.Name(), f.Mapped(), want)
		}
		f.Close()
	}
}

// Rules: parts are saved in order as one dataset, progress counts up every
// columns.Batch addresses and ends at the total, the file fits MaxBytesPerAddress, and
// loading gives back exactly what was saved.
func TestRoundTrip(t *testing.T) {
	all := testAddresses(3*columns.Batch+7, 5)
	forEach(t, func(t *testing.T, c Format, path string) {
		var progress []int
		if err := c.Save(context.Background(), path, func(n int) { progress = append(progress, n) }, all[:1000], all[1000:]); err != nil {
			t.Fatal(err)
		}
		if want := []int{columns.Batch, 2 * columns.Batch, 3 * columns.Batch, len(all)}; !slices.Equal(progress, want) {
			t.Fatalf("progress %v, want %v", progress, want)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if limit := int64(len(all) * c.MaxBytesPerAddress()); info.Size() > limit {
			t.Fatalf("file is %d bytes, more than MaxBytesPerAddress allows (%d)", info.Size(), limit)
		}
		noTempFiles(t, path)
		f, err := c.Load(path)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if !slices.Equal(f.Addrs, all) {
			t.Fatal("loaded addresses differ from saved ones")
		}
	})
}

// Rule: identical input gives identical output, whatever the format.
func TestAllFormatsLoadTheSameAddresses(t *testing.T) {
	want := testAddresses(10_000, 8)
	for _, c := range All {
		path := filepath.Join(t.TempDir(), c.FileName())
		if err := c.Save(context.Background(), path, nil, want); err != nil {
			t.Fatal(err)
		}
		f, err := c.Load(path)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(f.Addrs, want) {
			t.Errorf("%s loads different addresses than were saved", c.Name())
		}
		f.Close()
	}
}

// Rule: an empty dataset saves and loads as zero addresses.
func TestEmptyDataset(t *testing.T) {
	forEach(t, func(t *testing.T, c Format, path string) {
		var progress []int
		if err := c.Save(context.Background(), path, func(n int) { progress = append(progress, n) }); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(progress, []int{0}) {
			t.Fatalf("progress %v, want [0]", progress)
		}
		f, err := c.Load(path)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if len(f.Addrs) != 0 {
			t.Fatalf("got %d addresses", len(f.Addrs))
		}
	})
}

// Rule: a missing file is fs.ErrNotExist.
func TestLoadMissing(t *testing.T) {
	forEach(t, func(t *testing.T, c Format, path string) {
		if _, err := c.Load(path); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("got %v, want fs.ErrNotExist", err)
		}
	})
}

// Rule: contents that are not addresses in the format are columns.ErrInvalidFile.
func TestLoadGarbage(t *testing.T) {
	forEach(t, func(t *testing.T, c Format, path string) {
		os.MkdirAll(filepath.Dir(path), 0o755)
		if err := os.WriteFile(path, []byte("not addresses\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := c.Load(path); !errors.Is(err, columns.ErrInvalidFile) {
			t.Fatalf("got %v, want columns.ErrInvalidFile", err)
		}
	})
}

// Rule: a directory in place of the file is columns.ErrInvalidFile.
func TestLoadDirectory(t *testing.T) {
	forEach(t, func(t *testing.T, c Format, path string) {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := c.Load(path); !errors.Is(err, columns.ErrInvalidFile) {
			t.Fatalf("got %v, want columns.ErrInvalidFile", err)
		}
	})
}

// Rule: an unreadable file is a permission error, not columns.ErrInvalidFile.
func TestLoadUnreadable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read any file")
	}
	forEach(t, func(t *testing.T, c Format, path string) {
		if err := c.Save(context.Background(), path, nil, testAddresses(10, 1)); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0o000); err != nil {
			t.Fatal(err)
		}
		_, err := c.Load(path)
		if !errors.Is(err, fs.ErrPermission) || errors.Is(err, columns.ErrInvalidFile) {
			t.Fatalf("got %v, want a permission error", err)
		}
	})
}

// Rule: a cancelled save removes its temporary file and keeps the previous
// complete file untouched.
func TestCancelledSaveKeepsPreviousFile(t *testing.T) {
	old := testAddresses(100, 1)
	forEach(t, func(t *testing.T, c Format, path string) {
		if err := c.Save(context.Background(), path, nil, old); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := c.Save(ctx, path, nil, testAddresses(columns.Batch+1, 2)); !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v, want context.Canceled", err)
		}
		noTempFiles(t, path)
		f, err := c.Load(path)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if !slices.Equal(f.Addrs, old) {
			t.Fatal("the previous file was changed")
		}
	})
}

// Rule: a cancelled first save leaves nothing at all.
func TestCancelledFirstSaveLeavesNothing(t *testing.T) {
	forEach(t, func(t *testing.T, c Format, path string) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := c.Save(ctx, path, nil, testAddresses(10, 1)); !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v, want context.Canceled", err)
		}
		if m, _ := filepath.Glob(filepath.Join(filepath.Dir(path), "*")); len(m) != 0 {
			t.Fatalf("left %v behind", m)
		}
	})
}

// noTempFiles fails the test if a temporary file is left next to path.
func noTempFiles(t *testing.T, path string) {
	t.Helper()
	entries, _ := os.ReadDir(filepath.Dir(path))
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("the temporary file %s was left behind", e.Name())
		}
	}
}

// Rule: Save never writes through a symlink. One is planted at the old
// predictable temporary name and one in place of the file itself; the files
// they point to must stay untouched, and path must end up a regular file.
func TestSaveDoesNotWriteThroughSymlinks(t *testing.T) {
	forEach(t, func(t *testing.T, c Format, path string) {
		dir := filepath.Dir(path)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		victims := map[string]string{path + ".tmp": "victim-tmp", path: "victim-file"}
		for link, name := range victims {
			victim := filepath.Join(filepath.Dir(dir), name)
			if err := os.WriteFile(victim, []byte("someone else's data\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(victim, link); err != nil {
				t.Fatal(err)
			}
		}
		if err := c.Save(context.Background(), path, nil, testAddresses(1000, 4)); err != nil {
			t.Fatal(err)
		}
		for _, name := range victims {
			if b, _ := os.ReadFile(filepath.Join(filepath.Dir(dir), name)); string(b) != "someone else's data\n" {
				t.Fatalf("%s was overwritten through a symlink", name)
			}
		}
		if info, err := os.Lstat(path); err != nil || !info.Mode().IsRegular() {
			t.Fatalf("%s is not a regular file after the save: %v", path, err)
		}
	})
}

// Rule: SaveSeq writes exactly what Save writes for the same addresses.
func TestSaveSeqEqualsSave(t *testing.T) {
	all := testAddresses(columns.Batch+99, 11)
	forEach(t, func(t *testing.T, c Format, path string) {
		if err := c.Save(context.Background(), path, nil, all[:7], all[7:]); err != nil {
			t.Fatal(err)
		}
		want, _ := os.ReadFile(path)
		var progress []int
		if err := c.SaveSeq(context.Background(), path, func(n int) { progress = append(progress, n) }, columns.Parts(all)); err != nil {
			t.Fatal(err)
		}
		if got, _ := os.ReadFile(path); !slices.Equal(got, want) {
			t.Fatal("SaveSeq wrote a different file than Save")
		}
		if want := []int{columns.Batch, len(all)}; !slices.Equal(progress, want) {
			t.Fatalf("progress %v, want %v", progress, want)
		}
	})
}
