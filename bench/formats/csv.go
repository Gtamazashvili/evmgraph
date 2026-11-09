package formats

import (
	"bufio"
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"iter"
	"strconv"

	"evmgraph/internal/columns"
	"evmgraph/internal/evm"
)

// CSV stores a header, then one "id,address" line per address: the shape of a
// spreadsheet export.
type CSV struct{}

func (CSV) Name() string     { return "csv" }
func (CSV) FileName() string { return "addresses.csv" }

// MaxBytesPerAddress allows an ID of up to 19 digits, a comma, the address and
// a newline.
func (CSV) MaxBytesPerAddress() int { return 19 + 1 + textLineLen }

func (c CSV) Save(ctx context.Context, path string, progress func(int), parts ...[]evm.Address) error {
	return c.SaveSeq(ctx, path, progress, columns.Parts(parts...))
}

func (CSV) SaveSeq(ctx context.Context, path string, progress func(int), addrs iter.Seq[*evm.Address]) error {
	return columns.Write(ctx, path, progress, addrs, func(w io.Writer) (columns.RecordWriter[evm.Address], error) {
		cw := csv.NewWriter(w)
		return &csvWriter{cw: cw}, cw.Write([]string{"id", "address"})
	})
}

func (CSV) Load(path string) (*columns.AddressFile, error) {
	return loadDecoded(path, 50, func(r *bufio.Reader, addrs []evm.Address) ([]evm.Address, error) {
		cr := csv.NewReader(r)
		cr.FieldsPerRecord = 2
		cr.ReuseRecord = true
		header, err := cr.Read()
		if err == io.EOF {
			return addrs, nil
		}
		if err != nil {
			return nil, err
		}
		if header[0] != "id" || header[1] != "address" {
			return nil, fmt.Errorf("unexpected header %q", header)
		}
		for {
			rec, err := cr.Read()
			if err == io.EOF {
				return addrs, nil
			}
			if err != nil {
				return nil, err
			}
			a, err := evm.ParseHex([]byte(rec[1]))
			if err != nil {
				return nil, err
			}
			addrs = append(addrs, a)
		}
	})
}

type csvWriter struct{ cw *csv.Writer }

func (w *csvWriter) Write(i int, a *evm.Address) error {
	return w.cw.Write([]string{strconv.Itoa(i), a.Hex()})
}

func (w *csvWriter) Close() error { w.cw.Flush(); return w.cw.Error() }
