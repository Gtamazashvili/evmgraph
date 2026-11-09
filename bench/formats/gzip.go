package formats

import (
	"bufio"
	"compress/gzip"
	"context"
	"io"
	"iter"

	"evmgraph/internal/columns"
	"evmgraph/internal/evm"
)

// Gzip stores the binary format compressed with gzip. Random addresses do not
// compress, so it costs compression time and saves no space; it is here to
// measure exactly that.
type Gzip struct{}

func (Gzip) Name() string            { return "gzip" }
func (Gzip) FileName() string        { return "addresses.bin.gz" }
func (Gzip) MaxBytesPerAddress() int { return evm.AddressLen + 1 }

func (c Gzip) Save(ctx context.Context, path string, progress func(int), parts ...[]evm.Address) error {
	return c.SaveSeq(ctx, path, progress, columns.Parts(parts...))
}

func (Gzip) SaveSeq(ctx context.Context, path string, progress func(int), addrs iter.Seq[*evm.Address]) error {
	return columns.Write(ctx, path, progress, addrs, func(w io.Writer) (columns.RecordWriter[evm.Address], error) {
		return &gzipWriter{zw: gzip.NewWriter(w)}, nil
	})
}

func (Gzip) Load(path string) (*columns.AddressFile, error) {
	return loadDecoded(path, evm.AddressLen, func(r *bufio.Reader, addrs []evm.Address) ([]evm.Address, error) {
		zr, err := gzip.NewReader(r)
		if err != nil {
			return nil, err
		}
		var a evm.Address
		for {
			if _, err := io.ReadFull(zr, a[:]); err == io.EOF {
				return addrs, nil
			} else if err != nil {
				return nil, err
			}
			addrs = append(addrs, a)
		}
	})
}

type gzipWriter struct{ zw *gzip.Writer }

func (w *gzipWriter) Write(_ int, a *evm.Address) error { _, err := w.zw.Write(a[:]); return err }
func (w *gzipWriter) Close() error                      { return w.zw.Close() }
