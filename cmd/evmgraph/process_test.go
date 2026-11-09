package main

// End-to-end tests of main itself: the test binary runs main() when
// EVMGRAPH_TEST_MAIN=1, so a test can start the program as a real process and
// send it real signals.

import (
	"bufio"
	"context"
	"errors"
	"evmgraph/internal/evm"
	"evmgraph/internal/graph"
	"evmgraph/internal/store"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if os.Getenv("EVMGRAPH_TEST_MAIN") == "1" {
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// program starts the program as a separate process with args. The context
// timeout makes sure a regression cannot leave it running.
func program(t *testing.T, args ...string) *exec.Cmd {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, os.Args[0], args...)
	cmd.Env = append(os.Environ(), "EVMGRAPH_TEST_MAIN=1")
	return cmd
}

func exitCode(err error) int {
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode()
	}
	if err != nil {
		return -1
	}
	return 0
}

// The command comes first. Plain evmgraph prints the help instead of
// starting a 20M-address run; flags without a command, an unknown command and
// a stray argument after run's flags are refused with exit code 2 and a clear
// message, and nothing is created. -data and a small -n come first, so a
// regression neither hangs nor starts a large run.
func TestCommandsComeFirst(t *testing.T) {
	for _, args := range [][]string{{}, {"help"}, {"-h"}} {
		out, err := program(t, args...).CombinedOutput()
		if code := exitCode(err); code != 0 || !strings.Contains(string(out), "evmgraph run [flags]") {
			t.Fatalf("%v: exit %d, want the help:\n%s", args, code, out)
		}
	}
	dir := t.TempDir()
	for _, c := range []struct {
		args []string
		says string
	}{
		{[]string{"-data", dir, "-n", "10"}, "use evmgraph run -data"},
		{[]string{"generate", "-data", dir}, `unknown command "generate"`},
		{[]string{"run", "-data", dir, "-n", "10", "connections", "0x00"}, `unexpected argument "connections"`},
	} {
		out, err := program(t, c.args...).CombinedOutput()
		if code := exitCode(err); code != 2 || !strings.Contains(string(out), c.says) {
			t.Fatalf("%v: exit %d, want 2 and %q:\n%s", c.args, code, c.says, out)
		}
		if strings.Contains(string(out), "\x1b[") {
			t.Fatalf("colour codes in piped output: %q", out)
		}
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("it created files although it should have stopped: %v", entries)
	}
}

// SIGHUP, what a closed terminal or a dropped SSH session sends, saves like
// Ctrl+C instead of killing the program.
func TestSIGHUPSaves(t *testing.T) {
	if signal.Ignored(syscall.SIGHUP) {
		t.Skip("SIGHUP is ignored in this environment (nohup), so the program does not listen for it")
	}
	dir := t.TempDir()
	cmd := program(t, "run", "-data", dir, "-n", "1000", "-show", "0")
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	lines := bufio.NewScanner(stderr)
	var log strings.Builder
	for lines.Scan() {
		log.WriteString(lines.Text() + "\n")
		if strings.HasPrefix(lines.Text(), "running;") {
			if err := cmd.Process.Signal(syscall.SIGHUP); err != nil {
				t.Fatal(err)
			}
		}
	}
	if code := exitCode(cmd.Wait()); code != 0 {
		t.Fatalf("exit code %d, want 0:\n%s", code, log.String())
	}
	// stderr is a pipe here, not a terminal, so the log must be plain text.
	if strings.Contains(log.String(), "\x1b[") {
		t.Fatalf("colour codes in piped output: %q", log.String())
	}
	if m, _, _ := current(t, dir); m.Count != 1000 {
		t.Fatalf("1,000 addresses were not saved (%v, %+v):\n%s", err, m, log.String())
	}
}

// balance runs `evmgraph balance` as a real process and returns its stdout,
// stderr and exit code.
func balance(t *testing.T, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	cmd := program(t, append([]string{"balance"}, args...)...)
	var out, errOut strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errOut
	code = exitCode(cmd.Run())
	return out.String(), errOut.String(), code
}

