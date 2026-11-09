package store

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"evmgraph/internal/columns"
	"evmgraph/internal/evm"
	"evmgraph/internal/graph"
)

// Updates without a rebuild (decision 014): balance changes and new links.
// `evmgraph update` appends each one to a log in the current dataset's
// folder; every lookup reads the logs when it opens the dataset, so a change
// is visible at once. `evmgraph run` folds the logs into a new dataset folder
// (Fold) when it starts and when it stops, so they stay short and the column
// files stay the truth.
//
//	updates.log   balance changes: the address (20 bytes), the new balance
//	              (16, u128 little endian like balances.bin), a CRC-32
//	links.log     new links: the two addresses (20 + 20), a CRC-32
//
// Records hold addresses, not row numbers, because row numbers change when a
// dataset grows. Each record is one append, fsynced before update returns. A
// crash during an append can leave a partial or garbled last record; reading
// stops there, and everything before it, which was fsynced, counts.

// UpdatesFileName and LinksFileName are the logs' names inside a dataset
// folder.
const (
	UpdatesFileName = "updates.log"
	LinksFileName   = "links.log"
)

const (
	updateLen = evm.AddressLen + evm.WeiLen + 4
	linkLen   = 2*evm.AddressLen + 4
)

var (
	// ErrNotInDataset means an update names an address the dataset does not
	// hold; only existing addresses can change.
	ErrNotInDataset = errors.New("address not in the dataset")
	// ErrSameAddress refuses a link from an address to itself.
	ErrSameAddress = errors.New("an address cannot be connected to itself")
	// ErrAlreadyLinked means the two addresses are already connected.
	ErrAlreadyLinked = errors.New("the addresses are already connected")
)

// AppendBalance records b as the new balance of a in the current dataset d of
// the cache folder dir. The caller holds the write lock (WaitLock) and opened
// d under it, so d is current and no fold runs meanwhile.
func AppendBalance(dir string, d *Dataset, a evm.Address, b evm.Wei) error {
	if _, ok := d.find(a); !ok {
		return ErrNotInDataset
	}
	var body [updateLen - 4]byte // appendRecord adds the CRC-32
	copy(body[:], a[:])
	copy(body[evm.AddressLen:], b[:])
	return appendRecord(filepath.Join(dir, d.Manifest.Folder), UpdatesFileName, body[:])
}

// AppendLink records a new link between a and b in the current dataset d of
// the cache folder dir, under the same conditions as AppendBalance. Both must
// be in d, be different, and not be connected already.
func AppendLink(dir string, d *Dataset, a, b evm.Address) error {
	ra, okA := d.find(a)
	rb, okB := d.find(b)
	switch {
	case !okA || !okB:
		return ErrNotInDataset
	case ra == rb:
		return ErrSameAddress
	case !d.Manifest.HasGraph():
		return errors.New("the dataset has no connections to add to")
	case d.Graph().Has(uint32(ra), uint32(rb)):
		return ErrAlreadyLinked
	}
	var body [linkLen - 4]byte // appendRecord adds the CRC-32
	copy(body[:], a[:])
	copy(body[evm.AddressLen:], b[:])
	return appendRecord(filepath.Join(dir, d.Manifest.Folder), LinksFileName, body[:])
}

// appendRecord appends body and its CRC-32 to folder's log name, and fsyncs
// it (and the folder, when the log is new, so its name survives a power cut).
func appendRecord(folder, name string, body []byte) error {
	path := filepath.Join(folder, name)
	_, statErr := os.Lstat(path)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	rec := binary.LittleEndian.AppendUint32(append([]byte(nil), body...), crc32.ChecksumIEEE(body))
	if _, err := f.Write(rec); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if errors.Is(statErr, fs.ErrNotExist) {
		return syncDir(folder)
	}
	return nil
}

