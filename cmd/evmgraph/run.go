package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"iter"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"syscall"
	"time"

	"evmgraph/internal/capacity"
	"evmgraph/internal/columns"
	"evmgraph/internal/evm"
	"evmgraph/internal/generate"
	"evmgraph/internal/graph"
	"evmgraph/internal/order"
	"evmgraph/internal/store"
)

const runUsage = "usage: evmgraph run [-n count] [-seed N] [-data folder] [-degree D] [-skew S] [-show N] " +
	"[-generate-timeout D] [-save-timeout D]"

// shutdownSignals are Ctrl+C (SIGINT), docker stop (SIGTERM), and a closed
// terminal or dropped SSH session (SIGHUP). SIGHUP is left out when the program
// starts with it ignored, as under nohup: listening for it would undo nohup,
// and the program would stop when the terminal closes.
var shutdownSignals = func() []os.Signal {
	s := []os.Signal{os.Interrupt, syscall.SIGTERM}
	if !signal.Ignored(syscall.SIGHUP) {
		s = append(s, syscall.SIGHUP)
	}
	return s
}()

// runRun is `evmgraph run [flags]`, the program the brief describes: it
// loads the saved dataset, or generates and saves one, runs until it is told
// to stop, and leaves the data preserved in the cache folder.
func runRun(args []string) int {
	flags := flag.NewFlagSet("run", flag.ContinueOnError)
	flags.SetOutput(logOut)
	flags.Usage = func() {
		fmt.Fprintln(flags.Output(), runUsage+"\n\nflags:")
		flags.PrintDefaults()
	}
	n := flags.Int("n", 20_000_000, "number of addresses the dataset holds (decision 013)")
	seed := flags.Uint64("seed", 100000, "random seed; the same seed gives the same addresses")
	dir := flags.String("data", "cache", "folder that holds the generated data")
	show := flags.Int("show", 10, "number of addresses to print")
	degree := flags.Int("degree", 10, "average connections per address, even (decision 011)")
	skew := flags.Int("skew", 3, "hub strength: 1 = no hubs, 3 = like real chains, up to 4 (decision 011)")
	generateTimeout := flags.Duration("generate-timeout", 0,
		"give up generating (addresses, balances, connections) after this long, such as 5m; nothing new is saved then (0: no limit)")
	saveTimeout := flags.Duration("save-timeout", 0,
		"give up a save after this long, such as 9s to finish inside docker stop's 10 s; the previous dataset is kept (0: no limit)")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitFound
		}
		return exitUsage
	}
	// flag.Parse stops at the first argument that is not a flag. Without this
	// check, an argument such as a command after the flags would be ignored.
	if flags.NArg() > 0 {
		ui.fail("unexpected argument %q: run takes only flags; %s", flags.Arg(0), runUsage)
		return exitUsage
	}
	if *n < 0 {
		ui.fail("-n must be zero or more")
		return exitUsage
	}
	for _, err := range []error{store.CheckDegree(*degree), store.CheckSkew(*skew)} {
		if err != nil {
			ui.fail("-%v", err)
			return exitUsage
		}
	}
	if *generateTimeout < 0 || *saveTimeout < 0 {
		ui.fail("-generate-timeout and -save-timeout must be zero or more")
		return exitUsage
	}

	// A shutdown signal cancels ctx instead of killing the process, so the
	// program can stop at a safe point.
	ctx, stop := signal.NotifyContext(context.Background(), shutdownSignals...)
	err := runWith(ctx, settings{n: *n, seed: *seed, degree: *degree, skew: *skew, dir: *dir, show: *show,
		generateTimeout: *generateTimeout, saveTimeout: *saveTimeout})
	stop()

	switch {
	case errors.Is(err, context.DeadlineExceeded):
		ui.fail("%v", err)
		return exitDeadline
	case errors.Is(err, context.Canceled):
		ui.interrupted()
		return 130
	case err != nil:
		ui.fail("%v", err)
		return exitNotFound
	}
	ui.summary()
	return exitFound
}

// settings are run's flags.
type settings struct {
	n            int
	seed         uint64
	degree, skew int
	dir          string
	show         int
	// Deadlines; 0 means none. generateTimeout covers generating the rows,
	// sorting them and building the connections; saveTimeout each save (the
	// new dataset, and folding balance updates in). When one passes, nothing
	// new is saved, the previous dataset stays current, and the error wraps
	// context.DeadlineExceeded.
	generateTimeout, saveTimeout time.Duration
}