// End to end: a saved dataset answers with the exact balance, in ETH and wei,
// for an address in lower, upper or EIP-55 case, with or without 0x; a wrong
// checksum, misses, malformed input and a missing dataset get their own exit
// codes and change nothing.
func TestBalanceCommand(t *testing.T) {
	dir := newDataset(t, 1000, 1)
	addrs, balances := sortedDataset(t, 1000, 1)
	want := balances[500].ETH() + " ETH (" + balances[500].String() + " wei)\n"
	hex := addrs[500].Hex()

	checksummed := addrs[500].Checksum()
	if checksummed == hex {
		t.Fatal("test setup: the checksum of this address has no upper-case letters")
	}
	for _, a := range []string{hex, "0x" + strings.ToUpper(hex[2:]), checksummed, hex[2:], "0X" + hex[2:]} {
		out, errOut, code := balance(t, "-data", dir, a)
		if code != 0 || out != want {
			t.Fatalf("%s: exit %d, stdout %q, want %q (stderr %q)", a, code, out, want, errOut)
		}
		if strings.Contains(errOut, "\x1b[") {
			t.Fatalf("colour codes in piped stderr: %q", errOut)
		}
	}

	if out, errOut, code := balance(t, "-data", dir, "0x"+strings.Repeat("0", 40)); code != 1 || out != "" || !strings.Contains(errOut, "is not among the 1000 addresses") {
		t.Fatalf("absent address: exit %d, stdout %q, stderr %q", code, out, errOut)
	}
	// One letter's case flipped: the checksum catches it instead of a lookup
	// answering "not found" or, worse, for another address.
	typo := []byte(checksummed)
	for i := 2; i < len(typo); i++ {
		if c := typo[i]; c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F' {
			typo[i] ^= 'a' - 'A'
			break
		}
	}
	if out, errOut, code := balance(t, "-data", dir, string(typo)); code != 2 || out != "" || !strings.Contains(errOut, "EIP-55 checksum") {
		t.Fatalf("wrong checksum: exit %d, stdout %q, stderr %q", code, out, errOut)
	}
	for _, args := range [][]string{{"-data", dir, "0x1234"}, {"-data", dir}, {"-data", dir, hex, hex}, {"-nope"}} {
		if _, errOut, code := balance(t, args...); code != 2 {
			t.Fatalf("%v: exit %d, want 2 (stderr %q)", args, code, errOut)
		}
	}

	missing := filepath.Join(t.TempDir(), "nothing-here")
	if _, errOut, code := balance(t, "-data", missing, hex); code != 1 || !strings.Contains(errOut, "no dataset") {
		t.Fatalf("no dataset: exit %d, stderr %q", code, errOut)
	}
	if _, err := os.Stat(missing); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("balance created the data folder; it must only read")
	}
}

