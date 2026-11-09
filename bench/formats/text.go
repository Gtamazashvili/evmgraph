package formats

import (
	"bufio"
	"context"
	"encoding/hex"
	"io"
	"iter"

	"evmgraph/internal/columns"
	"evmgraph/internal/evm"
)

// Text stores one "0x..." address per line, the way addresses are printed.
type Text struct{}

const textLineLen = 2 + 2*evm.AddressLen + 1 // "0x" + 40 hex digits + newline

func (Text) Name() string            { return "text" }
func (Text) FileName() string        { return "addresses.txt" }
func (Text) MaxBytesPerAddress() int { return textLineLen }

func (c Text) Save(ctx context.Context, path string, progress func(int), parts ...[]evm.Address) error {
	return c.SaveSeq(ctx, path, progress, columns.Parts(parts...))
}

func (Text) SaveSeq(ctx context.Context, path string, progress func(int), addrs iter.Seq[*evm.Address]) error {
	return columns.Write(ctx, path, progress, addrs, func(w io.Writer) (columns.RecordWriter[evm.Address], error) {
		return &textWriter{w: w}, nil
	})
}

func (Text) Load(path string) (*columns.AddressFile, error) {
	return loadDecoded(path, textLineLen, func(r *bufio.Reader, addrs []evm.Address) ([]evm.Address, error) {
		s := bufio.NewScanner(r)
		for s.Scan() {
			a, err := evm.ParseHex(s.Bytes())
			if err != nil {
				return nil, err
			}
			addrs = append(addrs, a)
		}
		return addrs, s.Err()
	})
}

type textWriter struct {
	w    io.Writer
	line [textLineLen]byte
}

func (w *textWriter) Write(_ int, a *evm.Address) error {
	w.line[0], w.line[1] = '0', 'x'
	hex.Encode(w.line[2:], a[:])
	w.line[textLineLen-1] = '\n'
	_, err := w.w.Write(w.line[:])
	return err
}

func (w *textWriter) Close() error { return nil }
