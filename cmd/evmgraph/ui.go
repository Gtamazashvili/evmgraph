package main

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	"evmgraph/internal/capacity"
	"evmgraph/internal/evm"
	"evmgraph/internal/store"
)

// The screen: what the program tells the person running it. run reports
// events (loaded, generated, saved, ...) through ui, and ui draws them in one
// of two ways:
//
//   - the dashboard, when stderr is a terminal: a banner with the run's
//     settings and the machine's resources, one aligned line per step with its
//     time and speed, live progress bars, and a summary box at a clean exit;
//   - plain log lines everywhere else (pipes, `docker logs`, files, tests),
//     worded as they always were, with durations and sizes added at the end.
//
// Status goes to stderr; stdout stays for results (the addresses -show
// prints). Colour follows log.go: the dashboard is coloured unless NO_COLOR is
// set, plain lines never are in practice, because they go to a pipe or file.

// useDashboard is decided once, at start: stderr is a terminal that can move
// the cursor. A variable so tests can switch it.
var useDashboard = dashboardWanted()

func dashboardWanted() bool {
	if os.Getenv("TERM") == "dumb" {
		return false
	}
	info, err := os.Stderr.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// clock is time.Now, a variable so tests can fix durations.
var clock = time.Now

// screen has no fields: its state is the package variables above and below,
// like log.go's. ui is the one value of it, so run reads ui.loaded(...).
type screen struct{}

var ui screen

// ui tells the person what the store does to the cache folder.
var _ store.Events = ui

// session is what the summary box reports, gathered as the events arrive.
var session struct {
	began, waiting time.Time
	loaded, added  int
	path           string
	took           []timing
}

type timing struct {
	step string
	d    time.Duration
}

// Ansi codes beyond log.go's levels, and the control sequence that clears the
// current line, so a progress bar can be replaced by its result.
const (
	codeTitle = "1;36"
	clearLine = "\r\x1b[2K"
)

// Width of the label column in step lines and of the key column in boxes.
const (
	labelWidth = 9
	keyWidth   = 11
	textWidth  = 22 // the text column, when facts follow it
	maxBoxText = 64 // longer values in a box are shortened in the middle
	barWidth   = 20 // characters in a progress bar
	redrawGap  = 50 * time.Millisecond
)

// banner opens a run. In the dashboard it shows what the run will do and what
// the machine has to do it with.
func (screen) banner(n int, seed uint64, degree, skew int, dir string) {
	session.began, session.waiting = clock(), time.Time{}
	session.loaded, session.added, session.path, session.took = 0, 0, "", nil
	if !useDashboard {
		return
	}
	machine := fmt.Sprintf("%d cores", runtime.NumCPU())
	if m := capacity.Memory(); m > 0 {
		machine += " · " + capacity.FormatBytes(m) + " memory"
	}
	if free, _, err := capacity.Space(dir); err == nil {
		machine += " · " + capacity.FormatBytes(free) + " free"
	}
	box("evmgraph", [][2]string{
		{"dataset", fmt.Sprintf("%s addresses with u128 wei balances · seed %d", commas(n), seed)},
		{"graph", fmt.Sprintf("%d connections per address on average · skew %d", degree, skew)},
		{"storage", "binary columns, memory-mapped · " + dir},
		{"machine", machine},
	})
	fmt.Fprintln(logOut)
}

func (screen) DirCreated(dir string) {
	if useDashboard {
		info("cache", "created the folder "+dir)
		return
	}
	logf("cache directory %s does not exist, creating it", dir)
}

func (screen) DirMovedAside(dir, moved string) {
	if useDashboard {
		warn("cache", fmt.Sprintf("%s was not a folder; moved it to %s", dir, moved))
		return
	}
	warnf("%s is not a directory; moved it to %s (delete it if you do not need it)", dir, moved)
}

func (screen) noCache(dir string) {
	if useDashboard {
		info("cache", "no dataset in "+dir+", starting a new one")
		return
	}
	logf("no dataset in %s, starting a new one", dir)
}

func (screen) loaded(n int, folder string, took time.Duration, mapped bool) {
	session.loaded, session.path = n, folder
	session.took = append(session.took, timing{"load", took})
	if useDashboard {
		how := "read"
		if mapped {
			how = "memory-mapped"
		}
		done("cache", fmt.Sprintf("loaded %s addresses + balances", commas(n)), duration(took), how)
		return
	}
	okf("loaded %d addresses and balances from %s in %s", n, folder, duration(took))
}

// MovedAside reports a file in the program's way, moved to a new name.
func (screen) MovedAside(path, moved, reason string) {
	if useDashboard {
		warn("cache", fmt.Sprintf("%s: %s; moved it to %s", path, reason, moved))
		return
	}
	warnf("%s is invalid (%s); moved it to %s (delete it if you do not need it)", path, reason, moved)
}

// override reports a flag the recorded dataset overrides: the dataset is
// continued, not mixed with another.
func (screen) override(dir, setting, recorded, asked string) {
	if useDashboard {
		warn(setting, fmt.Sprintf("the cache holds %s %s; using it, not -%s %s (delete the cache to change it)", setting, recorded, setting, asked))
		return
	}
	warnf("%s holds the dataset of %s %s; using that %s, not -%s %s (delete the cache to change it)", dir, setting, recorded, setting, setting, asked)
}

func (screen) outdated(dir string, version int) {
	if useDashboard {
		warn("version", fmt.Sprintf("the dataset is from an older version (manifest version %d); it is generated again from its seed", version))
		return
	}
	warnf("%s holds a dataset from an older version (manifest version %d); it is generated again from its seed", dir, version)
}

func (screen) RemovedUnfinished(folder string) {
	if useDashboard {
		info("cache", "removed "+folder+", a dataset folder no manifest names (an unfinished save, or a broken dataset set aside)")
		return
	}
	logf("removed %s, a dataset folder no manifest names (an unfinished save, or a broken dataset set aside)", folder)
}

func (screen) NotOurFolder(path string) {
	if useDashboard {
		warn("cache", path+" is named like a dataset folder but holds other files, or balance updates; leaving it")
		return
	}
	warnf("%s is named like a dataset folder but holds other files, or balance updates; leaving it", path)
}

func (screen) keepingAll(have, n int) {
	if useDashboard {
		warn("size", fmt.Sprintf("the cache has %s addresses, more than -n %s; keeping all of them", commas(have), commas(n)))
		return
	}
	warnf("the cache has %d addresses, more than -n %d; keeping all of them", have, n)
}

func (screen) topUp(have, n int) {
	if useDashboard {
		info("top-up", fmt.Sprintf("%s cached, -n %s: generating all %s again from the seed, with connections", commas(have), commas(n), commas(n)))
		return
	}
	logf("the cache has %d addresses and -n is %d: generating all %d again from the seed", have, n, n)
}

// generating starts generation; the returned bar follows its progress.
func (screen) generating(seed uint64, count int) *progress {
	if !useDashboard {
		logf("generating addresses (seed %d)", seed)
	}
	return newProgress("generate", count)
}

func (screen) generated(count int, seed uint64, took time.Duration) {
	session.added = count
	session.took = append(session.took, timing{"generate", took})
	rate := perSecond(float64(count), took)
	if useDashboard {
		done("generate", commas(count)+" addresses + balances", duration(took), compact(rate)+"/s")
		return
	}
	okf("generated %d addresses with balances (seed %d) in %s (%s per second)", count, seed, duration(took), compact(rate))
}

func (screen) sorted(count int, took time.Duration) {
	session.took = append(session.took, timing{"sort", took})
	if useDashboard {
		done("sort", commas(count)+" rows by address", duration(took), "all cores")
		return
	}
	logf("sorted %d rows by address in %s", count, duration(took))
}

// connecting starts the graph build; its bar covers both passes, 2n
// positions.
func (screen) connecting(n int) *progress {
	if !useDashboard {
		logf("building the connections of %d addresses", n)
	}
	return newProgress("connect", 2*n)
}

func (screen) connected(n, entries int, took time.Duration) {
	session.took = append(session.took, timing{"graph", took})
	if useDashboard {
		done("connect", fmt.Sprintf("%s links between %s addresses", commas(entries/2), commas(n)), duration(took))
		return
	}
	okf("built %d connections between %d addresses in %s", entries/2, n, duration(took))
}

// stoppedBeforeSaving reports a signal that stopped the work before the
// save: nothing new was written.
func (screen) stoppedBeforeSaving(step string) {
	if useDashboard {
		warn("stop", "stopped while "+step+"; nothing new was saved, and the next run generates the same dataset again from the seed")
		return
	}
	warnf("shutdown signal received while %s; nothing new was saved, and the next run generates the same dataset again from the seed", step)
}

func (screen) running() {
	session.waiting = clock()
	if useDashboard {
		state("running", "the dataset is saved; press Ctrl+C to stop")
		return
	}
	statef("running; press Ctrl+C or send SIGTERM to stop")
}

func (screen) shutdown() {
	if useDashboard {
		fmt.Fprintln(logOut)
		state("shutdown", "signal received after "+duration(clock().Sub(session.waiting)))
		return
	}
	statef("shutdown signal received")
}

// keptLinks reports the added links a grown dataset keeps.
func (screen) keptLinks(n int) {
	if useDashboard {
		info("links", fmt.Sprintf("kept %s added links in the grown dataset", commas(n)))
		return
	}
	logf("kept %d added links in the grown dataset", n)
}

// upToDate reports a stop with nothing to write: the dataset was saved
// when it was generated, and no updates are waiting.
func (screen) upToDate() {
	if useDashboard {
		info("save", "nothing to save: the dataset is already on disk")
		return
	}
	logf("nothing to save: the dataset is already on disk")
}

// folding starts folding updates in; the bar follows balances.bin, when
// balances changed. what is pendingWords' "3 balance updates and 1 added link".
func (screen) folding(what string, total int) *progress {
	if !useDashboard {
		logf("folding %s into a new dataset folder", what)
	}
	return newProgress("updates", total)
}

func (screen) folded(what, folder string, took time.Duration) {
	session.path = folder
	session.took = append(session.took, timing{"updates", took})
	if useDashboard {
		done("updates", what+" folded in", duration(took))
		return
	}
	okf("folded %s into %s in %s", what, folder, duration(took))
}

// waitingForLock reports that update waits for run to finish writing.
func (screen) waitingForLock(dir string) {
	if useDashboard {
		state("update", "waiting for evmgraph run to finish writing "+dir)
		return
	}
	logf("waiting for evmgraph run to finish writing %s", dir)
}

func (screen) nothingToSave() {
	if useDashboard {
		info("save", "no new addresses to save")
		return
	}
	logf("no new addresses to save")
}

// saving starts the save; the two bars follow the addresses file, then the
// balances file.
func (screen) saving(have, added int) (addresses, balances *progress) {
	if !useDashboard {
		logf("saving addresses and balances (%d loaded + %d new)", have, added)
	}
	return newProgress("addresses", have+added), newProgress("balances", have+added)
}

// saved reports the save: written is the bytes of the addresses and
// balances files (the graph's files are written just before them).
func (screen) saved(total int, folder string, written int64, took time.Duration) {
	session.path = folder
	session.took = append(session.took, timing{"save", took})
	size := max(written, 0)
	if useDashboard {
		done("save", capacity.FormatBytes(uint64(size)), duration(took), capacity.FormatBytes(uint64(perSecond(float64(size), took)))+"/s")
		return
	}
	okf("saved %d addresses and balances to %s (%s in %s)", total, folder, capacity.FormatBytes(uint64(size)), duration(took))
}

// found reports a balance lookup on stderr, in the dashboard only: stdout
// already holds the answer, and scripts get just that.
func (screen) found(row, count int, open, find time.Duration, mapped bool) {
	if !useDashboard {
		return
	}
	how := "read"
	if mapped {
		how = "memory-mapped"
	}
	done("balance", fmt.Sprintf("row %s of %s", commas(row), commas(count)), "opened "+duration(open), "found "+duration(find), how)
}

// connectionsFound reports a connections query's exact totals on stderr,
// in every mode: stdout holds only the addresses, so a script can count them,
// and a person needs the totals to know what was left out.
func (screen) connectionsFound(level1, level2, shown int, took time.Duration, all bool) {
	total := level1 + level2
	cut := shown < total && !all
	if useDashboard {
		more := ""
		if cut {
			more = fmt.Sprintf("showing %s of %s; -all for everything", commas(shown), commas(total))
		}
		facts := []string{"level 1: " + commas(level1), "level 2: " + commas(level2), duration(took)}
		if more != "" {
			facts = append(facts, more)
		}
		done("connect", fmt.Sprintf("%s addresses within 2 steps", commas(total)), facts...)
		return
	}
	line := fmt.Sprintf("level 1: %d addresses, level 2: %d addresses", level1, level2)
	if cut {
		line += fmt.Sprintf("; showing %d of %d; -all for everything", shown, total)
	}
	logf("%s", line)
}

func (screen) notFound(a evm.Address, count int, dir string) {
	if useDashboard {
		warn("balance", fmt.Sprintf("%s is not among the %s addresses in %s", a.Checksum(), commas(count), dir))
		return
	}
	warnf("%s is not among the %d addresses in %s", a.Checksum(), count, dir)
}

// interrupted reports a save cancelled by a second signal.
func (screen) interrupted() {
	if useDashboard {
		warn("save", "interrupted: nothing was saved")
		return
	}
	warnf("interrupted: nothing was saved")
}

// fail reports why the program stops.
func (screen) fail(format string, args ...any) {
	if useDashboard {
		step("✗", codeError, "error", paint(codeError, fmt.Sprintf(format, args...)))
		return
	}
	errorf(format, args...)
}

// summary closes a clean run in the dashboard with what it holds now, where,
// how long each step took and how much memory it needed at most.
func (screen) summary() {
	if !useDashboard {
		return
	}
	addresses := commas(session.loaded) + " loaded"
	if session.added > 0 {
		addresses = fmt.Sprintf("%s (%s loaded + %s new)", commas(session.loaded+session.added), commas(session.loaded), commas(session.added))
	}
	files := "nothing saved"
	if size := folderSize(session.path); session.path != "" && size >= 0 {
		files = session.path + " · " + capacity.FormatBytes(uint64(size))
	}
	var took []string
	for _, t := range session.took {
		took = append(took, t.step+" "+duration(t.d))
	}
	rows := [][2]string{
		{"addresses", addresses},
		{"files", files},
		{"took", strings.Join(took, " · ")},
		{"total", "ran " + duration(clock().Sub(session.began))},
	}
	if peak := capacity.PeakMemory(); peak > 0 {
		rows = append(rows, [2]string{"memory", "peak " + capacity.FormatBytes(peak)})
	}
	fmt.Fprintln(logOut)
	box("summary", rows)
}