// A dataset still in generation order cannot be searched; balance says so and
// leaves it alone.
func TestBalanceRefusesUnsortedDataset(t *testing.T) {
	dir := newDataset(t, 100, 1)
	m, _, _ := current(t, dir)
	m.Version = store.VersionGenerationOrder
	writeManifest(t, dir, m)
	before, _ := os.ReadFile(store.ManifestPath(dir))
	addrs, _ := sortedDataset(t, 100, 1)
	if _, errOut, code := balance(t, "-data", dir, addrs[0].Hex()); code != 1 || !strings.Contains(errOut, "generation order") {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	if after, _ := os.ReadFile(store.ManifestPath(dir)); !slices.Equal(after, before) {
		t.Fatal("balance changed the manifest")
	}
}

// expectedConnections answers the query in-process, from the saved files, as
// the reference: "1 0x…" lines, then "2 0x…" lines.
func expectedConnections(t *testing.T, dir string, row uint32) ([]string, int, int) {
	t.Helper()
	d := openSaved(t, dir)
	res, err := graph.NewSearch(d.Graph()).Run(row)
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, r := range res.Level1 {
		lines = append(lines, "1 "+d.Addresses.Addrs[r].Checksum())
	}
	res.Level2(func(r uint32) bool { lines = append(lines, "2 "+d.Addresses.Addrs[r].Checksum()); return true })
	return lines, len(res.Level1), res.Level2Count
}

func connections(t *testing.T, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	cmd := program(t, append([]string{"connections"}, args...)...)
	var out, errOut strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errOut
	code = exitCode(cmd.Run())
	return out.String(), errOut.String(), code
}

// hubNeighbour returns a row next to the biggest hub: its level 2 is most of
// the hub's list, so the query has many results.
func hubNeighbour(t *testing.T, dir string) uint32 {
	t.Helper()
	d := openSaved(t, dir)
	g := d.Graph()
	hub := uint32(0)
	for r := range uint32(g.Len()) {
		if len(g.List(r)) > len(g.List(hub)) {
			hub = r
		}
	}
	return g.List(hub)[0]
}

// End to end: the printed lines are exactly the in-process answer, limited
// as asked, with exact totals on stderr.
func TestConnectionsCommand(t *testing.T) {
	dir := newDataset(t, 3000, 1)
	addrs, _ := sortedDataset(t, 3000, 1)
	row := hubNeighbour(t, dir)
	want, level1, level2 := expectedConnections(t, dir, row)
	if len(want) <= 100 {
		t.Fatalf("test setup: only %d results next to the hub", len(want))
	}
	hex := addrs[row].Hex()

	out, errOut, code := connections(t, "-data", dir, "-all", hex)
	if code != 0 || out != strings.Join(want, "\n")+"\n" {
		t.Fatalf("-all: exit %d, %d lines, want %d (stderr %q)", code, strings.Count(out, "\n"), len(want), errOut)
	}
	totals := fmt.Sprintf("level 1: %d addresses, level 2: %d addresses", level1, level2)
	if !strings.Contains(errOut, totals) || strings.Contains(errOut, "showing") {
		t.Fatalf("-all stderr %q, want %q", errOut, totals)
	}

	out, errOut, code = connections(t, "-data", dir, "0x"+strings.ToUpper(hex[2:]))
	if code != 0 || out != strings.Join(want[:100], "\n")+"\n" {
		t.Fatalf("default limit: exit %d, %d lines", code, strings.Count(out, "\n"))
	}
	if !strings.Contains(errOut, fmt.Sprintf("showing 100 of %d; -all for everything", len(want))) {
		t.Fatalf("default limit stderr %q", errOut)
	}
	if strings.Contains(errOut, "\x1b[") {
		t.Fatalf("colour codes in piped stderr: %q", errOut)
	}
	if out, _, _ := connections(t, "-data", dir, "-limit", "3", hex); out != strings.Join(want[:3], "\n")+"\n" {
		t.Fatalf("-limit 3: %q", out)
	}

	if out, errOut, code := connections(t, "-data", dir, "0x"+strings.Repeat("0", 40)); code != 1 || out != "" || !strings.Contains(errOut, "is not among") {
		t.Fatalf("absent address: exit %d, stdout %q, stderr %q", code, out, errOut)
	}
	for _, args := range [][]string{{"-data", dir, "0x12"}, {"-data", dir}, {"-data", dir, "-limit", "-1", hex}, {"-nope"}} {
		if _, errOut, code := connections(t, args...); code != 2 {
			t.Fatalf("%v: exit %d, want 2 (stderr %q)", args, code, errOut)
		}
	}
	missing := filepath.Join(t.TempDir(), "nothing-here")
	if _, errOut, code := connections(t, "-data", missing, hex); code != 1 || !strings.Contains(errOut, "no dataset") {
		t.Fatalf("no dataset: exit %d, stderr %q", code, errOut)
	}
	if _, err := os.Stat(missing); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("connections created the data folder")
	}
}

