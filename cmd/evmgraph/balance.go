package main

import (
	"errors"
	"flag"
	"fmt"
	"io"

	"evmgraph/internal/lookup"
)

const balanceUsage = "usage: evmgraph balance [-data folder] <address>"

// runBalance is `evmgraph balance [-data folder] <address>` (brief item 3a).
// It prints the address's balance from the saved dataset to stdout, in ETH
// with all 18 decimals and in wei (decision 006). It only reads: it creates,
// moves and cleans nothing, so it can run while the main program runs. It sees
// the saved dataset and the balance updates not yet folded in (decision 014);
// the main program saves right after generating (decision 002).
func runBalance(args []string) int {
	flags := flag.NewFlagSet("balance", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	dir := flags.String("data", "cache", "folder that holds the dataset")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(logOut, balanceUsage)
			return exitFound
		}
		ui.fail("%v; %s", err, balanceUsage)
		return exitUsage
	}
	if flags.NArg() != 1 {
		ui.fail("balance needs exactly one address; %s", balanceUsage)
		return exitUsage
	}
	addr, ok := parseAddress(flags.Arg(0))
	if !ok {
		return exitUsage
	}

	began := clock()
	d, ok := openForQuery(*dir)
	if !ok {
		return exitNotFound
	}
	defer d.Close()
	opened := clock()

	row, ok := lookup.Find(d.Addresses.Addrs, addr)
	found := clock()
	if !ok {
		ui.notFound(addr, d.Manifest.Count, *dir)
		return exitNotFound
	}
	b := d.Balance(row)
	fmt.Printf("%s ETH (%s wei)\n", b.ETH(), b)
	ui.found(row, d.Manifest.Count, opened.Sub(began), found.Sub(opened), d.Addresses.Mapped())
	return exitFound
}
