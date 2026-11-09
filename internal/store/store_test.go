package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"evmgraph/internal/evm"
	"evmgraph/internal/generate"
	"evmgraph/internal/graph"
	"evmgraph/internal/order"
)

// recorder is Events that remembers what it was told.
type recorder struct{ events []string }

func (r *recorder) DirCreated(dir string)           { r.events = append(r.events, "created "+dir) }
func (r *recorder) DirMovedAside(dir, to string)    { r.events = append(r.events, "moved "+dir) }
func (r *recorder) MovedAside(path, to, why string) { r.events = append(r.events, "moved "+path) }
func (r *recorder) RemovedUnfinished(folder string) { r.events = append(r.events, "removed "+folder) }
func (r *recorder) NotOurFolder(path string)        { r.events = append(r.events, "not ours "+path) }
func newCache(t *testing.T) (Cache, *recorder) {
	r := &recorder{}
	return Cache{Dir: t.TempDir(), Events: r}, r
}
func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// commit writes and commits a dataset of the first n positions of seed, as
// the program saves one, and returns its manifest.
func commit(t *testing.T, c Cache, n int, seed uint64, previous string) Manifest {
	t.Helper()
	rows, err := generate.Rows(context.Background(), seed, 0, n, nil)
	must(t, err)
	order.SortRows(rows)
	off, nb, err := graph.BuildContext(context.Background(), seed, n, 10, 3, order.RowOf(rows), func(e int) ([]uint32, error) { return make([]uint32, e), nil }, nil)
	must(t, err)
	d, err := c.NewDraft()
	must(t, err)
	defer d.Discard()
	ctx := context.Background()
	must(t, d.WriteGraph(ctx, off, nb))
	must(t, d.WriteAddresses(ctx, order.Addresses(order.Columns(rows)), nil))
	must(t, d.WriteBalances(ctx, order.Balances(order.Columns(rows)), nil))
	m := Manifest{Version: VersionCurrent, Seed: seed, Count: n, Degree: 10, Skew: 3}
	must(t, d.Commit(m, previous))
	m.Folder = filepath.Base(d.Path())
	return m
}

// A committed draft opens, read-only and for a run, with every column; the
// next commit replaces the folder and removes the previous one.
func TestDraftCommitAndOpen(t *testing.T) {
	c, _ := newCache(t)
	first := commit(t, c, 300, 1, "")
	second := commit(t, c, 500, 1, first.Folder)
	if second.Folder != "dataset-000002" {
		t.Fatalf("second folder %s", second.Folder)
	}
	if _, err := os.Stat(filepath.Join(c.Dir, first.Folder)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the previous folder was not removed after the commit")
	}
	for name, open := range map[string]func() (*Dataset, error){"read-only": func() (*Dataset, error) { return OpenReadOnly(c.Dir) }, "run": c.Open} {
		d, err := open()
		if err != nil || d == nil {
			t.Fatalf("%s: %v", name, err)
		}
		if d.Manifest != second || len(d.Addresses.Addrs) != 500 || len(d.Balances.Balances) != 500 || d.Graph().Len() != 500 {
			t.Fatalf("%s: opened %+v", name, d.Manifest)
		}
		d.Close()
	}
}

