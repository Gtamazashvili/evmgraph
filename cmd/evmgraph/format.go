package main

import (
	"fmt"
	"os"
	"strings"
	"time"
	"unicode/utf8"
)

// Formatting numbers, sizes and durations the way a person reads them.

func runes(s string) int { return utf8.RuneCountInString(s) }

// pad fills s with spaces to width columns; it never cuts.
func pad(s string, width int) string {
	return s + strings.Repeat(" ", max(0, width-runes(s)))
}

// shorten keeps the start and end of a long s, such as a path, with "…"
// between them.
func shorten(s string, limit int) string {
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	head := (limit - 1) / 2
	return string(r[:head]) + "…" + string(r[len(r)-(limit-1-head):])
}

// commas writes n with thousands separators: 5,000,000.
func commas(n int) string {
	s := fmt.Sprint(n)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

// compact writes a count in a few characters: 950, 3.8k, 208M, 1.20G.
func compact(v float64) string {
	switch {
	case v >= 1e9:
		return significant(v/1e9) + "G"
	case v >= 1e6:
		return significant(v/1e6) + "M"
	case v >= 1e3:
		return significant(v/1e3) + "k"
	}
	return fmt.Sprintf("%.0f", v)
}

// significant writes v (at least 1) with three significant digits.
func significant(v float64) string {
	switch {
	case v >= 100:
		return fmt.Sprintf("%.0f", v)
	case v >= 10:
		return fmt.Sprintf("%.1f", v)
	}
	return fmt.Sprintf("%.2f", v)
}

// duration writes d the way a person reads it: 640 ns, 31.0 µs, 2.40 ms, 24 ms,
// 4.2 s, 3m 05s.
func duration(d time.Duration) string {
	ms := float64(d) / float64(time.Millisecond)
	switch {
	case d < time.Microsecond:
		return fmt.Sprintf("%d ns", d.Nanoseconds())
	case d < time.Millisecond:
		return fmt.Sprintf("%.1f µs", float64(d)/float64(time.Microsecond))
	case d < 10*time.Millisecond:
		return fmt.Sprintf("%.2f ms", ms)
	case d < time.Second:
		return fmt.Sprintf("%.0f ms", ms)
	case d < time.Minute:
		return fmt.Sprintf("%.1f s", d.Seconds())
	}
	return fmt.Sprintf("%dm %02ds", int(d.Minutes()), int(d.Seconds())%60)
}

// perSecond is amount per second over d; 0 when d is 0.
func perSecond(amount float64, d time.Duration) float64 {
	if d <= 0 {
		return 0
	}
	return amount / d.Seconds()
}

// folderSize is the total size of the regular files in folder, or -1 if it
// cannot be read.
func folderSize(folder string) int64 {
	entries, err := os.ReadDir(folder)
	if err != nil {
		return -1
	}
	var total int64
	for _, e := range entries {
		if info, err := e.Info(); err == nil && info.Mode().IsRegular() {
			total += info.Size()
		}
	}
	return total
}
