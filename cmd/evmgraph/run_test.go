package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"evmgraph/internal/columns"
	"evmgraph/internal/evm"
	"evmgraph/internal/generate"
	"evmgraph/internal/graph"
	"evmgraph/internal/lookup"
	"evmgraph/internal/order"
	"evmgraph/internal/store"
)

// shutdownSoon returns a context that ends as soon as run starts waiting for
// a signal, standing in for a Ctrl+C that arrives after run has loaded or
// generated its data. It does not depend on timing, so a slow machine cannot
// make the "signal" arrive before generation; the 30-second timeout only
// stops a regression from hanging the tests.
func shutdownSoon(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	whenWaiting = cancel
	t.Cleanup(func() { cancel(); whenWaiting = nil })
	return ctx
}

// current returns the cache's manifest and the paths of its two column files.
func current(t *testing.T, dir string) (m store.Manifest, addrPath, balancePath string) {
	t.Helper()
	b, err := os.ReadFile(store.ManifestPath(dir))
	if err != nil {
		t.Fatalf("no manifest in %s: %v", dir, err)
	}
	if m, err = store.ParseManifest(string(b)); err != nil {
		t.Fatal(err)
	}
	folder := filepath.Join(dir, m.Folder)
	return m, filepath.Join(folder, columns.AddressesFileName), filepath.Join(folder, columns.BalancesFileName)
}

// writeManifest makes m the cache's manifest, as a crashed save or an older
// version of the program would have left it.
func writeManifest(t *testing.T, dir string, m store.Manifest) {
	t.Helper()
	must(t, os.WriteFile(store.ManifestPath(dir), []byte(m.String()), 0o644))
}

// openSaved opens the cache's saved dataset, closed when the test ends.
func openSaved(t *testing.T, dir string) *store.Dataset {
	t.Helper()
	d, err := store.OpenReadOnly(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)
	return d
}

// sortedRows returns the first n positions of seed's dataset sorted by
// address, with their positions: what a saved dataset of n holds.
func sortedRows(t testing.TB, n int, seed uint64) []order.Row {
	t.Helper()
	rows, err := generate.Rows(context.Background(), seed, 0, n, nil)
	if err != nil {
		t.Fatal(err)
	}
	order.SortRows(rows)
	return rows
}

// sortedDataset is sortedRows as two columns.
func sortedDataset(t testing.TB, n int, seed uint64) ([]evm.Address, []evm.Wei) {
	t.Helper()
	rows := sortedRows(t, n, seed)
	addrs, balances := make([]evm.Address, n), make([]evm.Wei, n)
	for i, r := range rows {
		addrs[i], balances[i] = r.Address, r.Balance
	}
	return addrs, balances
}

// mustHold checks that the cache holds exactly the first n positions of
// seed's dataset at degree 10 and skew 3: addresses and balances sorted by
// address, and the graph built from the same links.
func mustHold(t *testing.T, dir string, n int, seed uint64) {
	t.Helper()
	mustHoldGraph(t, dir, n, seed, 10, 3)
}

func mustHoldGraph(t *testing.T, dir string, n int, seed uint64, degree, skew int) {
	t.Helper()
	m, _, _ := current(t, dir)
	if m.Version != store.VersionCurrent || m.Count != n || m.Seed != seed || m.Degree != degree || m.Skew != skew {
		t.Fatalf("manifest %+v, want version %d, %d addresses, seed %d, degree %d, skew %d",
			m, store.VersionCurrent, n, seed, degree, skew)
	}
	d := openSaved(t, dir)
	rows := sortedRows(t, n, seed)
	for i, r := range rows {
		if d.Addresses.Addrs[i] != r.Address || d.Balances.Balances[i] != r.Balance {
			t.Fatalf("row %d is not the sorted dataset of seed %d", i, seed)
		}
	}
	off, nb, err := graph.BuildContext(context.Background(), seed, n, degree, skew, order.RowOf(rows),
		func(e int) ([]uint32, error) { return make([]uint32, e), nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
	if g := d.Graph(); !slices.Equal(g.Offsets, off) || !slices.Equal(g.Neighbors, nb) {
		t.Fatal("the saved graph differs from a fresh build")
	}
}

// datasetFolders lists the numbered folders in dir.
func datasetFolders(dir string) []string {
	m, _ := filepath.Glob(filepath.Join(dir, "dataset-*"))
	return m
}

// newDataset makes a cache folder with a saved dataset of n positions.
func newDataset(t *testing.T, n int, seed uint64) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "cache")
	if err := run(shutdownSoon(t), n, seed, dir, 0); err != nil {
		t.Fatal(err)
	}
	return dir
}

