package formats

import (
	"bufio"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"iter"

	"evmgraph/internal/columns"
	"evmgraph/internal/evm"
)

// JSON stores one array of "0x..." strings. It is written and read as a
// stream, so neither side holds the whole document in memory as text.
type JSON struct{}

func (JSON) Name() string     { return "json" }
func (JSON) FileName() string { return "addresses.json" }

// MaxBytesPerAddress is 45 bytes per item (comma, quotes, "0x", 40 hex digits)
// plus room for the 3 bytes of "[" and "]\n" around the array.
func (JSON) MaxBytesPerAddress() int { return 48 }

func (c JSON) Save(ctx context.Context, path string, progress func(int), parts ...[]evm.Address) error {
	return c.SaveSeq(ctx, path, progress, columns.Parts(parts...))
}

func (JSON) SaveSeq(ctx context.Context, path string, progress func(int), addrs iter.Seq[*evm.Address]) error {
	return columns.Write(ctx, path, progress, addrs, func(w io.Writer) (columns.RecordWriter[evm.Address], error) {
		_, err := io.WriteString(w, "[")
		return &jsonWriter{w: w}, err
	})
}

func (JSON) Load(path string) (*columns.AddressFile, error) {
	return loadDecoded(path, 45, func(r *bufio.Reader, addrs []evm.Address) ([]evm.Address, error) {
		dec := json.NewDecoder(r)
		if tok, err := dec.Token(); err != nil {
			return nil, err
		} else if tok != json.Delim('[') {
			return nil, fmt.Errorf("expected a JSON array, found %v", tok)
		}
		for dec.More() {
			var s string
			if err := dec.Decode(&s); err != nil {
				return nil, err
			}
			a, err := evm.ParseHex([]byte(s))
			if err != nil {
				return nil, err
			}
			addrs = append(addrs, a)
		}
		if _, err := dec.Token(); err != nil { // the closing ']'
			return nil, err
		}
		return addrs, nil
	})
}

type jsonWriter struct {
	w    io.Writer
	item [1 + 1 + 2 + 2*evm.AddressLen + 1]byte // comma, quote, "0x", hex, quote
}

func (w *jsonWriter) Write(i int, a *evm.Address) error {
	b := w.item[:0]
	if i > 0 {
		b = append(b, ',')
	}
	b = append(b, `"0x`...)
	b = hex.AppendEncode(b, a[:])
	b = append(b, '"')
	_, err := w.w.Write(b)
	return err
}

func (w *jsonWriter) Close() error { _, err := io.WriteString(w.w, "]\n"); return err }
