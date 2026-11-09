// Command e2e measures the program end to end, the way a person uses it, and
// prints the README's "Measured" table in Markdown: the first start (generate,
// sort, build the graph, save) and a restart, then each lookup as a separate
// process, run once cold-ish and then several times warm, with its time and
// memory.
//
//	go run ./bench/e2e                       # 20,000,000 addresses, a temporary folder
//	go run ./bench/e2e -n 1000000 -runs 3    # quicker
//	task bench:e2e
//
// The dataset goes in a new temporary folder (removed afterwards) unless
// -data names one; an existing dataset there is used as it is, not
// regenerated, if -n matches it. Memory is the process's maximum resident set
// size from the operating system, which counts the mapped file pages a lookup
// touched; on macOS the table also shows the peak memory footprint (what the
// process itself allocated) from /usr/bin/time -l. "First" is the first run
// after the dataset was written, whose pages may already be in the OS cache;
// a truly cold run needs the cache dropped, which needs root.
package main

import (
	"bufio"
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"evmgraph/internal/store"
)

func main() {
	n := flag.Int("n", 20_000_000, "addresses in the dataset")
	runs := flag.Int("runs", 5, "warm runs of each lookup; the table shows their median")
	data := flag.String("data", "", "dataset folder to use (default: a new temporary folder, removed afterwards)")
	flag.Parse()

	work, err := os.MkdirTemp("", "evmgraph-e2e-")
	check(err)
	defer os.RemoveAll(work)
	bin := filepath.Join(work, "evmgraph")
	build := exec.Command("go", "build", "-trimpath", "-o", bin, "./cmd/evmgraph")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	build.Stderr = os.Stderr
	check(build.Run())
	dir := *data
	if dir == "" {
		dir = filepath.Join(work, "cache")
	}

	var rows []row
	logf("first start: %d addresses into %s", *n, dir)
	first := start(bin, dir, *n)
	rows = append(rows,
		row{"First start: generate, sort, build the graph", fmt.Sprintf("%s + %s + %s", first.step("generate"), first.step("sort"), first.step("graph")), first.rss, first.footprint},
		row{"Save (all four files), right after generating", first.step("save"), "", ""},
	)
	logf("restart")
	again := start(bin, dir, *n)
	rows = append(rows, row{"Restart: open the saved dataset", again.step("load"), again.rss, again.footprint})

	d, err := store.OpenReadOnly(dir)
	check(err)
	g := d.Graph()
	typical := g.Len() / 3
	hub := 0
	for r := range g.Len() {
		if len(g.List(uint32(r))) > len(g.List(uint32(hub))) {
			hub = r
		}
	}
	neighbour := int(g.List(uint32(hub))[0])
	for _, r := range g.List(uint32(hub)) {
		if len(g.List(r)) <= 10 {
			neighbour = int(r)
			break
		}
	}
	addrOf := map[int]string{}
	for _, r := range []int{typical, hub, neighbour} {
		addrOf[r] = d.Addresses.Addrs[r].Checksum()
	}
	addr := func(r int) string { return addrOf[r] }
	hubDegree := len(g.List(uint32(hub)))
	d.Close()

	queries := []struct {
		label string
		args  []string
	}{
		{"`balance`", []string{"balance", "-data", dir, addr(typical)}},
		{"`connections`, a typical address, default limit", []string{"connections", "-data", dir, addr(typical)}},
		{"`connections`, next to the biggest hub, default limit", []string{"connections", "-data", dir, addr(neighbour)}},
		{"`connections -all`, next to the biggest hub", []string{"connections", "-data", dir, "-all", addr(neighbour)}},
		{fmt.Sprintf("`connections`, the biggest hub itself (%s links)", commas(hubDegree)), []string{"connections", "-data", dir, addr(hub)}},
		{"`connections -all`, the biggest hub itself", []string{"connections", "-data", dir, "-all", addr(hub)}},
	}
	for _, q := range queries {
		logf("%s", q.label)
		firstRun := measure(bin, q.args)
		var warm []measurement
		for range *runs {
			warm = append(warm, measure(bin, q.args))
		}
		w := median(warm)
		label := q.label
		if firstRun.results != "" {
			label += " (" + firstRun.results + ")"
		}
		rows = append(rows, row{label, fmt.Sprintf("%s warm, %s first run", seconds(w.took), seconds(firstRun.took)), bytesOf(w.rss), bytesOf(w.footprint)})
	}
	logf("update")
	u := measure(bin, []string{"update", "-data", dir, "balance", addr(typical), "1000000000000000000"})
	rows = append(rows, row{"`update balance` (append to the log, fsync)", seconds(u.took), bytesOf(u.rss), bytesOf(u.footprint)})
	l := measure(bin, []string{"update", "-data", dir, "link", addr(typical), addr(hub)})
	rows = append(rows, row{"`update link` (check, append to the log, fsync)", seconds(l.took), bytesOf(l.rss), bytesOf(l.footprint)})

	printTable(*n, *runs, rows)
}

