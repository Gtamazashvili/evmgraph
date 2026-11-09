package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"evmgraph/internal/evm"
	"evmgraph/internal/graph"
	"evmgraph/internal/lookup"
	"evmgraph/internal/store"
)

const searchUsage = "usage: evmgraph search [-data folder] [-limit N]"

const searchHelp = `Paste an address to see its balance and its neighbours, two levels deep.

  0x…   or   /s 0x…            balance and neighbours (the first -limit of each level)
  /b 0x…                       balance only
  /c 0x… [-limit N | -all]     connections, one per line, as evmgraph connections prints them
  /help                        this help
  /quit                        leave (also Ctrl+D, or Ctrl+C at the prompt)

Addresses can be lower case, upper case or EIP-55, with or without 0x.
Ctrl+C during a long answer stops that answer and returns to the prompt.
`

// runSearch is `evmgraph search [-data folder] [-limit N]`: a prompt that
// answers balance and connection lookups on the saved dataset, one per line,
// until /quit, end of input or Ctrl+C. Like balance and connections it only
// reads, so it can run beside the main program. It opens the dataset once,
// so each answer costs only the lookup and the search, not opening the files.
// When stdin is not a terminal it prints no prompt, so lines can be piped in.
func runSearch(args []string) int {
	flags := flag.NewFlagSet("search", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	dir := flags.String("data", "cache", "folder that holds the dataset")
	limit := flags.Int("limit", 10, "addresses shown per level for a pasted address")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(logOut, searchUsage)
			return exitFound
		}
		ui.fail("%v; %s", err, searchUsage)
		return exitUsage
	}
	if flags.NArg() != 0 || *limit < 0 {
		ui.fail("search takes no arguments and -limit must be zero or more; %s", searchUsage)
		return exitUsage
	}

	d, ok := openForQuery(*dir)
	if !ok {
		return exitNotFound
	}
	defer d.Close()

	s := &searcher{d: d, dir: *dir, limit: *limit, out: bufio.NewWriterSize(os.Stdout, 64<<10)}
	if d.Manifest.HasGraph() {
		s.search = graph.NewSearch(d.Graph()) // one bitset, reused by every query
	}
	interactive := stdinIsTerminal()

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigs)
	lines := make(chan string)
	go func() {
		in := bufio.NewScanner(os.Stdin)
		for in.Scan() {
			lines <- in.Text()
		}
		close(lines)
	}()

	if interactive {
		fmt.Fprintf(s.out, "Searching %s addresses in %s. Paste an address, or /help.\n", commas(d.Manifest.Count), *dir)
	}
	for {
		if interactive {
			s.out.WriteString("evmgraph> ")
		}
		s.out.Flush()
		select {
		case <-sigs:
			if interactive {
				s.out.WriteString("\n")
			}
			s.out.Flush()
			return exitFound
		case line, ok := <-lines:
			if !ok {
				if interactive {
					s.out.WriteString("\n")
				}
				s.out.Flush()
				return exitFound
			}
			if s.handle(line, sigs) {
				s.out.Flush()
				return exitFound
			}
		}
	}
}

// searcher answers the prompt's lines from one open dataset.
type searcher struct {
	d      *store.Dataset
	dir    string
	limit  int
	search *graph.Search // nil when the dataset has no connections (manifest version < 3)
	out    *bufio.Writer
	sum    evm.Checksummer
}

// handle answers one line and reports whether it asked to quit. A signal
// that arrives while the answer is being worked out stops only that answer.
func (s *searcher) handle(line string, sigs <-chan os.Signal) (quit bool) {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return false
	}
	cmd, args := fields[0], fields[1:]
	if !strings.HasPrefix(cmd, "/") {
		cmd, args = "/s", fields // a pasted address
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		select {
		case <-sigs:
			cancel()
		case <-done:
		}
	}()
	defer func() { close(done); cancel() }()

	switch cmd {
	case "/s", "/search":
		s.overview(ctx, args)
	case "/b", "/balance":
		if row, _, ok := s.find(args); ok {
			b := s.d.Balance(int(row))
			fmt.Fprintf(s.out, "%s ETH (%s wei)\n", b.ETH(), b)
		}
	case "/c", "/connections":
		s.connections(ctx, args)
	case "/h", "/help", "/?":
		s.out.WriteString(searchHelp)
	case "/q", "/quit", "/exit":
		return true
	default:
		s.fail("unknown command %q; /help lists them", cmd)
	}
	return false
}

// fail reports a problem with one line on stderr, after the answers so far.
func (s *searcher) fail(format string, args ...any) {
	s.out.Flush()
	ui.fail(format, args...)
}