// The generated dataset reaches the disk, in a dataset folder named by the
// manifest, and nothing else is left behind.
func TestRunSavesOnShutdown(t *testing.T) {
	dir := newDataset(t, 1000, 1)
	mustHold(t, dir, 1000, 1)
	entries, _ := os.ReadDir(dir)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if !slices.Equal(names, []string{"dataset-000001", "lock", "manifest"}) {
		t.Fatalf("cache holds %v", names)
	}
	_, addrPath, balancePath := current(t, dir)
	if fileSize(addrPath) != 1000*evm.AddressLen || fileSize(balancePath) != 1000*evm.WeiLen {
		t.Fatalf("files are %d and %d bytes", fileSize(addrPath), fileSize(balancePath))
	}
	folder := filepath.Dir(addrPath)
	if fileSize(filepath.Join(folder, columns.OffsetsFileName)) != 1001*4 || fileSize(filepath.Join(folder, columns.NeighborsFileName)) <= 0 {
		t.Fatal("the graph files are missing or the wrong size")
	}
}

// A shutdown before anything was generated saves nothing.
func TestRunShutdownBeforeGenerating(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cache")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := run(ctx, 1000, 1, dir, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(store.ManifestPath(dir)); !errors.Is(err, fs.ErrNotExist) || len(datasetFolders(dir)) != 0 {
		t.Fatal("something was saved although nothing was generated")
	}
}

// With 3,000 cached and -n 10,000, run adds the 7,000 a fresh run of 10,000
// would have made, balances included, and replaces the old folder.
func TestRunTopsUpCache(t *testing.T) {
	dir := newDataset(t, 3000, 1)
	if err := run(shutdownSoon(t), 10000, 1, dir, 0); err != nil {
		t.Fatal(err)
	}
	mustHold(t, dir, 10000, 1)
	if folders := datasetFolders(dir); len(folders) != 1 || filepath.Base(folders[0]) != "dataset-000002" {
		t.Fatalf("dataset folders after the top-up: %v", folders)
	}
}

// The seed of a dataset is recorded; a later run with another -seed
// continues the recorded dataset instead of mixing in another.
func TestRecordedSeedWins(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cache")
	if err := run(shutdownSoon(t), 1000, 1, dir, 0); err != nil {
		t.Fatal(err)
	}
	if err := run(shutdownSoon(t), 2000, 2, dir, 0); err != nil {
		t.Fatal(err)
	}
	mustHold(t, dir, 2000, 1)
}