// run is runWith at the default degree and skew; tests use it.
func run(ctx context.Context, n int, seed uint64, dir string, show int) error {
	return runWith(ctx, settings{n: n, seed: seed, degree: 10, skew: 3, dir: dir, show: show})
}

// runWith is the program's life:
//
//  1. Take the cache's write lock, so no other program writes it meanwhile.
//  2. Load the saved dataset and fold in any balance updates waiting in its
//     log; or generate a dataset of s.n addresses (also when the saved one
//     is smaller or from an older version), with balances and connections,
//     and save it at once (decision 002). A signal before the save stops the
//     work and saves nothing new; one during the save lets it finish.
//  3. Release the lock and wait for a shutdown signal. Lookups and updates
//     run meanwhile as other processes.
//  4. On the signal, fold in the balance updates made while it ran.
//
// The data is on disk from step 2 on: a crash or kill -9 later loses
// nothing, and nothing is held in memory while it waits.
//
// A dataset grows by being generated again in full from its seed (decision
// 012): new addresses add entries to old addresses' lists, so the graph is
// rebuilt anyway. The addresses it already held keep their saved balances,
// so balance updates survive the growth.
func runWith(ctx context.Context, s settings) error {
	n, seed, dir := s.n, s.seed, s.dir
	degree, skew := s.degree, s.skew
	ui.banner(n, seed, degree, skew, dir)
	cache := store.Cache{Dir: dir, Events: ui}
	if err := cache.Prepare(); err != nil {
		return err
	}
	lock, err := cache.TryLock()
	if errors.Is(err, store.ErrLocked) {
		return fmt.Errorf("another evmgraph is writing %s right now (a run, or an update); wait for it, then try again", dir)
	}
	if err != nil {
		return err
	}
	defer lock.Unlock()

	// A missing or broken dataset is not fatal: it is generated from the
	// seed, so it can always be made again. Anything in the way is moved
	// aside, not deleted (package store). Other errors, such as a missing
	// permission, are returned, so a good dataset is never touched.
	began := clock()
	data, err := cache.Open()
	if err != nil {
		return err
	}
	var (
		have     int
		previous string // the current dataset's folder, replaced on save
		outdated bool   // from an older version: generated again
	)
	if data != nil {
		defer func() {
			if data != nil {
				data.Close()
			}
		}()
		m := data.Manifest
		ui.loaded(m.Count, filepath.Join(dir, m.Folder), clock().Sub(began), data.Addresses.Mapped())
		previous, have = m.Folder, m.Count
		if m.Seed != seed {
			ui.override(dir, "seed", fmt.Sprint(m.Seed), fmt.Sprint(seed))
			seed = m.Seed
		}
		if m.HasGraph() {
			if m.Degree != degree {
				ui.override(dir, "degree", fmt.Sprint(m.Degree), fmt.Sprint(degree))
				degree = m.Degree
			}
			if m.Skew != skew {
				ui.override(dir, "skew", fmt.Sprint(m.Skew), fmt.Sprint(skew))
				skew = m.Skew
			}
		}
		if !m.Current() {
			outdated = true
			ui.outdated(dir, m.Version)
		}
	} else {
		ui.noCache(dir)
	}
	if err := cache.Clean(previous); err != nil {
		return err
	}

	if data != nil && n <= have && !outdated {
		if n < have {
			ui.keepingAll(have, n)
		}
		if err := foldUpdates(ctx, cache, data, s.saveTimeout); err != nil {
			return err
		}
		printDataset(s.show, data.Rows())
		data.Close()
		data = nil
		lock.Unlock()
		waitForShutdown(ctx)
		return foldAtShutdown(ctx, cache, s.saveTimeout)
	}

	// Generate the whole dataset: n addresses, or the outdated one's size.
	target := max(n, have)
	diskBytes := evm.AddressLen + evm.WeiLen + 4 + 4*degree // per address: the four column files
	if entries := uint64(target) * uint64(degree); entries > graph.MaxEntries {
		return fmt.Errorf("cannot connect %d addresses at degree %d: that is %d neighbour entries, and 4-byte offsets allow %d; "+
			"use -n %d or less", target, degree, entries, uint64(graph.MaxEntries), uint64(graph.MaxEntries)/uint64(max(degree, 1)))
	}
	if err := capacity.Check(dir, capacity.Need{
		// rows, then while the graph is built: position → row, degrees,
		// offsets and the neighbour entries
		New: target, NewBytes: rowLen + 3*4 + 4*degree,
		Total: target, DiskBytes: diskBytes,
	}); err != nil {
		return err
	}
	if have > 0 && !outdated {
		ui.topUp(have, target)
	}
	// Until the save, a shutdown signal or the -generate-timeout deadline
	// stops the work at once and nothing new is saved: the previous dataset,
	// if any, stays current, and the next run generates exactly the same
	// dataset again from the seed. Stopping fast matters under docker stop,
	// which kills 10 s after its signal; at 20M, sorting, building and saving
	// take about that long.
	work, cancelWork := ctx, context.CancelFunc(func() {})
	if s.generateTimeout > 0 {
		work, cancelWork = context.WithTimeout(ctx, s.generateTimeout)
	}
	defer cancelWork()
	stoppedAt := func(step string, err error) error {
		if ctx.Err() != nil { // the signal
			ui.stoppedBeforeSaving(step)
			return nil
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("%s took longer than -generate-timeout %v, so nothing new was saved and the previous dataset is unchanged: %w",
				step, s.generateTimeout, err)
		}
		return err
	}

	bar := ui.generating(seed, target)
	began = clock()
	rows, err := generate.Rows(work, seed, 0, target, bar.update)
	bar.end()
	if err != nil {
		return stoppedAt("generating the addresses", err)
	}
	ui.generated(len(rows), seed, clock().Sub(began))
	if len(rows) == 0 {
		ui.nothingToSave()
		return nil
	}
	began = clock()
	order.SortRows(rows)
	ui.sorted(len(rows), clock().Sub(began))
	if err := work.Err(); err != nil {
		return stoppedAt("sorting the addresses", err)
	}
	if data != nil && !outdated {
		keepSavedBalances(rows, data)
	}

	// The neighbour lists are filled in memory, where entries land in random
	// order, and written in one sequential pass: filling a file mapped for
	// writing instead took 63 s for 2M addresses on a Docker Desktop bind
	// mount (decision 012).
	bar = ui.connecting(len(rows))
	began = clock()
	offsets, neighbors, err := graph.BuildContext(work, seed, len(rows), degree, skew, order.RowOf(rows),
		func(entries int) ([]uint32, error) { return make([]uint32, entries), nil }, bar.update)
	bar.end()
	if err != nil {
		return stoppedAt("building the connections", err)
	}
	ui.connected(len(rows), len(neighbors), clock().Sub(began))
	if data != nil && !outdated && data.Manifest.HasGraph() {
		g := graph.Graph{Offsets: offsets, Neighbors: neighbors}
		if links := addedLinks(rows, data, g); len(links) > 0 {
			g.Extra = graph.Overlay(g, links)
			if offsets, neighbors, err = graph.WithExtra(g); err != nil {
				return err
			}
			ui.keptLinks(len(links))
		}
	}

	// Save now, so the data is preserved from here on, whatever happens
	// later. A signal during the save does not stop it (at 20M it takes
	// about 4 s, inside docker stop's 10); a second one cancels it, and then
	// the previous dataset stays current.
	m := store.Manifest{Version: store.VersionCurrent, Seed: seed, Count: len(rows), Degree: degree, Skew: skew}
	if err := saveDataset(ctx, cache, m, previous, rows, offsets, neighbors, have, s.saveTimeout); err != nil {
		return err
	}
	if data != nil {
		data.Close()
		data = nil
	}
	lock.Unlock()

	printDataset(s.show, order.Columns(rows))
	// Everything is on disk: give the memory back before waiting.
	rows, offsets, neighbors = nil, nil, nil
	debug.FreeOSMemory()
	if ctx.Err() != nil { // the signal came during the save, which finished
		return nil
	}
	waitForShutdown(ctx)
	return foldAtShutdown(ctx, cache, s.saveTimeout)
}

