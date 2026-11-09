package columns

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

func saveAddresses(t *testing.T, ctx context.Context, path string, progress func(int), parts ...[]evm.Address) error {
	t.Helper()
	return SaveAddresses(ctx, path, progress, Parts(parts...))
}

// Parts are saved in order as one file of 20 bytes per address, progress
// counts up every Batch and ends at the total, and the file opens mapped,
// with exactly the addresses saved.
func TestAddressesRoundTrip(t *testing.T) {
	all := testAddresses(2*Batch+7, 1)
	path := filepath.Join(t.TempDir(), "cache", AddressesFileName)
	var progress []int
	if err := saveAddresses(t, context.Background(), path, func(n int) { progress = append(progress, n) }, all[:Batch+3], all[Batch+3:]); err != nil {
		t.Fatal(err)
	}
	if want := []int{Batch, 2 * Batch, len(all)}; !slices.Equal(progress, want) {
		t.Fatalf("progress %v, want %v", progress, want)
	}
	if info, _ := os.Stat(path); info.Size() != int64(len(all)*evm.AddressLen) {
		t.Fatalf("file is %d bytes, want %d", info.Size(), len(all)*evm.AddressLen)
	}
	f, err := OpenAddresses(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if !f.Mapped() || !slices.Equal(f.Addrs, all) {
		t.Fatal("the opened file is not the saved addresses, mapped")
	}
	noTempFiles(t, path)
}

// An empty dataset saves and opens as zero addresses; a size that is not
// whole addresses, or a directory in place of the file, is ErrInvalidFile;
// a missing file is fs.ErrNotExist.
func TestAddressesEdgeCases(t *testing.T) {
	dir := t.TempDir()
	empty := filepath.Join(dir, "empty.bin")
	if err := saveAddresses(t, context.Background(), empty, nil); err != nil {
		t.Fatal(err)
	}
	if f, err := OpenAddresses(empty); err != nil || len(f.Addrs) != 0 {
		t.Fatalf("empty file: %v", err)
	}
	partial := filepath.Join(dir, "partial.bin")
	if err := os.WriteFile(partial, make([]byte, 2*evm.AddressLen+7), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenAddresses(partial); !errors.Is(err, ErrInvalidFile) {
		t.Fatalf("partial address: got %v, want ErrInvalidFile", err)
	}
	if _, err := OpenAddresses(dir); !errors.Is(err, ErrInvalidFile) {
		t.Fatalf("directory: got %v, want ErrInvalidFile", err)
	}
	if _, err := OpenAddresses(filepath.Join(dir, "missing.bin")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing file: got %v, want fs.ErrNotExist", err)
	}
}

// A cancelled write removes its temporary file and keeps the previous
// complete file; a cancelled first write leaves nothing at all.
func TestCancelledWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), AddressesFileName)
	old := testAddresses(100, 1)
	if err := saveAddresses(t, context.Background(), path, nil, old); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := saveAddresses(t, ctx, path, nil, testAddresses(Batch+1, 2)); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context.Canceled", err)
	}
	noTempFiles(t, path)
	f, err := OpenAddresses(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if !slices.Equal(f.Addrs, old) {
		t.Fatal("the previous file was changed")
	}

	first := filepath.Join(t.TempDir(), "new", AddressesFileName)
	if err := saveAddresses(t, ctx, first, nil, testAddresses(10, 1)); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context.Canceled", err)
	}
	if m, _ := filepath.Glob(filepath.Join(filepath.Dir(first), "*")); len(m) != 0 {
		t.Fatalf("left %v behind", m)
	}
}

// A write never goes through a symlink: one is planted at the old
// predictable temporary name and one in place of the file itself; the files
// they point to stay untouched, and path ends up a regular file.
func TestWriteDoesNotFollowSymlinks(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "cache")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, AddressesFileName)
	victims := map[string]string{path + ".tmp": "victim-tmp", path: "victim-file"}
	for link, name := range victims {
		victim := filepath.Join(root, name)
		if err := os.WriteFile(victim, []byte("someone else's data\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(victim, link); err != nil {
			t.Fatal(err)
		}
	}
	if err := saveAddresses(t, context.Background(), path, nil, testAddresses(1000, 4)); err != nil {
		t.Fatal(err)
	}
	for _, name := range victims {
		if b, _ := os.ReadFile(filepath.Join(root, name)); string(b) != "someone else's data\n" {
			t.Fatalf("%s was overwritten through a symlink", name)
		}
	}
	if info, err := os.Lstat(path); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("%s is not a regular file after the write: %v", path, err)
	}
}

// RemoveStaleTemps removes only what a save can leave behind.
func TestRemoveStaleTemps(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "addresses.bin")
	victim := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(victim, []byte("keep me"), 0o644); err != nil {
		t.Fatal(err)
	}
	// What a killed save leaves, made the same way Write makes it.
	f, err := os.CreateTemp(dir, "addresses.bin.*.tmp")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	stale := []string{filepath.Base(f.Name()), "addresses.bin.tmp", "addresses.bin.42.tmp"}
	for _, name := range stale[1:] {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(victim, filepath.Join(dir, "addresses.bin.7.tmp")); err != nil {
		t.Fatal(err)
	}
	stale = append(stale, "addresses.bin.7.tmp")
	keep := []string{"addresses.bin", "addresses.bin.backup.tmp", "addresses.bin..tmp", "other.bin.1.tmp", "notes.txt"}
	for _, name := range keep {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "addresses.bin.9.tmp"), 0o755); err != nil {
		t.Fatal(err)
	}
	keep = append(keep, "addresses.bin.9.tmp") // a directory is never ours

	if err := RemoveStaleTemps(path); err != nil {
		t.Fatal(err)
	}
	for _, name := range stale {
		if _, err := os.Lstat(filepath.Join(dir, name)); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s was not removed", name)
		}
	}
	for _, name := range keep {
		if _, err := os.Lstat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s was removed", name)
		}
	}
	if b, _ := os.ReadFile(victim); string(b) != "keep me" {
		t.Fatal("the file behind a stale symlink was changed")
	}
}