// A write cancelled before the commit leaves the previous dataset current,
// and Discard removes the draft's folder.
func TestCancelledWriteKeepsPreviousDataset(t *testing.T) {
	c, _ := newCache(t)
	before := commit(t, c, 100, 1, "")
	rows, _ := generate.Rows(context.Background(), 1, 0, 200, nil)
	d, err := c.NewDraft()
	must(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := d.WriteAddresses(ctx, order.Addresses(order.Columns(rows)), nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context.Canceled", err)
	}
	d.Discard()
	if folders, _ := filepath.Glob(filepath.Join(c.Dir, "dataset-*")); len(folders) != 1 {
		t.Fatalf("the cancelled write left %v", folders)
	}
	got, err := OpenReadOnly(c.Dir)
	must(t, err)
	defer got.Close()
	if got.Manifest != before {
		t.Fatalf("current dataset %+v, want %+v", got.Manifest, before)
	}
}

// Clean removes a folder named like a dataset folder only when it holds
// nothing but files a save writes, and never the one to keep.
func TestCleanRemovesOnlyOurs(t *testing.T) {
	c, r := newCache(t)
	keep := commit(t, c, 10, 1, "").Folder
	ours := filepath.Join(c.Dir, "dataset-000007")
	must(t, os.Mkdir(ours, 0o755))
	must(t, os.WriteFile(filepath.Join(ours, "balances.bin.42.tmp"), nil, 0o644))
	theirs := filepath.Join(c.Dir, "dataset-000008")
	must(t, os.Mkdir(theirs, 0o755))
	must(t, os.WriteFile(filepath.Join(theirs, "notes.txt"), []byte("mine"), 0o644))

	must(t, c.Clean(keep))
	if _, err := os.Stat(ours); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("an unfinished save folder was not removed")
	}
	if b, _ := os.ReadFile(filepath.Join(theirs, "notes.txt")); string(b) != "mine" {
		t.Fatal("a folder with other files was touched")
	}
	if _, err := os.Stat(filepath.Join(c.Dir, keep)); err != nil {
		t.Fatal("the current folder was removed")
	}
	if want := []string{"removed " + ours, "not ours " + theirs}; !slices.Equal(r.events, want) {
		t.Fatalf("events %q, want %q", r.events, want)
	}
}