type row struct{ label, time, rss, footprint string }

// startResult is what one `evmgraph run` reported until it was running.
type startResult struct {
	steps          map[string]string
	rss, footprint string
}

func (s startResult) step(name string) string {
	if v, ok := s.steps[name]; ok {
		return v
	}
	return "—"
}

// Plain log lines of `evmgraph run` (cmd/evmgraph/ui.go) and the step each
// times.
var stepLines = map[string]*regexp.Regexp{
	"generate": regexp.MustCompile(`^generated \d+ addresses with balances \(seed \d+\) in (\S+ \S+)`),
	"sort":     regexp.MustCompile(`^sorted \d+ rows by address in (\S+ \S+)`),
	"graph":    regexp.MustCompile(`^built \d+ connections between \d+ addresses in (\S+ \S+)`),
	"save":     regexp.MustCompile(`^saved \d+ addresses and balances to \S+ \(\S+ \S+ in (\S+ \S+)\)`),
	"load":     regexp.MustCompile(`^loaded \d+ addresses and balances from \S+ in (\S+ \S+)`),
}

// start runs `evmgraph run` until it is running, stops it with SIGINT, and
// returns the times it logged and its memory.
func start(bin, dir string, n int) startResult {
	cmd, timeOut := timed(bin, "run", "-data", dir, "-n", strconv.Itoa(n), "-show", "0")
	stderr, err := cmd.StderrPipe()
	check(err)
	check(cmd.Start())
	res := startResult{steps: map[string]string{}}
	lines := bufio.NewScanner(stderr)
	for lines.Scan() {
		line := lines.Text()
		for name, re := range stepLines {
			if m := re.FindStringSubmatch(line); m != nil {
				res.steps[name] = m[1]
			}
		}
		if strings.HasPrefix(line, "running; press") {
			check(signalChild(cmd))
		}
		timeOut.WriteString(line + "\n")
	}
	if err := cmd.Wait(); err != nil {
		fmt.Fprintf(os.Stderr, "evmgraph run: %v\n%s", err, timeOut.String())
		os.Exit(1)
	}
	res.rss, res.footprint = memoryOf(cmd, timeOut.String())
	return res
}

type measurement struct {
	took           time.Duration
	rss, footprint uint64
	results        string // the totals line connections prints
}

var totalsLine = regexp.MustCompile(`level 1: (\d+) addresses, level 2: (\d+) addresses`)

// measure runs one lookup to completion and returns its wall time, memory
// and, for connections, the number of results.
func measure(bin string, args []string) measurement {
	cmd, timeOut := timed(bin, args...)
	cmd.Stdout = io.Discard
	cmd.Stderr = timeOut
	began := time.Now()
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "evmgraph %s: %v\n%s", strings.Join(args, " "), err, timeOut.String())
		os.Exit(1)
	}
	m := measurement{took: time.Since(began)}
	m.rss, m.footprint = memoryBytes(cmd, timeOut.String())
	if t := totalsLine.FindStringSubmatch(timeOut.String()); t != nil {
		a, _ := strconv.Atoi(t[1])
		b, _ := strconv.Atoi(t[2])
		m.results = commas(a+b) + " results"
	}
	return m
}