// The program must recover, not fail, when the cache is missing or broken, and
// must not destroy anything it finds in its way: what it cannot use is moved
// aside with its contents intact.
func TestRunRecovers(t *testing.T) {
	cases := []struct {
		name string
		// setup breaks the cache folder dir; a valid dataset of 3 is there
		// first when withData is true.
		withData bool
		setup    func(t *testing.T, dir string)
		// moved is where the thing in the way ends up, as a glob relative to
		// the parent of dir, and what must still be inside it.
		moved, keeps string
	}{
		{"missing directory", false, func(*testing.T, string) {}, "", ""},
		{"empty directory", false, func(t *testing.T, dir string) { must(t, os.Mkdir(dir, 0o755)) }, "", ""},
		{"file instead of directory", false, func(t *testing.T, dir string) {
			must(t, os.WriteFile(dir, []byte("not a directory"), 0o644))
		}, "cache.invalid-*", ""},
		{"manifest is not a manifest", true, func(t *testing.T, dir string) {
			must(t, os.WriteFile(store.ManifestPath(dir), []byte("hello\n"), 0o644))
		}, "cache/manifest.invalid-*", ""},
		{"manifest is a directory", true, func(t *testing.T, dir string) {
			must(t, os.Remove(store.ManifestPath(dir)))
			must(t, os.MkdirAll(filepath.Join(store.ManifestPath(dir), "inner"), 0o755))
		}, "cache/manifest.invalid-*", "inner"},
		{"manifest names a missing folder", true, func(t *testing.T, dir string) {
			must(t, os.RemoveAll(filepath.Join(dir, "dataset-000001")))
		}, "cache/manifest.invalid-*", ""},
		{"addresses file of the wrong size", true, func(t *testing.T, dir string) {
			_, addrPath, _ := current(t, dir)
			must(t, os.WriteFile(addrPath, make([]byte, 33), 0o644))
		}, "cache/manifest.invalid-*", ""},
		{"offsets of the wrong length", true, func(t *testing.T, dir string) {
			_, addrPath, _ := current(t, dir)
			must(t, os.Truncate(filepath.Join(filepath.Dir(addrPath), columns.OffsetsFileName), 8))
		}, "cache/manifest.invalid-*", ""},
		{"a neighbour beyond the last row", true, func(t *testing.T, dir string) {
			_, addrPath, _ := current(t, dir)
			path := filepath.Join(filepath.Dir(addrPath), columns.NeighborsFileName)
			b, err := os.ReadFile(path)
			must(t, err)
			b[0], b[1], b[2], b[3] = 0xff, 0xff, 0xff, 0x7f
			must(t, os.WriteFile(path, b, 0o644))
		}, "cache/manifest.invalid-*", ""},
		{"fewer balances than addresses", true, func(t *testing.T, dir string) {
			_, _, balancePath := current(t, dir)
			must(t, os.Truncate(balancePath, 2*evm.WeiLen))
		}, "cache/manifest.invalid-*", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "cache")
			if c.withData {
				if err := run(shutdownSoon(t), 3, 1, dir, 0); err != nil {
					t.Fatal(err)
				}
			}
			c.setup(t, dir)
			if err := run(shutdownSoon(t), 7, 1, dir, 0); err != nil {
				t.Fatalf("run failed: %v", err)
			}
			mustHold(t, dir, 7, 1)
			// Whatever was in the way was moved aside, not deleted; the broken
			// dataset's folder was removed, being the program's own; nothing
			// else is left: cache, its manifest, its lock file and one
			// dataset folder.
			root := filepath.Dir(dir)
			outside, _ := filepath.Glob(filepath.Join(root, "*"))
			inside, _ := filepath.Glob(filepath.Join(dir, "*"))
			want := 4
			if c.moved != "" {
				want++
				moved, _ := filepath.Glob(filepath.Join(root, c.moved))
				if len(moved) != 1 {
					t.Fatalf("expected one %s, found %v", c.moved, moved)
				}
				if c.keeps != "" {
					if _, err := os.Stat(filepath.Join(moved[0], c.keeps)); err != nil {
						t.Fatalf("the moved directory lost its contents: %v", err)
					}
				}
			}
			if got := len(outside) + len(inside); got != want {
				t.Fatalf("expected %d entries, found %v %v", want, outside, inside)
			}
			// A second run with a smaller -n loads the dataset and keeps it.
			if err := run(shutdownSoon(t), 5, 2, dir, 0); err != nil {
				t.Fatal(err)
			}
			mustHold(t, dir, 7, 1)
		})
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// A file that cannot be read is a permission problem, not a broken dataset:
// run reports it and changes nothing.
func TestRunKeepsUnreadableFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read any file")
	}
	dir := newDataset(t, 5, 1)
	_, addrPath, _ := current(t, dir)
	must(t, os.Chmod(addrPath, 0o000))
	t.Cleanup(func() { os.Chmod(addrPath, 0o644) })
	if err := run(shutdownSoon(t), 5, 1, dir, 0); !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("got %v, want a permission error", err)
	}
	if m, _, _ := current(t, dir); m.Count != 5 || fileSize(addrPath) != 5*evm.AddressLen {
		t.Fatal("the dataset was changed")
	}
}

