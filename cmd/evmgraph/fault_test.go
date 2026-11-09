package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"evmgraph/internal/columns"
	"evmgraph/internal/evm"
)

// A dataset file another program shrinks while a command has it mapped makes
// the next read past the new end fault. The command reports it and exits 1
// instead of crashing with a bus error.
func TestShrunkFileIsAnErrorNotACrash(t *testing.T) {
	path := filepath.Join(t.TempDir(), columns.AddressesFileName)
	addrs := make([]evm.Address, 100_000)
	for i := range addrs {
		addrs[i][0] = byte(i)
	}
	must(t, columns.SaveAddresses(t.Context(), path, nil, columns.Parts(addrs)))
	f, err := columns.OpenAddresses(path)
	must(t, err)
	defer f.Close()

	buf := captureLog(t, false)
	code := guardFaults(func() int {
		must(t, os.Truncate(path, 0)) // another program empties the file
		for _, a := range f.Addrs[len(f.Addrs)-1000:] {
			sink ^= a[0] // a package variable, so the reads cannot be left out
		}
		return exitFound
	})
	if code != exitNotFound || !strings.Contains(buf.String(), "changed while it was being read") {
		t.Fatalf("exit %d, log %q", code, buf.String())
	}
}

// sink keeps the test's reads of the mapped file.
var sink byte