// timed returns the command, run under /usr/bin/time -l on macOS for the
// peak memory footprint, and a buffer for its stderr.
func timed(bin string, args ...string) (*exec.Cmd, *bytes.Buffer) {
	if runtime.GOOS == "darwin" {
		if _, err := os.Stat("/usr/bin/time"); err == nil {
			return exec.CommandContext(context.Background(), "/usr/bin/time", append([]string{"-l", bin}, args...)...), new(bytes.Buffer)
		}
	}
	return exec.CommandContext(context.Background(), bin, args...), new(bytes.Buffer)
}

// signalChild sends SIGINT to the program, through /usr/bin/time if it runs
// under it (time forwards nothing, so the program is its only child).
func signalChild(cmd *exec.Cmd) error {
	if filepath.Base(cmd.Path) == "time" {
		out, err := exec.Command("pgrep", "-P", strconv.Itoa(cmd.Process.Pid)).Output()
		if err != nil {
			return err
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(out)))
		if err != nil {
			return err
		}
		return syscall.Kill(pid, syscall.SIGINT)
	}
	return cmd.Process.Signal(os.Interrupt)
}

var footprintLine = regexp.MustCompile(`(\d+)\s+peak memory footprint`)

// memoryBytes is the process's maximum RSS and, from /usr/bin/time -l, its
// peak memory footprint (0 when unknown).
func memoryBytes(cmd *exec.Cmd, timeOutput string) (rss, footprint uint64) {
	if ru, ok := cmd.ProcessState.SysUsage().(*syscall.Rusage); ok {
		rss = uint64(ru.Maxrss)
		if runtime.GOOS == "linux" {
			rss *= 1024 // Linux reports kilobytes, macOS bytes
		}
	}
	if m := footprintLine.FindStringSubmatch(timeOutput); m != nil {
		footprint, _ = strconv.ParseUint(m[1], 10, 64)
	}
	return rss, footprint
}

func memoryOf(cmd *exec.Cmd, timeOutput string) (string, string) {
	rss, footprint := memoryBytes(cmd, timeOutput)
	return bytesOf(rss), bytesOf(footprint)
}

func median(ms []measurement) measurement {
	slices.SortFunc(ms, func(a, b measurement) int { return int(a.took - b.took) })
	return ms[len(ms)/2]
}

func printTable(n, runs int, rows []row) {
	fmt.Printf("Measured with `go run ./bench/e2e -n %d -runs %d` on %s/%s, %d cores, %s; %s.\n\n",
		n, runs, runtime.GOOS, runtime.GOARCH, runtime.NumCPU(), time.Now().Format("2 January 2006"), loadAverage())
	fmt.Println("| | Time | Max RSS | Held by the process |")
	fmt.Println("|---|---|---|---|")
	for _, r := range rows {
		fmt.Printf("| %s | %s | %s | %s |\n", r.label, r.time, orDash(r.rss), orDash(r.footprint))
	}
}

func loadAverage() string {
	out, err := exec.Command("uptime").Output()
	if err != nil {
		return "load unknown"
	}
	s := string(out)
	if i := strings.Index(s, "load average"); i >= 0 {
		return strings.TrimSpace(s[i:])
	}
	return strings.TrimSpace(s)
}

func seconds(d time.Duration) string {
	switch {
	case d < 10*time.Millisecond:
		return fmt.Sprintf("%.1f ms", float64(d)/float64(time.Millisecond))
	case d < time.Second:
		return fmt.Sprintf("%.0f ms", float64(d)/float64(time.Millisecond))
	default:
		return fmt.Sprintf("%.2f s", d.Seconds())
	}
}

func bytesOf(b uint64) string {
	switch {
	case b == 0:
		return ""
	case b >= 1e9:
		return fmt.Sprintf("%.2f GB", float64(b)/1e9)
	case b >= 1e6:
		return fmt.Sprintf("%.0f MB", float64(b)/1e6)
	default:
		return fmt.Sprintf("%.0f kB", float64(b)/1e3)
	}
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func commas(n int) string {
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

func logf(format string, args ...any) { fmt.Fprintf(os.Stderr, "e2e: "+format+"\n", args...) }

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "e2e:", err)
		os.Exit(1)
	}
}