// find reads the one address in args and finds its row, reporting a missing,
// malformed, mistyped or absent address.
func (s *searcher) find(args []string) (row uint32, a evm.Address, ok bool) {
	if len(args) != 1 {
		s.fail("give one address, such as /b 0x2aA22D2D411f98B31014ae167a95999df17eAcDA")
		return 0, a, false
	}
	s.out.Flush()
	if a, ok = parseAddress(args[0]); !ok {
		return 0, a, false
	}
	r, found := lookup.Find(s.d.Addresses.Addrs, a)
	if !found {
		ui.notFound(a, s.d.Manifest.Count, s.dir)
		return 0, a, false
	}
	return uint32(r), a, true
}

// run searches the connections of row, or says why it cannot.
func (s *searcher) run(ctx context.Context, row uint32) (graph.Result, bool) {
	if s.search == nil {
		s.out.Flush()
		noGraph(s.d, s.dir)
		return graph.Result{}, false
	}
	res, err := s.search.RunContext(ctx, row)
	if errors.Is(err, context.Canceled) {
		s.fail("stopped")
		return res, false
	}
	if err != nil {
		s.fail("%v", err)
		return res, false
	}
	return res, true
}

// overview prints an address's balance, its connection totals and the first
// -limit addresses of each level.
func (s *searcher) overview(ctx context.Context, args []string) {
	row, a, ok := s.find(args)
	if !ok {
		return
	}
	b := s.d.Balance(int(row))
	fmt.Fprintf(s.out, "address     %s\nbalance     %s ETH\n", a.Checksum(), b.ETH())
	if s.search == nil {
		fmt.Fprintf(s.out, "neighbours  not in this dataset (manifest version %d)\n", s.d.Manifest.Version)
		return
	}
	began := clock()
	res, ok := s.run(ctx, row)
	if !ok {
		return
	}
	took := clock().Sub(began)
	level1, level2 := len(res.Level1), res.Level2Count
	fmt.Fprintf(s.out, "neighbours  %s direct + %s at level 2 = %s (searched in %s)\n",
		commas(level1), commas(level2), commas(level1+level2), duration(took))

	fmt.Fprintf(s.out, "level 1     %s\n", s.count(level1))
	for _, r := range res.Level1[:min(level1, s.limit)] {
		s.address(r)
	}
	s.more(level1, a)
	if level2 == 0 {
		return
	}
	fmt.Fprintf(s.out, "level 2     %s\n", s.count(level2))
	shown := 0
	res.Level2(func(r uint32) bool {
		if shown == s.limit {
			return false
		}
		s.address(r)
		shown++
		return true
	})
	s.more(level2, a)
}

// count describes how many addresses a level has and how many are shown.
func (s *searcher) count(n int) string {
	if n > s.limit {
		return fmt.Sprintf("%s addresses, the first %d:", commas(n), s.limit)
	}
	return commas(n) + " addresses"
}

// address prints one indented address in EIP-55 form.
func (s *searcher) address(r uint32) {
	line := s.sum.Append([]byte("            "), s.d.Addresses.Addrs[r])
	s.out.Write(append(line, '\n'))
}

// more says how to see a level's addresses that were left out.
func (s *searcher) more(n int, a evm.Address) {
	if n > s.limit {
		fmt.Fprintf(s.out, "            … %s more: /c %s -all\n", commas(n-s.limit), a.Checksum())
	}
}

// connections is /c: the same lines and limits as evmgraph connections, with
// the totals after them.
func (s *searcher) connections(ctx context.Context, args []string) {
	room, all := 100, false
	var rest []string
	for i := 0; i < len(args); i++ {
		switch arg := args[i]; {
		case arg == "-all" || arg == "--all":
			all = true
		case arg == "-limit" || arg == "--limit":
			if i+1 == len(args) {
				s.fail("-limit needs a number")
				return
			}
			i++
			n, err := strconv.Atoi(args[i])
			if err != nil || n < 0 {
				s.fail("-limit %q: give a number, zero or more", args[i])
				return
			}
			room = n
		case strings.HasPrefix(arg, "-limit=") || strings.HasPrefix(arg, "--limit="):
			n, err := strconv.Atoi(arg[strings.Index(arg, "=")+1:])
			if err != nil || n < 0 {
				s.fail("%s: give a number, zero or more", arg)
				return
			}
			room = n
		default:
			rest = append(rest, arg)
		}
	}
	if all {
		room = -1
	}
	row, _, ok := s.find(rest)
	if !ok {
		return
	}
	res, ok := s.run(ctx, row)
	if !ok {
		return
	}
	shown, late := writeConnections(ctx, s.out, s.d.Addresses.Addrs, res, room)
	total := len(res.Level1) + res.Level2Count
	fmt.Fprintf(s.out, "level 1: %d addresses, level 2: %d addresses", len(res.Level1), res.Level2Count)
	if late {
		fmt.Fprintf(s.out, "; stopped after %d lines\n", shown)
		return
	}
	if shown < total {
		fmt.Fprintf(s.out, "; showing %d of %d; -all for everything", shown, total)
	}
	s.out.WriteString("\n")
}

// stdinIsTerminal reports whether a person is typing, rather than lines being
// piped in; only then does search print its banner and prompt.
func stdinIsTerminal() bool {
	info, err := os.Stdin.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
