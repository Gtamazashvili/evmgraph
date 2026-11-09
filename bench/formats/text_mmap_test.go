package formats

// Experiments for the text format with mmap, beyond what the Format
// interface can express. Run with:
//
//	go test ./bench/formats -run '^$' -bench 'TextDecode|TextView' -benchmem -count 5
//
// TextDecode isolates parallel decoding: the same mapped file decoded by one
// core and by all of them. TextView maps the file and decodes a line only
// when that address is asked for, so opening does no decoding at all. It is
// not an Format, because the interface hands back a decoded
// []evm.Address, and is not used by the program.

import (
	"context"
	"path/filepath"
	"runtime"
	"slices"
	"syscall"
	"testing"

	"evmgraph/internal/columns"
	"evmgraph/internal/evm"
)

// textView is a mapped text address file that decodes on demand.
type textView struct{ data []byte }

func openTextView(path string) (*textView, error) {
	data, err := columns.MapFile(path, textLineLen)
	return &textView{data}, err
}

func (v *textView) Len() int { return len(v.data) / textLineLen }

func (v *textView) At(i int) (evm.Address, error) {
	var a evm.Address
	err := decodeTextLine(v.data[i*textLineLen:(i+1)*textLineLen], &a)
	return a, err
}

func (v *textView) Close() error {
	if v.data == nil {
		return nil
	}
	return syscall.Munmap(v.data)
}

func TestTextViewMatches(t *testing.T) {
	want := testAddresses(5000, 6)
	path := filepath.Join(t.TempDir(), "addresses.txt")
	if err := (Text{}).Save(context.Background(), path, nil, want); err != nil {
		t.Fatal(err)
	}
	v, err := openTextView(path)
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	got := make([]evm.Address, v.Len())
	for i := range got {
		if got[i], err = v.At(i); err != nil {
			t.Fatal(err)
		}
	}
	if !slices.Equal(got, want) {
		t.Fatal("the view decodes different addresses than were saved")
	}
}

func TestDecodeTextLinesAnyWorkerCount(t *testing.T) {
	want := testAddresses(1001, 7)
	path := filepath.Join(t.TempDir(), "addresses.txt")
	if err := (Text{}).Save(context.Background(), path, nil, want); err != nil {
		t.Fatal(err)
	}
	data, err := columns.MapFile(path, textLineLen)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Munmap(data)
	for _, workers := range []int{1, 2, 3, 8, 2000} {
		got, err := decodeTextLines(data, workers)
		if err != nil || !slices.Equal(got, want) {
			t.Fatalf("%d workers: wrong result (%v)", workers, err)
		}
	}
}

func textBenchFile(b *testing.B) string {
	path := filepath.Join(b.TempDir(), "addresses.txt")
	if err := (Text{}).Save(context.Background(), path, nil, testAddresses(benchN, 1)); err != nil {
		b.Fatal(err)
	}
	return path
}

func BenchmarkTextDecode(b *testing.B) {
	path := textBenchFile(b)
	data, err := columns.MapFile(path, textLineLen)
	if err != nil {
		b.Fatal(err)
	}
	defer syscall.Munmap(data)
	for _, workers := range []int{1, runtime.GOMAXPROCS(0)} {
		b.Run(map[bool]string{true: "1-core", false: "all-cores"}[workers == 1], func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := decodeTextLines(data, workers); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkTextView(b *testing.B) {
	path := textBenchFile(b)
	for _, use := range []struct {
		name string
		fn   func(v *textView)
	}{
		{"open", func(*textView) {}},
		{"one-lookup", func(v *textView) {
			a, _ := v.At(v.Len() / 2)
			sink ^= a[0]
		}},
		{"read-all", func(v *textView) {
			var x byte
			for i := range v.Len() {
				a, _ := v.At(i)
				x ^= a[0] ^ a[19]
			}
			sink ^= x
		}},
	} {
		b.Run(use.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				v, err := openTextView(path)
				if err != nil {
					b.Fatal(err)
				}
				use.fn(v)
				v.Close()
			}
		})
	}
}