// A save can stop at any moment (kill -9, a power cut). At every point, the
// next start must load a complete dataset, the old or the new one, and clean up
// what the save left.
func TestCrashAtEveryStepOfASave(t *testing.T) {
	// Each case builds the state a save leaves when it dies at that step,
	// starting from a committed dataset of 100 in dataset-000001.
	cases := []struct {
		name  string
		crash func(t *testing.T, dir string)
		want  int // addresses the next start must load
	}{
		{"while writing the new folder", func(t *testing.T, dir string) {
			folder := filepath.Join(dir, "dataset-000002")
			must(t, os.Mkdir(folder, 0o755))
			must(t, os.WriteFile(filepath.Join(folder, "addresses.bin.12345.tmp"), make([]byte, 999), 0o644))
		}, 100},
		{"after the new folder, before the manifest", func(t *testing.T, dir string) {
			other := newDataset(t, 300, 1)
			must(t, os.Rename(filepath.Join(other, "dataset-000001"), filepath.Join(dir, "dataset-000002")))
			must(t, os.WriteFile(filepath.Join(dir, "manifest.777.tmp"), []byte("half a manifest"), 0o644))
		}, 100},
		{"after the manifest, before deleting the old folder", func(t *testing.T, dir string) {
			other := newDataset(t, 300, 1)
			must(t, os.Rename(filepath.Join(other, "dataset-000001"), filepath.Join(dir, "dataset-000002")))
			writeManifest(t, dir, store.Manifest{Version: store.VersionCurrent, Seed: 1, Count: 300,
				Folder: "dataset-000002", Degree: 10, Skew: 3})
		}, 300},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := newDataset(t, 100, 1)
			c.crash(t, dir)
			if err := run(shutdownSoon(t), 1, 1, dir, 0); err != nil {
				t.Fatal(err)
			}
			mustHold(t, dir, c.want, 1)
			if folders := datasetFolders(dir); len(folders) != 1 {
				t.Fatalf("leftover folders: %v", folders)
			}
			if tmp, _ := filepath.Glob(filepath.Join(dir, "*.tmp")); len(tmp) != 0 {
				t.Fatalf("leftover temporary files: %v", tmp)
			}
		})
	}
}

// Files a version before the manifest kept directly in the cache folder, or
// anyone's file with such a name, are left exactly as they are.
func TestRunLeavesOldLayoutAlone(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cache")
	must(t, os.Mkdir(dir, 0o755))
	theirs := map[string][]byte{
		"addresses.bin":      make([]byte, 40),
		"addresses.bin.seed": []byte("1\n"),
		"addresses.csv":      []byte("name,email\nAlice,alice@example.com\n"),
	}
	for name, b := range theirs {
		must(t, os.WriteFile(filepath.Join(dir, name), b, 0o644))
	}
	if err := run(shutdownSoon(t), 10, 1, dir, 0); err != nil {
		t.Fatal(err)
	}
	for name, b := range theirs {
		if got, _ := os.ReadFile(filepath.Join(dir, name)); !slices.Equal(got, b) {
			t.Fatalf("%s was changed", name)
		}
	}
	mustHold(t, dir, 10, 1)
}

// -data pointing at an existing file by mistake must not destroy it.
func TestRunKeepsFileGivenAsDataFolder(t *testing.T) {
	notes := filepath.Join(t.TempDir(), "notes.txt")
	must(t, os.WriteFile(notes, []byte("my notes"), 0o644))
	if err := run(shutdownSoon(t), 10, 1, notes, 0); err != nil {
		t.Fatal(err)
	}
	moved, _ := filepath.Glob(notes + ".invalid-*")
	if len(moved) != 1 {
		t.Fatalf("expected notes.txt to be moved aside, found %v", moved)
	}
	if b, _ := os.ReadFile(moved[0]); string(b) != "my notes" {
		t.Fatal("notes.txt lost its contents")
	}
}

// A symlink in place of the manifest is moved aside itself; the file it points
// to is never touched.
func TestSymlinkedManifestIsMovedNotFollowed(t *testing.T) {
	root := t.TempDir()
	victim := filepath.Join(root, "victim")
	must(t, os.WriteFile(victim, []byte("someone else's file"), 0o644))
	dir := filepath.Join(root, "cache")
	must(t, os.Mkdir(dir, 0o755))
	must(t, os.Symlink(victim, store.ManifestPath(dir)))
	if err := run(shutdownSoon(t), 10, 1, dir, 0); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(victim); string(b) != "someone else's file" {
		t.Fatal("the file behind the symlink was changed")
	}
	if info, err := os.Lstat(store.ManifestPath(dir)); err != nil || !info.Mode().IsRegular() {
		t.Fatal("manifest is not a fresh regular file")
	}
	mustHold(t, dir, 10, 1)
}

// -show prints each address, in EIP-55 form, with its balance in wei.
func TestShowPrintsBalances(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout := os.Stdout
	os.Stdout = w
	printDataset(2, order.Columns([]order.Row{
		{Account: evm.Account{Address: evm.Address{0xab}, Balance: evm.WeiFromParts(5, 0)}},
		{Account: evm.Account{Address: evm.Address{0xcd}, Balance: evm.WeiFromParts(0, 1)}},
	}))
	w.Close()
	os.Stdout = stdout
	out, _ := io.ReadAll(r)
	want := evm.Address{0xab}.Checksum() + " 5 wei\n" +
		evm.Address{0xcd}.Checksum() + " 18446744073709551616 wei\n"
	if string(out) != want {
		t.Fatalf("got\n%s\nwant\n%s", out, want)
	}
}

