package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"

	"evmgraph/internal/evm"
	"evmgraph/internal/lookup"
	"evmgraph/internal/store"
)

const updateUsage = "usage: evmgraph update [-data folder] balance <address> <wei> | link <address> <address>"

// runUpdate is `evmgraph update [-data folder] balance <address> <wei>` and
// `evmgraph update [-data folder] link <address> <address>` (decision 014): it
// sets an address's balance, or connects two addresses, without rebuilding
// the dataset. The change is appended to a log in the dataset's folder and
// fsynced before it returns, so it survives a crash, and every lookup sees it
// from then on; `evmgraph run` folds the logs into the column files when it
// starts and stops. Only addresses already in the dataset can change.
//
// It takes the cache's write lock, so it waits while `evmgraph run` is
// generating or folding, but not while run is merely running.
func runUpdate(args []string) int {
	flags := flag.NewFlagSet("update", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	dir := flags.String("data", "cache", "folder that holds the dataset")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(logOut, updateUsage)
			return exitFound
		}
		ui.fail("%v; %s", err, updateUsage)
		return exitUsage
	}
	kind := flags.Arg(0)
	if flags.NArg() != 3 || kind != "balance" && kind != "link" {
		ui.fail("update changes one balance or adds one link; %s", updateUsage)
		return exitUsage
	}
	addr, ok := parseAddress(flags.Arg(1))
	if !ok {
		return exitUsage
	}
	var wei evm.Wei
	var other evm.Address
	if kind == "balance" {
		var err error
		if wei, err = evm.ParseWei(flags.Arg(2)); err != nil {
			ui.fail("%v", err)
			return exitUsage
		}
	} else if other, ok = parseAddress(flags.Arg(2)); !ok {
		return exitUsage
	}
	if kind == "link" && addr == other {
		ui.fail("%v: %s", store.ErrSameAddress, addr.Checksum())
		return exitUsage
	}
	if _, err := os.Stat(store.ManifestPath(*dir)); errors.Is(err, fs.ErrNotExist) {
		openForQuery(*dir) // reports the missing dataset in the usual words
		return exitNotFound
	}

	cache := store.Cache{Dir: *dir}
	lock, err := cache.TryLock()
	if errors.Is(err, store.ErrLocked) {
		ui.waitingForLock(*dir)
		lock, err = store.WaitLock(*dir)
	}
	if err != nil {
		ui.fail("%v", err)
		return exitNotFound
	}
	defer lock.Unlock()

	d, ok := openForQuery(*dir)
	if !ok {
		return exitNotFound
	}
	defer d.Close()
	if !d.Manifest.Current() {
		ui.fail("the dataset in %s is from an older version (manifest version %d); run evmgraph run -data %s once, "+
			"which generates it again, then update it", *dir, d.Manifest.Version, *dir)
		return exitNotFound
	}
	if kind == "balance" {
		err = store.AppendBalance(*dir, d, addr, wei)
	} else {
		err = store.AppendLink(*dir, d, addr, other)
	}
	switch {
	case errors.Is(err, store.ErrNotInDataset):
		missing := addr
		if _, found := lookup.Find(d.Addresses.Addrs, addr); found {
			missing = other
		}
		ui.notFound(missing, d.Manifest.Count, *dir)
		return exitNotFound
	case errors.Is(err, store.ErrAlreadyLinked):
		fmt.Printf("%s and %s are already connected\n", addr.Checksum(), other.Checksum())
		return exitFound
	case err != nil:
		ui.fail("%v", err)
		return exitNotFound
	case kind == "balance":
		fmt.Printf("%s ETH (%s wei)\n", wei.ETH(), wei)
	default:
		fmt.Printf("%s and %s are now connected\n", addr.Checksum(), other.Checksum())
	}
	return exitFound
}