// readRecords calls each with the body of every whole, intact record of
// folder's log name, of recLen bytes including the CRC-32, stopping at a torn
// last record. A missing log has no records.
func readRecords(folder, name string, recLen int, each func(body []byte)) error {
	b, err := os.ReadFile(filepath.Join(folder, name))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for ; len(b) >= recLen; b = b[recLen:] {
		body := b[:recLen-4]
		if crc32.ChecksumIEEE(body) != binary.LittleEndian.Uint32(b[recLen-4:recLen]) {
			break // a torn last record
		}
		each(body)
	}
	return nil
}

// readUpdates reads folder's balance log: each address's latest balance.
func readUpdates(folder string) (map[evm.Address]evm.Wei, error) {
	var updates map[evm.Address]evm.Wei
	err := readRecords(folder, UpdatesFileName, updateLen, func(body []byte) {
		var a evm.Address
		var w evm.Wei
		copy(a[:], body)
		copy(w[:], body[evm.AddressLen:])
		if updates == nil {
			updates = map[evm.Address]evm.Wei{}
		}
		updates[a] = w
	})
	return updates, err
}

// readLinks reads folder's link log as address pairs.
func readLinks(folder string) ([][2]evm.Address, error) {
	var links [][2]evm.Address
	err := readRecords(folder, LinksFileName, linkLen, func(body []byte) {
		var l [2]evm.Address
		copy(l[0][:], body)
		copy(l[1][:], body[evm.AddressLen:])
		links = append(links, l)
	})
	return links, err
}

// Fold makes d's pending updates part of the dataset: a new dataset folder
// with the added links merged into offsets.bin and neighbors.bin and the
// balance changes written into balances.bin, committed like any save. Every
// file without a pending change is a hard link to d's (a copy where the file
// system cannot link). The old folder, with its logs, is then removed. It
// returns how many balances and links were folded in and the new folder, or
// zeros and "" when there was nothing to fold. The caller holds the write
// lock, and d is the current dataset, opened under it.
func (c Cache) Fold(ctx context.Context, d *Dataset, progress func(int)) (balances, links int, folder string, err error) {
	balances, links = d.PendingBalances(), d.PendingLinks()
	if balances == 0 && links == 0 {
		return 0, 0, "", nil
	}
	draft, err := c.NewDraft()
	if err != nil {
		return 0, 0, "", err
	}
	defer draft.Discard()
	from := filepath.Join(c.Dir, d.Manifest.Folder)
	keep := []string{columns.AddressesFileName}
	if links == 0 && d.Manifest.HasGraph() {
		keep = append(keep, columns.OffsetsFileName, columns.NeighborsFileName)
	}
	if balances == 0 {
		keep = append(keep, columns.BalancesFileName)
	}
	for _, name := range keep {
		if err := linkOrCopy(filepath.Join(from, name), filepath.Join(draft.Path(), name)); err != nil {
			return 0, 0, "", err
		}
	}
	if links > 0 {
		offsets, neighbors, err := graph.WithExtra(d.Graph())
		if err != nil {
			return 0, 0, "", err
		}
		if err := draft.WriteGraph(ctx, offsets, neighbors); err != nil {
			return 0, 0, "", err
		}
	}
	if balances > 0 {
		all := func(yield func(*evm.Wei) bool) {
			for i := range d.Balances.Balances {
				b := d.Balance(i)
				if !yield(&b) {
					return
				}
			}
		}
		if err := draft.WriteBalances(ctx, all, progress); err != nil {
			return 0, 0, "", err
		}
	}
	if err := draft.Commit(d.Manifest, d.Manifest.Folder); err != nil {
		return 0, 0, "", err
	}
	return balances, links, draft.Path(), nil
}

// linkOrCopy makes dst the same file as src: a hard link, which costs
// nothing, or a copy, fsynced, when the file system cannot link. The column
// files are never written in place (columns.Write replaces them), so sharing
// them between folders is safe.
func linkOrCopy(src, dst string) error {
	if err := os.Link(src, dst); err == nil {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return fmt.Errorf("copying %s: %w", src, err)
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