// A fresh run and a top-up give byte-identical files, the graph included: a
// top-up generates the whole dataset again from the seed (decision 012).
func TestFreshRunAndTopUpGiveIdenticalFiles(t *testing.T) {
	fresh := newDataset(t, 10_000, 3)
	grown := newDataset(t, 3_000, 3)
	if err := run(shutdownSoon(t), 10_000, 3, grown, 0); err != nil {
		t.Fatal(err)
	}
	_, freshA, freshB := current(t, fresh)
	_, grownA, grownB := current(t, grown)
	pairs := [][2]string{{freshA, grownA}, {freshB, grownB}}
	for _, name := range []string{columns.OffsetsFileName, columns.NeighborsFileName} {
		pairs = append(pairs, [2]string{filepath.Join(filepath.Dir(freshA), name), filepath.Join(filepath.Dir(grownA), name)})
	}
	for _, pair := range pairs {
		a, _ := os.ReadFile(pair[0])
		b, _ := os.ReadFile(pair[1])
		if len(a) == 0 || !slices.Equal(a, b) {
			t.Fatalf("%s differs between a fresh run and a top-up", filepath.Base(pair[0]))
		}
	}
	mustHold(t, grown, 10_000, 3)
}

// Datasets saved before connections existed (manifest versions 1 and 2) still
// load, and the next run generates them again from their seed, at their size,
// as the current version with the graph, deleting nothing else.
func TestOlderDatasetIsGeneratedAgain(t *testing.T) {
	for _, version := range []int{store.VersionGenerationOrder, store.VersionSorted} {
		t.Run(fmt.Sprint("version ", version), func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "cache")
			folder := filepath.Join(dir, "dataset-000001")
			rows, _ := generate.Rows(context.Background(), 5, 0, 2000, nil)
			if version >= store.VersionSorted {
				order.SortRows(rows) // what "sorted" promised from version 2 on
			}
			ctx := context.Background()
			must(t, columns.SaveAddresses(ctx, filepath.Join(folder, columns.AddressesFileName), nil, order.Addresses(order.Columns(rows))))
			must(t, columns.SaveBalancesSeq(ctx, filepath.Join(folder, columns.BalancesFileName), nil, order.Balances(order.Columns(rows))))
			writeManifest(t, dir, store.Manifest{Version: version, Seed: 5, Count: 2000, Folder: "dataset-000001"})

			if err := run(shutdownSoon(t), 1000, 5, dir, 0); err != nil {
				t.Fatal(err)
			}
			mustHold(t, dir, 2000, 5)
			if folders := datasetFolders(dir); len(folders) != 1 || filepath.Base(folders[0]) != "dataset-000002" {
				t.Fatalf("folders after regenerating: %v", folders)
			}
		})
	}
}

// The degree and skew are recorded; a later run with other flags continues
// the recorded graph instead of mixing two.
func TestRecordedDegreeAndSkewWin(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cache")
	if err := runWith(shutdownSoon(t), settings{n: 500, seed: 2, degree: 6, skew: 2, dir: dir}); err != nil {
		t.Fatal(err)
	}
	if err := runWith(shutdownSoon(t), settings{n: 900, seed: 2, degree: 10, skew: 3, dir: dir}); err != nil {
		t.Fatal(err)
	}
	mustHoldGraph(t, dir, 900, 2, 6, 2)
}

// A generation deadline that passes saves nothing new: on a fresh folder
// nothing is left, and an existing dataset stays as it was.
func TestGenerateTimeout(t *testing.T) {
	t.Run("fresh", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "cache")
		err := runWith(shutdownSoon(t), settings{n: 100_000, seed: 1, degree: 10, skew: 3, dir: dir,
			generateTimeout: time.Nanosecond})
		if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "-generate-timeout") {
			t.Fatalf("got %v, want a generation deadline error", err)
		}
		if _, err := os.Stat(store.ManifestPath(dir)); !errors.Is(err, fs.ErrNotExist) || len(datasetFolders(dir)) != 0 {
			t.Fatal("something was saved after the deadline")
		}
	})
	t.Run("over an existing dataset", func(t *testing.T) {
		dir := newDataset(t, 100, 1)
		err := runWith(shutdownSoon(t), settings{n: 5000, seed: 1, degree: 10, skew: 3, dir: dir,
			generateTimeout: time.Nanosecond})
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("got %v, want a generation deadline error", err)
		}
		mustHold(t, dir, 100, 1)
		if folders := datasetFolders(dir); len(folders) != 1 {
			t.Fatalf("folders after the deadline: %v", folders)
		}
	})
}