// A dataset from before connections existed is refused, and left alone.
func TestConnectionsRefusesOlderDataset(t *testing.T) {
	dir := newDataset(t, 100, 1)
	m, _, _ := current(t, dir)
	m.Version = store.VersionSorted
	writeManifest(t, dir, m)
	before, _ := os.ReadFile(store.ManifestPath(dir))
	addrs, _ := sortedDataset(t, 100, 1)
	if _, errOut, code := connections(t, "-data", dir, addrs[0].Hex()); code != 1 || !strings.Contains(errOut, "before connections existed") {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	if after, _ := os.ReadFile(store.ManifestPath(dir)); !slices.Equal(after, before) {
		t.Fatal("connections changed the manifest")
	}
	// balance still answers from a version 2 dataset.
	if _, _, code := balance(t, "-data", dir, addrs[0].Hex()); code != 0 {
		t.Fatalf("balance on version 2: exit %d", code)
	}
}

// `connections -all | head` must not hang: when the reader goes away, the
// program stops instead of waiting to write.
func TestConnectionsStopsWhenOutputCloses(t *testing.T) {
	dir := newDataset(t, 3000, 1)
	addrs, _ := sortedDataset(t, 3000, 1)
	cmd := program(t, "connections", "-data", dir, "-all", addrs[hubNeighbour(t, dir)].Hex())
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	began := time.Now()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if _, err := bufio.NewReader(stdout).ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	stdout.Close() // what head does after its lines
	cmd.Wait()
	if took := time.Since(began); took > 10*time.Second {
		t.Fatalf("the program kept running %v after its reader left", took)
	}
}

// -timeout stops a query that takes too long, with exit code 3; a negative
// timeout is a usage error.
func TestConnectionsTimeout(t *testing.T) {
	dir := newDataset(t, 3000, 1)
	addrs, _ := sortedDataset(t, 3000, 1)
	hex := addrs[hubNeighbour(t, dir)].Hex()
	if out, errOut, code := connections(t, "-data", dir, "-timeout", "1ns", "-all", hex); code != 3 || !strings.Contains(errOut, "longer than -timeout") {
		t.Fatalf("1ns: exit %d, stdout %d bytes, stderr %q", code, len(out), errOut)
	}
	if _, errOut, code := connections(t, "-data", dir, "-timeout", "-1s", hex); code != 2 {
		t.Fatalf("-1s: exit %d, want 2 (stderr %q)", code, errOut)
	}
	if _, errOut, code := connections(t, "-data", dir, "-timeout", "1m", hex); code != 0 {
		t.Fatalf("1m: exit %d (stderr %q)", code, errOut)
	}
}

// The main program's deadlines, as a process: a passed deadline exits with
// code 3 and says which one; a negative one is a usage error.
func TestMainDeadlines(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cache")
	out, err := program(t, "run", "-data", dir, "-n", "50000", "-generate-timeout", "1ns").CombinedOutput()
	if code := exitCode(err); code != 3 || !strings.Contains(string(out), "took longer than -generate-timeout") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	out, err = program(t, "run", "-data", dir, "-n", "10", "-save-timeout", "-1s").CombinedOutput()
	if code := exitCode(err); code != 2 {
		t.Fatalf("negative -save-timeout: exit %d:\n%s", code, out)
	}
}

// search runs `evmgraph search` with input as its stdin.
func search(t *testing.T, input string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	cmd := program(t, append([]string{"search"}, args...)...)
	cmd.Stdin = strings.NewReader(input)
	var out, errOut strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errOut
	code = exitCode(cmd.Run())
	return out.String(), errOut.String(), code
}

// End to end: the search prompt answers a pasted address with its balance
// and the first -limit of each level, /b and /c like the balance and
// connections commands, reports bad lines on stderr and carries on, and stops
// at /quit. Piped input gets no banner or prompt.
func TestSearchCommand(t *testing.T) {
	dir := newDataset(t, 1000, 1)
	addrs, balances := sortedDataset(t, 1000, 1)
	row := hubNeighbour(t, dir)
	want, level1, level2 := expectedConnections(t, dir, row)
	a := addrs[row]
	input := strings.Join([]string{
		a.Hex(),                           // a pasted address, lower case
		"/b " + a.Checksum(),              // balance only, EIP-55
		"/c " + a.Hex()[2:] + " -limit 3", // no 0x, flag after the address
		"/c -all " + a.Hex(),
		"",
		"0x1234",                       // malformed
		"0x" + strings.Repeat("0", 40), // absent
		"/nope",
		"/help",
		"/quit",
		"/b " + a.Hex(), // after /quit: never answered
	}, "\n") + "\n"
	out, errOut, code := search(t, input, "-data", dir, "-limit", "2")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	for _, s := range []string{
		"address     " + a.Checksum() + "\n",
		"balance     " + balances[row].ETH() + " ETH\n",
		fmt.Sprintf("neighbours  %s direct + %s at level 2 = %s", commas(level1), commas(level2), commas(level1+level2)),
		"            " + strings.TrimPrefix(want[0], "1 ") + "\n", // level 1, first address
		"/c " + a.Checksum() + " -all\n",                          // how to see the rest
		balances[row].ETH() + " ETH (" + balances[row].String() + " wei)\n",
		strings.Join(want[:3], "\n") + "\nlevel 1: ",
		strings.Join(want, "\n") + "\n" + fmt.Sprintf("level 1: %d addresses, level 2: %d addresses\n", level1, level2),
		"Paste an address to see its balance",
	} {
		if !strings.Contains(out, s) {
			t.Fatalf("stdout lacks %q:\n%s", s, out)
		}
	}
	if strings.Count(out, " ETH (") != 1 {
		t.Fatalf("a line after /quit was answered:\n%s", out)
	}
	if strings.Contains(out, "evmgraph> ") || strings.Contains(out, "Searching ") {
		t.Fatalf("prompt or banner printed for piped input:\n%s", out)
	}
	for _, s := range []string{"is not an address", "is not among the 1000 addresses", `unknown command "/nope"`} {
		if !strings.Contains(errOut, s) {
			t.Fatalf("stderr lacks %q: %q", s, errOut)
		}
	}

	// End of input leaves cleanly too; no dataset is an error before any prompt.
	if _, errOut, code := search(t, a.Hex()+"\n", "-data", dir); code != 0 {
		t.Fatalf("end of input: exit %d, stderr %q", code, errOut)
	}
	if _, errOut, code := search(t, "", "-data", filepath.Join(t.TempDir(), "none")); code != 1 || !strings.Contains(errOut, "no dataset") {
		t.Fatalf("no dataset: exit %d, stderr %q", code, errOut)
	}
	if _, _, code := search(t, "", "-data", dir, "extra"); code != 2 {
		t.Fatalf("an argument: exit %d, want 2", code)
	}
}

// Ctrl+C at the prompt leaves search cleanly, with exit code 0.
func TestSearchLeavesOnInterrupt(t *testing.T) {
	dir := newDataset(t, 100, 1)
	cmd := program(t, "search", "-data", dir)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	// Wait until it answers, so the signal handler is in place.
	fmt.Fprintln(stdin, "/help")
	lines := bufio.NewScanner(stdout)
	for lines.Scan() && !strings.Contains(lines.Text(), "Paste an address") {
	}
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	if code := exitCode(cmd.Wait()); code != 0 {
		t.Fatalf("exit %d after Ctrl+C, want 0", code)
	}
}

func update(t *testing.T, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	cmd := program(t, append([]string{"update"}, args...)...)
	var out, errOut strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errOut
	code = exitCode(cmd.Run())
	return out.String(), errOut.String(), code
}

// End to end: update changes a balance at once, durably, without a rebuild:
// balance answers with the new value straight away. Bad input, an address
// outside the dataset and a missing dataset get their own exit codes.
func TestUpdateCommand(t *testing.T) {
	dir := newDataset(t, 1000, 1)
	addrs, _ := sortedDataset(t, 1000, 1)
	a := addrs[500].Checksum()
	out, errOut, code := update(t, "-data", dir, "balance", a, "1500000000000000000")
	if code != 0 || out != "1.500000000000000000 ETH (1500000000000000000 wei)\n" {
		t.Fatalf("update: exit %d, stdout %q, stderr %q", code, out, errOut)
	}
	if out, _, code := balance(t, "-data", dir, a); code != 0 || out != "1.500000000000000000 ETH (1500000000000000000 wei)\n" {
		t.Fatalf("balance after the update: exit %d, %q", code, out)
	}
	if folders := datasetFolders(dir); len(folders) != 1 {
		t.Fatalf("the update rebuilt the dataset: %v", folders)
	}
	for _, c := range []struct {
		args []string
		code int
		says string
	}{
		{[]string{"-data", dir, "balance", a, "-5"}, 2, "not a balance in wei"},
		{[]string{"-data", dir, "balance", a, "1.5"}, 2, "not a balance in wei"},
		{[]string{"-data", dir, "balance", "0x1234", "5"}, 2, "is not an address"},
		{[]string{"-data", dir, "nonce", a, "5"}, 2, "update changes one balance"},
		{[]string{"-data", dir, "balance", "0x" + strings.Repeat("0", 40), "5"}, 1, "is not among the 1000 addresses"},
		{[]string{"-data", filepath.Join(t.TempDir(), "none"), "balance", a, "5"}, 1, "no dataset"},
	} {
		if _, errOut, code := update(t, c.args...); code != c.code || !strings.Contains(errOut, c.says) {
			t.Fatalf("%v: exit %d, stderr %q; want %d and %q", c.args, code, errOut, c.code, c.says)
		}
	}
}

// End to end: update link connects two addresses at once, in both
// directions, without a rebuild; linking again is harmless; an address linked
// to itself is a usage error, an address outside the dataset is not found.
func TestUpdateLinkCommand(t *testing.T) {
	dir := newDataset(t, 1000, 1)
	d := openSaved(t, dir)
	g := d.Graph()
	var a, b evm.Address
	for r := uint32(1); r < uint32(g.Len()); r++ {
		if !g.Has(0, r) {
			a, b = d.Addresses.Addrs[0], d.Addresses.Addrs[r]
			break
		}
	}
	out, errOut, code := update(t, "-data", dir, "link", a.Checksum(), b.Hex())
	if code != 0 || out != a.Checksum()+" and "+b.Checksum()+" are now connected\n" {
		t.Fatalf("link: exit %d, stdout %q, stderr %q", code, out, errOut)
	}
	for _, pair := range [][2]evm.Address{{a, b}, {b, a}} {
		out, _, code := connections(t, "-data", dir, "-all", pair[0].Checksum())
		if code != 0 || !strings.Contains(out, "1 "+pair[1].Checksum()+"\n") {
			t.Fatalf("connections of %s do not list %s at level 1", pair[0].Checksum(), pair[1].Checksum())
		}
	}
	if out, _, code := update(t, "-data", dir, "link", b.Checksum(), a.Checksum()); code != 0 || !strings.Contains(out, "already connected") {
		t.Fatalf("linking again: exit %d, %q", code, out)
	}
	if _, errOut, code := update(t, "-data", dir, "link", a.Checksum(), a.Hex()); code != 2 || !strings.Contains(errOut, "connected to itself") {
		t.Fatalf("self-link: exit %d, %q", code, errOut)
	}
	if _, errOut, code := update(t, "-data", dir, "link", a.Checksum(), "0x"+strings.Repeat("0", 40)); code != 1 || !strings.Contains(errOut, "is not among the 1000 addresses") {
		t.Fatalf("an address outside the dataset: exit %d, %q", code, errOut)
	}
	if folders := datasetFolders(dir); len(folders) != 1 {
		t.Fatalf("the link rebuilt the dataset: %v", folders)
	}
}
