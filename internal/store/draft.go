package store

import (
	"context"
	"iter"
	"os"
	"path/filepath"

	"evmgraph/internal/columns"
	"evmgraph/internal/evm"
)

// A Draft is a new dataset folder being written. It is not part of the
// dataset until Commit names it in the manifest; until then Discard, or the
// next start's Clean, removes it, and the previous dataset stays current.
type Draft struct {
	dir, name string
	committed bool
}

// NewDraft creates the next numbered dataset folder.
func (c Cache) NewDraft() (*Draft, error) {
	name, err := nextFolder(c.Dir)
	if err != nil {
		return nil, err
	}
	d := &Draft{dir: c.Dir, name: name}
	return d, os.Mkdir(d.Path(), 0o755)
}

// Path is the draft's folder.
func (d *Draft) Path() string { return filepath.Join(d.dir, d.name) }

// WriteGraph writes the connections: neighbors.bin and offsets.bin.
func (d *Draft) WriteGraph(ctx context.Context, offsets, neighbors []uint32) error {
	if err := columns.SaveUint32s(ctx, filepath.Join(d.Path(), columns.NeighborsFileName), nil, neighbors); err != nil {
		return err
	}
	return columns.SaveUint32s(ctx, filepath.Join(d.Path(), columns.OffsetsFileName), nil, offsets)
}

// WriteAddresses writes the addresses, in address order; progress, if not
// nil, is called with the number written so far.
func (d *Draft) WriteAddresses(ctx context.Context, addrs iter.Seq[*evm.Address], progress func(int)) error {
	return columns.SaveAddresses(ctx, filepath.Join(d.Path(), columns.AddressesFileName), progress, addrs)
}

// WriteBalances writes the balances, in the same order as the addresses.
func (d *Draft) WriteBalances(ctx context.Context, balances iter.Seq[*evm.Wei], progress func(int)) error {
	return columns.SaveBalancesSeq(ctx, filepath.Join(d.Path(), columns.BalancesFileName), progress, balances)
}

// Commit makes the draft the current dataset: it fsyncs the folder, then
// replaces the manifest, the commit point, with m naming this folder. The
// previous folder, if any, is deleted after the commit; a failure there only
// leaves an old folder, which the next start's Clean removes.
func (d *Draft) Commit(m Manifest, previous string) error {
	if err := syncDir(d.Path()); err != nil {
		return err
	}
	m.Folder = d.name
	if err := writeManifest(d.dir, m); err != nil {
		return err
	}
	d.committed = true
	if previous != "" && previous != d.name {
		os.RemoveAll(filepath.Join(d.dir, previous))
	}
	return nil
}

// Discard removes the draft's folder unless it was committed. It is safe to
// defer right after NewDraft.
func (d *Draft) Discard() {
	if d != nil && !d.committed {
		os.RemoveAll(d.Path())
	}
}
