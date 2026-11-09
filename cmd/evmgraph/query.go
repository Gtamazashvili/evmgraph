package main

import (
	"errors"

	"evmgraph/internal/evm"
	"evmgraph/internal/store"
)

// What the read-only commands (balance, connections, search) share. They
// open the saved dataset without changing anything, so they can run beside
// `evmgraph run`, and they see what was saved.

// Exit codes of every command.
const (
	exitFound    = 0
	exitNotFound = 1 // not in the dataset, no dataset to search, or another error
	exitUsage    = 2 // malformed address or arguments
	exitDeadline = 3 // a deadline (-timeout, -generate-timeout, -save-timeout) passed first
)

// openForQuery opens the saved dataset in dir, or reports why it cannot and
// returns false; the caller exits with exitNotFound.
func openForQuery(dir string) (*store.Dataset, bool) {
	d, err := store.OpenReadOnly(dir)
	switch {
	case errors.Is(err, store.ErrNoDataset):
		ui.fail("no dataset in %s; generate one first: evmgraph run -data %s -n <count>", dir, dir)
	case errors.Is(err, store.ErrUnsorted):
		ui.fail("the dataset in %s is from an older version, in generation order, and cannot be searched; "+
			"run evmgraph run -data %s once and stop it, which generates it again", dir, dir)
	case err != nil:
		ui.fail("%v", err)
	default:
		return d, true
	}
	return nil, false
}

// noGraph reports a dataset saved before connections existed.
func noGraph(d *store.Dataset, dir string) {
	ui.fail("the dataset in %s was made before connections existed (manifest version %d); "+
		"run evmgraph run -data %s once and stop it, which generates it again with connections", dir, d.Manifest.Version, dir)
}

// parseAddress reads the address argument (decision 007): 40 hex digits with
// or without 0x, any letter case, and a mixed-case address must match its
// EIP-55 checksum. On failure it reports why and returns false; the caller
// exits with exitUsage.
func parseAddress(arg string) (evm.Address, bool) {
	a, err := evm.Parse(arg)
	switch {
	case errors.Is(err, evm.ErrChecksum):
		ui.fail("%q does not match its EIP-55 checksum (the letter case): a character is probably mistyped; "+
			"check it, or type the address in lower case to skip the check", arg)
		return a, false
	case err != nil:
		ui.fail("%q is not an address: expected 40 hex digits, with or without 0x", arg)
		return a, false
	}
	return a, true
}
