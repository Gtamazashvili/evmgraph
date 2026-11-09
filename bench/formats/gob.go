package formats

import (
	"bufio"
	"context"
	"encoding/gob"
	"io"
	"iter"

	"evmgraph/internal/columns"
	"evmgraph/internal/evm"
)

// Gob stores the addresses with Go's own binary encoding, as a stream of
// batches of up to columns.Batch addresses each.
type Gob struct{}

func (Gob) Name() string     { return "gob" }
func (Gob) FileName() string { return "addresses.gob" }

// MaxBytesPerAddress allows 20 bytes, gob's per-element length prefix and a
// share of each batch's header.
func (Gob) MaxBytesPerAddress() int { return 32 }

func (c Gob) Save(ctx context.Context, path string, progress func(int), parts ...[]evm.Address) error {
	return c.SaveSeq(ctx, path, progress, columns.Parts(parts...))
}

func (Gob) SaveSeq(ctx context.Context, path string, progress func(int), addrs iter.Seq[*evm.Address]) error {
	return columns.Write(ctx, path, progress, addrs, func(w io.Writer) (columns.RecordWriter[evm.Address], error) {
		return &gobWriter{enc: gob.NewEncoder(w)}, nil
	})
}

func (Gob) Load(path string) (*columns.AddressFile, error) {
	return loadDecoded(path, 31, func(r *bufio.Reader, addrs []evm.Address) ([]evm.Address, error) {
		dec := gob.NewDecoder(r)
		for {
			var batch []evm.Address
			if err := dec.Decode(&batch); err == io.EOF {
				return addrs, nil
			} else if err != nil {
				return nil, err
			}
			addrs = append(addrs, batch...)
		}
	})
}

type gobWriter struct {
	enc   *gob.Encoder
	batch []evm.Address
}

func (w *gobWriter) Write(_ int, a *evm.Address) error {
	w.batch = append(w.batch, *a)
	if len(w.batch) == columns.Batch {
		return w.flush()
	}
	return nil
}

func (w *gobWriter) Close() error { return w.flush() }

func (w *gobWriter) flush() error {
	if len(w.batch) == 0 {
		return nil
	}
	err := w.enc.Encode(w.batch)
	w.batch = w.batch[:0]
	return err
}
