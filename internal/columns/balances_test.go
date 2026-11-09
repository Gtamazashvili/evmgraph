package columns

import (
	"context"
	"errors"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"evmgraph/internal/evm"
)

func testBalances(n int) []evm.Wei {
	b := make([]evm.Wei, n)
	for i := range b {
		b[i] = evm.WeiFromParts(uint64(i)*0x9e3779b97f4a7c15, uint64(i))
	}
	return b
}

// Balances follow the same rules as addresses: saved in parts as one file,
// progress per Batch and at the end, 16 bytes each, loaded back exactly, no
// temporary file left.
func TestBalancesRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data", BalancesFileName)
	all := testBalances(2*Batch + 3)
	all[0] = evm.WeiFromParts(math.MaxUint64, math.MaxUint64) // 2^128 − 1
	var progress []int
	if err := SaveBalances(context.Background(), path, func(n int) { progress = append(progress, n) }, all[:10], all[10:]); err != nil {
		t.Fatal(err)
	}
	if want := []int{Batch, 2 * Batch, len(all)}; !slices.Equal(progress, want) {
		t.Fatalf("progress %v, want %v", progress, want)
	}
	if info, _ := os.Stat(path); info.Size() != int64(len(all)*evm.WeiLen) {
		t.Fatalf("file is %d bytes, want %d", info.Size(), len(all)*evm.WeiLen)
	}
	noTempFiles(t, path)
	f, err := OpenBalances(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if !slices.Equal(f.Balances, all) {
		t.Fatal("loaded balances differ from saved ones")
	}
}

func TestBalancesEmptyAndInvalid(t *testing.T) {
	dir := t.TempDir()
	empty := filepath.Join(dir, "empty.bin")
	if err := SaveBalances(context.Background(), empty, nil); err != nil {
		t.Fatal(err)
	}
	if f, err := OpenBalances(empty); err != nil || len(f.Balances) != 0 {
		t.Fatalf("empty file: %v, %d balances", err, len(f.Balances))
	}
	partial := filepath.Join(dir, "partial.bin")
	if err := os.WriteFile(partial, make([]byte, 17), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenBalances(partial); !errors.Is(err, ErrInvalidFile) {
		t.Fatalf("17 bytes: got %v, want ErrInvalidFile", err)
	}
	if _, err := OpenBalances(filepath.Join(dir, "missing.bin")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing: got %v, want fs.ErrNotExist", err)
	}
}

// A cancelled save keeps the previous file.
func TestBalancesCancelledSaveKeepsPreviousFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), BalancesFileName)
	old := testBalances(100)
	if err := SaveBalances(context.Background(), path, nil, old); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := SaveBalances(ctx, path, nil, testBalances(Batch+1)); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context.Canceled", err)
	}
	noTempFiles(t, path)
	f, err := OpenBalances(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if !slices.Equal(f.Balances, old) {
		t.Fatal("the previous file was changed")
	}
}
