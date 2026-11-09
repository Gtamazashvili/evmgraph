package store

import (
	"errors"
	"fmt"
	"io/fs"
	"iter"
	"path/filepath"

	"evmgraph/internal/columns"
	"evmgraph/internal/evm"
	"evmgraph/internal/graph"
	"evmgraph/internal/lookup"
)

// Dataset is an opened dataset. Its columns are views of the mapped files,
// valid until Close; offsets and neighbors are nil before version 3.
type Dataset struct {
	Manifest  Manifest
	Addresses *columns.AddressFile
	Balances  *columns.BalanceFile
	offsets   *columns.Uint32File
	neighbors *columns.Uint32File
	updates   map[evm.Address]evm.Wei // balance updates not yet folded in (updates.go)
	extra     map[uint32][]uint32     // links added and not yet folded in, by row (graph.Overlay)
}

// Balance is row's balance: its pending update if there is one, else the
// saved balance. Lookups use it instead of Balances, so an update is
// visible at once.
func (d *Dataset) Balance(row int) evm.Wei {
	if len(d.updates) > 0 {
		if b, ok := d.updates[d.Addresses.Addrs[row]]; ok {
			return b
		}
	}
	return d.Balances.Balances[row]
}

// PendingBalances is how many addresses have a balance update not yet folded
// into balances.bin.
func (d *Dataset) PendingBalances() int { return len(d.updates) }

// PendingLinks is how many added links are not yet folded into the graph's
// files.
func (d *Dataset) PendingLinks() int {
	n := 0
	for _, l := range d.extra {
		n += len(l)
	}
	return n / 2 // each link is in both rows' lists
}

// find returns a's row.
func (d *Dataset) find(a evm.Address) (int, bool) { return lookup.Find(d.Addresses.Addrs, a) }

// Close unmaps the dataset's files.
func (d *Dataset) Close() {
	if d.Addresses != nil {
		d.Addresses.Close()
	}
	if d.Balances != nil {
		d.Balances.Close()
	}
	if d.offsets != nil {
		d.offsets.Close()
	}
	if d.neighbors != nil {
		d.neighbors.Close()
	}
}

// Graph returns the dataset's connections; only when Manifest.HasGraph.
func (d *Dataset) Graph() graph.Graph {
	return graph.Graph{Offsets: d.offsets.Values, Neighbors: d.neighbors.Values, Extra: d.extra}
}

// Rows yields every row's address and balance, updates applied, in address
// order.
func (d *Dataset) Rows() iter.Seq2[*evm.Address, *evm.Wei] {
	return func(yield func(*evm.Address, *evm.Wei) bool) {
		for i := range d.Addresses.Addrs {
			b := d.Balance(i)
			if !yield(&d.Addresses.Addrs[i], &b) {
				return
			}
		}
	}
}

// errIncomplete marks a manifest whose folder does not hold the dataset it
// describes.
var errIncomplete = errors.New("the dataset it describes is incomplete")

// load opens the files of the dataset m describes in the cache folder dir and
// checks that they hold exactly m.Count rows: addresses, balances and, from
// version 3, the graph's offsets (count + 1, from 0 to the number of
// neighbour entries).
func load(dir string, m Manifest) (_ *Dataset, err error) {
	incomplete := func(err error) error {
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, columns.ErrInvalidFile) {
			return fmt.Errorf("%w: %v", errIncomplete, err)
		}
		return err
	}
	folder := filepath.Join(dir, m.Folder)
	d := &Dataset{Manifest: m}
	defer func() {
		if err != nil {
			d.Close()
		}
	}()
	if d.Addresses, err = columns.OpenAddresses(filepath.Join(folder, columns.AddressesFileName)); err != nil {
		return nil, incomplete(err)
	}
	if d.Balances, err = columns.OpenBalances(filepath.Join(folder, columns.BalancesFileName)); err != nil {
		return nil, incomplete(err)
	}
	if len(d.Addresses.Addrs) != m.Count || len(d.Balances.Balances) != m.Count {
		return nil, fmt.Errorf("%w: %d addresses and %d balances, the manifest says %d",
			errIncomplete, len(d.Addresses.Addrs), len(d.Balances.Balances), m.Count)
	}
	if d.updates, err = readUpdates(folder); err != nil {
		return nil, err
	}
	if !m.HasGraph() {
		return d, nil
	}
	if d.offsets, err = columns.OpenUint32s(filepath.Join(folder, columns.OffsetsFileName)); err != nil {
		return nil, incomplete(err)
	}
	if d.neighbors, err = columns.OpenUint32s(filepath.Join(folder, columns.NeighborsFileName)); err != nil {
		return nil, incomplete(err)
	}
	off := d.offsets.Values
	if len(off) != m.Count+1 || off[0] != 0 || int(off[m.Count]) != len(d.neighbors.Values) {
		return nil, fmt.Errorf("%w: the offsets do not frame %d rows and %d neighbour entries",
			errIncomplete, m.Count, len(d.neighbors.Values))
	}
	pairs, err := readLinks(folder)
	if err != nil {
		return nil, err
	}
	if len(pairs) > 0 {
		rows := make([][2]uint32, 0, len(pairs))
		for _, p := range pairs {
			a, okA := d.find(p[0])
			b, okB := d.find(p[1])
			if okA && okB {
				rows = append(rows, [2]uint32{uint32(a), uint32(b)})
			}
		}
		d.extra = graph.Overlay(graph.Graph{Offsets: off, Neighbors: d.neighbors.Values}, rows)
	}
	return d, nil
}

// Why the queries cannot open a dataset.
var (
	ErrNoDataset = errors.New("no dataset")
	ErrUnsorted  = errors.New("dataset in generation order")
)

// OpenReadOnly opens the current dataset in the cache folder dir without
// changing anything: it creates, moves and cleans nothing, so queries can run
// beside the main program. If a save commits a new dataset and deletes the
// old folder between reading the manifest and opening the files, it reads the
// new manifest and tries again; once a file is mapped, deleting it does not
// disturb the mapping. It returns ErrNoDataset when there is no manifest and
// ErrUnsorted for a dataset in generation order; the caller checks
// Manifest.HasGraph when it needs the connections.
func OpenReadOnly(dir string) (*Dataset, error) {
	var err error
	for range 3 {
		var b []byte
		b, err = readSmallRegularFile(ManifestPath(dir), maxManifest)
		if errors.Is(err, fs.ErrNotExist) {
			return nil, ErrNoDataset
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", ManifestPath(dir), err)
		}
		m, perr := ParseManifest(string(b))
		if perr != nil {
			return nil, fmt.Errorf("%s is not a manifest (%v); run evmgraph run once to set it aside", ManifestPath(dir), perr)
		}
		if !m.Sorted() {
			return nil, ErrUnsorted
		}
		var d *Dataset
		d, err = load(dir, m)
		if err == nil {
			return d, nil
		}
		if !errors.Is(err, errIncomplete) {
			return nil, err
		}
	}
	return nil, err
}
