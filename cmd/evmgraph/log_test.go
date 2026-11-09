package main

import (
	"bytes"
	"strings"
	"testing"
)

// captureLog sends log output to a buffer as plain log lines (not the
// dashboard), with colour forced on or off, and restores everything when the
// test ends.
func captureLog(t *testing.T, color bool) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	oldOut, oldColor, oldDashboard := logOut, useColor, useDashboard
	logOut, useColor, useDashboard = &buf, color, false
	t.Cleanup(func() { logOut, useColor, useDashboard = oldOut, oldColor, oldDashboard })
	return &buf
}

var levels = []struct {
	name string
	log  func(string, ...any)
	code string // empty: plain
}{
	{"errorf", errorf, codeError},
	{"warnf", warnf, codeWarn},
	{"okf", okf, codeOK},
	{"statef", statef, codeState},
	{"logf", logf, ""},
}

// With colour on, each level wraps its line in its own code and a reset;
// routine lines stay plain.
func TestLogLevelsWithColor(t *testing.T) {
	for _, l := range levels {
		t.Run(l.name, func(t *testing.T) {
			buf := captureLog(t, true)
			l.log("saved %d addresses", 7)
			want := "saved 7 addresses\n"
			if l.code != "" {
				want = "\x1b[" + l.code + "msaved 7 addresses\x1b[0m\n"
			}
			if got := buf.String(); got != want {
				t.Fatalf("got %q, want %q", got, want)
			}
		})
	}
}

// With colour off, no level writes an escape byte.
func TestLogWithoutColor(t *testing.T) {
	buf := captureLog(t, false)
	for _, l := range levels {
		l.log("line from %s", l.name)
	}
	bar := newProgress("save", 10)
	bar.update(5)
	bar.end()
	if strings.Contains(buf.String(), "\x1b") {
		t.Fatalf("escape codes without colour: %q", buf.String())
	}
	if !strings.Contains(buf.String(), "line from okf\n") || !strings.Contains(buf.String(), "save: 5/10\n") {
		t.Fatalf("plain output changed: %q", buf.String())
	}
}

// The plain progress counter writes one dim line per tenth of the work, so a
// log file gets a few whole lines, never one line rewritten in place.
func TestProgressLineIsDim(t *testing.T) {
	buf := captureLog(t, true)
	bar := newProgress("save", 100)
	for done := 1; done <= 100; done++ {
		bar.update(done)
	}
	bar.end()
	if strings.Count(buf.String(), "\n") != 10 || strings.Contains(buf.String(), "\r") {
		t.Fatalf("want 10 whole lines, got %q", buf.String())
	}
	if !strings.HasPrefix(buf.String(), "\x1b[2msave: 10/100\x1b[0m\n") || !strings.HasSuffix(buf.String(), "\x1b[2msave: 100/100\x1b[0m\n") {
		t.Fatalf("got %q", buf.String())
	}
}

// NO_COLOR (no-color.org) and TERM=dumb turn colour off, whatever stderr is.
func TestColorWantedRespectsEnvironment(t *testing.T) {
	t.Run("NO_COLOR", func(t *testing.T) {
		t.Setenv("NO_COLOR", "1")
		if colorWanted() {
			t.Fatal("colour with NO_COLOR set")
		}
	})
	t.Run("TERM=dumb", func(t *testing.T) {
		t.Setenv("NO_COLOR", "")
		t.Setenv("TERM", "dumb")
		if colorWanted() {
			t.Fatal("colour on a dumb terminal")
		}
	})
}