// keepSavedBalances gives every generated row that the saved dataset already
// holds its saved balance, which includes folded balance updates. Both are
// sorted by address, so one merge pass finds them; a grown dataset holds
// every address of the smaller one (generate.Rows).
func keepSavedBalances(rows []order.Row, saved *store.Dataset) {
	addrs, j := saved.Addresses.Addrs, 0
	for i := range rows {
		for j < len(addrs) && bytes.Compare(addrs[j][:], rows[i].Address[:]) < 0 {
			j++
		}
		if j < len(addrs) && addrs[j] == rows[i].Address {
			rows[i].Balance = saved.Balance(j)
		}
	}
}

// addedLinks returns, as rows of the grown dataset, the links the saved
// dataset has that the generated graph g lacks: the links people added with
// `evmgraph update link`, folded in or still pending (decision 014). Links
// only point backwards (decision 011), so the generated links among the saved
// addresses are the same in any larger dataset of the same seed; a saved link
// g lacks can only be an added one. rows and the saved addresses are both
// sorted, and a grown dataset holds every saved address.
func addedLinks(rows []order.Row, saved *store.Dataset, g graph.Graph) [][2]uint32 {
	old := saved.Addresses.Addrs
	toNew := make([]uint32, len(old))
	j := 0
	for i := range rows {
		if j < len(old) && rows[i].Address == old[j] {
			toNew[j] = uint32(i)
			j++
		}
	}
	if j != len(old) {
		return nil // not a grown dataset of the same seed: nothing to carry
	}
	sg := saved.Graph()
	var links [][2]uint32
	keep := func(a, b uint32) {
		if b > a && !g.Has(toNew[a], toNew[b]) {
			links = append(links, [2]uint32{toNew[a], toNew[b]})
		}
	}
	for a := range uint32(sg.Len()) {
		for _, b := range sg.List(a) {
			keep(a, b)
		}
		for _, b := range sg.Extra[a] {
			keep(a, b)
		}
	}
	return links
}