// OpenReadOnly changes nothing, whatever it finds.
func TestOpenReadOnlyChangesNothing(t *testing.T) {
	dir := t.TempDir()
	if _, err := OpenReadOnly(dir); !errors.Is(err, ErrNoDataset) {
		t.Fatalf("empty folder: %v", err)
	}
	must(t, os.WriteFile(ManifestPath(dir), []byte("hello\n"), 0o644))
	if _, err := OpenReadOnly(dir); err == nil || !strings.Contains(err.Error(), "is not a manifest") {
		t.Fatalf("broken manifest: %v", err)
	}
	old := Manifest{Version: VersionGenerationOrder, Seed: 1, Count: 0, Folder: "dataset-000001"}
	must(t, os.WriteFile(ManifestPath(dir), []byte(old.String()), 0o644))
	if _, err := OpenReadOnly(dir); !errors.Is(err, ErrUnsorted) {
		t.Fatalf("generation order: %v", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("OpenReadOnly changed the folder: %v", entries)
	}
}

// The manifest is read strictly: anything unexpected makes it not a manifest.
func TestParseManifest(t *testing.T) {
	good := Manifest{Version: 4, Seed: 7, Count: 42, Folder: "dataset-000003", Degree: 10, Skew: 3}
	for _, m := range []Manifest{good,
		{Version: 3, Seed: 7, Count: 42, Folder: "dataset-000003", Degree: 10, Skew: 3},
		{Version: 2, Seed: 7, Count: 42, Folder: "dataset-000003"},
		{Version: 1, Seed: 7, Count: 42, Folder: "dataset-000003"},
	} {
		if got, err := ParseManifest(m.String()); err != nil || got != m {
			t.Fatalf("version %d round trip: %+v, %v", m.Version, got, err)
		}
	}
	v3 := Manifest{Version: 3, Seed: 7, Count: 42, Folder: "dataset-000003", Degree: 10, Skew: 3}.String()
	v2 := strings.Replace(strings.Replace(v3, "version 3", "version 2", 1), "degree 10\nskew 3\n", "", 1)
	for name, text := range map[string]string{
		"empty":           "",
		"wrong magic":     strings.Replace(good.String(), manifestMagic, "something else", 1),
		"newer version":   strings.Replace(good.String(), "version 4", "version 5", 1),
		"format in v4":    good.String() + "format binary\n",
		"v3 csv format":   strings.Replace(v3, "format binary", "format csv", 1),
		"v3 no format":    strings.Replace(v3, "format binary\n", "", 1),
		"negative count":  strings.Replace(good.String(), "addresses 42", "addresses -1", 1),
		"bad folder":      strings.Replace(good.String(), "dataset-000003", "../etc", 1),
		"unknown field":   good.String() + "colour blue\n",
		"field twice":     good.String() + "seed 8\n",
		"missing field":   strings.Replace(good.String(), "seed 7\n", "", 1),
		"seed not number": strings.Replace(good.String(), "seed 7", "seed seven", 1),
		"odd degree":      strings.Replace(good.String(), "degree 10", "degree 9", 1),
		"skew too high":   strings.Replace(good.String(), "skew 3", "skew 5", 1),
		"v3 without skew": strings.Replace(good.String(), "skew 3\n", "", 1),
		"degree in v2":    v2 + "degree 10\n",
	} {
		if _, err := ParseManifest(text); err == nil {
			t.Errorf("%s: accepted %q", name, text)
		}
	}
}

// moveAside never replaces an earlier moved file, even within one second.
func TestMoveAsideNeverReplaces(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manifest")
	var moved []string
	for _, content := range []string{"first", "second", "third"} {
		must(t, os.WriteFile(path, []byte(content), 0o644))
		m, err := moveAside(path)
		must(t, err)
		moved = append(moved, m)
	}
	for i, content := range []string{"first", "second", "third"} {
		if b, _ := os.ReadFile(moved[i]); string(b) != content {
			t.Fatalf("%s holds %q, want %q", moved[i], b, content)
		}
	}
}

// Only one program holds the write lock; it is free again after Unlock.
func TestLock(t *testing.T) {
	c, _ := newCache(t)
	first, err := c.TryLock()
	must(t, err)
	if _, err := c.TryLock(); !errors.Is(err, ErrLocked) {
		t.Fatalf("second lock: got %v, want ErrLocked", err)
	}
	first.Unlock()
	first.Unlock() // twice is harmless
	again, err := c.TryLock()
	must(t, err)
	again.Unlock()
}

// An update is durable and visible to the next opening, read-only or for a
// run; the latest update of an address wins; an address outside the dataset
// is refused; a torn last record is ignored.
func TestBalanceUpdates(t *testing.T) {
	c, _ := newCache(t)
	commit(t, c, 1000, 1, "")
	d, err := OpenReadOnly(c.Dir)
	must(t, err)
	a, b := d.Addresses.Addrs[10], d.Addresses.Addrs[20]
	must(t, AppendBalance(c.Dir, d, a, evm.WeiFromParts(5, 0)))
	must(t, AppendBalance(c.Dir, d, b, evm.WeiFromParts(7, 0)))
	must(t, AppendBalance(c.Dir, d, a, evm.WeiFromParts(6, 0)))
	if err := AppendBalance(c.Dir, d, evm.Address{0xff}, evm.WeiFromParts(1, 0)); !errors.Is(err, ErrNotInDataset) {
		t.Fatalf("an address outside the dataset: got %v", err)
	}
	d.Close()

	log := filepath.Join(c.Dir, "dataset-000001", UpdatesFileName)
	f, err := os.OpenFile(log, os.O_WRONLY|os.O_APPEND, 0)
	must(t, err)
	f.Write(make([]byte, updateLen-3)) // a crash halfway through an append
	f.Close()

	for name, open := range map[string]func() (*Dataset, error){"read-only": func() (*Dataset, error) { return OpenReadOnly(c.Dir) }, "run": c.Open} {
		d, err := open()
		must(t, err)
		if got := d.Balance(10); got != evm.WeiFromParts(6, 0) {
			t.Fatalf("%s: balance of row 10 is %s, want the latest update, 6", name, got)
		}
		if got := d.Balance(20); got != evm.WeiFromParts(7, 0) {
			t.Fatalf("%s: balance of row 20 is %s, want 7", name, got)
		}
		if got, want := d.Balance(30), d.Balances.Balances[30]; got != want {
			t.Fatalf("%s: row 30 changed without an update", name)
		}
		if d.PendingBalances() != 2 {
			t.Fatalf("%s: %d pending updates, want 2", name, d.PendingBalances())
		}
		d.Close()
	}
}

// Folding writes the updates into a new folder's balances.bin, links the
// other files, and leaves no log behind; with nothing to fold it does nothing.
func TestFold(t *testing.T) {
	c, _ := newCache(t)
	before := commit(t, c, 1000, 1, "")
	d, err := OpenReadOnly(c.Dir)
	must(t, err)
	if n, _, _, err := c.Fold(context.Background(), d, nil); err != nil || n != 0 {
		t.Fatalf("nothing to fold: %d, %v", n, err)
	}
	want := d.Balances.Balances[10]
	want[0]++
	must(t, AppendBalance(c.Dir, d, d.Addresses.Addrs[10], want))
	d.Close()

	d, err = OpenReadOnly(c.Dir)
	must(t, err)
	n, _, folder, err := c.Fold(context.Background(), d, nil)
	d.Close()
	if err != nil || n != 1 || filepath.Base(folder) != "dataset-000002" {
		t.Fatalf("fold: %d, %s, %v", n, folder, err)
	}
	after, err := OpenReadOnly(c.Dir)
	must(t, err)
	defer after.Close()
	if after.PendingBalances() != 0 || after.Balances.Balances[10] != want {
		t.Fatal("the update is not in balances.bin after the fold")
	}
	if after.Manifest.Count != before.Count || after.Manifest.Seed != before.Seed || after.Graph().Len() != 1000 {
		t.Fatalf("the fold changed the dataset: %+v", after.Manifest)
	}
	if _, err := os.Stat(filepath.Join(c.Dir, before.Folder)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the folded folder, with its log, was not removed")
	}
	if _, err := os.Stat(filepath.Join(folder, UpdatesFileName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the new folder has an update log")
	}
}

// A folder with an update log is never cleaned away, even when no manifest
// names it: balance updates cannot be generated again.
func TestCleanKeepsBalanceUpdates(t *testing.T) {
	c, r := newCache(t)
	commit(t, c, 100, 1, "")
	d, err := OpenReadOnly(c.Dir)
	must(t, err)
	must(t, AppendBalance(c.Dir, d, d.Addresses.Addrs[3], d.Balances.Balances[4]))
	d.Close()
	must(t, os.Rename(ManifestPath(c.Dir), ManifestPath(c.Dir)+".aside")) // as when the dataset is set aside

	must(t, c.Clean(""))
	folder := filepath.Join(c.Dir, "dataset-000001")
	if _, err := os.Stat(filepath.Join(folder, UpdatesFileName)); err != nil {
		t.Fatal("the folder with balance updates was removed")
	}
	if want := []string{"not ours " + folder}; !slices.Equal(r.events, want) {
		t.Fatalf("events %q, want %q", r.events, want)
	}
}

// unlinked returns two rows of d that are not connected.
func unlinked(t *testing.T, d *Dataset) (uint32, uint32) {
	t.Helper()
	g := d.Graph()
	for a := uint32(0); a < uint32(g.Len()); a++ {
		for b := a + 1; b < uint32(g.Len()); b++ {
			if !g.Has(a, b) {
				return a, b
			}
		}
	}
	t.Fatal("every row is connected to every other")
	return 0, 0
}

// An added link is durable and visible to the next opening, read-only or
// for a run, in both directions and in the search; repeats, self-links and
// addresses outside the dataset are refused; a torn last record is ignored;
// a fold writes it into the graph's files and links the files it does not
// change.
func TestLinkUpdates(t *testing.T) {
	c, _ := newCache(t)
	commit(t, c, 1000, 1, "")
	d, err := OpenReadOnly(c.Dir)
	must(t, err)
	ra, rb := unlinked(t, d)
	a, b := d.Addresses.Addrs[ra], d.Addresses.Addrs[rb]
	must(t, AppendLink(c.Dir, d, a, b))
	for _, c2 := range []struct {
		a, b evm.Address
		want error
	}{
		{a, a, ErrSameAddress},
		{a, evm.Address{0xff}, ErrNotInDataset},
	} {
		if err := AppendLink(c.Dir, d, c2.a, c2.b); !errors.Is(err, c2.want) {
			t.Fatalf("AppendLink: got %v, want %v", err, c2.want)
		}
	}
	d.Close()
	f, err := os.OpenFile(filepath.Join(c.Dir, "dataset-000001", LinksFileName), os.O_WRONLY|os.O_APPEND, 0)
	must(t, err)
	f.Write(make([]byte, linkLen-1)) // a crash halfway through an append
	f.Close()

	for name, open := range map[string]func() (*Dataset, error){"read-only": func() (*Dataset, error) { return OpenReadOnly(c.Dir) }, "run": c.Open} {
		d, err := open()
		must(t, err)
		g := d.Graph()
		if !g.Has(ra, rb) || !g.Has(rb, ra) || d.PendingLinks() != 1 {
			t.Fatalf("%s: the added link is not visible in both directions (pending %d)", name, d.PendingLinks())
		}
		res, err := graph.NewSearch(g).Run(ra)
		must(t, err)
		if !slices.Contains(res.Level1, rb) {
			t.Fatalf("%s: the search does not see the added link", name)
		}
		if err := AppendLink(c.Dir, d, b, a); !errors.Is(err, ErrAlreadyLinked) {
			t.Fatalf("%s: linking again: got %v, want ErrAlreadyLinked", name, err)
		}
		d.Close()
	}

	d, err = OpenReadOnly(c.Dir)
	must(t, err)
	balancesBefore := slices.Clone(d.Balances.Balances)
	balances, links, folder, err := c.Fold(context.Background(), d, nil)
	before := d.Graph()
	entries := len(before.Neighbors)
	d.Close()
	if err != nil || balances != 0 || links != 1 {
		t.Fatalf("fold: %d balances, %d links, %v", balances, links, err)
	}
	after, err := OpenReadOnly(c.Dir)
	must(t, err)
	defer after.Close()
	g := after.Graph()
	if after.PendingLinks() != 0 || g.Extra != nil || !g.Has(ra, rb) || !g.Has(rb, ra) || len(g.Neighbors) != entries+2 {
		t.Fatal("the link is not in the graph's files after the fold")
	}
	if err := g.Validate(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(folder, LinksFileName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the new folder has a link log")
	}
	if _, err := os.Stat(filepath.Join(c.Dir, "dataset-000001")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the folded folder was not removed")
	}
	// No balance changed, so balances.bin is the same file, linked, with the
	// same contents.
	if !slices.Equal(after.Balances.Balances, balancesBefore) {
		t.Fatal("the fold changed the balances")
	}
}

// Addresses out of order would make lookups answer wrong without an error,
// so a run sets such a dataset aside, like a broken one.
func TestOpenSetsAsideUnsortedAddresses(t *testing.T) {
	c, r := newCache(t)
	commit(t, c, 1000, 1, "")
	path := filepath.Join(c.Dir, "dataset-000001", "addresses.bin")
	b, err := os.ReadFile(path)
	must(t, err)
	row := make([]byte, 20)
	copy(row, b[100*20:101*20])
	copy(b[100*20:], b[101*20:102*20])
	copy(b[101*20:], row) // rows 100 and 101 swapped
	must(t, os.WriteFile(path, b, 0o600))

	d, err := c.Open()
	if err != nil || d != nil {
		t.Fatalf("got %v, %v; want the dataset set aside", d, err)
	}
	if len(r.events) != 1 || !strings.HasPrefix(r.events[0], "moved ") {
		t.Fatalf("events %q, want the manifest moved aside", r.events)
	}
}
