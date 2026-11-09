package main

import (
	"fmt"
	"io"
	"os"
)

// Status messages go to stderr, keeping stdout for results. When a person is
// watching a terminal they are coloured by meaning, so a warning or an error
// stands out from routine lines:
//
//	errorf  bold red  the program stops because of it
//	warnf   yellow    the program carries on, but something was not as asked
//	okf     green     data was loaded, generated or saved
//	statef  cyan      the program starts or stops waiting
//	logf    plain     routine progress
//
// The x/y progress counter is dim. Pipes, `docker logs`, files and tests get
// plain text, the same as before colours existed.

// logOut and useColor are variables so tests can capture the output and force
// colour on or off.
var (
	logOut   io.Writer = os.Stderr
	useColor           = colorWanted()
)

// ANSI SGR codes.
const (
	codeError = "1;31"
	codeWarn  = "33"
	codeOK    = "32"
	codeState = "36"
	codeDim   = "2"
)

// colorWanted decides once, at start, whether to colour: only when stderr is
// a terminal, NO_COLOR is not set to a non-empty value (no-color.org) and the
// terminal is not "dumb".
func colorWanted() bool {
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return false
	}
	info, err := os.Stderr.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// paint wraps s in the ANSI code and a reset when colour is on.
func paint(code, s string) string {
	if !useColor {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

func logLine(code, format string, args ...any) {
	line := fmt.Sprintf(format, args...)
	if code != "" {
		line = paint(code, line)
	}
	fmt.Fprintln(logOut, line)
}

// logf writes a routine status line.
func logf(format string, args ...any) { logLine("", format, args...) }

// errorf writes why the program stops.
func errorf(format string, args ...any) { logLine(codeError, format, args...) }

// warnf writes something the user should notice; the program carries on.
func warnf(format string, args ...any) { logLine(codeWarn, format, args...) }

// okf writes that data was loaded, generated or saved.
func okf(format string, args ...any) { logLine(codeOK, format, args...) }

// statef writes that the program starts or stops waiting for a signal.
func statef(format string, args ...any) { logLine(codeState, format, args...) }