// saveDataset writes a new dataset folder and commits it: the graph, then the
// addresses and balances of rows, then the manifest. The caller holds the
// write lock.
func saveDataset(ctx context.Context, cache store.Cache, m store.Manifest, previous string, rows []order.Row,
	offsets, neighbors []uint32, have int, timeout time.Duration) error {
	saveCtx, cancel := saveContext(ctx, timeout)
	defer cancel()
	total := len(rows)
	// The free space was checked before generating, but generating can take
	// long at large sizes. Check again rather than fill the disk.
	if err := capacity.CheckDisk(cache.Dir, total, evm.AddressLen+evm.WeiLen+4+4*m.Degree); err != nil {
		return fmt.Errorf("%w; nothing was saved, and the previous dataset is unchanged", err)
	}
	draft, err := cache.NewDraft()
	if err != nil {
		return err
	}
	defer draft.Discard() // removes the folder unless it was committed
	addrBar, balanceBar := ui.saving(have, total-have)
	began := clock()
	err = draft.WriteGraph(saveCtx, offsets, neighbors)
	if err == nil {
		err = draft.WriteAddresses(saveCtx, order.Addresses(order.Columns(rows)), addrBar.update)
		addrBar.end()
	}
	if err == nil {
		err = draft.WriteBalances(saveCtx, order.Balances(order.Columns(rows)), balanceBar.update)
		balanceBar.end()
	}
	if err == nil {
		err = draft.Commit(m, previous)
	}
	if err != nil {
		return saveFailed(err, timeout)
	}
	written := fileSize(filepath.Join(draft.Path(), columns.AddressesFileName)) + fileSize(filepath.Join(draft.Path(), columns.BalancesFileName))
	ui.saved(total, draft.Path(), written, clock().Sub(began))
	return nil
}

