package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"

	"evmgraph/internal/evm"
	"evmgraph/internal/graph"
	"evmgraph/internal/lookup"
)

const connectionsUsage = "usage: evmgraph connections [-data folder] [-limit N | -all] [-timeout D] <address>"

// runConnections is `evmgraph connections [-data folder] [-limit N | -all]
// <address>` (brief item 3b): the address's direct connections and their
// connections, two levels deep (decision 004). Each address is reported once,
// at its shortest level; the queried address is left out; lines are ordered
// by level, then address. At most -limit lines are printed (100 by default,
// decision 005), and the exact total of each level is always reported. It only
// reads, like balance, and sees what was saved.
func runConnections(args []string) int {
	flags := flag.NewFlagSet("connections", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	dir := flags.String("data", "cache", "folder that holds the dataset")
	limit := flags.Int("limit", 100, "print at most this many addresses; the totals are always exact")
	all := flags.Bool("all", false, "print every address")
	timeout := flags.Duration("timeout", 0, "give up after this long, such as 5s (0: no limit)")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(logOut, connectionsUsage)
			return exitFound
		}
		ui.fail("%v; %s", err, connectionsUsage)
		return exitUsage
	}
	if flags.NArg() != 1 {
		ui.fail("connections needs exactly one address; %s", connectionsUsage)
		return exitUsage
	}
	if *limit < 0 || *timeout < 0 {
		ui.fail("-limit and -timeout must be zero or more; %s", connectionsUsage)
		return exitUsage
	}
	// The deadline covers the whole query: opening, searching and printing.
	ctx := context.Background()
	if *timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *timeout)
		defer cancel()
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
	if !d.Manifest.HasGraph() {
		noGraph(d, *dir)
		return exitNotFound
	}

	row, ok := lookup.Find(d.Addresses.Addrs, addr)
	if !ok {
		ui.notFound(addr, d.Manifest.Count, *dir)
		return exitNotFound
	}
	res, err := graph.NewSearch(d.Graph()).RunContext(ctx, uint32(row))
	if errors.Is(err, context.DeadlineExceeded) {
		ui.fail("the query took longer than -timeout %v and was stopped; nothing was printed", *timeout)
		return exitDeadline
	}
	if err != nil {
		ui.fail("%v; run evmgraph run -data %s once to set the dataset aside and generate it again", err, *dir)
		return exitNotFound
	}
	searched := clock()

	room := *limit
	if *all {
		room = -1
	}
	out := bufio.NewWriterSize(os.Stdout, 64<<10)
	shown, late := writeConnections(ctx, out, d.Addresses.Addrs, res, room)
	out.Flush()

	ui.connectionsFound(len(res.Level1), res.Level2Count, shown, searched.Sub(began), *all)
	if late {
		ui.fail("printing took longer than -timeout %v and was stopped after %d lines; the totals above are exact", *timeout, shown)
		return exitDeadline
	}
	return exitFound
}

// writeConnections prints res as "level address" lines, level 1 then level 2,
// each address in EIP-55 form (decision 007), at most room lines (room < 0:
// all of them). It stops early when ctx ends, with late true, or when a write
// fails. (When `| head` closes stdout, Go's runtime ends the process with
// SIGPIPE at the failed write, which is the usual, quiet end of a pipe.)
// connections and search share it.
func writeConnections(ctx context.Context, out *bufio.Writer, addrs []evm.Address, res graph.Result, room int) (shown int, late bool) {
	var sum evm.Checksummer
	line := make([]byte, 0, 64)
	print := func(level int, r uint32) bool {
		if room == 0 {
			return false
		}
		if shown%4096 == 0 && ctx.Err() != nil {
			late = true
			return false
		}
		line = append(strconv.AppendInt(line[:0], int64(level), 10), ' ')
		line = append(sum.Append(line, addrs[r]), '\n')
		if _, err := out.Write(line); err != nil {
			return false // stdout closed, as by `| head`
		}
		shown++
		if room > 0 {
			room--
		}
		return true
	}
	for _, r := range res.Level1 {
		if !print(1, r) {
			return shown, late
		}
	}
	res.Level2(func(r uint32) bool { return print(2, r) })
	return shown, late
}
