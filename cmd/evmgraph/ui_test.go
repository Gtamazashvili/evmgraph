package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// dashboard switches to the dashboard without colour, so the expected output
// is readable, captures it, and fixes the clock: every clock() call advances
// it by step.
func dashboard(t *testing.T, step time.Duration) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	oldOut, oldColor, oldDashboard, oldClock := logOut, useColor, useDashboard, clock
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	logOut, useColor, useDashboard = &buf, false, true
	clock = func() time.Time { now = now.Add(step); return now }
	t.Cleanup(func() { logOut, useColor, useDashboard, clock = oldOut, oldColor, oldDashboard, oldClock })
	return &buf
}

// Every line of a box is exactly as wide as its frame.
func TestBoxLinesAreAligned(t *testing.T) {
	buf := dashboard(t, time.Millisecond)
	box("evmgraph", [][2]string{
		{"dataset", "5,000,000 addresses · seed 2000"},
		{"storage", "binary · cache/addresses.bin"},
		{"machine", "8 cores · 17.2 GB memory · 33.1 GB free"},
	})
	want := `╭─ evmgraph ─────────────────────────────────────────╮
│ dataset    5,000,000 addresses · seed 2000         │
│ storage    binary · cache/addresses.bin            │
│ machine    8 cores · 17.2 GB memory · 33.1 GB free │
╰────────────────────────────────────────────────────╯
`
	if buf.String() != want {
		t.Fatalf("got\n%s\nwant\n%s", buf.String(), want)
	}
}

// A long value, such as a deep path, is shortened in the middle so the box
// stays narrow.
func TestBoxShortensLongValues(t *testing.T) {
	buf := dashboard(t, time.Millisecond)
	box("summary", [][2]string{{"file", "/very/long/" + strings.Repeat("folder/", 20) + "addresses.bin"}})
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if n := runes(line); n > maxBoxText+keyWidth+4 {
			t.Fatalf("line is %d columns: %s", n, line)
		}
	}
	if !strings.Contains(buf.String(), "…") || !strings.Contains(buf.String(), "addresses.bin") {
		t.Fatalf("the end of the path was lost:\n%s", buf.String())
	}
}

// Step lines: icon, label column, text, then facts in their own column.
func TestStepLines(t *testing.T) {
	buf := dashboard(t, time.Millisecond)
	ui.generated(5_000_000, 2000, 24*time.Millisecond)
	ui.override("cache", "seed", "1", "9")
	ui.running()
	want := " ✓ generate  5,000,000 addresses + balances  24 ms  208M/s\n" +
		" ⚠ seed      the cache holds seed 1; using it, not -seed 9 (delete the cache to change it)\n" +
		" ● running   the dataset is saved; press Ctrl+C to stop\n"
	if buf.String() != want {
		t.Fatalf("got\n%q\nwant\n%q", buf.String(), want)
	}
}

// The bar redraws in place and is cleared when the work ends, so the result
// line takes its place.
func TestProgressBar(t *testing.T) {
	buf := dashboard(t, time.Second) // 1 s per clock() call: every update redraws
	bar := newProgress("save", 5_000_000)
	bar.update(3_800_000)
	bar.update(5_000_000)
	bar.end()
	got := buf.String()
	if !strings.HasPrefix(got, clearLine+" ⠙ save      ███████████████░░░░░   76%  3.80M/5.00M  3.80M/s") {
		t.Fatalf("first frame: %q", got)
	}
	if !strings.Contains(got, "████████████████████  100%  5.00M/5.00M") {
		t.Fatalf("last frame: %q", got)
	}
	if !strings.HasSuffix(got, clearLine) {
		t.Fatalf("the bar was not cleared at the end: %q", got)
	}
}

// Updates closer together than redrawGap are skipped, except the last.
func TestProgressBarThrottles(t *testing.T) {
	buf := dashboard(t, time.Millisecond)
	bar := newProgress("generate", 100)
	for i := 1; i <= 100; i++ {
		bar.update(i)
	}
	if frames := strings.Count(buf.String(), clearLine); frames > 5 {
		t.Fatalf("%d redraws for 100 updates 1 ms apart, want a handful", frames)
	}
	if !strings.Contains(buf.String(), "100%") {
		t.Fatal("the last update was not drawn")
	}
}

// A whole run in the dashboard: banner, steps, summary, in that order.
func TestDashboardRun(t *testing.T) {
	buf := dashboard(t, time.Millisecond)
	dir := filepath.Join(t.TempDir(), "cache")
	if err := run(shutdownSoon(t), 1000, 1, dir, 0); err != nil {
		t.Fatal(err)
	}
	ui.summary()
	out := buf.String()
	order := []string{
		"╭─ evmgraph", "dataset    1,000 addresses with u128 wei balances · seed 1", "storage    binary columns, memory-mapped",
		"· cache     created the folder", ", starting a new one",
		"✓ generate  1,000 addresses + balances", "✓ sort      1,000 rows by address",
		"✓ connect   ", "links between 1,000 addresses", "✓ save      ", "● running", "● shutdown  signal received after",
		"· save      nothing to save: the dataset is already on disk", "╭─ summary", "addresses  1,000 (0 loaded + 1,000 new)",
		"files      ", "dataset-000001 · ", "took       generate", "sort", "graph", "save", "total      ran",
	}
	at := 0
	for _, want := range order {
		i := strings.Index(out[at:], want)
		if i < 0 {
			t.Fatalf("%q missing or out of order after byte %d:\n%s", want, at, out)
		}
		at += i
	}
}

func TestFormatting(t *testing.T) {
	for in, want := range map[int]string{0: "0", 999: "999", 1000: "1,000", 5_000_000: "5,000,000", -1234: "-1,234"} {
		if got := commas(in); got != want {
			t.Errorf("commas(%d) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[float64]string{950: "950", 3800: "3.80k", 208_333_333: "208M", 1.2e9: "1.20G"} {
		if got := compact(in); got != want {
			t.Errorf("compact(%v) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[time.Duration]string{
		640 * time.Nanosecond: "640 ns", 31 * time.Microsecond: "31.0 µs", 2400 * time.Microsecond: "2.40 ms", 24 * time.Millisecond: "24 ms",
		4200 * time.Millisecond: "4.2 s", 185 * time.Second: "3m 05s",
	} {
		if got := duration(in); got != want {
			t.Errorf("duration(%v) = %q, want %q", in, got, want)
		}
	}
	if got := shorten("abcdefghij", 7); got != "abc…hij" {
		t.Errorf("shorten = %q", got)
	}
}

// Plain lines keep the wording scripts, docs and the Docker tests rely on.
func TestPlainLinesKeepTheirWording(t *testing.T) {
	buf := captureLog(t, false)
	ui.running()
	ui.nothingToSave()
	ui.generated(1000, 7, 2*time.Millisecond)
	ui.summary() // nothing in plain mode
	for _, want := range []string{
		"running; press Ctrl+C or send SIGTERM to stop\n",
		"no new addresses to save\n",
		"generated 1000 addresses with balances (seed 7) in 2.00 ms",
	} {
		if !strings.Contains(buf.String(), want) {
			t.Fatalf("missing %q in:\n%s", want, buf.String())
		}
	}
	if strings.Contains(buf.String(), "╭") {
		t.Fatal("a summary box in plain mode")
	}
}
