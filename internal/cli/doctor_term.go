package cli

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/mattn/go-runewidth"

	"github.com/jack-work/figaro/internal/term"
)

// runDoctorTerm prints what figaro believes about this terminal, and what the
// terminal itself says when asked.
//
// It exists because a right-edge overflow was reported repeatedly and could not
// be reproduced from another machine: every surface, every width from 20 to 200,
// live turns, resizes, nvim's embedded terminal, all clean. What cannot be
// checked from here is whether the reporter's terminal DRAWS a glyph the width
// figaro measured it to be, and if it does not, every row figaro builds is
// wrong by the difference, invisibly.
//
// One command, run where the trouble is, answers it.
func runDoctorTerm() error {
	w, h := term.Width(), term.Height()
	fmt.Printf("TERM         %s\n", envOr("TERM", "(unset)"))
	fmt.Printf("LANG         %s   LC_CTYPE %s\n", envOr("LANG", "(unset)"), envOr("LC_CTYPE", "(unset)"))
	fmt.Printf("size         %d x %d (ioctl)\n", w, h)
	fmt.Printf("stdout tty   %v    stdin tty %v\n",
		term.IsTerminal(int(os.Stdout.Fd())), term.IsTerminal(int(os.Stdin.Fd())))

	fmt.Printf("\nglyph widths: what figaro MEASURES vs what your terminal DRAWS\n")
	fmt.Printf("  a mismatch on any row below is the bug: every row containing that\n")
	fmt.Printf("  glyph is built to the wrong width and runs past the edge.\n\n")

	for _, g := range []struct {
		name string
		s    string
	}{
		{"─ U+2500 (rules)", "─────"},
		{"│ U+2502 (gutter)", "│││││"},
		{"… U+2026", "………"},
		{"ascii", "xxxxx"},
		{"CJK", "日本語"},
		{"emoji", "🎉🎉🎉"},
	} {
		measured := runewidth.StringWidth(g.s)
		drawn, ok := term.MeasureDrawn(g.s, 150*time.Millisecond)
		switch {
		case !ok:
			fmt.Printf("  %-20s measured %2d   drawn  ?  (no DSR reply)\n", g.name, measured)
		case drawn != measured:
			fmt.Printf("  %-20s measured %2d   drawn %2d   <-- MISMATCH\n", g.name, measured, drawn)
		default:
			fmt.Printf("  %-20s measured %2d   drawn %2d\n", g.name, measured, drawn)
		}
	}

	fmt.Printf("\nambiguous-width setting\n")
	fmt.Printf("  FIGARO_AMBIGUOUS_WIDE  %s\n", envOr("FIGARO_AMBIGUOUS_WIDE", "(unset)"))
	fmt.Printf("  in effect              %v\n", runewidth.DefaultCondition.EastAsianWidth)
	if p := ambiWidthCachePath(); p != "" {
		if b, err := os.ReadFile(p); err == nil {
			fmt.Printf("  cached answer          %s (%s)\n", strings.TrimSpace(string(b)), p)
		} else {
			fmt.Printf("  cached answer          none yet (%s)\n", p)
		}
	}
	fmt.Printf("\nIf a MISMATCH appears above, set FIGARO_AMBIGUOUS_WIDE=1 (or 0) to\n")
	fmt.Printf("match your terminal, and tell whoever is chasing this what you saw.\n")
	return nil
}

func envOr(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}
