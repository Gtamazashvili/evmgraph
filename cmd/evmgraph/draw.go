package main

import (
	"fmt"
	"strings"
	"time"
)

// Drawing: the step lines, the summary box and the progress bars the screen
// is made of (ui.go decides what to say, this file how it looks).

// Step lines: an icon, a label column, the text and, after it, facts such as
// the time taken and the speed.

func done(label, text string, facts ...string) { step("✓", codeOK, label, text, facts...) }
func warn(label, text string)                  { step("⚠", codeWarn, label, paint(codeWarn, text)) }
func state(label, text string)                 { step("●", codeState, label, text) }
func info(label, text string)                  { step("·", codeDim, label, text) }

func step(icon, code, label, text string, facts ...string) {
	line := " " + paint(code, icon) + " " + pad(label, labelWidth) + " "
	if len(facts) == 0 {
		line += text
	} else {
		line += pad(text, textWidth) + "  " + paint(codeDim, strings.Join(facts, "  "))
	}
	fmt.Fprintln(logOut, line)
}

// box draws rows of key and value in a rounded frame titled title. All runes
// used are one column wide, so counting runes is counting columns.
func box(title string, rows [][2]string) {
	lines := make([]string, len(rows))
	width := runes(title) + 2
	for i, r := range rows {
		lines[i] = pad(r[0], keyWidth) + shorten(r[1], maxBoxText)
		width = max(width, runes(lines[i]))
	}
	border := func(s string) string { return paint(codeDim, s) }
	fmt.Fprintln(logOut, border("╭─ ")+paint(codeTitle, title)+border(" "+strings.Repeat("─", width-runes(title)-1)+"╮"))
	for i, l := range lines {
		key := paint(codeDim, pad(rows[i][0], keyWidth))
		fmt.Fprintln(logOut, border("│ ")+key+pad(l, width)[keyWidth:]+border(" │"))
	}
	fmt.Fprintln(logOut, border("╰"+strings.Repeat("─", width+2)+"╯"))
}

// progress is a live bar in the dashboard and, elsewhere, a plain line at
// every tenth of the work ("save: 2000000/20000000"), so a log file gets a
// few readable lines, not one line rewritten hundreds of times. Call end when
// the work stops, finished or not.
type progress struct {
	label        string
	total        int
	began, drawn time.Time
	frame        int
	open         bool
	tenth        int // plain mode: the last tenth of the work reported
}

var spinner = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

func newProgress(label string, total int) *progress {
	return &progress{label: label, total: total, began: clock()}
}

// update shows that done of total are finished. The dashboard redraws at most
// every redrawGap, and always for the last update, so a fast loop stays fast.
func (p *progress) update(done int) {
	if !useDashboard {
		tenth := 10
		if p.total > 0 {
			tenth = done * 10 / p.total
		}
		if tenth > p.tenth {
			p.tenth = tenth
			fmt.Fprintln(logOut, paint(codeDim, fmt.Sprintf("%s: %d/%d", p.label, done, p.total)))
		}
		return
	}
	now := clock()
	if p.open && done < p.total && now.Sub(p.drawn) < redrawGap {
		return
	}
	p.drawn, p.open = now, true
	p.frame = (p.frame + 1) % len(spinner)
	filled, percent := barWidth, 100
	if p.total > 0 {
		filled, percent = done*barWidth/p.total, done*100/p.total
	}
	bar := paint(codeState, strings.Repeat("█", filled)) + paint(codeDim, strings.Repeat("░", barWidth-filled))
	facts := fmt.Sprintf("%3d%%  %s/%s  %s/s", percent, compact(float64(done)), compact(float64(p.total)),
		compact(perSecond(float64(done), now.Sub(p.began))))
	fmt.Fprint(logOut, clearLine+" "+paint(codeState, spinner[p.frame])+" "+pad(p.label, labelWidth)+" "+bar+"  "+paint(codeDim, facts))
}

// end removes the dashboard's bar, so the result line takes its place.
func (p *progress) end() {
	if !p.open {
		return
	}
	fmt.Fprint(logOut, clearLine)
	p.open = false
}