// A save deadline that passes keeps the previous dataset and leaves no new
// folder behind.
func TestSaveTimeout(t *testing.T) {
	dir := newDataset(t, 100, 1)
	err := runWith(shutdownSoon(t), settings{n: 5000, seed: 1, degree: 10, skew: 3, dir: dir,
		saveTimeout: time.Nanosecond})
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "-save-timeout") {
		t.Fatalf("got %v, want a save deadline error", err)
	}
	mustHold(t, dir, 100, 1)
	if folders := datasetFolders(dir); len(folders) != 1 {
		t.Fatalf("folders after the deadline: %v", folders)
	}
}

// The dataset is on disk before the program waits for a signal: a crash or
// kill -9 while it waits loses nothing.
func TestDatasetIsSavedBeforeWaiting(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cache")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	saved := false
	whenWaiting = func() {
		if d, err := store.OpenReadOnly(dir); err == nil {
			saved = true
			d.Close()
		}
		cancel()
	}
	t.Cleanup(func() { whenWaiting = nil })
	if err := run(ctx, 1000, 1, dir, 0); err != nil {
		t.Fatal(err)
	}
	if !saved {
		t.Fatal("the dataset was not saved before waiting")
	}
	mustHold(t, dir, 1000, 1)
}

// updateBalance appends a balance update to the cache's dataset, as
// evmgraph update does, and returns the new balance.
func updateBalance(t *testing.T, dir string, row int) evm.Wei {
	t.Helper()
	lock, err := store.WaitLock(dir)
	must(t, err)
	defer lock.Unlock()
	d, err := store.OpenReadOnly(dir)
	must(t, err)
	defer d.Close()
	b := d.Balance(row)
	b[15]++ // a balance no model draw gives
	must(t, store.AppendBalance(dir, d, d.Addresses.Addrs[row], b))
	return b
}

// Balance updates waiting in the log are folded into a new dataset folder
// when run starts, and those made while it runs when it stops.
func TestRunFoldsBalanceUpdates(t *testing.T) {
	dir := newDataset(t, 1000, 1)
	before := updateBalance(t, dir, 10)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var atStart store.Manifest
	var during evm.Wei
	whenWaiting = func() {
		atStart, _, _ = current(t, dir)
		during = updateBalance(t, dir, 20)
		cancel()
	}
	t.Cleanup(func() { whenWaiting = nil })
	if err := run(ctx, 1000, 1, dir, 0); err != nil {
		t.Fatal(err)
	}
	if atStart.Folder != "dataset-000002" {
		t.Fatalf("the update waiting at the start was not folded: folder %s", atStart.Folder)
	}
	m, _, _ := current(t, dir)
	d := openSaved(t, dir)
	if m.Folder != "dataset-000003" || d.PendingBalances() != 0 {
		t.Fatalf("the update made while running was not folded at shutdown: folder %s, %d pending", m.Folder, d.PendingBalances())
	}
	if d.Balances.Balances[10] != before || d.Balances.Balances[20] != during {
		t.Fatal("balances.bin does not hold the updates")
	}
	if folders := datasetFolders(dir); len(folders) != 1 {
		t.Fatalf("folders after folding: %v", folders)
	}
}

// Growing a dataset keeps the saved balances of the addresses it already
// held, updates included; the new addresses get generated balances.
func TestTopUpKeepsSavedBalances(t *testing.T) {
	dir := newDataset(t, 1000, 1)
	d := openSaved(t, dir)
	addr := d.Addresses.Addrs[10]
	want := updateBalance(t, dir, 10)
	if err := run(shutdownSoon(t), 3000, 1, dir, 0); err != nil {
		t.Fatal(err)
	}
	d = openSaved(t, dir)
	row, ok := lookup.Find(d.Addresses.Addrs, addr)
	if !ok || d.Manifest.Count != 3000 || d.Balance(row) != want {
		t.Fatal("the grown dataset lost the updated balance")
	}
	for i, r := range sortedRows(t, 3000, 1) {
		if r.Address != addr && d.Balances.Balances[i] != r.Balance {
			t.Fatalf("row %d does not hold its generated balance", i)
		}
	}
}