// foldUpdates folds the balance updates waiting in d's log into a new
// dataset folder. The caller holds the write lock, and d is current. ctx is
// the program's signal context, so that at shutdown, where the first signal
// has already come, the next one cancels the fold (saveContext).
func foldUpdates(ctx context.Context, cache store.Cache, d *store.Dataset, timeout time.Duration) error {
	balances, links := d.PendingBalances(), d.PendingLinks()
	if balances == 0 && links == 0 {
		return nil
	}
	// The fold writes balances.bin if balances changed and the graph's files
	// if links were added; the rest are hard links.
	perAddress := 0
	if balances > 0 {
		perAddress += evm.WeiLen
	}
	if links > 0 {
		perAddress += 4 + 4*d.Manifest.Degree + 1 // offsets, neighbours, and room for the added links
	}
	if err := capacity.CheckDisk(cache.Dir, d.Manifest.Count, perAddress); err != nil {
		return fmt.Errorf("%w; the previous dataset is unchanged, and the pending updates (%s) stay in their logs, to be folded in at the next start",
			err, pendingWords(balances, links))
	}
	foldCtx, cancel := saveContext(ctx, timeout)
	defer cancel()
	bar := ui.folding(pendingWords(balances, links), d.Manifest.Count)
	began := clock()
	balances, links, folder, err := cache.Fold(foldCtx, d, bar.update)
	bar.end()
	if err != nil {
		return saveFailed(err, timeout)
	}
	ui.folded(pendingWords(balances, links), folder, clock().Sub(began))
	return nil
}

// pendingWords names a number of balance updates and added links, as in
// "3 balance updates and 1 added link".
func pendingWords(balances, links int) string {
	plural := func(n int, one, many string) string {
		if n == 1 {
			return "1 " + one
		}
		return fmt.Sprintf("%d %s", n, many)
	}
	switch {
	case links == 0:
		return plural(balances, "balance update", "balance updates")
	case balances == 0:
		return plural(links, "added link", "added links")
	}
	return plural(balances, "balance update", "balance updates") + " and " + plural(links, "added link", "added links")
}

// foldAtShutdown folds in the balance updates made while the program waited.
// It waits for the write lock, in case an update is being appended.
func foldAtShutdown(ctx context.Context, cache store.Cache, timeout time.Duration) error {
	lock, err := store.WaitLock(cache.Dir)
	if err != nil {
		return err
	}
	defer lock.Unlock()
	d, err := store.OpenReadOnly(cache.Dir)
	if err != nil {
		return err
	}
	defer d.Close()
	if d.PendingBalances() == 0 && d.PendingLinks() == 0 {
		ui.upToDate()
		return nil
	}
	return foldUpdates(ctx, cache, d, timeout)
}

// saveContext is the context of one save. A shutdown signal does not stop
// a save, because saving is how the program preserves its data; a second
// signal does, as does the -save-timeout deadline. parent is the program's
// signal context: when it has already ended, the first signal came before
// the save began, and the next one is the second.
func saveContext(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	if timeout > 0 {
		var cancelTimeout context.CancelFunc
		ctx, cancelTimeout = context.WithTimeout(ctx, timeout)
		prev := cancel
		cancel = func() { cancelTimeout(); prev() }
	}
	seen := 0
	if parent.Err() != nil { // checked before listening, so no signal counts twice
		seen = 1
	}
	sigs := make(chan os.Signal, 2)
	signal.Notify(sigs, shutdownSignals...)
	go func() {
		for {
			select {
			case <-sigs:
				if seen++; seen >= 2 {
					cancel()
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	return ctx, func() { signal.Stop(sigs); cancel() }
}

// saveFailed explains a failed save.
func saveFailed(err error, timeout time.Duration) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("saving took longer than -save-timeout %v, so nothing was saved and the previous dataset is unchanged: %w",
			timeout, err)
	}
	return err
}

// rowLen is the memory one generated row takes while the program holds it:
// an address, its balance and its generation position.
const rowLen = evm.AddressLen + evm.WeiLen + 4

// whenWaiting, if set, is called once the program starts waiting for a
// shutdown signal. Tests use it to send their "Ctrl+C" at exactly that point
// rather than after a guessed delay; the program never sets it.
var whenWaiting func()

func waitForShutdown(ctx context.Context) {
	ui.running()
	if whenWaiting != nil {
		whenWaiting()
	}
	<-ctx.Done()
	ui.shutdown()
}

// printDataset prints the first show rows: each address with its balance.
func printDataset(show int, rows iter.Seq2[*evm.Address, *evm.Wei]) {
	if show <= 0 {
		return
	}
	for a, b := range rows {
		fmt.Printf("%s %s wei\n", a.Checksum(), b)
		if show--; show == 0 {
			return
		}
	}
}

// fileSize is the size of path in bytes, or 0 if it cannot be read.
func fileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}
