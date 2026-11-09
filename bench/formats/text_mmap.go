//go:build unix

package formats

import (
	"context"
	"encoding/hex"
	"fmt"
	"iter"
	"runtime"
	"sync"
	"syscall"

	"evmgraph/internal/columns"
	"evmgraph/internal/evm"
)

// TextMapped stores the same file as Text (one "0x..." line per address) but
// loads it differently: it maps the file instead of reading it, and decodes
// the lines on all CPU cores at once. That works because every line is
// exactly textLineLen bytes, so line i starts at byte i × textLineLen and the
// file splits into independent chunks. It exists to measure how much of the
// gap to binary mmap and parallel decoding can close for a text format.
type TextMapped struct{}

func (TextMapped) Name() string            { return "text-mmap" }
func (TextMapped) FileName() string        { return "addresses.mmap.txt" }
func (TextMapped) MaxBytesPerAddress() int { return textLineLen }

func (TextMapped) Save(ctx context.Context, path string, progress func(int), parts ...[]evm.Address) error {
	return Text{}.Save(ctx, path, progress, parts...)
}

func (TextMapped) SaveSeq(ctx context.Context, path string, progress func(int), addrs iter.Seq[*evm.Address]) error {
	return Text{}.SaveSeq(ctx, path, progress, addrs)
}

func (TextMapped) Load(path string) (*columns.AddressFile, error) {
	data, err := columns.MapFile(path, textLineLen)
	if err != nil {
		return nil, err
	}
	if data == nil {
		return &columns.AddressFile{}, nil // an empty file
	}
	defer syscall.Munmap(data) // the decoded addresses do not point into it

	addrs, err := decodeTextLines(data, runtime.GOMAXPROCS(0))
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %v", columns.ErrInvalidFile, path, err)
	}
	return &columns.AddressFile{Addrs: addrs}, nil
}

// decodeTextLines decodes fixed-width "0x...\n" lines, splitting them into one
// chunk per worker.
func decodeTextLines(data []byte, workers int) ([]evm.Address, error) {
	addrs := make([]evm.Address, len(data)/textLineLen)
	per := (len(addrs) + workers - 1) / workers
	errs := make([]error, workers)
	var wg sync.WaitGroup
	for w := range workers {
		from, to := w*per, min((w+1)*per, len(addrs))
		if from >= to {
			break
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := from; i < to; i++ {
				if err := decodeTextLine(data[i*textLineLen:(i+1)*textLineLen], &addrs[i]); err != nil {
					errs[w] = fmt.Errorf("line %d: %v", i+1, err)
					return
				}
			}
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	return addrs, nil
}

// decodeTextLine decodes one "0x" + 40 hex digits + "\n" line into a.
func decodeTextLine(line []byte, a *evm.Address) error {
	if line[0] != '0' || line[1] != 'x' || line[textLineLen-1] != '\n' {
		return fmt.Errorf("bad line %q", line)
	}
	_, err := hex.Decode(a[:], line[2:textLineLen-1])
	return err
}