// Only one program writes a cache folder: a run refuses to start while
// another holds the write lock, and changes nothing.
func TestRunRefusesWhileLocked(t *testing.T) {
	dir := newDataset(t, 100, 1)
	lock, err := (store.Cache{Dir: dir}).TryLock()
	must(t, err)
	defer lock.Unlock()
	err = run(shutdownSoon(t), 500, 1, dir, 0)
	if err == nil || !strings.Contains(err.Error(), "another evmgraph is writing") {
		t.Fatalf("got %v, want a refusal", err)
	}
	mustHold(t, dir, 100, 1)
}

// A signal before the save stops a growth at once: nothing new is written,
// and the previous dataset stays current.
func TestSignalBeforeSaveKeepsPreviousDataset(t *testing.T) {
	dir := newDataset(t, 100, 1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := run(ctx, 5000, 1, dir, 0); err != nil {
		t.Fatal(err)
	}
	mustHold(t, dir, 100, 1)
	if folders := datasetFolders(dir); len(folders) != 1 {
		t.Fatalf("folders after the signal: %v", folders)
	}
}

// -n 0 on an empty cache folder has nothing to generate or load: it saves
// nothing and exits cleanly.
func TestRunZeroOnEmptyCache(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cache")
	if err := run(shutdownSoon(t), 0, 1, dir, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(store.ManifestPath(dir)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("a dataset was saved for -n 0")
	}
}

// linkUnlinked adds a link, as evmgraph update link does, between two
// addresses of the cache's dataset that are not connected, and returns them.
func linkUnlinked(t *testing.T, dir string, from int) (evm.Address, evm.Address) {
	t.Helper()
	lock, err := store.WaitLock(dir)
	must(t, err)
	defer lock.Unlock()
	d, err := store.OpenReadOnly(dir)
	must(t, err)
	defer d.Close()
	g := d.Graph()
	for b := uint32(from + 1); b < uint32(g.Len()); b++ {
		if !g.Has(uint32(from), b) {
			a, bb := d.Addresses.Addrs[from], d.Addresses.Addrs[b]
			must(t, store.AppendLink(dir, d, a, bb))
			return a, bb
		}
	}
	t.Fatal("no unlinked pair")
	return evm.Address{}, evm.Address{}
}

// Added links survive a fold and a growth: a grown dataset holds the
// generated graph of its size plus every added link, folded or pending.
func TestTopUpKeepsAddedLinks(t *testing.T) {
	dir := newDataset(t, 1000, 1)
	a1, b1 := linkUnlinked(t, dir, 10)
	if err := run(shutdownSoon(t), 1000, 1, dir, 0); err != nil { // folds the first link in
		t.Fatal(err)
	}
	if d := openSaved(t, dir); d.PendingLinks() != 0 {
		t.Fatal("the link was not folded in")
	}
	a2, b2 := linkUnlinked(t, dir, 20) // left pending
	if err := run(shutdownSoon(t), 3000, 1, dir, 0); err != nil {
		t.Fatal(err)
	}
	d := openSaved(t, dir)
	g := d.Graph()
	for _, l := range [][2]evm.Address{{a1, b1}, {a2, b2}} {
		ra, okA := lookup.Find(d.Addresses.Addrs, l[0])
		rb, okB := lookup.Find(d.Addresses.Addrs, l[1])
		if !okA || !okB || !g.Has(uint32(ra), uint32(rb)) || !g.Has(uint32(rb), uint32(ra)) {
			t.Fatalf("the grown dataset lost the added link %s-%s", l[0].Checksum(), l[1].Checksum())
		}
	}
	rows := sortedRows(t, 3000, 1)
	off, nb, err := graph.BuildContext(context.Background(), 1, 3000, 10, 3, order.RowOf(rows),
		func(e int) ([]uint32, error) { return make([]uint32, e), nil }, nil)
	must(t, err)
	if len(g.Neighbors) != len(nb)+4 || len(off) != len(g.Offsets) || d.PendingLinks() != 0 {
		t.Fatalf("%d entries, want the generated %d plus the 2 links", len(g.Neighbors), len(nb))
	}
}
